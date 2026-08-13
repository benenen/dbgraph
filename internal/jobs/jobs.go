package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benenen/dbgraph/internal/jsoncheck"
)

var (
	ErrInvalidJob  = errors.New("invalid job")
	ErrJobNotFound = errors.New("job not found")
)

type Type int

const (
	TypeSchemaScan Type = 1
)

type Status int

const (
	StatusPending   Status = 1
	StatusRunning   Status = 2
	StatusSucceeded Status = 3
	StatusFailed    Status = 4
	StatusCancelled Status = 5
)

type Job struct {
	ID           int64
	Type         Type
	Status       Status
	Payload      json.RawMessage
	Result       json.RawMessage
	ErrorCode    string
	ErrorMessage string
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
	RevisionNo   int
}

type CreateJob struct {
	Type    Type
	Payload json.RawMessage
}

type Repository interface {
	CreateJob(context.Context, Job) error
	GetJob(context.Context, int64) (Job, error)
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

func (s *Service) Create(ctx context.Context, command CreateJob) (Job, error) {
	if command.Type != TypeSchemaScan ||
		jsoncheck.ValidateObject(command.Payload, jsoncheck.Limits{MaxBytes: 20_000, MaxDepth: 16}) != nil {
		return Job{}, ErrInvalidJob
	}
	jobID, err := s.ids.Next(ctx)
	if err != nil {
		return Job{}, fmt.Errorf("generate job ID: %w", err)
	}
	job := Job{
		ID:         jobID,
		Type:       command.Type,
		Status:     StatusPending,
		Payload:    append(json.RawMessage(nil), command.Payload...),
		CreatedAt:  s.now().UTC(),
		RevisionNo: 1,
	}
	if err := s.repository.CreateJob(ctx, job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) Get(ctx context.Context, jobID int64) (Job, error) {
	if jobID <= 0 {
		return Job{}, ErrInvalidJob
	}
	return s.repository.GetJob(ctx, jobID)
}
