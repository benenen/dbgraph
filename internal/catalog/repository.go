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
	Name          string
	RemoteURL     string
	DefaultBranch string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CreateCodeRepository struct {
	Name          string
	RemoteURL     string
	DefaultBranch string
}

type AdminCreateCodeRepository struct {
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
	ListCodeRepositories(context.Context, int) ([]CodeRepository, error)
}

type CodeRepositoryService struct {
	repository CodeRepositoryStore
	ids        IDGenerator
	now        func() time.Time
}

// validateAdminMetadata checks who is acting and why, and supplies a stated
// default when the operator leaves the reason blank.
func validateAdminMetadata(
	principal relations.Principal,
	reason string,
	requestID string,
	fallbackReason string,
) (string, string, string, error) {
	if principal.Role != relations.RoleAdmin {
		return "", "", "", ErrForbidden
	}
	actor := strings.TrimSpace(principal.Actor)
	reason = strings.TrimSpace(reason)
	requestID = strings.TrimSpace(requestID)
	if actor == "" || len(actor) > 200 || len(reason) > 2000 ||
		requestID == "" || len(requestID) > 200 ||
		(principal.Origin != audit.OriginAgent && principal.Origin != audit.OriginWeb) {
		return "", "", "", ErrInvalidRepository
	}
	return actor, auditReason(reason, fallbackReason), requestID, nil
}

func NewCodeRepositoryService(
	repository CodeRepositoryStore,
	ids IDGenerator,
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
	actor, reason, requestID, err := validateAdminMetadata(
		command.Principal, command.Reason, command.RequestID, "Registered from the console",
	)
	if err != nil {
		return CodeRepository{}, err
	}
	return s.create(ctx, CreateCodeRepository{
		Name:      command.Name,
		RemoteURL: command.RemoteURL, DefaultBranch: command.DefaultBranch,
	}, func(repository CodeRepository, auditID int64, occurredAt time.Time) audit.Event {
		details, _ := json.Marshal(map[string]string{"name": repository.Name, "defaultBranch": repository.DefaultBranch})
		return audit.Event{
			ID: auditID, Actor: actor, Origin: command.Principal.Origin,
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
	if name == "" || len(name) > 200 ||
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

func (s *CodeRepositoryService) List(ctx context.Context, limit int) ([]CodeRepository, error) {
	return s.repository.ListCodeRepositories(ctx, clampListLimit(limit))
}
