package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/jsoncheck"
)

var ErrInvalidEvent = errors.New("invalid audit event")

type Origin int

const (
	OriginAgent  Origin = 1
	OriginWeb    Origin = 2
	OriginSystem Origin = 3
)

type Event struct {
	ID               int64
	Actor            string
	Origin           Origin
	Action           string
	SubjectType      string
	SubjectID        int64
	Reason           string
	RequestID        string
	ExpectedRevision *int
	Details          json.RawMessage
	OccurredAt       time.Time
}

type RecordEvent struct {
	Actor            string
	Origin           Origin
	Action           string
	SubjectType      string
	SubjectID        int64
	Reason           string
	RequestID        string
	ExpectedRevision *int
	Details          json.RawMessage
}

type Repository interface {
	AppendAuditEvent(context.Context, Event) error
	ListAuditEvents(context.Context, int) ([]Event, error)
}

type IDGenerator interface {
	Next(context.Context) (int64, error)
}

type Service struct {
	repository Repository
	ids        IDGenerator
	now        func() time.Time
}

func NewService(repository Repository, ids IDGenerator, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, ids: ids, now: now}
}

func (s *Service) Record(ctx context.Context, command RecordEvent) (Event, error) {
	if err := validateRecord(command); err != nil {
		return Event{}, err
	}
	eventID, err := s.ids.Next(ctx)
	if err != nil {
		return Event{}, fmt.Errorf("generate audit event ID: %w", err)
	}
	event := Event{
		ID:               eventID,
		Actor:            strings.TrimSpace(command.Actor),
		Origin:           command.Origin,
		Action:           strings.TrimSpace(command.Action),
		SubjectType:      strings.TrimSpace(command.SubjectType),
		SubjectID:        command.SubjectID,
		Reason:           strings.TrimSpace(command.Reason),
		RequestID:        strings.TrimSpace(command.RequestID),
		ExpectedRevision: copyInt(command.ExpectedRevision),
		Details:          append(json.RawMessage(nil), command.Details...),
		OccurredAt:       s.now().UTC(),
	}
	if err := s.repository.AppendAuditEvent(ctx, event); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (s *Service) ListEvents(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		return nil, ErrInvalidEvent
	}
	return s.repository.ListAuditEvents(ctx, limit)
}

func validateRecord(command RecordEvent) error {
	if command.SubjectID <= 0 {
		return ErrInvalidEvent
	}
	if command.Origin < OriginAgent || command.Origin > OriginSystem {
		return ErrInvalidEvent
	}
	if strings.TrimSpace(command.Actor) == "" || len(command.Actor) > 200 {
		return ErrInvalidEvent
	}
	if strings.TrimSpace(command.Action) == "" || len(command.Action) > 100 {
		return ErrInvalidEvent
	}
	if strings.TrimSpace(command.SubjectType) == "" || len(command.SubjectType) > 100 {
		return ErrInvalidEvent
	}
	if strings.TrimSpace(command.Reason) == "" || len(command.Reason) > 2000 {
		return ErrInvalidEvent
	}
	if strings.TrimSpace(command.RequestID) == "" || len(command.RequestID) > 200 {
		return ErrInvalidEvent
	}
	if jsoncheck.ValidateObject(command.Details, jsoncheck.Limits{MaxBytes: 20_000, MaxDepth: 16}) != nil {
		return ErrInvalidEvent
	}
	return nil
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
