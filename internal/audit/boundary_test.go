package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
)

func TestServiceRejectsInvalidMetadataBeforeAllocation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		mutate func(*audit.RecordEvent)
	}{
		{name: "missing subject", mutate: func(event *audit.RecordEvent) { event.SubjectID = 0 }},
		{name: "invalid origin", mutate: func(event *audit.RecordEvent) { event.Origin = audit.Origin(99) }},
		{name: "missing actor", mutate: func(event *audit.RecordEvent) { event.Actor = " " }},
		{name: "long actor", mutate: func(event *audit.RecordEvent) { event.Actor = strings.Repeat("a", 201) }},
		{name: "missing action", mutate: func(event *audit.RecordEvent) { event.Action = "" }},
		{name: "long action", mutate: func(event *audit.RecordEvent) { event.Action = strings.Repeat("a", 101) }},
		{name: "missing subject type", mutate: func(event *audit.RecordEvent) { event.SubjectType = "" }},
		{name: "long subject type", mutate: func(event *audit.RecordEvent) { event.SubjectType = strings.Repeat("s", 101) }},
		{name: "missing reason", mutate: func(event *audit.RecordEvent) { event.Reason = "" }},
		{name: "long reason", mutate: func(event *audit.RecordEvent) { event.Reason = strings.Repeat("r", 2001) }},
		{name: "missing request ID", mutate: func(event *audit.RecordEvent) { event.RequestID = "" }},
		{name: "long request ID", mutate: func(event *audit.RecordEvent) { event.RequestID = strings.Repeat("r", 201) }},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ids := &auditCounterIDs{}
			repository := &auditCounterRepository{}
			service := audit.NewService(repository, ids, time.Now)
			command := validAuditRecord()
			testCase.mutate(&command)

			if _, err := service.Record(context.Background(), command); !errors.Is(err, audit.ErrInvalidEvent) {
				t.Fatalf("Record error = %v, want invalid event", err)
			}
			if ids.calls != 0 || repository.calls != 0 {
				t.Fatalf("invalid event allocated or persisted: ids=%d repository=%d", ids.calls, repository.calls)
			}
		})
	}
}

func TestServiceCopiesCallerOwnedAuditInput(t *testing.T) {
	t.Parallel()

	ids := &auditCounterIDs{}
	repository := &recordingAuditRepository{}
	fixedTime := time.Date(2026, time.August, 11, 22, 0, 0, 0, time.UTC)
	service := audit.NewService(repository, ids, func() time.Time { return fixedTime })
	expectedRevision := 2
	details := json.RawMessage(`{"source":"web"}`)
	command := validAuditRecord()
	command.Actor = "  editor  "
	command.Reason = "  Correct relation  "
	command.RequestID = "  request-1  "
	command.ExpectedRevision = &expectedRevision
	command.Details = details

	recorded, err := service.Record(context.Background(), command)
	if err != nil {
		t.Fatalf("record audit event: %v", err)
	}
	expectedRevision = 99
	details[2] = 'X'
	if recorded.Actor != "editor" || recorded.Reason != "Correct relation" || recorded.RequestID != "request-1" {
		t.Fatalf("metadata was not normalized: %#v", recorded)
	}
	if recorded.ExpectedRevision == nil || *recorded.ExpectedRevision != 2 {
		t.Fatalf("expected revision changed with caller input: %v", recorded.ExpectedRevision)
	}
	if string(recorded.Details) != `{"source":"web"}` {
		t.Fatalf("details changed with caller input: %s", recorded.Details)
	}
	if !recorded.OccurredAt.Equal(fixedTime) || repository.event.ID != recorded.ID {
		t.Fatalf("recorded event = %#v, repository event = %#v", recorded, repository.event)
	}
}

func TestServiceRejectsInvalidAuditListBoundaries(t *testing.T) {
	t.Parallel()

	service := audit.NewService(&auditCounterRepository{}, &auditCounterIDs{}, nil)
	for _, input := range []struct {
		limit int
	}{{0}, {1001}} {
		if _, err := service.ListEvents(context.Background(), input.limit); !errors.Is(err, audit.ErrInvalidEvent) {
			t.Fatalf("ListEvents(%d) error = %v", input.limit, err)
		}
	}
}

type recordingAuditRepository struct {
	event audit.Event
}

func (r *recordingAuditRepository) AppendAuditEvent(_ context.Context, event audit.Event) error {
	r.event = event
	return nil
}

func (r *recordingAuditRepository) ListAuditEvents(context.Context, int) ([]audit.Event, error) {
	return nil, nil
}

func validAuditRecord() audit.RecordEvent {
	return audit.RecordEvent{
		Actor: "actor", Origin: audit.OriginWeb,
		Action: "RELATION_PROPOSED", SubjectType: "RELATION", SubjectID: 2,
		Reason: "Evidence supports the relation", RequestID: "request-1", Details: json.RawMessage(`{}`),
	}
}
