package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestSchemaScanCoordinatorPersistsRunnerFailure(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, time.August, 11, 20, 0, 0, 0, time.UTC)
	store := &schemaScanStore{completed: make(chan jobs.Job, 1)}
	coordinator := jobs.NewSchemaScanCoordinator(
		store,
		dataSourceCatalog{source: catalog.DataSource{ID: 8, ProjectID: 7, Kind: catalog.DataSourceMySQL}},
		schemaRunner{called: make(chan int64, 1), err: errors.New("source unavailable")},
		&sequenceIDs{next: 200},
		func() time.Time { return fixedTime },
	)
	created, err := coordinator.Start(context.Background(), validSchemaScanCommand())
	if err != nil {
		t.Fatalf("start schema scan: %v", err)
	}

	completed := runCoordinatorUntilCompletion(t, coordinator, store.completed)
	if completed.ID != created.ID || completed.Status != jobs.StatusFailed || completed.RevisionNo != 3 {
		t.Fatalf("failed job = %#v", completed)
	}
	if completed.ErrorCode != "SCHEMA_SCAN_FAILED" || completed.ErrorMessage != "schema scan did not complete" {
		t.Fatalf("failure details = %q/%q", completed.ErrorCode, completed.ErrorMessage)
	}
	if completed.CompletedAt == nil || !completed.CompletedAt.Equal(fixedTime) {
		t.Fatalf("completion time = %v, want %s", completed.CompletedAt, fixedTime)
	}
	retrieved, err := coordinator.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get failed job: %v", err)
	}
	if retrieved.Status != jobs.StatusFailed || retrieved.ErrorCode != "SCHEMA_SCAN_FAILED" {
		t.Fatalf("retrieved failed job = %#v", retrieved)
	}
}

func TestSchemaScanCoordinatorResumesPendingJobAfterRestart(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, time.August, 11, 20, 30, 0, 0, time.UTC)
	store := &schemaScanStore{completed: make(chan jobs.Job, 1)}
	catalogSource := dataSourceCatalog{
		source: catalog.DataSource{ID: 8, ProjectID: 7, Kind: catalog.DataSourceMySQL},
	}
	ids := &sequenceIDs{next: 300}
	beforeRestart := jobs.NewSchemaScanCoordinator(
		store,
		catalogSource,
		schemaRunner{called: make(chan int64, 1)},
		ids,
		func() time.Time { return fixedTime },
	)
	created, err := beforeRestart.Start(context.Background(), validSchemaScanCommand())
	if err != nil {
		t.Fatalf("queue schema scan: %v", err)
	}

	afterRestart := jobs.NewSchemaScanCoordinator(
		store,
		catalogSource,
		schemaRunner{
			called: make(chan int64, 1),
			result: catalog.PublishedSnapshot{
				ScanRunID: 501, NodeCount: 4, PublishedAt: fixedTime,
			},
		},
		ids,
		func() time.Time { return fixedTime },
	)
	completed := runCoordinatorUntilCompletion(t, afterRestart, store.completed)
	if completed.ID != created.ID || completed.Status != jobs.StatusSucceeded {
		t.Fatalf("restarted worker completed job = %#v, queued = %#v", completed, created)
	}
	var result struct {
		ScanRunID string `json:"scanRunId"`
	}
	if err := json.Unmarshal(completed.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.ScanRunID != "501" {
		t.Fatalf("scan run ID = %q, want 501", result.ScanRunID)
	}
}

func TestSchemaScanCoordinatorRejectsInvalidPublicInputs(t *testing.T) {
	t.Parallel()

	coordinator := jobs.NewSchemaScanCoordinator(
		&schemaScanStore{completed: make(chan jobs.Job, 1)},
		dataSourceCatalog{source: catalog.DataSource{ID: 8, ProjectID: 7, Kind: catalog.DataSourceMySQL}},
		schemaRunner{called: make(chan int64, 1)},
		&sequenceIDs{},
		time.Now,
	)

	testCases := []struct {
		name     string
		mutate   func(*jobs.StartSchemaScan)
		expected error
	}{
		{name: "missing project", mutate: func(command *jobs.StartSchemaScan) { command.ProjectID = 0 }, expected: jobs.ErrInvalidJob},
		{name: "missing data source", mutate: func(command *jobs.StartSchemaScan) { command.DataSourceID = 0 }, expected: jobs.ErrInvalidJob},
		{name: "missing actor", mutate: func(command *jobs.StartSchemaScan) { command.Principal.Actor = " " }, expected: jobs.ErrInvalidJob},
		{name: "invalid origin", mutate: func(command *jobs.StartSchemaScan) { command.Principal.Origin = audit.OriginSystem }, expected: jobs.ErrInvalidJob},
		{name: "missing reason", mutate: func(command *jobs.StartSchemaScan) { command.Reason = "" }, expected: jobs.ErrInvalidJob},
		{name: "missing request ID", mutate: func(command *jobs.StartSchemaScan) { command.RequestID = "" }, expected: jobs.ErrInvalidJob},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			command := validSchemaScanCommand()
			testCase.mutate(&command)
			if _, err := coordinator.Start(context.Background(), command); !errors.Is(err, testCase.expected) {
				t.Fatalf("Start error = %v, want %v", err, testCase.expected)
			}
		})
	}

	if _, err := coordinator.Get(context.Background(), 0); !errors.Is(err, jobs.ErrInvalidJob) {
		t.Fatalf("Get(0) error = %v, want invalid job", err)
	}
	if _, err := coordinator.Get(context.Background(), 999); !errors.Is(err, jobs.ErrJobNotFound) {
		t.Fatalf("Get(missing) error = %v, want not found", err)
	}
	if err := jobs.NewSchemaScanCoordinator(nil, nil, nil, nil, nil).Run(context.Background()); !errors.Is(err, jobs.ErrInvalidJob) {
		t.Fatalf("Run without dependencies error = %v, want invalid job", err)
	}
}

func TestJobServiceRejectsInvalidCommandsBeforeUsingDependencies(t *testing.T) {
	t.Parallel()

	service := jobs.NewService(nil, nil, nil)
	testCases := []jobs.CreateJob{
		{ProjectID: 0, Type: jobs.TypeSchemaScan, Payload: json.RawMessage(`{}`)},
		{ProjectID: 1, Type: jobs.Type(99), Payload: json.RawMessage(`{}`)},
		{ProjectID: 1, Type: jobs.TypeSchemaScan, Payload: json.RawMessage(`[]`)},
		{ProjectID: 1, Type: jobs.TypeSchemaScan, Payload: json.RawMessage(`{"source":`)},
	}
	for _, command := range testCases {
		if _, err := service.Create(context.Background(), command); !errors.Is(err, jobs.ErrInvalidJob) {
			t.Fatalf("Create(%s) error = %v, want invalid job", command.Payload, err)
		}
	}
	if _, err := service.Get(context.Background(), -1); !errors.Is(err, jobs.ErrInvalidJob) {
		t.Fatalf("Get(-1) error = %v, want invalid job", err)
	}
}

func runCoordinatorUntilCompletion(
	t *testing.T,
	coordinator *jobs.SchemaScanCoordinator,
	completedJobs <-chan jobs.Job,
) jobs.Job {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- coordinator.Run(ctx)
	}()

	var completed jobs.Job
	select {
	case completed = <-completedJobs:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("schema scan job was not completed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker shutdown: %v", err)
	}
	return completed
}

func validSchemaScanCommand() jobs.StartSchemaScan {
	return jobs.StartSchemaScan{
		ProjectID: 7, DataSourceID: 8,
		Principal: relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb},
		Reason:    "Refresh source metadata", RequestID: "scan-boundary-1",
	}
}
