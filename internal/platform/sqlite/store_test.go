package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/migrations"
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

	if status.SchemaVersion != 2 {
		t.Fatalf("schema version = %d, want 2", status.SchemaVersion)
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

func TestSourceBindingMigrationUpgradesV1AndEnforcesHistoryInvariants(t *testing.T) {
	t.Parallel()

	databasePath := bootstrapV1SourceBindingDatabase(t)
	upgradeSourceBindingDatabase(t, databasePath)
	database := openSourceBindingVerificationDatabase(t, databasePath)
	assertSourceBindingMigrationTables(t, database)
	assertLegacyRepositoryIdentityBackfill(t, database)
	seedSourceBindingInvariantRows(t, database)
	assertSQLRejected(t, database,
		"UPDATE source_binding_current SET binding_revision_id = 41 WHERE binding_set_id = 30",
		"FOREIGN KEY constraint failed")
	for _, statement := range []string{
		"UPDATE source_binding_revisions SET reason = 'changed' WHERE id = 40",
		"DELETE FROM source_binding_revisions WHERE id = 40",
		"UPDATE source_binding_members SET data_source_id = 21 WHERE binding_revision_id = 40 AND data_source_id = 20",
		"DELETE FROM source_binding_members WHERE binding_revision_id = 40 AND data_source_id = 20",
	} {
		assertSQLRejected(t, database, statement, "append-only")
	}
}

func bootstrapV1SourceBindingDatabase(t *testing.T) string {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "dbgraph.db")
	bootstrap, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open v1 bootstrap database: %v", err)
	}
	v1, err := migrations.Files.ReadFile("001_init.sql")
	if err != nil {
		t.Fatalf("read v1 migration: %v", err)
	}
	if _, err := bootstrap.ExecContext(t.Context(), `
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
`+string(v1)+`
INSERT INTO repositories(id, name, remote_url, default_branch, created_at, updated_at)
VALUES (9, 'legacy-service', 'https://github.com/acme/legacy.git', 'main',
        '2026-08-14T06:00:00Z', '2026-08-14T06:00:00Z');
INSERT INTO schema_migrations(version) VALUES (1);
`); err != nil {
		t.Fatalf("bootstrap v1 database: %v", err)
	}
	seedLegacyRepositoryBatch(t, bootstrap, 205)
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close v1 bootstrap database: %v", err)
	}
	return databasePath
}

func seedLegacyRepositoryBatch(t *testing.T, database *sql.DB, count int) {
	t.Helper()
	for index := range count {
		repositoryID := int64(1000 + index)
		name := fmt.Sprintf("legacy-service-%d", index)
		remote := fmt.Sprintf("https://github.com/acme/legacy-%d.git", index)
		if _, err := database.ExecContext(t.Context(), `
INSERT INTO repositories(id, name, remote_url, default_branch, created_at, updated_at)
VALUES (?, ?, ?, 'main', '2026-08-14T06:00:00Z', '2026-08-14T06:00:00Z')
`, repositoryID, name, remote); err != nil {
			t.Fatalf("seed legacy repository %d: %v", index, err)
		}
	}
}

func upgradeSourceBindingDatabase(t *testing.T, databasePath string) {
	t.Helper()
	store, err := dbsqlite.Open(t.Context(), dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("upgrade v1 database: %v", err)
	}
	status, err := store.Status(t.Context())
	if err != nil {
		t.Fatalf("read upgraded status: %v", err)
	}
	if status.SchemaVersion != 2 {
		t.Fatalf("upgraded schema version = %d, want 2", status.SchemaVersion)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close upgraded store: %v", err)
	}
}

func openSourceBindingVerificationDatabase(t *testing.T, databasePath string) *sql.DB {
	t.Helper()
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := databaseURL.Query()
	query.Add("_pragma", "foreign_keys(1)")
	databaseURL.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatalf("open upgraded verification database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close upgraded verification database: %v", err)
		}
	})
	return database
}

func assertSourceBindingMigrationTables(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, tableName := range []string{
		"repository_identities", "repository_identity_backfill_state", "source_binding_sets", "source_binding_revisions",
		"source_binding_members", "source_binding_current",
	} {
		var found string
		if err := database.QueryRowContext(t.Context(),
			"SELECT name FROM sqlite_schema WHERE type = 'table' AND name = ?", tableName,
		).Scan(&found); err != nil {
			t.Fatalf("find upgraded table %q: %v", tableName, err)
		}
	}
}

func assertLegacyRepositoryIdentityBackfill(t *testing.T, database *sql.DB) {
	t.Helper()
	var legacyIdentity string
	if err := database.QueryRowContext(t.Context(), `
SELECT normalized_value
FROM repository_identities
WHERE repository_id = 9 AND identity_kind = 'GIT_REMOTE'
`).Scan(&legacyIdentity); err != nil {
		t.Fatalf("read backfilled legacy repository identity: %v", err)
	}
	if legacyIdentity != "https://github.com/acme/legacy.git" {
		t.Fatalf("legacy repository identity = %q", legacyIdentity)
	}
	var identityCount int
	if err := database.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM repository_identities",
	).Scan(&identityCount); err != nil || identityCount != 206 {
		t.Fatalf("backfilled repository identity count = %d, error = %v", identityCount, err)
	}
	var lastRepositoryID int64
	var backfillComplete bool
	if err := database.QueryRowContext(t.Context(), `
SELECT last_repository_id, completed_at IS NOT NULL
FROM repository_identity_backfill_state
WHERE singleton = 1
`).Scan(&lastRepositoryID, &backfillComplete); err != nil || !backfillComplete || lastRepositoryID != 1204 {
		t.Fatalf("repository identity backfill state = (%d, %v), error = %v", lastRepositoryID, backfillComplete, err)
	}
}

func seedSourceBindingInvariantRows(t *testing.T, database *sql.DB) {
	t.Helper()
	now := time.Date(2026, 8, 14, 7, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO repositories(id, name, remote_url, default_branch, created_at, updated_at)
VALUES (10, 'orders-service', 'https://github.com/acme/orders.git', 'main', ?, ?);
INSERT INTO data_sources(id, name, source_kind, dsn_environment, created_at, updated_at)
VALUES (20, 'orders-primary', 1, 'ORDERS_DSN', ?, ?),
       (21, 'orders-shadow', 1, 'ORDERS_SHADOW_DSN', ?, ?);
INSERT INTO source_binding_sets(id, repository_id, context_name, created_at)
VALUES (30, 10, 'production', ?), (31, 10, 'staging', ?);
INSERT INTO source_binding_revisions(
    id, binding_set_id, revision_no, expected_revision_no, actor, origin, reason, request_id, created_at
) VALUES (40, 30, 1, 0, 'admin', 1, 'bind production', 'binding-1', ?),
         (41, 31, 1, 0, 'admin', 1, 'bind staging', 'binding-2', ?);
INSERT INTO source_binding_members(binding_revision_id, data_source_id) VALUES (40, 20);
INSERT INTO source_binding_current(binding_set_id, binding_revision_id) VALUES (30, 40);
`, now, now, now, now, now, now, now, now, now, now); err != nil {
		t.Fatalf("seed upgraded source binding schema: %v", err)
	}
}

func assertSQLRejected(t *testing.T, database *sql.DB, statement string, message string) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), statement)
	if err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("statement %q error = %v, want %q", statement, err, message)
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
