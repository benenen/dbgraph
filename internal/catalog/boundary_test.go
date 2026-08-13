package catalog_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestDataSourceGetAndSearchBoundaries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	catalogService := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })

	if _, err := catalogService.GetDataSource(ctx, 0); !errors.Is(err, catalog.ErrInvalidDataSource) {
		t.Fatalf("Get data source zero error = %v", err)
	}
	if _, err := catalogService.GetDataSource(ctx, 999); !errors.Is(err, catalog.ErrDataSourceNotFound) {
		t.Fatalf("Get missing data source error = %v", err)
	}
	if _, err := catalogService.FindCurrentNode(ctx, 0, "schema.table.column"); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("FindCurrentNode invalid data source error = %v", err)
	}
	if _, err := catalogService.SearchCurrentNodes(ctx, 0, "node", 0); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("SearchCurrentNodes invalid limit error = %v", err)
	}
	if _, err := catalogService.SearchCurrentNodes(ctx, -1, "node", 10); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("SearchCurrentNodes negative data source error = %v", err)
	}
}

func TestCodeRepositoryServiceCreatesAndRetrievesEvidenceMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	service := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), ids, func() time.Time { return fixedTime })

	created, err := service.Create(ctx, catalog.CreateCodeRepository{

		Name:          "  learning-service  ",
		RemoteURL:     "  https://example.test/learning-service.git  ",
		DefaultBranch: "  main  ",
	})
	if err != nil {
		t.Fatalf("create code repository: %v", err)
	}
	if created.Name != "learning-service" || created.RemoteURL != "https://example.test/learning-service.git" || created.DefaultBranch != "main" {
		t.Fatalf("created repository was not normalized: %#v", created)
	}
	if !created.CreatedAt.Equal(fixedTime) || !created.UpdatedAt.Equal(fixedTime) {
		t.Fatalf("repository timestamps = %s/%s", created.CreatedAt, created.UpdatedAt)
	}
	retrieved, err := service.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get code repository: %v", err)
	}
	if retrieved != created {
		t.Fatalf("retrieved repository = %#v, want %#v", retrieved, created)
	}

	if _, err := service.Get(ctx, 0); !errors.Is(err, catalog.ErrInvalidRepository) {
		t.Fatalf("Get repository zero error = %v", err)
	}
	if _, err := service.Get(ctx, 999); !errors.Is(err, catalog.ErrRepositoryNotFound) {
		t.Fatalf("Get missing repository error = %v", err)
	}
}

func TestAdminCreatesAnAuditedCodeRepository(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	service := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), ids, func() time.Time { return fixedTime })
	command := catalog.AdminCreateCodeRepository{
		Name: "learning-service", RemoteURL: "https://example.test/learning-service.git", DefaultBranch: "main",
		Principal: relations.Principal{Actor: "viewer", Role: relations.RoleViewer, Origin: audit.OriginWeb},
		RequestID: "repository-1",
	}
	if _, err := service.CreateAsAdmin(ctx, command); !errors.Is(err, catalog.ErrForbidden) {
		t.Fatalf("viewer error = %v, want ErrForbidden", err)
	}
	command.Principal = relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb}
	created, err := service.CreateAsAdmin(ctx, command)
	if err != nil {
		t.Fatalf("CreateAsAdmin: %v", err)
	}
	if created.Name != "learning-service" || created.DefaultBranch != "main" {
		t.Fatalf("created repository = %#v", created)
	}
	events, err := dbsqlite.NewAuditRepository(store).ListAuditEvents(ctx, 10)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 || events[0].Action != "CODE_REPOSITORY_CREATED" ||
		events[0].SubjectID != created.ID || events[0].Reason != "Registered from the console" ||
		events[0].RequestID != "repository-1" {
		t.Fatalf("audit events = %#v", events)
	}
}

func TestAdminCodeRepositoryRejectsInvalidAuditMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	service := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), ids, func() time.Time { return fixedTime })
	base := catalog.AdminCreateCodeRepository{
		Name: "repository", Principal: relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb},
		Reason: "Register evidence source", RequestID: "repository-1",
	}
	tests := []struct {
		name   string
		mutate func(*catalog.AdminCreateCodeRepository)
	}{
		{name: "blank actor", mutate: func(command *catalog.AdminCreateCodeRepository) { command.Principal.Actor = " " }},
		{name: "long actor", mutate: func(command *catalog.AdminCreateCodeRepository) { command.Principal.Actor = strings.Repeat("a", 201) }},
		{name: "long reason", mutate: func(command *catalog.AdminCreateCodeRepository) { command.Reason = strings.Repeat("r", 2001) }},
		{name: "blank request", mutate: func(command *catalog.AdminCreateCodeRepository) { command.RequestID = " " }},
		{name: "long request", mutate: func(command *catalog.AdminCreateCodeRepository) { command.RequestID = strings.Repeat("r", 201) }},
		{name: "system origin", mutate: func(command *catalog.AdminCreateCodeRepository) { command.Principal.Origin = audit.OriginSystem }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := base
			test.mutate(&command)
			if _, err := service.CreateAsAdmin(ctx, command); !errors.Is(err, catalog.ErrInvalidRepository) {
				t.Fatalf("CreateAsAdmin error = %v, want ErrInvalidRepository", err)
			}
		})
	}
}

func TestCatalogServicesRejectInvalidCreationInputs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)

	repositoryService := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), ids, func() time.Time { return fixedTime })
	testCases := []catalog.CreateCodeRepository{
		{Name: " "},
		{Name: "repository", RemoteURL: "https://user:password@example.test/private.git"},
		{Name: "repository", RemoteURL: "://invalid"},
		{Name: strings.Repeat("r", 201)},
		{Name: "repository", RemoteURL: strings.Repeat("r", 2001)},
		{Name: "repository", DefaultBranch: strings.Repeat("b", 501)},
	}
	for _, command := range testCases {
		if _, err := repositoryService.Create(ctx, command); !errors.Is(err, catalog.ErrInvalidRepository) {
			t.Fatalf("Create repository %#v error = %v", command, err)
		}
	}
}

func openCatalogBoundaryStore(
	t *testing.T,
	ctx context.Context,
) (*dbsqlite.Store, *id.Generator, time.Time) {
	t.Helper()

	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	fixedTime := time.Date(2026, time.August, 11, 21, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(13, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	return store, ids, fixedTime
}
