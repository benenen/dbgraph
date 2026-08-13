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

	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{}); err != nil {
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
	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{}); err != nil {
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
		DSNEnvironment: "GRAPH_FIXTURE_DSN",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, store, project.ID, source.ID, catalogService
}

// Columns were always scanned and never readable; indexes were never scanned at
// all. Both belong to a table, and both have to survive a round trip through
// storage for the console to show them.
func TestTableDetailReturnsColumnsAndIndexes(t *testing.T) {
	t.Parallel()

	ctx, store, project, source, catalogService := newGraphFixture(t, "detail")
	nodes := sampleTables()
	for position := range nodes {
		if nodes[position].StableKey != "table:shop.orders" {
			continue
		}
		nodes[position].Indexes = []catalog.Index{
			{Name: "PRIMARY", Unique: true, Primary: true, Columns: []string{"id"}},
			{Name: "idx_user_buyer", Columns: []string{"user_id", "buyer_id"}},
		}
	}
	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{}); err != nil {
		t.Fatalf("PublishSnapshot: %v", err)
	}
	repository := dbsqlite.NewCatalogRepository(store, nil)
	listed, err := repository.ListTables(ctx, project, source, "orders", 50)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListTables = %#v err = %v", listed, err)
	}

	detail, err := repository.LoadTableDetail(ctx, project, listed[0].ID)
	if err != nil {
		t.Fatalf("LoadTableDetail: %v", err)
	}
	if len(detail.Columns) != 2 {
		t.Fatalf("columns = %#v, want user_id and buyer_id", detail.Columns)
	}
	// Ordinal order is the table's own order, which is how a reader expects to
	// read a table rather than alphabetically.
	if detail.Columns[0].Name != "user_id" || detail.Columns[1].Name != "buyer_id" {
		t.Fatalf("column order = %#v, want user_id then buyer_id", detail.Columns)
	}
	if detail.Columns[0].DataType != "bigint" {
		t.Fatalf("first column = %#v", detail.Columns[0])
	}
	if len(detail.Indexes) != 2 {
		t.Fatalf("indexes = %#v, want two", detail.Indexes)
	}
	if !detail.Indexes[0].Primary || !detail.Indexes[0].Unique {
		t.Fatalf("PRIMARY = %#v, want primary and unique", detail.Indexes[0])
	}
	// A composite index serves a prefix of its columns and no other, so the
	// declared order is the useful part of recording it.
	composite := detail.Indexes[1]
	if len(composite.Columns) != 2 || composite.Columns[0] != "user_id" || composite.Columns[1] != "buyer_id" {
		t.Fatalf("composite index = %#v, want user_id then buyer_id", composite)
	}
	if composite.Unique {
		t.Fatalf("index %q reported unique", composite.Name)
	}

	// A table with no recorded indexes reads back empty, not as an error: every
	// row scanned before indexes were captured holds the empty object.
	users, err := repository.ListTables(ctx, project, source, "users", 50)
	if err != nil || len(users) != 1 {
		t.Fatalf("ListTables users = %#v err = %v", users, err)
	}
	plain, err := repository.LoadTableDetail(ctx, project, users[0].ID)
	if err != nil {
		t.Fatalf("LoadTableDetail without indexes: %v", err)
	}
	if len(plain.Indexes) != 0 || len(plain.Columns) != 1 {
		t.Fatalf("plain table = %#v", plain)
	}
}
