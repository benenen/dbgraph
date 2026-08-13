package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestSchemaScanLifecyclePublishesThePrestartedRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	service := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })
	source, err := service.CreateDataSource(ctx, catalog.CreateDataSource{
		Name: "primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "PRIMARY_MYSQL_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}

	run, err := service.BeginSchemaScan(ctx, source.ID)
	if err != nil {
		t.Fatalf("BeginSchemaScan: %v", err)
	}
	if run.ID <= 0 || run.DataSourceID != source.ID || !run.StartedAt.Equal(fixedTime) {
		t.Fatalf("scan run = %#v", run)
	}
	published, err := service.PublishStartedSnapshot(ctx, run, catalog.PublishSnapshot{
		DataSourceID: source.ID,
		Nodes: []catalog.NodeInput{
			{StableKey: "database:primary", Kind: catalog.NodeDatabase, Name: "primary", QualifiedName: "primary"},
			{StableKey: "schema:app", ParentStableKey: "database:primary", Kind: catalog.NodeSchema, Name: "app", QualifiedName: "app"},
			{StableKey: "table:app.orders", ParentStableKey: "schema:app", Kind: catalog.NodeTable, Name: "orders", QualifiedName: "app.orders"},
		},
	})
	if err != nil {
		t.Fatalf("PublishStartedSnapshot: %v", err)
	}
	if published.ScanRunID != run.ID || published.NodeCount != 3 {
		t.Fatalf("published snapshot = %#v, run = %#v", published, run)
	}
	table, err := service.FindCurrentNode(ctx, source.ID, "app.orders")
	if err != nil {
		t.Fatalf("find current table: %v", err)
	}
	got, err := service.GetCurrentNode(ctx, table.ID)
	if err != nil {
		t.Fatalf("GetCurrentNode: %v", err)
	}
	if got.ID != table.ID || got.ScanRunID != run.ID {
		t.Fatalf("current node = %#v, table = %#v", got, table)
	}
}

func TestSchemaScanLifecycleRejectsInvalidTransitionsAndRecordsFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	service := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })
	source, err := service.CreateDataSource(ctx, catalog.CreateDataSource{
		Name: "failed", Kind: catalog.DataSourceMySQL, DSNEnvironment: "FAILED_MYSQL_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}
	if _, err := service.BeginSchemaScan(ctx, 0); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("BeginSchemaScan invalid error = %v", err)
	}
	if err := service.FailSchemaScan(ctx, catalog.SchemaScanRun{}, "CONNECTION_FAILED"); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("FailSchemaScan invalid run error = %v", err)
	}

	run, err := service.BeginSchemaScan(ctx, source.ID)
	if err != nil {
		t.Fatalf("BeginSchemaScan: %v", err)
	}
	if _, err := service.PublishStartedSnapshot(ctx, run, catalog.PublishSnapshot{
		DataSourceID: source.ID + 1,
		Nodes:        []catalog.NodeInput{{StableKey: "database:failed", Kind: catalog.NodeDatabase, Name: "failed", QualifiedName: "failed"}},
	}); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("mismatched publish error = %v", err)
	}
	if err := service.FailSchemaScan(ctx, run, " CONNECTION_FAILED "); err != nil {
		t.Fatalf("FailSchemaScan: %v", err)
	}

	if err := service.DeleteDataSource(ctx, source.ID); err != nil {
		t.Fatalf("DeleteDataSource after failed scan: %v", err)
	}
	if _, err := service.GetDataSource(ctx, source.ID); !errors.Is(err, catalog.ErrDataSourceNotFound) {
		t.Fatalf("GetDataSource after delete error = %v", err)
	}
	if err := service.DeleteDataSource(ctx, 0); !errors.Is(err, catalog.ErrInvalidDataSource) {
		t.Fatalf("DeleteDataSource invalid error = %v", err)
	}
	if _, err := service.GetCurrentNode(ctx, 0); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("GetCurrentNode invalid error = %v", err)
	}
}
