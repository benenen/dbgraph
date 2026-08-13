package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/jobs"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/relations"
)

type sequenceIDs struct {
	mu   sync.Mutex
	next int64
}

func (s *sequenceIDs) Next(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return s.next, nil
}

type schemaScanStore struct {
	mu             sync.Mutex
	job            jobs.Job
	audit          audit.Event
	completed      chan jobs.Job
	recoveryErrors []error
	claimErrors    []error
	recoveries     int
}

func (s *schemaScanStore) CreateSchemaScanJob(_ context.Context, job jobs.Job, event audit.Event, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.job = job
	s.audit = event
	return nil
}

func (s *schemaScanStore) ClaimNextSchemaScan(_ context.Context, startedAt time.Time) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.claimErrors) > 0 {
		err := s.claimErrors[0]
		s.claimErrors = append([]error(nil), s.claimErrors[1:]...)
		return jobs.Job{}, err
	}
	if s.job.Status != jobs.StatusPending {
		return jobs.Job{}, jobs.ErrNoPendingJob
	}
	s.job.Status = jobs.StatusRunning
	s.job.RevisionNo++
	s.job.StartedAt = &startedAt
	return s.job, nil
}

func (s *schemaScanStore) RecoverRunningSchemaScans(_ context.Context, _ time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoveries++
	if len(s.recoveryErrors) > 0 {
		err := s.recoveryErrors[0]
		s.recoveryErrors = append([]error(nil), s.recoveryErrors[1:]...)
		return 0, err
	}
	if s.job.Status != jobs.StatusRunning {
		return 0, nil
	}
	s.job.Status = jobs.StatusPending
	s.job.StartedAt = nil
	s.job.RevisionNo++
	return 1, nil
}

func (s *schemaScanStore) FinishSchemaScan(_ context.Context, completion jobs.SchemaScanCompletion) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job.ID != completion.JobID || s.job.Status != jobs.StatusRunning || s.job.RevisionNo != completion.ExpectedRevisionNo {
		return jobs.Job{}, jobs.ErrJobConflict
	}
	s.job.Status = completion.Status
	s.job.Result = append(json.RawMessage(nil), completion.Result...)
	s.job.ErrorCode = completion.ErrorCode
	s.job.ErrorMessage = completion.ErrorMessage
	s.job.CompletedAt = &completion.CompletedAt
	s.job.RevisionNo++
	finished := s.job
	select {
	case s.completed <- finished:
	default:
	}
	return finished, nil
}

func (s *schemaScanStore) GetJob(_ context.Context, jobID int64) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job.ID != jobID {
		return jobs.Job{}, jobs.ErrJobNotFound
	}
	return s.job, nil
}

type dataSourceCatalog struct {
	source catalog.DataSource
	// linkedProject is the only project that may scan the source, mirroring
	// project_data_sources.
	linkedProject int64
}

func (s dataSourceCatalog) GetDataSource(context.Context, int64) (catalog.DataSource, error) {
	return s.source, nil
}

// The link is what authorizes a scan, so the double answers only for the
// project that owns the fixture.
func (s dataSourceCatalog) GetProjectDataSource(
	ctx context.Context,
	dataSourceID int64,
) (catalog.DataSource, error) {
	if s.linkedProject != 0 && projectID != s.linkedProject {
		return catalog.DataSource{}, catalog.ErrDataSourceNotFound
	}
	return s.GetDataSource(ctx, dataSourceID)
}

type schemaRunner struct {
	called chan int64
	result catalog.PublishedSnapshot
	err    error
}

func (r schemaRunner) Run(_ context.Context, dataSourceID int64) (catalog.PublishedSnapshot, error) {
	r.called <- dataSourceID
	return r.result, r.err
}

type incrementalJobRunner struct {
	called chan []string
	result catalog.PublishedSnapshot
}

func (r incrementalJobRunner) Run(context.Context, int64, int64) (catalog.PublishedSnapshot, error) {
	return catalog.PublishedSnapshot{}, errors.New("full schema scan was called")
}

func (r incrementalJobRunner) RunIncremental(_ context.Context, _ int64, dataSourceID int64,
	tables []string,
) (catalog.PublishedSnapshot, error) {
	if dataSourceID != 8 {
		return catalog.PublishedSnapshot{}, errors.New("wrong data source")
	}
	r.called <- append([]string(nil), tables...)
	return r.result, nil
}

func TestSchemaScanCoordinatorDispatchesIncrementalTableScope(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, 8, 11, 14, 5, 0, 0, time.UTC)
	store := &schemaScanStore{completed: make(chan jobs.Job, 1)}
	runner := incrementalJobRunner{
		called: make(chan []string, 1),
		result: catalog.PublishedSnapshot{ScanRunID: 95, NodeCount: 4, PublishedAt: fixedTime},
	}
	coordinator := jobs.NewSchemaScanCoordinator(
		store,
		dataSourceCatalog{source: catalog.DataSource{ID: 8, Kind: catalog.DataSourceMySQL}},
		runner,
		&sequenceIDs{next: 120},
		func() time.Time { return fixedTime },
	)
	if _, err := coordinator.Start(context.Background(), jobs.StartSchemaScan{
		Tables:    []string{"learn.orders"},
		Principal: relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginAgent},
		Reason:    "Refresh changed table", RequestID: "scan-incremental-1",
	}); err != nil {
		t.Fatalf("queue incremental schema scan: %v", err)
	}
	completed := runCoordinatorUntilCompletion(t, coordinator, store.completed)
	if completed.Status != jobs.StatusSucceeded {
		t.Fatalf("incremental job = %#v", completed)
	}
	select {
	case tables := <-runner.called:
		if len(tables) != 1 || tables[0] != "learn.orders" {
			t.Fatalf("incremental tables = %#v", tables)
		}
	default:
		t.Fatal("incremental runner was not called")
	}
}

func TestSchemaScanCoordinatorQueuesAuditsAndCompletesJob(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	store := &schemaScanStore{completed: make(chan jobs.Job, 1)}
	runner := schemaRunner{
		called: make(chan int64, 1),
		result: catalog.PublishedSnapshot{ScanRunID: 91, NodeCount: 12, StaleCount: 2, PublishedAt: fixedTime},
	}
	coordinator := jobs.NewSchemaScanCoordinator(
		store,
		dataSourceCatalog{source: catalog.DataSource{ID: 8, Kind: catalog.DataSourceMySQL}},
		runner,
		&sequenceIDs{next: 100},
		func() time.Time { return fixedTime },
	)

	created, err := coordinator.Start(context.Background(), jobs.StartSchemaScan{
		Principal: relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginAgent},
		Reason:    "Refresh source metadata", RequestID: "scan-1",
	})
	if err != nil {
		t.Fatalf("start schema scan: %v", err)
	}
	if created.Status != jobs.StatusPending || created.RevisionNo != 1 {
		t.Fatalf("created job = %#v", created)
	}
	if store.audit.Action != "SCHEMA_SCAN_QUEUED" || store.audit.SubjectID != created.ID || store.audit.RequestID != "scan-1" {
		t.Fatalf("audit event = %#v", store.audit)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	select {
	case dataSourceID := <-runner.called:
		if dataSourceID != 8 {
			t.Fatalf("runner data source = %d", dataSourceID)
		}
	case <-time.After(time.Second):
		t.Fatal("schema runner was not called")
	}
	select {
	case completed := <-store.completed:
		if completed.Status != jobs.StatusSucceeded || completed.RevisionNo != 3 {
			t.Fatalf("completed job = %#v", completed)
		}
		var result struct {
			ScanRunID  string `json:"scanRunId"`
			NodeCount  int    `json:"nodeCount"`
			StaleCount int    `json:"staleCount"`
		}
		if err := json.Unmarshal(completed.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.ScanRunID != "91" || result.NodeCount != 12 || result.StaleCount != 2 {
			t.Fatalf("job result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("schema scan job was not completed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker shutdown: %v", err)
	}
}

func TestSchemaScanCoordinatorRejectsUnauthorizedOrMismatchedProject(t *testing.T) {
	t.Parallel()

	store := &schemaScanStore{completed: make(chan jobs.Job, 1)}
	coordinator := jobs.NewSchemaScanCoordinator(
		store,
		dataSourceCatalog{source: catalog.DataSource{ID: 8, Kind: catalog.DataSourceMySQL}, linkedProject: 7},
		schemaRunner{called: make(chan int64, 1)},
		&sequenceIDs{},
		time.Now,
	)
	viewer := jobs.StartSchemaScan{
		Principal: relations.Principal{Actor: "viewer", Role: relations.RoleViewer, Origin: audit.OriginWeb},
		Reason:    "Refresh source metadata", RequestID: "scan-2",
	}
	if _, err := coordinator.Start(context.Background(), viewer); !errors.Is(err, jobs.ErrForbidden) {
		t.Fatalf("viewer start error = %v, want forbidden", err)
	}
	viewer.Principal = relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb}
	viewer.ProjectID = 9
	// A project that has not linked the source is told the source does not
	// exist, rather than that it exists and belongs to someone else.
	if _, err := coordinator.Start(context.Background(), viewer); !errors.Is(err, catalog.ErrDataSourceNotFound) {
		t.Fatalf("cross-project start error = %v, want data source not found", err)
	}
	if store.job.ID != 0 || store.audit.ID != 0 {
		t.Fatalf("rejected command persisted state: job=%#v audit=%#v", store.job, store.audit)
	}
}

func TestSchemaScanCoordinatorPersistsLifecycleAndAudit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixedTime := time.Date(2026, 8, 11, 15, 0, 0, 0, time.UTC)
	ids := &sequenceIDs{next: 1_000}
	projectService := catalog.NewProjectService(dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime })
	project, err := projectService.Create(ctx, catalog.CreateProject{Name: "Integration"})
	if err != nil {
		t.Fatal(err)
	}
	catalogService := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })
	source, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{
		Name: "primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "PRIMARY_MYSQL_DSN",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := schemaRunner{called: make(chan int64, 1), result: catalog.PublishedSnapshot{
		ScanRunID: 2_000, NodeCount: 5, PublishedAt: fixedTime,
	}}
	coordinator := jobs.NewSchemaScanCoordinator(
		dbsqlite.NewJobRepository(store), catalogService, runner, ids, func() time.Time { return fixedTime },
	)
	job, err := coordinator.Start(ctx, jobs.StartSchemaScan{
		Principal: relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb},
		Reason:    "Run integration scan", RequestID: "web-request-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	workerContext, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(workerContext) }()
	deadline := time.Now().Add(2 * time.Second)
	var persisted jobs.Job
	for time.Now().Before(deadline) {
		persisted, err = coordinator.Get(ctx, job.ID)
		if err == nil && persisted.Status == jobs.StatusSucceeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if persisted.Status != jobs.StatusSucceeded || persisted.RevisionNo != 3 {
		t.Fatalf("persisted job = %#v, err=%v", persisted, err)
	}
	events, err := dbsqlite.NewAuditRepository(store).ListAuditEvents(ctx, project.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "SCHEMA_SCAN_QUEUED" || events[0].SubjectID != job.ID {
		t.Fatalf("audit events = %#v", events)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSchemaScanCoordinatorRecoversRunningJobAfterRestart(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, 8, 11, 16, 0, 0, 0, time.UTC)
	startedAt := fixedTime.Add(-time.Minute)
	store := &schemaScanStore{
		completed: make(chan jobs.Job, 1),
		job: jobs.Job{
			ID: 41, Type: jobs.TypeSchemaScan, Status: jobs.StatusRunning,
			Payload: json.RawMessage(`{"dataSourceId":"8"}`), CreatedAt: fixedTime.Add(-2 * time.Minute),
			StartedAt: &startedAt, RevisionNo: 2,
		},
	}
	coordinator := jobs.NewSchemaScanCoordinator(
		store,
		dataSourceCatalog{source: catalog.DataSource{ID: 8, Kind: catalog.DataSourceMySQL}},
		schemaRunner{called: make(chan int64, 1), result: catalog.PublishedSnapshot{ScanRunID: 92, PublishedAt: fixedTime}},
		&sequenceIDs{},
		func() time.Time { return fixedTime },
	)

	completed := runCoordinatorUntilCompletion(t, coordinator, store.completed)
	if completed.ID != 41 || completed.Status != jobs.StatusSucceeded || completed.RevisionNo != 5 {
		t.Fatalf("recovered job = %#v", completed)
	}
	if store.recoveries != 1 {
		t.Fatalf("recovery calls = %d, want 1", store.recoveries)
	}
}

func TestSchemaScanCoordinatorRetriesTemporaryStoreBackpressure(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, 8, 11, 16, 30, 0, 0, time.UTC)
	store := &schemaScanStore{
		completed:      make(chan jobs.Job, 1),
		recoveryErrors: []error{jobs.ErrStoreBusy},
		claimErrors:    []error{jobs.ErrStoreBusy},
		job: jobs.Job{
			ID: 42, Type: jobs.TypeSchemaScan, Status: jobs.StatusPending,
			Payload: json.RawMessage(`{"dataSourceId":"8"}`), CreatedAt: fixedTime, RevisionNo: 1,
		},
	}
	coordinator := jobs.NewSchemaScanCoordinator(
		store,
		dataSourceCatalog{source: catalog.DataSource{ID: 8, Kind: catalog.DataSourceMySQL}},
		schemaRunner{called: make(chan int64, 1), result: catalog.PublishedSnapshot{ScanRunID: 93, PublishedAt: fixedTime}},
		&sequenceIDs{},
		func() time.Time { return fixedTime },
	)

	completed := runCoordinatorUntilCompletion(t, coordinator, store.completed)
	if completed.ID != 42 || completed.Status != jobs.StatusSucceeded {
		t.Fatalf("job after temporary backpressure = %#v", completed)
	}
	if store.recoveries < 2 {
		t.Fatalf("recovery calls = %d, want a retry", store.recoveries)
	}
}
