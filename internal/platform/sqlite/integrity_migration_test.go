package sqlite_test

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestHistoricalRelationComponentsAreAppendOnly(t *testing.T) {
	t.Parallel()

	database := openIntegrityFixture(t)
	for _, mutation := range []string{
		"UPDATE relation_version_endpoints SET node_id = 5 WHERE version_id = 9 AND endpoint_kind = 1",
		"DELETE FROM relation_version_endpoints WHERE version_id = 9 AND endpoint_kind = 1",
		"UPDATE relation_references SET node_id = 5 WHERE version_id = 9 AND reference_kind = 1",
		"DELETE FROM relation_references WHERE version_id = 9 AND reference_kind = 1",
		"UPDATE relation_evidence SET symbol = 'changed' WHERE version_id = 9 AND ordinal = 1",
		"DELETE FROM relation_evidence WHERE version_id = 9 AND ordinal = 1",
	} {
		expectIntegrityFailure(t, database, mutation)
	}
}

func TestFoundationEnumsAndRelationProjectionPairsAreDatabaseEnforced(t *testing.T) {
	t.Parallel()

	database := openIntegrityFixture(t)
	timestamp := "2026-08-11T15:30:00Z"
	fingerprint := strings.Repeat("c", 64)
	checks := []struct {
		statement string
		arguments []any
	}{
		{
			`INSERT INTO data_sources(id, name, source_kind, dsn_environment, created_at, updated_at)
             VALUES (20, 'invalid-kind', 99, 'INVALID_KIND_DSN', ?, ?)`,
			[]any{timestamp, timestamp},
		},
		{
			`INSERT INTO schema_scan_runs(id, data_source_id, status, started_at)
             VALUES (20, 2, 99, ?)`,
			[]any{timestamp},
		},
		{
			`INSERT INTO nodes(id, data_source_id, stable_key, kind, created_at)
             VALUES (20, 2, 'invalid-kind', 99, ?)`,
			[]any{timestamp},
		},
		{
			`INSERT INTO node_versions(
                 id, node_id, scan_run_id, status, name, qualified_name, nullable, created_at
             ) VALUES (20, 4, 3, 99, 'invalid', 'learn.invalid', 0, ?)`,
			[]any{timestamp},
		},
		{
			`INSERT INTO jobs(id, job_type, status, payload_json, created_at)
             VALUES (20, 99, 1, '{}', ?)`,
			[]any{timestamp},
		},
		{
			`INSERT INTO jobs(id, job_type, status, payload_json, created_at)
             VALUES (20, 1, 99, '{}', ?)`,
			[]any{timestamp},
		},
		{
			`INSERT INTO audit_events(
                 id, actor, origin, action, subject_type, reason, request_id, occurred_at
             ) VALUES (20, 'actor', 99, 'INVALID', 'TEST', 'reason', 'request', ?)`,
			[]any{timestamp},
		},
		{
			`INSERT INTO relations(id, relation_type, create_fingerprint, created_at)
             VALUES (20, 99, ?, ?)`,
			[]any{fingerprint, timestamp},
		},
		{
			`INSERT INTO relation_current(
                 relation_id, latest_revision_no, active_version_id, proposed_version_id, status, updated_at
             ) VALUES (8, 1, 11, NULL, 1, ?)`,
			[]any{timestamp},
		},
	}
	for _, check := range checks {
		expectIntegrityFailure(t, database, check.statement, check.arguments...)
	}
}

func openIntegrityFixture(t *testing.T) *sql.DB {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "dbgraph.sqlite")
	store, err := dbsqlite.Open(context.Background(), dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := databaseURL.Query()
	query.Add("_pragma", "foreign_keys(1)")
	databaseURL.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatalf("open integrity fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close integrity fixture: %v", err)
		}
	})
	timestamp := "2026-08-11T15:30:00Z"
	transaction, err := database.Begin()
	if err != nil {
		t.Fatalf("begin integrity fixture: %v", err)
	}
	statements := []struct {
		query     string
		arguments []any
	}{
		{`INSERT INTO data_sources(
             id, name, source_kind, dsn_environment, created_at, updated_at
         ) VALUES (2, 'source', 1, 'INTEGRITY_DSN', ?, ?)`, []any{timestamp, timestamp}},
		{`INSERT INTO schema_scan_runs(
             id, data_source_id, status, started_at, completed_at
         ) VALUES (3, 2, 2, ?, ?)`, []any{timestamp, timestamp}},
		{`INSERT INTO nodes(id, data_source_id, stable_key, kind, created_at)
         VALUES (4, 2, 'source-node', 4, ?)`, []any{timestamp}},
		{`INSERT INTO nodes(id, data_source_id, stable_key, kind, created_at)
         VALUES (5, 2, 'target-node', 4, ?)`, []any{timestamp}},
		{`INSERT INTO node_versions(
             id, node_id, scan_run_id, status, name, qualified_name, nullable, created_at
         ) VALUES (6, 4, 3, 1, 'source', 'learn.source', 0, ?)`, []any{timestamp}},
		{`INSERT INTO node_versions(
             id, node_id, scan_run_id, status, name, qualified_name, nullable, created_at
         ) VALUES (7, 5, 3, 1, 'target', 'learn.target', 0, ?)`, []any{timestamp}},
		{`INSERT INTO relations(id, relation_type, create_fingerprint, created_at)
         VALUES (8, 1, ?, ?)`, []any{strings.Repeat("a", 64), timestamp}},
		{`INSERT INTO relation_versions(
             id, relation_id, revision_no, proposal_kind, confidence_bps, transform_json,
             content_fingerprint, actor, origin, reason, request_id, created_at
         ) VALUES (9, 8, 1, 1, 9000, '{"kind":"column","nodeId":4}', ?,
             'agent', 1, 'evidence', 'request-1', ?)`, []any{strings.Repeat("b", 64), timestamp}},
		{`INSERT INTO relation_version_endpoints(version_id, endpoint_kind, node_id)
         VALUES (9, 1, 4), (9, 2, 5)`, nil},
		{`INSERT INTO relation_references(version_id, reference_kind, node_id) VALUES (9, 1, 4)`, nil},
		{`INSERT INTO relation_evidence(
             version_id, ordinal, evidence_kind, repository_name, commit_hash,
             file_path, symbol, start_line, end_line
         ) VALUES (9, 1, 1, 'repo', 'abc123', 'src/File.java', 'copyValue', 10, 12)`, nil},
		{`INSERT INTO relations(id, relation_type, create_fingerprint, created_at)
         VALUES (10, 1, ?, ?)`, []any{strings.Repeat("d", 64), timestamp}},
		{`INSERT INTO relation_versions(
             id, relation_id, revision_no, proposal_kind, confidence_bps, transform_json,
             content_fingerprint, actor, origin, reason, request_id, created_at
         ) VALUES (11, 10, 1, 1, 9000, '{"kind":"column","nodeId":4}', ?,
             'agent', 1, 'other', 'request-2', ?)`, []any{strings.Repeat("e", 64), timestamp}},
	}
	for _, statement := range statements {
		if _, err := transaction.Exec(statement.query, statement.arguments...); err != nil {
			_ = transaction.Rollback()
			t.Fatalf("seed integrity fixture: %v", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit integrity fixture: %v", err)
	}
	return database
}

func expectIntegrityFailure(t *testing.T, database *sql.DB, statement string, arguments ...any) {
	t.Helper()
	transaction, err := database.Begin()
	if err != nil {
		t.Fatalf("begin invalid mutation: %v", err)
	}
	_, executeErr := transaction.Exec(statement, arguments...)
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("rollback invalid mutation: %v", err)
	}
	if executeErr == nil {
		t.Fatalf("database accepted invalid statement: %s", statement)
	}
}
