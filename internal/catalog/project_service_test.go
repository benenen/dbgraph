package catalog_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestProjectServiceCreatesRetrievableProject(t *testing.T) {
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

	fixedTime := time.Date(2026, time.August, 11, 11, 30, 0, 0, time.UTC)
	idGenerator, err := id.NewGenerator(3, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	service := catalog.NewProjectService(
		dbsqlite.NewProjectRepository(store),
		idGenerator,
		func() time.Time { return fixedTime },
	)

	created, err := service.Create(ctx, catalog.CreateProject{
		Name:        "Learning Platform",
		Description: "Database graph for the learning platform.",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if created.ID <= 0 {
		t.Fatalf("created project ID = %d, want positive", created.ID)
	}
	if created.Name != "Learning Platform" {
		t.Fatalf("created project name = %q", created.Name)
	}
	if !created.CreatedAt.Equal(fixedTime) || !created.UpdatedAt.Equal(fixedTime) {
		t.Fatalf("created timestamps = %s/%s, want %s", created.CreatedAt, created.UpdatedAt, fixedTime)
	}

	retrieved, err := service.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if retrieved != created {
		t.Fatalf("retrieved project = %#v, want %#v", retrieved, created)
	}
}

func TestAdminBootstrapsAuditedProjectAndEvidenceRepository(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixedTime := time.Date(2026, 8, 11, 18, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(10, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	admin := relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb}
	projectService := catalog.NewProjectService(dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime })
	if _, err := projectService.CreateAsAdmin(ctx, catalog.AdminCreateProject{
		Name: "forbidden", Principal: relations.Principal{Actor: "viewer", Role: relations.RoleViewer, Origin: audit.OriginWeb},
		Reason: "reason", RequestID: "p-0",
	}); !errors.Is(err, catalog.ErrForbidden) {
		t.Fatalf("viewer project error = %v", err)
	}
	project, err := projectService.CreateAsAdmin(ctx, catalog.AdminCreateProject{
		Name: "Learning", Description: "Lineage", Principal: admin,
		Reason: "Initialize dbgraph", RequestID: "p-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	repositoryService := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), ids, func() time.Time { return fixedTime })
	repository, err := repositoryService.CreateAsAdmin(ctx, catalog.AdminCreateCodeRepository{
		ProjectID: project.ID, Name: "service", RemoteURL: "https://example.test/service.git", DefaultBranch: "main",
		Principal: admin, Reason: "Register Agent evidence source", RequestID: "r-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.ProjectID != project.ID {
		t.Fatalf("repository = %#v", repository)
	}
	events, err := dbsqlite.NewAuditRepository(store).ListAuditEvents(ctx, project.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Action != "CODE_REPOSITORY_CREATED" || events[1].Action != "PROJECT_CREATED" {
		t.Fatalf("audit events = %#v", events)
	}
}
