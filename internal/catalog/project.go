package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/relations"
)

var (
	ErrInvalidProject  = errors.New("invalid project")
	ErrProjectNotFound = errors.New("project not found")
)

type Project struct {
	ID          int64
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CreateProject struct {
	Name        string
	Description string
}

type AdminCreateProject struct {
	Name        string
	Description string
	Principal   relations.Principal
	Reason      string
	RequestID   string
}

type ProjectRepository interface {
	CreateProject(context.Context, Project) error
	CreateProjectWithAudit(context.Context, Project, audit.Event) error
	GetProject(context.Context, int64) (Project, error)
}

type ProjectIDGenerator interface {
	Next(context.Context) (int64, error)
}

type ProjectService struct {
	repository ProjectRepository
	ids        ProjectIDGenerator
	now        func() time.Time
}

func NewProjectService(
	repository ProjectRepository,
	ids ProjectIDGenerator,
	now func() time.Time,
) *ProjectService {
	if now == nil {
		now = time.Now
	}
	return &ProjectService{repository: repository, ids: ids, now: now}
}

func (s *ProjectService) Create(ctx context.Context, command CreateProject) (Project, error) {
	return s.create(ctx, command, nil)
}

func (s *ProjectService) CreateAsAdmin(ctx context.Context, command AdminCreateProject) (Project, error) {
	actor, reason, requestID, err := validateAdminMetadata(command.Principal, command.Reason, command.RequestID)
	if err != nil {
		return Project{}, err
	}
	return s.create(ctx, CreateProject{Name: command.Name, Description: command.Description},
		func(project Project, auditID int64, occurredAt time.Time) audit.Event {
			return audit.Event{
				ID: auditID, ProjectID: project.ID, Actor: actor, Origin: command.Principal.Origin,
				Action: "PROJECT_CREATED", SubjectType: "PROJECT", SubjectID: project.ID,
				Reason: reason, RequestID: requestID, Details: []byte(`{}`), OccurredAt: occurredAt,
			}
		})
}

type projectAuditBuilder func(Project, int64, time.Time) audit.Event

func (s *ProjectService) create(ctx context.Context, command CreateProject, auditBuilder projectAuditBuilder) (Project, error) {
	name := strings.TrimSpace(command.Name)
	description := strings.TrimSpace(command.Description)
	if name == "" || len(name) > 200 || len(description) > 2000 {
		return Project{}, ErrInvalidProject
	}

	projectID, err := s.ids.Next(ctx)
	if err != nil {
		return Project{}, fmt.Errorf("generate project ID: %w", err)
	}
	now := s.now().UTC()
	project := Project{
		ID:          projectID,
		Name:        name,
		Description: description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if auditBuilder != nil {
		auditID, err := s.ids.Next(ctx)
		if err != nil {
			return Project{}, fmt.Errorf("generate project audit ID: %w", err)
		}
		if err := s.repository.CreateProjectWithAudit(ctx, project, auditBuilder(project, auditID, now)); err != nil {
			return Project{}, err
		}
		return project, nil
	}
	if err := s.repository.CreateProject(ctx, project); err != nil {
		return Project{}, err
	}
	return project, nil
}

func validateAdminMetadata(principal relations.Principal, reason string, requestID string) (string, string, string, error) {
	if principal.Role != relations.RoleAdmin {
		return "", "", "", ErrForbidden
	}
	actor := strings.TrimSpace(principal.Actor)
	reason = strings.TrimSpace(reason)
	requestID = strings.TrimSpace(requestID)
	if actor == "" || len(actor) > 200 || reason == "" || len(reason) > 2000 ||
		requestID == "" || len(requestID) > 200 ||
		(principal.Origin != audit.OriginAgent && principal.Origin != audit.OriginWeb) {
		return "", "", "", ErrInvalidProject
	}
	return actor, reason, requestID, nil
}

func (s *ProjectService) Get(ctx context.Context, projectID int64) (Project, error) {
	if projectID <= 0 {
		return Project{}, ErrInvalidProject
	}
	return s.repository.GetProject(ctx, projectID)
}
