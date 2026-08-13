package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestListQueriesOrderByName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	createdAt := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	catalogRepository := dbsqlite.NewCatalogRepository(store, nil)
	for _, source := range []catalog.DataSource{
		{ID: 31, Name: "orders-replica", Kind: catalog.DataSourceMySQL, DSNEnvironment: "ORDERS_REPLICA_DSN", CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: 30, Name: "orders-primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "ORDERS_DSN", CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: 32, Name: "warehouse-primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "WAREHOUSE_DSN", CreatedAt: createdAt, UpdatedAt: createdAt},
	} {
		if err := catalogRepository.CreateDataSource(ctx, source); err != nil {
			t.Fatal(err)
		}
	}

	repositories := dbsqlite.NewCodeRepository(store)
	for _, repository := range []catalog.CodeRepository{
		{ID: 41, Name: "orders-web", RemoteURL: "", DefaultBranch: "main", CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: 40, Name: "orders-api", RemoteURL: "", DefaultBranch: "main", CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: 42, Name: "warehouse-api", RemoteURL: "", DefaultBranch: "main", CreatedAt: createdAt, UpdatedAt: createdAt},
	} {
		if err := repositories.CreateCodeRepository(ctx, repository); err != nil {
			t.Fatal(err)
		}
	}

	sources, err := catalogRepository.ListAllDataSources(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 || sources[0].Name != "orders-primary" || sources[1].Name != "orders-replica" {
		t.Fatalf("data sources = %#v, want every source in name order", sources)
	}
	if sources[0].DSNEnvironment != "ORDERS_DSN" || sources[0].Kind != catalog.DataSourceMySQL {
		t.Fatalf("data source = %#v", sources[0])
	}

	listedRepositories, err := repositories.ListCodeRepositories(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(listedRepositories) != 3 || listedRepositories[0].Name != "orders-api" {
		t.Fatalf("repositories = %#v, want every repository in name order", listedRepositories)
	}

}

func TestSearchCurrentNodesFiltersByDataSource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	createdAt := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(11, func() time.Time { return createdAt })
	if err != nil {
		t.Fatal(err)
	}
	catalogRepository := dbsqlite.NewCatalogRepository(store, ids)
	for _, source := range []catalog.DataSource{
		{ID: 30, Name: "primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "PRIMARY_DSN", CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: 31, Name: "replica", Kind: catalog.DataSourceMySQL, DSNEnvironment: "REPLICA_DSN", CreatedAt: createdAt, UpdatedAt: createdAt},
	} {
		if err := catalogRepository.CreateDataSource(ctx, source); err != nil {
			t.Fatal(err)
		}
	}

	service := catalog.NewService(catalogRepository, ids, func() time.Time { return createdAt })
	for _, dataSourceID := range []int64{30, 31} {
		if _, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
			DataSourceID: dataSourceID,
			Nodes: []catalog.NodeInput{
				{StableKey: "database:app", Kind: catalog.NodeDatabase, Name: "app", QualifiedName: "mysql://app"},
				{StableKey: "schema:app", ParentStableKey: "database:app", Kind: catalog.NodeSchema, Name: "app", QualifiedName: "app"},
				{StableKey: "table:app.orders", ParentStableKey: "schema:app", Kind: catalog.NodeTable, Name: "orders", QualifiedName: "app.orders"},
				{StableKey: "column:app.orders.tenant_id", ParentStableKey: "table:app.orders", Kind: catalog.NodeColumn,
					Name: "tenant_id", QualifiedName: "app.orders.tenant_id", DataType: "bigint", Ordinal: 1},
			},
		}); err != nil {
			t.Fatalf("publish snapshot for data source %d: %v", dataSourceID, err)
		}
	}

	all, err := catalogRepository.SearchCurrentNodes(ctx, 0, "tenant_id", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered search returned %d nodes, want one per data source", len(all))
	}

	scoped, err := catalogRepository.SearchCurrentNodes(ctx, 30, "tenant_id", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].DataSourceID != 30 {
		t.Fatalf("scoped search = %#v, want only data source 30", scoped)
	}
}
