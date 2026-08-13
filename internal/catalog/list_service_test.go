package catalog_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
)

func TestCatalogServiceListAllDataSourcesClampsTheLimit(t *testing.T) {
	t.Parallel()

	stub := &listCatalogStub{sources: []catalog.DataSource{{ID: 10, Name: "orders"}}}
	service := catalog.NewService(stub, nil, time.Now)
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
			sources, err := service.ListAllDataSources(context.Background(), test.limit)
			if err != nil {
				t.Fatalf("ListAllDataSources: %v", err)
			}
			if len(sources) != 1 {
				t.Fatalf("sources = %#v", sources)
			}
			if stub.limit != test.wantLimit {
				t.Fatalf("repository limit = %d, want %d", stub.limit, test.wantLimit)
			}
		})
	}
}

func TestCodeRepositoryServiceListClampsTheLimit(t *testing.T) {
	t.Parallel()

	stub := &listRepositoryStub{repositories: []catalog.CodeRepository{{ID: 10, Name: "orders"}}}
	service := catalog.NewCodeRepositoryService(stub, nil, time.Now)
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
			repositories, err := service.List(context.Background(), test.limit)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(repositories) != 1 {
				t.Fatalf("repositories = %#v", repositories)
			}
			if stub.limit != test.wantLimit {
				t.Fatalf("repository limit = %d, want %d", stub.limit, test.wantLimit)
			}
		})
	}
}

func TestCatalogServiceBrowsesTablesThroughTheOptionalReadInterfaces(t *testing.T) {
	t.Parallel()

	detail := catalog.TableDetail{Table: catalog.TableSummary{ID: 12, Name: "orders", QualifiedName: "app.orders"}}
	stub := &listCatalogStub{
		tables: []catalog.TableSummary{detail.Table},
		detail: detail,
	}
	service := catalog.NewService(stub, nil, time.Now)

	tables, err := service.ListTables(context.Background(), 9, "orders", 0)
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(tables) != 1 || tables[0] != detail.Table {
		t.Fatalf("tables = %#v", tables)
	}
	if stub.dataSourceID != 9 || stub.filter != "orders" || stub.tableLimit != catalog.MaximumTableListLimit {
		t.Fatalf("list input = dataSource:%d filter:%q limit:%d", stub.dataSourceID, stub.filter, stub.tableLimit)
	}

	gotDetail, err := service.TableDetail(context.Background(), detail.Table.ID)
	if err != nil {
		t.Fatalf("TableDetail: %v", err)
	}
	if gotDetail.Table != detail.Table || stub.tableID != detail.Table.ID {
		t.Fatalf("detail = %#v, table ID = %d", gotDetail, stub.tableID)
	}

	if _, err := service.ListTables(context.Background(), 0, "", 10); !errors.Is(err, catalog.ErrInvalidDataSource) {
		t.Fatalf("invalid data source error = %v", err)
	}
	if _, err := service.ListTables(context.Background(), 9, strings.Repeat("x", 201), 10); !errors.Is(err, catalog.ErrInvalidDataSource) {
		t.Fatalf("long filter error = %v", err)
	}
	if _, err := service.TableDetail(context.Background(), 0); !errors.Is(err, catalog.ErrInvalidDataSource) {
		t.Fatalf("invalid table error = %v", err)
	}
}

type listCatalogStub struct {
	sources      []catalog.DataSource
	tables       []catalog.TableSummary
	detail       catalog.TableDetail
	limit        int
	dataSourceID int64
	filter       string
	tableLimit   int
	tableID      int64
}

func (s *listCatalogStub) CreateDataSource(context.Context, catalog.DataSource) error { return nil }
func (s *listCatalogStub) CreateDataSourceWithAudit(context.Context, catalog.DataSource, audit.Event) error {
	return nil
}
func (s *listCatalogStub) GetDataSource(context.Context, int64) (catalog.DataSource, error) {
	return catalog.DataSource{}, catalog.ErrDataSourceNotFound
}
func (s *listCatalogStub) ListAllDataSources(_ context.Context, limit int) ([]catalog.DataSource, error) {
	s.limit = limit
	return s.sources, nil
}
func (s *listCatalogStub) UpdateDataSourceWithAudit(context.Context, catalog.DataSource, bool, audit.Event) error {
	return nil
}
func (s *listCatalogStub) DeleteDataSource(context.Context, int64) error { return nil }
func (s *listCatalogStub) BeginSchemaScan(context.Context, catalog.SchemaScanRun) error {
	return nil
}
func (s *listCatalogStub) FailSchemaScan(context.Context, catalog.SchemaScanFailure) error {
	return nil
}
func (s *listCatalogStub) PublishSnapshot(context.Context, catalog.SnapshotPublication) (catalog.PublishedSnapshot, error) {
	return catalog.PublishedSnapshot{}, nil
}
func (s *listCatalogStub) FindCurrentNode(context.Context, int64, string) (catalog.Node, error) {
	return catalog.Node{}, catalog.ErrNodeNotFound
}
func (s *listCatalogStub) GetCurrentNode(context.Context, int64) (catalog.Node, error) {
	return catalog.Node{}, catalog.ErrNodeNotFound
}
func (s *listCatalogStub) SearchCurrentNodes(context.Context, int64, string, int) ([]catalog.Node, error) {
	return nil, nil
}
func (s *listCatalogStub) ListTables(_ context.Context, dataSourceID int64, filter string, limit int) ([]catalog.TableSummary, error) {
	s.dataSourceID = dataSourceID
	s.filter = filter
	s.tableLimit = limit
	return s.tables, nil
}
func (s *listCatalogStub) LoadTableDetail(_ context.Context, tableID int64) (catalog.TableDetail, error) {
	s.tableID = tableID
	return s.detail, nil
}

type listRepositoryStub struct {
	repositories []catalog.CodeRepository
	limit        int
}

func (s *listRepositoryStub) CreateCodeRepository(context.Context, catalog.CodeRepository) error {
	return nil
}
func (s *listRepositoryStub) CreateCodeRepositoryWithAudit(context.Context, catalog.CodeRepository, audit.Event) error {
	return nil
}
func (s *listRepositoryStub) GetCodeRepository(context.Context, int64) (catalog.CodeRepository, error) {
	return catalog.CodeRepository{}, catalog.ErrRepositoryNotFound
}
func (s *listRepositoryStub) ListCodeRepositories(_ context.Context, limit int) ([]catalog.CodeRepository, error) {
	s.limit = limit
	return s.repositories, nil
}
