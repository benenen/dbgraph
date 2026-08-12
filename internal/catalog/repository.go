package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/relations"
)

var (
	ErrInvalidRepository  = errors.New("invalid code repository")
	ErrRepositoryNotFound = errors.New("code repository not found")
)

type CodeRepository struct {
	ID            int64
	ProjectID     int64
	Name          string
	RemoteURL     string
	DefaultBranch string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CreateCodeRepository struct {
	ProjectID     int64
	Name          string
	RemoteURL     string
	DefaultBranch string
}

type AdminCreateCodeRepository struct {
	ProjectID     int64
	Name          string
	RemoteURL     string
	DefaultBranch string
	Principal     relations.Principal
	Reason        string
	RequestID     string
}

type CodeRepositoryStore interface {
	CreateCodeRepository(context.Context, CodeRepository) error
	CreateCodeRepositoryWithAudit(context.Context, CodeRepository, audit.Event) error
	GetCodeRepository(context.Context, int64) (CodeRepository, error)
	ListCodeRepositories(context.Context, int64, int) ([]CodeRepository, error)
}

type CodeRepositoryService struct {
	repository CodeRepositoryStore
	ids        ProjectIDGenerator
	now        func() time.Time
}

func NewCodeRepositoryService(
	repository CodeRepositoryStore,
	ids ProjectIDGenerator,
	now func() time.Time,
) *CodeRepositoryService {
	if now == nil {
		now = time.Now
	}
	return &CodeRepositoryService{repository: repository, ids: ids, now: now}
}

func (s *CodeRepositoryService) Create(
	ctx context.Context,
	command CreateCodeRepository,
) (CodeRepository, error) {
	return s.create(ctx, command, nil)
}

func (s *CodeRepositoryService) CreateAsAdmin(
	ctx context.Context,
	command AdminCreateCodeRepository,
) (CodeRepository, error) {
	actor, reason, requestID, err := validateAdminMetadata(command.Principal, command.Reason, command.RequestID)
	if err != nil {
		return CodeRepository{}, err
	}
	return s.create(ctx, CreateCodeRepository{
		ProjectID: command.ProjectID, Name: command.Name,
		RemoteURL: command.RemoteURL, DefaultBranch: command.DefaultBranch,
	}, func(repository CodeRepository, auditID int64, occurredAt time.Time) audit.Event {
		details, _ := json.Marshal(map[string]string{"name": repository.Name, "defaultBranch": repository.DefaultBranch})
		return audit.Event{
			ID: auditID, ProjectID: repository.ProjectID, Actor: actor, Origin: command.Principal.Origin,
			Action: "CODE_REPOSITORY_CREATED", SubjectType: "CODE_REPOSITORY", SubjectID: repository.ID,
			Reason: reason, RequestID: requestID, Details: details, OccurredAt: occurredAt,
		}
	})
}

type codeRepositoryAuditBuilder func(CodeRepository, int64, time.Time) audit.Event

func (s *CodeRepositoryService) create(
	ctx context.Context,
	command CreateCodeRepository,
	auditBuilder codeRepositoryAuditBuilder,
) (CodeRepository, error) {
	name := strings.TrimSpace(command.Name)
	remoteURL := strings.TrimSpace(command.RemoteURL)
	defaultBranch := strings.TrimSpace(command.DefaultBranch)
	if remoteURL != "" {
		parsedURL, err := url.Parse(remoteURL)
		if err != nil || parsedURL.User != nil {
			return CodeRepository{}, ErrInvalidRepository
		}
	}
	if command.ProjectID <= 0 || name == "" || len(name) > 200 ||
		len(remoteURL) > 2000 || len(defaultBranch) > 500 {
		return CodeRepository{}, ErrInvalidRepository
	}
	repositoryID, err := s.ids.Next(ctx)
	if err != nil {
		return CodeRepository{}, fmt.Errorf("generate code repository ID: %w", err)
	}
	now := s.now().UTC()
	repository := CodeRepository{
		ID:            repositoryID,
		ProjectID:     command.ProjectID,
		Name:          name,
		RemoteURL:     remoteURL,
		DefaultBranch: defaultBranch,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if auditBuilder != nil {
		auditID, err := s.ids.Next(ctx)
		if err != nil {
			return CodeRepository{}, fmt.Errorf("generate code repository audit ID: %w", err)
		}
		if err := s.repository.CreateCodeRepositoryWithAudit(ctx, repository, auditBuilder(repository, auditID, now)); err != nil {
			return CodeRepository{}, err
		}
		return repository, nil
	}
	if err := s.repository.CreateCodeRepository(ctx, repository); err != nil {
		return CodeRepository{}, err
	}
	return repository, nil
}

func (s *CodeRepositoryService) Get(ctx context.Context, repositoryID int64) (CodeRepository, error) {
	if repositoryID <= 0 {
		return CodeRepository{}, ErrInvalidRepository
	}
	return s.repository.GetCodeRepository(ctx, repositoryID)
}

func (s *CodeRepositoryService) List(ctx context.Context, projectID int64, limit int) ([]CodeRepository, error) {
	if projectID <= 0 {
		return nil, ErrInvalidRepository
	}
	return s.repository.ListCodeRepositories(ctx, projectID, clampListLimit(limit))
}
