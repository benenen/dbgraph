package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/benenen/dbgraph/internal/sourcebinding"
)

type sourceBindingRepositoryScenario struct {
	ctx        context.Context
	adapter    *dbsqlite.SourceBindingRepository
	service    *sourcebinding.Service
	repository catalog.CodeRepository
	source     catalog.DataSource
	principal  relations.Principal
	create     sourcebinding.ReplaceBindingSet
	first      sourcebinding.BindingRevision
}

func TestSourceBindingRepositoryPersistsCurrentRevisionAndIdempotency(t *testing.T) {
	t.Parallel()

	scenario := newSourceBindingRepositoryScenario(t)
	assertSourceBindingIdempotency(t, scenario)
	assertSourceBindingUnbind(t, scenario)
	assertSourceBindingRepositoryBoundaries(t, scenario)
}

func newSourceBindingRepositoryScenario(t *testing.T) sourceBindingRepositoryScenario {
	t.Helper()
	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(23, func() time.Time { return now })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	repository, source := seedSourceBindingRepositoryScenario(t, ctx, store, ids, now)
	adapter := dbsqlite.NewSourceBindingRepository(store)
	service := sourcebinding.NewService(adapter, ids, func() time.Time { return now })
	principal := relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb}
	create := sourcebinding.ReplaceBindingSet{
		RepositoryID: repository.ID, Context: "production", DataSourceIDs: []int64{source.ID},
		ExpectedRevisionNo: 0, Principal: principal, Reason: "Bind production.", RequestID: "binding-adapter-1",
	}
	first, err := service.ReplaceBindingSet(ctx, create)
	if err != nil {
		t.Fatalf("create binding: %v", err)
	}
	if first.RevisionNo != 1 || len(first.DataSources) != 1 || first.DataSources[0].ID != source.ID {
		t.Fatalf("first revision = %#v", first)
	}
	return sourceBindingRepositoryScenario{
		ctx: ctx, adapter: adapter, service: service, repository: repository, source: source,
		principal: principal, create: create, first: first,
	}
}

func seedSourceBindingRepositoryScenario(
	t *testing.T,
	ctx context.Context,
	store *dbsqlite.Store,
	ids *id.Generator,
	now time.Time,
) (catalog.CodeRepository, catalog.DataSource) {
	t.Helper()
	repositories := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), ids, func() time.Time { return now })
	repository, err := repositories.Create(ctx, catalog.CreateCodeRepository{
		Name: "orders-service", RemoteURL: "https://github.com/acme/orders.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	catalogService := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return now })
	source, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{
		Name: "orders-primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "ORDERS_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}
	return repository, source
}

func assertSourceBindingIdempotency(t *testing.T, scenario sourceBindingRepositoryScenario) {
	t.Helper()
	repeated, err := scenario.service.ReplaceBindingSet(scenario.ctx, scenario.create)
	if err != nil {
		t.Fatalf("repeat binding: %v", err)
	}
	if repeated.ID != scenario.first.ID || repeated.RevisionNo != scenario.first.RevisionNo {
		t.Fatalf("repeated revision = %#v, want %#v", repeated, scenario.first)
	}
	current, err := scenario.adapter.GetCurrentBinding(scenario.ctx, scenario.repository.ID, "production")
	if err != nil {
		t.Fatalf("get current binding: %v", err)
	}
	if current.ID != scenario.first.ID || len(current.DataSources) != 1 {
		t.Fatalf("current binding = %#v", current)
	}
}

func assertSourceBindingUnbind(t *testing.T, scenario sourceBindingRepositoryScenario) {
	t.Helper()
	unbind := sourcebinding.ReplaceBindingSet{
		RepositoryID: scenario.repository.ID, Context: "production", ExpectedRevisionNo: 1,
		Principal: scenario.principal, Reason: "Unbind production.", RequestID: "binding-adapter-2",
	}
	second, err := scenario.service.ReplaceBindingSet(scenario.ctx, unbind)
	if err != nil {
		t.Fatalf("unbind: %v", err)
	}
	if second.RevisionNo != 2 || len(second.DataSources) != 0 {
		t.Fatalf("second revision = %#v", second)
	}
	resolution, err := scenario.service.ResolveWorkspace(scenario.ctx, sourcebinding.WorkspaceEvidence{
		Remotes: []string{"https://github.com/acme/orders.git"}, Context: "production",
	})
	if err != nil {
		t.Fatalf("resolve unbound repository: %v", err)
	}
	if resolution.Status != sourcebinding.StatusUnbound || resolution.BindingRevisionNo != 2 {
		t.Fatalf("unbound resolution = %#v", resolution)
	}
}

func assertSourceBindingRepositoryBoundaries(t *testing.T, scenario sourceBindingRepositoryScenario) {
	t.Helper()
	stale := scenario.create
	stale.RequestID = "binding-adapter-stale"
	if _, err := scenario.service.ReplaceBindingSet(scenario.ctx, stale); !errors.Is(err, sourcebinding.ErrRevisionConflict) {
		t.Fatalf("stale replacement error = %v", err)
	}
	if _, err := scenario.adapter.GetRepository(scenario.ctx, -1); !errors.Is(err, sourcebinding.ErrRepositoryNotFound) {
		t.Fatalf("missing repository error = %v", err)
	}
	if matches, err := scenario.adapter.FindRepositoriesByCanonicalRemotes(scenario.ctx, nil, 0); err != nil || len(matches) != 0 {
		t.Fatalf("empty identity lookup = %#v, %v", matches, err)
	}
}
