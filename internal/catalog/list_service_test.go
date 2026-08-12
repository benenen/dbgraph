package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
)

type listProjectStub struct {
	limit    int
	projects []catalog.Project
}

func (s *listProjectStub) CreateProject(context.Context, catalog.Project) error { return nil }
func (s *listProjectStub) CreateProjectWithAudit(context.Context, catalog.Project, audit.Event) error {
	return nil
}
func (s *listProjectStub) GetProject(context.Context, int64) (catalog.Project, error) {
	return catalog.Project{}, catalog.ErrProjectNotFound
}
func (s *listProjectStub) ListProjects(_ context.Context, limit int) ([]catalog.Project, error) {
	s.limit = limit
	return s.projects, nil
}

func TestProjectServiceListClampsTheLimit(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	stub := &listProjectStub{projects: []catalog.Project{{ID: 10, Name: "orders"}}}
	service := catalog.NewProjectService(stub, nil, func() time.Time { return now })

	tests := []struct {
		name      string
		limit     int
		wantLimit int
	}{
		{name: "zero uses the default", limit: 0, wantLimit: catalog.DefaultListLimit},
		{name: "negative uses the default", limit: -5, wantLimit: catalog.DefaultListLimit},
		{name: "above the ceiling clamps", limit: 5000, wantLimit: catalog.MaximumListLimit},
		{name: "inside the range passes through", limit: 7, wantLimit: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projects, err := service.List(context.Background(), test.limit)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(projects) != 1 {
				t.Fatalf("projects = %#v", projects)
			}
			if stub.limit != test.wantLimit {
				t.Fatalf("repository limit = %d, want %d", stub.limit, test.wantLimit)
			}
		})
	}
}

func TestListMethodsRejectAnInvalidProject(t *testing.T) {
	t.Parallel()

	catalogService := catalog.NewService(&listCatalogStub{}, nil, time.Now)
	if _, err := catalogService.ListDataSources(context.Background(), 0, 10); !errors.Is(err, catalog.ErrInvalidDataSource) {
		t.Fatalf("ListDataSources error = %v, want ErrInvalidDataSource", err)
	}

	repositoryService := catalog.NewCodeRepositoryService(&listRepositoryStub{}, nil, time.Now)
	if _, err := repositoryService.List(context.Background(), -1, 10); !errors.Is(err, catalog.ErrInvalidRepository) {
		t.Fatalf("List error = %v, want ErrInvalidRepository", err)
	}
}

type listCatalogStub struct{ sources []catalog.DataSource }

func (s *listCatalogStub) CreateDataSource(context.Context, catalog.DataSource, int64) error {
	return nil
}
func (s *listCatalogStub) CreateDataSourceWithAudit(context.Context, catalog.DataSource, int64, audit.Event) error {
	return nil
}
func (s *listCatalogStub) GetDataSource(context.Context, int64) (catalog.DataSource, error) {
	return catalog.DataSource{}, catalog.ErrDataSourceNotFound
}

func (s *listCatalogStub) GetProjectDataSource(ctx context.Context, _ int64, dataSourceID int64) (catalog.DataSource, error) {
	return s.GetDataSource(ctx, dataSourceID)
}
func (s *listCatalogStub) ListDataSources(context.Context, int64, int) ([]catalog.DataSource, error) {
	return s.sources, nil
}
func (s *listCatalogStub) BeginSchemaScan(context.Context, catalog.SchemaScanRun) error { return nil }
func (s *listCatalogStub) FailSchemaScan(context.Context, catalog.SchemaScanFailure) error {
	return nil
}
func (s *listCatalogStub) PublishSnapshot(context.Context, catalog.SnapshotPublication) (catalog.PublishedSnapshot, error) {
	return catalog.PublishedSnapshot{}, nil
}
func (s *listCatalogStub) FindCurrentNode(context.Context, int64, int64, string) (catalog.Node, error) {
	return catalog.Node{}, catalog.ErrNodeNotFound
}
func (s *listCatalogStub) GetCurrentNode(context.Context, int64, int64) (catalog.Node, error) {
	return catalog.Node{}, catalog.ErrNodeNotFound
}
func (s *listCatalogStub) SearchCurrentNodes(context.Context, int64, int64, string, int) ([]catalog.Node, error) {
	return nil, nil
}

type listRepositoryStub struct{ repositories []catalog.CodeRepository }

func (s *listRepositoryStub) CreateCodeRepository(context.Context, catalog.CodeRepository) error {
	return nil
}
func (s *listRepositoryStub) CreateCodeRepositoryWithAudit(context.Context, catalog.CodeRepository, audit.Event) error {
	return nil
}
func (s *listRepositoryStub) GetCodeRepository(context.Context, int64) (catalog.CodeRepository, error) {
	return catalog.CodeRepository{}, catalog.ErrRepositoryNotFound
}
func (s *listRepositoryStub) ListCodeRepositories(context.Context, int64, int) ([]catalog.CodeRepository, error) {
	return s.repositories, nil
}
