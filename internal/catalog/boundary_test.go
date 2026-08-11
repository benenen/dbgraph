package catalog_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestProjectAndDataSourceGetBoundaries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	projectService := catalog.NewProjectService(dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime })
	catalogService := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })

	if _, err := projectService.Get(ctx, 0); !errors.Is(err, catalog.ErrInvalidProject) {
		t.Fatalf("Get project zero error = %v", err)
	}
	if _, err := projectService.Get(ctx, 999); !errors.Is(err, catalog.ErrProjectNotFound) {
		t.Fatalf("Get missing project error = %v", err)
	}
	if _, err := catalogService.GetDataSource(ctx, 0); !errors.Is(err, catalog.ErrInvalidDataSource) {
		t.Fatalf("Get data source zero error = %v", err)
	}
	if _, err := catalogService.GetDataSource(ctx, 999); !errors.Is(err, catalog.ErrDataSourceNotFound) {
		t.Fatalf("Get missing data source error = %v", err)
	}
	if _, err := catalogService.FindCurrentNode(ctx, 0, 1, "schema.table.column"); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("FindCurrentNode invalid project error = %v", err)
	}
	if _, err := catalogService.SearchCurrentNodes(ctx, 1, "node", 0); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("SearchCurrentNodes invalid limit error = %v", err)
	}
}

func TestCodeRepositoryServiceCreatesAndRetrievesEvidenceMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	projectService := catalog.NewProjectService(dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime })
	project, err := projectService.Create(ctx, catalog.CreateProject{Name: "Repository Boundary"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), ids, func() time.Time { return fixedTime })

	created, err := service.Create(ctx, catalog.CreateCodeRepository{
		ProjectID:     project.ID,
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

func TestCatalogServicesRejectInvalidCreationInputs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	projectService := catalog.NewProjectService(dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime })
	if _, err := projectService.Create(ctx, catalog.CreateProject{Name: " "}); !errors.Is(err, catalog.ErrInvalidProject) {
		t.Fatalf("empty project name error = %v", err)
	}
	if _, err := projectService.Create(ctx, catalog.CreateProject{Name: "project", Description: strings.Repeat("x", 2001)}); !errors.Is(err, catalog.ErrInvalidProject) {
		t.Fatalf("long project description error = %v", err)
	}

	repositoryService := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), ids, func() time.Time { return fixedTime })
	testCases := []catalog.CreateCodeRepository{
		{ProjectID: 0, Name: "repository"},
		{ProjectID: 1, Name: " "},
		{ProjectID: 1, Name: "repository", RemoteURL: "https://user:password@example.test/private.git"},
		{ProjectID: 1, Name: "repository", RemoteURL: "://invalid"},
		{ProjectID: 1, Name: strings.Repeat("r", 201)},
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
