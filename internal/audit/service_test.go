package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

type auditCounterIDs struct{ calls int }

func (g *auditCounterIDs) Next(context.Context) (int64, error) {
	g.calls++
	return 1, nil
}

type auditCounterRepository struct{ calls int }

func (r *auditCounterRepository) AppendAuditEvent(context.Context, audit.Event) error {
	r.calls++
	return nil
}

func (r *auditCounterRepository) ListAuditEvents(context.Context, int) ([]audit.Event, error) {
	return nil, nil
}

func TestServiceRejectsUnboundedOrNonObjectDetailsBeforeAllocation(t *testing.T) {
	t.Parallel()

	for name, details := range map[string]json.RawMessage{
		"scalar":    json.RawMessage(`null`),
		"array":     json.RawMessage(`[]`),
		"too deep":  json.RawMessage(`{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":{"j":{"k":{"l":{"m":{"n":{"o":{"p":{}}}}}}}}}}}}}}}}}`),
		"too large": append(json.RawMessage(`{"value":"`), append(bytes.Repeat([]byte{'x'}, 20_001), []byte(`"}`)...)...),
	} {
		t.Run(name, func(t *testing.T) {
			ids := &auditCounterIDs{}
			repository := &auditCounterRepository{}
			service := audit.NewService(repository, ids, time.Now)
			_, err := service.Record(context.Background(), audit.RecordEvent{
				SubjectType: "TEST", SubjectID: 1, Reason: "reason", RequestID: "request", Details: details,
			})
			if !errors.Is(err, audit.ErrInvalidEvent) {
				t.Fatalf("error = %v, want invalid event", err)
			}
			if ids.calls != 0 || repository.calls != 0 {
				t.Fatalf("rejected details allocated/wrote: ids=%d repository=%d", ids.calls, repository.calls)
			}
		})
	}
}

func TestServiceRecordsRetrievableEvent(t *testing.T) {
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

	fixedTime := time.Date(2026, time.August, 11, 12, 30, 0, 0, time.UTC)
	idGenerator, err := id.NewGenerator(5, func() time.Time { return fixedTime })
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

	service := audit.NewService(
		dbsqlite.NewAuditRepository(store),
		idGenerator,
		func() time.Time { return fixedTime },
	)
	expectedRevision := 2
	recorded, err := service.Record(ctx, audit.RecordEvent{
		Actor:            "web-editor@example.test",
		Origin:           audit.OriginWeb,
		Action:           "RELATION_REVISION_PROPOSED",
		SubjectType:      "RELATION",
		SubjectID:        9001,
		Reason:           "Correct the target column.",
		RequestID:        "request-001",
		ExpectedRevision: &expectedRevision,
		Details:          []byte(`{"source":"relation-details"}`),
	})
	if err != nil {
		t.Fatalf("record event: %v", err)
	}
	if recorded.ID <= 0 || !recorded.OccurredAt.Equal(fixedTime) {
		t.Fatalf("recorded event = %#v", recorded)
	}

	events, err := service.ListProject(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].ID != recorded.ID || events[0].Reason != "Correct the target column." {
		t.Fatalf("retrieved event = %#v, want %#v", events[0], recorded)
	}
}
