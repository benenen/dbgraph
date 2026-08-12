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

// Relations join columns, but a reader wants to see which tables are connected.
// The graph rolls each end up to its owning table while keeping the columns, so
// two relations between the same pair stay distinguishable.
func TestDataSourceGraphDrawsColumnRelationsBetweenTables(t *testing.T) {
	t.Parallel()

	ctx, store, project, source, catalogService := newGraphFixture(t, "graph")

	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project, DataSourceID: source,
		Nodes: sampleTables(),
		ForeignKeys: []catalog.DeclaredForeignKey{
			{ConstraintSchema: "shop", Name: "fk_order_user", SourceColumn: "shop.orders.user_id", TargetColumn: "shop.users.id", Ordinal: 1},
			{ConstraintSchema: "shop", Name: "fk_order_buyer", SourceColumn: "shop.orders.buyer_id", TargetColumn: "shop.users.id", Ordinal: 1},
		},
	}); err != nil {
		t.Fatalf("PublishSnapshot: %v", err)
	}

	result, err := dbsqlite.NewGraphRepository(store).LoadDataSourceGraph(ctx, project, source, 100)
	if err != nil {
		t.Fatalf("LoadDataSourceGraph: %v", err)
	}
	if len(result.Edges) != 2 {
		t.Fatalf("edges = %#v, want the two foreign keys", result.Edges)
	}
	// Two edges, two tables: the pair is not duplicated per relation.
	if len(result.Tables) != 2 {
		t.Fatalf("tables = %#v, want orders and users once each", result.Tables)
	}
	columns := map[string]string{}
	for _, edge := range result.Edges {
		if edge.SourceTableID == edge.TargetTableID {
			t.Fatalf("edge %#v collapsed to a self-reference", edge)
		}
		columns[edge.SourceColumn] = edge.TargetColumn
	}
	if columns["user_id"] != "id" || columns["buyer_id"] != "id" {
		t.Fatalf("column ends = %#v, want both pointing at id", columns)
	}
	if result.Truncated {
		t.Fatal("two edges under a limit of 100 reported truncation")
	}
}

// A relation reaching another data source is left out: a half-drawn edge reads
// as a missing table rather than as a relation that leaves the picture.
func TestDataSourceGraphIsEmptyForAScanWithoutForeignKeys(t *testing.T) {
	t.Parallel()

	ctx, store, project, source, catalogService := newGraphFixture(t, "no-keys")

	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project, DataSourceID: source, Nodes: sampleTables(),
	}); err != nil {
		t.Fatalf("PublishSnapshot: %v", err)
	}

	result, err := dbsqlite.NewGraphRepository(store).LoadDataSourceGraph(ctx, project, source, 100)
	if err != nil {
		t.Fatalf("LoadDataSourceGraph: %v", err)
	}
	if len(result.Edges) != 0 || len(result.Tables) != 0 {
		t.Fatalf("graph = %#v, want empty for a catalog with no relations", result)
	}
}

// Browsing has to work on exactly that catalog, so the table list is a
// substring match and an empty filter lists everything.
func TestListTablesBrowsesAndFiltersLiterally(t *testing.T) {
	t.Parallel()

	ctx, store, project, source, catalogService := newGraphFixture(t, "browse")
	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project, DataSourceID: source, Nodes: sampleTables(),
	}); err != nil {
		t.Fatalf("PublishSnapshot: %v", err)
	}
	repository := dbsqlite.NewCatalogRepository(store, nil)

	all, err := repository.ListTables(ctx, project, source, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Name != "orders" || all[1].Name != "users" {
		t.Fatalf("tables = %#v, want orders then users", all)
	}

	filtered, err := repository.ListTables(ctx, project, source, "user", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Name != "users" {
		t.Fatalf("filtered = %#v, want only users", filtered)
	}

	// A wildcard typed into the filter is a character, not a pattern.
	wild, err := repository.ListTables(ctx, project, source, "%", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(wild) != 0 {
		t.Fatalf("filter %%%% matched %#v, want nothing", wild)
	}
}

func sampleTables() []catalog.NodeInput {
	return []catalog.NodeInput{
		{StableKey: "database:shop", Kind: catalog.NodeDatabase, Name: "shop", QualifiedName: "mysql://shop"},
		{StableKey: "schema:shop", ParentStableKey: "database:shop", Kind: catalog.NodeSchema, Name: "shop", QualifiedName: "shop"},
		{StableKey: "table:shop.orders", ParentStableKey: "schema:shop", Kind: catalog.NodeTable, Name: "orders", QualifiedName: "shop.orders"},
		{StableKey: "column:shop.orders.user_id", ParentStableKey: "table:shop.orders", Kind: catalog.NodeColumn, Name: "user_id", QualifiedName: "shop.orders.user_id", DataType: "bigint", Ordinal: 1},
		{StableKey: "column:shop.orders.buyer_id", ParentStableKey: "table:shop.orders", Kind: catalog.NodeColumn, Name: "buyer_id", QualifiedName: "shop.orders.buyer_id", DataType: "bigint", Ordinal: 2},
		{StableKey: "table:shop.users", ParentStableKey: "schema:shop", Kind: catalog.NodeTable, Name: "users", QualifiedName: "shop.users"},
		{StableKey: "column:shop.users.id", ParentStableKey: "table:shop.users", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "shop.users.id", DataType: "bigint", Ordinal: 1},
	}
}

func newGraphFixture(t *testing.T, name string) (
	context.Context, *dbsqlite.Store, int64, int64, *catalog.Service,
) {
	t.Helper()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fixedTime := time.Date(2026, time.August, 12, 14, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(24, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	project, err := catalog.NewProjectService(
		dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime },
	).Create(ctx, catalog.CreateProject{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	catalogService := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime },
	)
	source, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{
		ProjectID: project.ID, Name: name, Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "GRAPH_FIXTURE_DSN",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, store, project.ID, source.ID, catalogService
}
