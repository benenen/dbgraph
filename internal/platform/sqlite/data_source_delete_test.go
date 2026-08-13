package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

// A source whose scans all failed imported nothing, and that is precisely the
// misconfigured source an operator needs to remove. Its failed runs describe
// attempts to read a source that is going away, so they go with it.
func TestDeleteDataSourceRemovesOneWhoseScansOnlyFailed(t *testing.T) {
	t.Parallel()

	ctx, repository, _ := newDeleteFixture(t)

	startedAt := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
	run := catalog.SchemaScanRun{ID: 50, DataSourceID: 30, StartedAt: startedAt}
	if err := repository.BeginSchemaScan(ctx, run); err != nil {
		t.Fatalf("BeginSchemaScan: %v", err)
	}
	if err := repository.FailSchemaScan(ctx, catalog.SchemaScanFailure{
		Run:         run,
		ErrorCode:   "SOURCE_CONNECTION_FAILED",
		CompletedAt: startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("FailSchemaScan: %v", err)
	}

	if err := repository.DeleteDataSource(ctx, 30); err != nil {
		t.Fatalf("DeleteDataSource with only failed scans: %v", err)
	}

	sources, err := repository.ListAllDataSources(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("data sources = %#v, want the source gone", sources)
	}
}

// An imported catalog is the data itself. Deleting the source would orphan it,
// so the operator has to unlink instead.
func TestDeleteDataSourceRefusesOneThatImportedACatalog(t *testing.T) {
	t.Parallel()

	ctx, repository, service := newDeleteFixture(t)

	if _, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		Nodes: []catalog.NodeInput{
			{StableKey: "database:app", Kind: catalog.NodeDatabase, Name: "app", QualifiedName: "mysql://app"},
		},
	}); err != nil {
		t.Fatalf("PublishSnapshot: %v", err)
	}

	err := repository.DeleteDataSource(ctx, 30)
	if !errors.Is(err, catalog.ErrDataSourceInUse) {
		t.Fatalf("DeleteDataSource = %v, want ErrDataSourceInUse", err)
	}

	sources, err := repository.ListAllDataSources(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("data sources = %#v, want the source kept", sources)
	}
}

func newDeleteFixture(t *testing.T) (context.Context, *dbsqlite.CatalogRepository, *catalog.Service) {
	t.Helper()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	createdAt := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(12, func() time.Time { return createdAt })
	if err != nil {
		t.Fatal(err)
	}
	if err := dbsqlite.NewProjectRepository(store).CreateProject(ctx, catalog.Project{
		ID: 10, Name: "orders", CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	repository := dbsqlite.NewCatalogRepository(store, ids)
	if err := repository.CreateDataSource(ctx, catalog.DataSource{
		ID: 30, Name: "orders-primary", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "ORDERS_DSN", CreatedAt: createdAt, UpdatedAt: createdAt,
	}, 10); err != nil {
		t.Fatal(err)
	}
	return ctx, repository, catalog.NewService(repository, ids, func() time.Time { return createdAt })
}
