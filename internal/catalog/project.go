package catalog

import (
	"context"
	"encoding/json"
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
	ListProjects(context.Context, int) ([]Project, error)
	UpdateProjectWithAudit(context.Context, Project, audit.Event) error
	ArchiveProject(context.Context, int64, time.Time) error
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
	actor, reason, requestID, err := validateAdminMetadata(command.Principal, command.Reason, command.RequestID, "Created from the console")
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
		return "", "", "", ErrInvalidProject
	}
	return actor, auditReason(reason, fallbackReason), requestID, nil
}

func (s *ProjectService) Get(ctx context.Context, projectID int64) (Project, error) {
	if projectID <= 0 {
		return Project{}, ErrInvalidProject
	}
	return s.repository.GetProject(ctx, projectID)
}

func (s *ProjectService) List(ctx context.Context, limit int) ([]Project, error) {
	return s.repository.ListProjects(ctx, clampListLimit(limit))
}

// AdminUpdateProject renames or re-describes a project.
type AdminUpdateProject struct {
	ProjectID   int64
	Name        string
	Description string
	Principal   relations.Principal
	Reason      string
	RequestID   string
}

// UpdateAsAdmin changes a project's descriptive fields and records who did it.
func (s *ProjectService) UpdateAsAdmin(ctx context.Context, command AdminUpdateProject) (Project, error) {
	if command.Principal.Role != relations.RoleAdmin {
		return Project{}, ErrForbidden
	}
	name := strings.TrimSpace(command.Name)
	description := strings.TrimSpace(command.Description)
	actor := strings.TrimSpace(command.Principal.Actor)
	reason := strings.TrimSpace(command.Reason)
	requestID := strings.TrimSpace(command.RequestID)
	if command.ProjectID <= 0 || name == "" || len(name) > 200 || len(description) > 2000 ||
		actor == "" || len(actor) > 200 || len(reason) > 2000 ||
		requestID == "" || len(requestID) > 200 {
		return Project{}, ErrInvalidProject
	}
	reason = auditReason(reason, "Updated from the console")

	existing, err := s.repository.GetProject(ctx, command.ProjectID)
	if err != nil {
		return Project{}, err
	}
	eventID, err := s.ids.Next(ctx)
	if err != nil {
		return Project{}, fmt.Errorf("generate audit event ID: %w", err)
	}
	now := s.now().UTC()
	updated := existing
	updated.Name = name
	updated.Description = description
	updated.UpdatedAt = now

	details, _ := json.Marshal(map[string]string{"name": name})
	event := audit.Event{
		ID: eventID, ProjectID: updated.ID, Actor: actor, Origin: command.Principal.Origin,
		Action: "PROJECT_UPDATED", SubjectType: "PROJECT", SubjectID: updated.ID,
		Reason: reason, RequestID: requestID, Details: details, OccurredAt: now,
	}
	if err := s.repository.UpdateProjectWithAudit(ctx, updated, event); err != nil {
		return Project{}, err
	}
	return updated, nil
}

// Delete retires a project. It archives rather than removes, because the
// project's audit history is append-only and must outlive it.
func (s *ProjectService) Delete(ctx context.Context, projectID int64) error {
	if projectID <= 0 {
		return ErrInvalidProject
	}
	return s.repository.ArchiveProject(ctx, projectID, s.now().UTC())
}
