package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestBackupCapturesCommittedDataWhileWriterIsRunning(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.sqlite")
	backupPath := filepath.Join(directory, "backups", "snapshot.sqlite")
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: sourcePath})
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ids, err := id.NewGenerator(7, time.Now)
	if err != nil {
		t.Fatalf("create IDs: %v", err)
	}
	sources := dbsqlite.NewCatalogRepository(store, ids)
	created := catalog.DataSource{
		ID: 42, Name: "backup-proof", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "BACKUP_DSN", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := sources.CreateDataSource(ctx, created); err != nil {
		t.Fatalf("create data source: %v", err)
	}

	if err := dbsqlite.Backup(ctx, sourcePath, backupPath); err != nil {
		t.Fatalf("backup live database: %v", err)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("backup permissions = %o, want 600", permissions)
	}

	backupStore, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: backupPath})
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	t.Cleanup(func() { _ = backupStore.Close() })
	restored, err := dbsqlite.NewCatalogRepository(backupStore, ids).GetDataSource(ctx, 42)
	if err != nil || restored.Name != "backup-proof" {
		t.Fatalf("restored data source = %#v, error = %v", restored, err)
	}
}

func TestBackupRefusesToOverwriteDestination(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.sqlite")
	destinationPath := filepath.Join(directory, "existing.sqlite")
	store, err := dbsqlite.Open(t.Context(), dbsqlite.Config{Path: sourcePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := os.WriteFile(destinationPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = dbsqlite.Backup(t.Context(), sourcePath, destinationPath)
	if !errors.Is(err, dbsqlite.ErrBackupDestinationExists) {
		t.Fatalf("backup error = %v, want ErrBackupDestinationExists", err)
	}
	contents, readErr := os.ReadFile(destinationPath)
	if readErr != nil || string(contents) != "keep" {
		t.Fatalf("existing destination = %q, error = %v", contents, readErr)
	}
}

func TestBackupDoesNotFollowDestinationReplacementDuringStaging(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.sqlite")
	destinationPath := filepath.Join(directory, "snapshot.sqlite")
	victimPath := filepath.Join(directory, "victim")

	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`
PRAGMA journal_mode = DELETE;
CREATE TABLE proof(value TEXT NOT NULL);
INSERT INTO proof(value) VALUES ('committed');
`); err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	connection, err := source.Conn(t.Context())
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), "BEGIN EXCLUSIVE"); err != nil {
		_ = connection.Close()
		_ = source.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		_ = connection.Close()
		_ = source.Close()
	})
	if err := os.WriteFile(victimPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	backupDone := make(chan error, 1)
	go func() { backupDone <- dbsqlite.Backup(context.Background(), sourcePath, destinationPath) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		stagingStarted := false
		info, statErr := os.Lstat(destinationPath)
		if statErr == nil && info.Mode().IsRegular() && info.Size() == 0 {
			stagingStarted = true
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal(statErr)
		}
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), ".dbgraph-backup-") {
				stagingStarted = true
				break
			}
		}
		if stagingStarted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backup staging did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.Remove(destinationPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Symlink(victimPath, destinationPath); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}

	var backupErr error
	select {
	case backupErr = <-backupDone:
	case <-time.After(3 * time.Second):
		t.Fatal("backup did not finish")
	}
	if !errors.Is(backupErr, dbsqlite.ErrBackupDestinationExists) {
		t.Fatalf("backup error = %v, want ErrBackupDestinationExists", backupErr)
	}
	contents, err := os.ReadFile(victimPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 0 {
		t.Fatalf("backup followed replaced destination symlink and wrote %d victim bytes", len(contents))
	}
	target, err := os.Readlink(destinationPath)
	if err != nil || target != victimPath {
		t.Fatalf("competing destination target = %q, error = %v", target, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".dbgraph-backup-") {
			t.Fatalf("backup left temporary artifact %q after publication conflict", entry.Name())
		}
	}
}
