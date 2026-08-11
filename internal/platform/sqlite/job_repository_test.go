package sqlite_test

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/jobs"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestJobRepositoryRecoveryTerminatesOrphanedSchemaScanRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "dbgraph.sqlite")
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	recoveredAt := time.Date(2026, 8, 11, 17, 5, 0, 0, time.UTC)
	startedAt := recoveredAt.Add(-time.Minute)
	if err := dbsqlite.NewProjectRepository(store).CreateProject(ctx, catalog.Project{
		ID: 10, Name: "Recovery", CreatedAt: startedAt, UpdatedAt: startedAt,
	}); err != nil {
		t.Fatal(err)
	}
	catalogRepository := dbsqlite.NewCatalogRepository(store, nil)
	if err := catalogRepository.CreateDataSource(ctx, catalog.DataSource{
		ID: 20, ProjectID: 10, Name: "interrupted", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "INTERRUPTED_SCAN_DSN", CreatedAt: startedAt, UpdatedAt: startedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalogRepository.BeginSchemaScan(ctx, catalog.SchemaScanRun{
		ID: 30, ProjectID: 10, DataSourceID: 20, StartedAt: startedAt,
	}); err != nil {
		t.Fatal(err)
	}

	recoveredJobs, err := dbsqlite.NewJobRepository(store).RecoverRunningSchemaScans(ctx, recoveredAt)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredJobs != 0 {
		t.Fatalf("recovered jobs = %d, want 0", recoveredJobs)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	databaseURL.RawQuery = query.Encode()
	reader, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	var runID int64
	var projectID int64
	var dataSourceID int64
	var status int
	var errorCode sql.NullString
	var errorMessage sql.NullString
	var persistedStartedAt string
	var completedAt sql.NullString
	if err := reader.QueryRowContext(ctx, `
SELECT id, project_id, data_source_id, status, error_code, error_message, started_at, completed_at
FROM schema_scan_runs
WHERE id = ?
`, 30).Scan(
		&runID, &projectID, &dataSourceID, &status, &errorCode, &errorMessage,
		&persistedStartedAt, &completedAt,
	); err != nil {
		t.Fatal(err)
	}
	if runID != 30 || projectID != 10 || dataSourceID != 20 || status != 3 ||
		errorCode.String != "PROCESS_INTERRUPTED" || errorMessage.String != "schema scan did not complete" ||
		persistedStartedAt != startedAt.Format(time.RFC3339Nano) ||
		completedAt.String != recoveredAt.Format(time.RFC3339Nano) {
		t.Fatalf(
			"recovered run id=%d project=%d source=%d status=%d code=%q message=%q started=%q completed=%q",
			runID, projectID, dataSourceID, status, errorCode.String, errorMessage.String,
			persistedStartedAt, completedAt.String,
		)
	}
}

func TestJobRepositoryRecoversRunningSchemaScans(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := dbsqlite.NewJobRepository(store)
	createdAt := time.Date(2026, 8, 11, 17, 0, 0, 0, time.UTC)
	if err := dbsqlite.NewProjectRepository(store).CreateProject(ctx, catalog.Project{
		ID: 10, Name: "Jobs", CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{
		ID: 501, ProjectID: 10, Type: jobs.TypeSchemaScan, Status: jobs.StatusPending,
		Payload: []byte(`{"dataSourceId":"20"}`), CreatedAt: createdAt, RevisionNo: 1,
	}
	if err := repository.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimNextSchemaScan(ctx, createdAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != jobs.StatusRunning || claimed.RevisionNo != 2 {
		t.Fatalf("claimed job = %#v", claimed)
	}

	recovered, err := repository.RecoverRunningSchemaScans(ctx, createdAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered jobs = %d, want 1", recovered)
	}
	got, err := repository.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != jobs.StatusPending || got.StartedAt != nil || got.RevisionNo != 3 {
		t.Fatalf("recovered job = %#v", got)
	}
}
