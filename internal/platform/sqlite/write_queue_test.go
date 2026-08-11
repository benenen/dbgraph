package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestBoundedWriteQueueRejectsExcessRepositoryWrite(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "dbgraph.sqlite")
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{
		Path:               databasePath,
		WriteQueueCapacity: 1,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	blockerDatabase := openQueueBlocker(t, databasePath)
	blockerTransaction, err := blockerDatabase.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin blocker transaction: %v", err)
	}
	if _, err := blockerTransaction.ExecContext(ctx, `
INSERT INTO projects(id, name, description, created_at, updated_at)
VALUES (900, 'blocker', '', '2026-08-11T12:00:00Z', '2026-08-11T12:00:00Z')
`); err != nil {
		t.Fatalf("hold external SQLite write lock: %v", err)
	}

	type writeResult struct {
		projectID int64
		err       error
	}
	repository := dbsqlite.NewProjectRepository(store)
	results := make(chan writeResult, 3)
	start := make(chan struct{})
	timestamp := time.Date(2026, time.August, 11, 12, 1, 0, 0, time.UTC)
	for projectID := int64(1); projectID <= 3; projectID++ {
		project := catalog.Project{
			ID:        projectID,
			Name:      fmt.Sprintf("project-%d", projectID),
			CreatedAt: timestamp,
			UpdatedAt: timestamp,
		}
		go func() {
			<-start
			results <- writeResult{
				projectID: project.ID,
				err:       repository.CreateProject(ctx, project),
			}
		}()
	}
	close(start)

	var rejectedID int64
	select {
	case result := <-results:
		if !errors.Is(result.err, dbsqlite.ErrWriteQueueFull) {
			t.Fatalf("first completed write error = %v, want ErrWriteQueueFull", result.err)
		}
		rejectedID = result.projectID
	case <-time.After(2 * time.Second):
		t.Fatal("excess repository write was not rejected while the queue was full")
	}
	if err := blockerTransaction.Rollback(); err != nil {
		t.Fatalf("release external SQLite write lock: %v", err)
	}

	acceptedIDs := make([]int64, 0, 2)
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("accepted repository write for project %d: %v", result.projectID, result.err)
			}
			acceptedIDs = append(acceptedIDs, result.projectID)
		case <-time.After(2 * time.Second):
			t.Fatal("accepted repository write did not complete after releasing the blocker")
		}
	}

	for _, projectID := range acceptedIDs {
		if _, err := repository.GetProject(ctx, projectID); err != nil {
			t.Fatalf("get accepted project %d: %v", projectID, err)
		}
	}
	if _, err := repository.GetProject(ctx, rejectedID); !errors.Is(err, catalog.ErrProjectNotFound) {
		t.Fatalf("get rejected project %d error = %v, want ErrProjectNotFound", rejectedID, err)
	}
}

func openQueueBlocker(t *testing.T, databasePath string) *sql.DB {
	t.Helper()
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := databaseURL.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	databaseURL.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatalf("open external SQLite blocker: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close external SQLite blocker: %v", err)
		}
	})
	return database
}
