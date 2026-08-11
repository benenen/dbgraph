package jobs_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	"github.com/benenen/dbgraph/internal/jobs"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestServiceCreatesPendingSchemaScanJob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{
		Path: filepath.Join(t.TempDir(), "dbgraph.sqlite"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	fixedTime := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	idGenerator, err := id.NewGenerator(4, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	projectService := catalog.NewProjectService(
		dbsqlite.NewProjectRepository(store),
		idGenerator,
		func() time.Time { return fixedTime },
	)
	project, err := projectService.Create(ctx, catalog.CreateProject{Name: "Learning Platform"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	service := jobs.NewService(
		dbsqlite.NewJobRepository(store),
		idGenerator,
		func() time.Time { return fixedTime },
	)
	created, err := service.Create(ctx, jobs.CreateJob{
		ProjectID: project.ID,
		Type:      jobs.TypeSchemaScan,
		Payload:   []byte(`{"source":"primary"}`),
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if created.Status != jobs.StatusPending {
		t.Fatalf("job status = %v, want PENDING", created.Status)
	}
	if created.RevisionNo != 1 {
		t.Fatalf("job revision = %d, want 1", created.RevisionNo)
	}

	retrieved, err := service.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if retrieved.ID != created.ID || retrieved.ProjectID != project.ID {
		t.Fatalf("retrieved job = %#v, want IDs from %#v", retrieved, created)
	}
	if string(retrieved.Payload) != `{"source":"primary"}` {
		t.Fatalf("retrieved payload = %s", retrieved.Payload)
	}
}
