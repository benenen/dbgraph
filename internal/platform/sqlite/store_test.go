package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestStoreOpensMigratedDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "dbgraph.db")

	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}

	if status.SchemaVersion != 8 {
		t.Fatalf("schema version = %d, want 8", status.SchemaVersion)
	}
	if status.SQLiteVersion == "" {
		t.Fatal("SQLite version is empty")
	}
	if status.JournalMode != "wal" {
		t.Fatalf("journal mode = %q, want wal", status.JournalMode)
	}
	if !status.ForeignKeysEnabled {
		t.Fatal("foreign keys are disabled")
	}
}

func TestFoundationMigrationCreatesRequiredTables(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "dbgraph.db")
	store, err := dbsqlite.Open(context.Background(), dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	databaseURL.RawQuery = query.Encode()
	reader, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatalf("open read-only verification database: %v", err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close verification database: %v", err)
		}
	})

	for _, tableName := range []string{"audit_events", "jobs"} {
		var found string
		err := reader.QueryRow(
			"SELECT name FROM sqlite_schema WHERE type = 'table' AND name = ?",
			tableName,
		).Scan(&found)
		if err != nil {
			t.Fatalf("find table %q: %v", tableName, err)
		}
		if found != tableName {
			t.Fatalf("table name = %q, want %q", found, tableName)
		}
	}
}

func TestOnlyOneWriterOwnsDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "dbgraph.db")
	const contenders = 8

	type openResult struct {
		store *dbsqlite.Store
		err   error
	}
	start := make(chan struct{})
	results := make(chan openResult, contenders)
	for range contenders {
		go func() {
			<-start
			store, err := dbsqlite.Open(context.Background(), dbsqlite.Config{Path: databasePath})
			results <- openResult{store: store, err: err}
		}()
	}
	close(start)

	var owner *dbsqlite.Store
	conflicts := 0
	for range contenders {
		result := <-results
		switch {
		case result.err == nil:
			if owner != nil {
				_ = result.store.Close()
				t.Fatal("more than one writer opened the database")
			}
			owner = result.store
		case errors.Is(result.err, dbsqlite.ErrWriterAlreadyActive):
			conflicts++
		default:
			t.Fatalf("open contender: %v", result.err)
		}
	}
	if owner == nil {
		t.Fatal("no writer opened the database")
	}
	if conflicts != contenders-1 {
		t.Fatalf("writer conflicts = %d, want %d", conflicts, contenders-1)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}

	reopened, err := dbsqlite.Open(context.Background(), dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("reopen after owner closes: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
}

func TestDatabaseSymlinkCannotBypassWriterOwnership(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "dbgraph.db")
	aliasPath := filepath.Join(directory, "dbgraph-alias.db")

	bootstrap, err := dbsqlite.Open(context.Background(), dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("bootstrap database: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap store: %v", err)
	}
	if err := os.Symlink(databasePath, aliasPath); err != nil {
		t.Fatalf("create database symlink: %v", err)
	}

	owner, err := dbsqlite.Open(context.Background(), dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("open database owner: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close database owner: %v", err)
		}
	})

	alias, err := dbsqlite.Open(context.Background(), dbsqlite.Config{Path: aliasPath})
	if alias != nil {
		_ = alias.Close()
	}
	if !errors.Is(err, dbsqlite.ErrWriterAlreadyActive) {
		t.Fatalf("open symlink alias error = %v, want ErrWriterAlreadyActive", err)
	}
}

func TestOpenRestrictsDatabaseAndLockPermissions(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "permissions.sqlite")
	store, err := dbsqlite.Open(t.Context(), dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, path := range []string{databasePath, databasePath + ".writer.lock"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if permissions := info.Mode().Perm(); permissions != 0o600 {
			t.Fatalf("%s permissions = %o, want 600", filepath.Base(path), permissions)
		}
	}
}
