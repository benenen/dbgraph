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

func TestListQueriesOrderByNameAndScopeToTheirProject(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	createdAt := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	projects := dbsqlite.NewProjectRepository(store)
	for _, project := range []catalog.Project{
		{ID: 20, Name: "warehouse", Description: "second", CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: 10, Name: "orders", Description: "first", CreatedAt: createdAt, UpdatedAt: createdAt},
	} {
		if err := projects.CreateProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}

	catalogRepository := dbsqlite.NewCatalogRepository(store, nil)
	for _, linked := range []struct {
		source    catalog.DataSource
		projectID int64
	}{
		{source: catalog.DataSource{ID: 31, Name: "orders-replica", Kind: catalog.DataSourceMySQL, DSNEnvironment: "ORDERS_REPLICA_DSN", CreatedAt: createdAt, UpdatedAt: createdAt}, projectID: 10},
		{source: catalog.DataSource{ID: 30, Name: "orders-primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "ORDERS_DSN", CreatedAt: createdAt, UpdatedAt: createdAt}, projectID: 10},
		{source: catalog.DataSource{ID: 32, Name: "warehouse-primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "WAREHOUSE_DSN", CreatedAt: createdAt, UpdatedAt: createdAt}, projectID: 20},
	} {
		if err := catalogRepository.CreateDataSource(ctx, linked.source, linked.projectID); err != nil {
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

	listedProjects, err := projects.ListProjects(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(listedProjects) != 2 || listedProjects[0].Name != "orders" || listedProjects[1].Name != "warehouse" {
		t.Fatalf("projects = %#v, want orders then warehouse", listedProjects)
	}
	if !listedProjects[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("project createdAt = %v, want %v", listedProjects[0].CreatedAt, createdAt)
	}

	if limited, err := projects.ListProjects(ctx, 1); err != nil || len(limited) != 1 {
		t.Fatalf("limited projects = %#v err = %v, want one row", limited, err)
	}

	sources, err := catalogRepository.ListDataSources(ctx, 10, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].Name != "orders-primary" || sources[1].Name != "orders-replica" {
		t.Fatalf("data sources = %#v, want the two project 10 rows in name order", sources)
	}
	if sources[0].DSNEnvironment != "ORDERS_DSN" || sources[0].Kind != catalog.DataSourceMySQL {
		t.Fatalf("data source = %#v", sources[0])
	}

	listedRepositories, err := repositories.ListCodeRepositories(ctx, 10, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(listedRepositories) != 2 || listedRepositories[0].Name != "orders-api" {
		t.Fatalf("repositories = %#v, want the two project 10 rows in name order", listedRepositories)
	}

	empty, err := catalogRepository.ListDataSources(ctx, 999, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("data sources for an unknown project = %#v, want none", empty)
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
	if err := dbsqlite.NewProjectRepository(store).CreateProject(ctx, catalog.Project{
		ID: 10, Name: "orders", CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	catalogRepository := dbsqlite.NewCatalogRepository(store, ids)
	for _, source := range []catalog.DataSource{
		{ID: 30, Name: "primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "PRIMARY_DSN", CreatedAt: createdAt, UpdatedAt: createdAt},
		{ID: 31, Name: "replica", Kind: catalog.DataSourceMySQL, DSNEnvironment: "REPLICA_DSN", CreatedAt: createdAt, UpdatedAt: createdAt},
	} {
		if err := catalogRepository.CreateDataSource(ctx, source, 10); err != nil {
			t.Fatal(err)
		}
	}

	service := catalog.NewService(catalogRepository, ids, func() time.Time { return createdAt })
	for _, dataSourceID := range []int64{30, 31} {
		if _, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
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

	all, err := catalogRepository.SearchCurrentNodes(ctx, 10, 0, "tenant_id", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered search returned %d nodes, want one per data source", len(all))
	}

	scoped, err := catalogRepository.SearchCurrentNodes(ctx, 10, 30, "tenant_id", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].DataSourceID != 30 {
		t.Fatalf("scoped search = %#v, want only data source 30", scoped)
	}
}
