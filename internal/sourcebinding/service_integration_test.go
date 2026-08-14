package sourcebinding_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	"github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/benenen/dbgraph/internal/sourcebinding"
)

var sourceBindingTestTime = time.Date(2026, time.August, 14, 5, 0, 0, 123, time.UTC)

func TestServiceReplacesAndResolvesAnExactRemoteBindingSet(t *testing.T) {
	t.Parallel()

	scenario := createExactBindingScenario(t)
	assertExactBindingRevision(t, scenario)
	assertExactBindingResolution(t, scenario)
	assertExactBindingAudit(t, scenario)
}

type exactBindingScenario struct {
	fixture    sourceBindingFixture
	repository catalog.CodeRepository
	primary    catalog.DataSource
	analytics  catalog.DataSource
	created    sourcebinding.BindingRevision
}

func createExactBindingScenario(t *testing.T) exactBindingScenario {
	t.Helper()
	fixture := newSourceBindingFixture(t)
	repository, err := fixture.repositories.Create(fixture.ctx, catalog.CreateCodeRepository{
		Name: "orders-service", RemoteURL: "https://github.com/acme/orders-service.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	primary, err := fixture.catalog.CreateDataSource(fixture.ctx, catalog.CreateDataSource{
		Name: "orders-primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "ORDERS_PRIMARY_DSN",
	})
	if err != nil {
		t.Fatalf("create primary data source: %v", err)
	}
	analytics, err := fixture.catalog.CreateDataSource(fixture.ctx, catalog.CreateDataSource{
		Name: "orders-analytics", Kind: catalog.DataSourceMySQL, DSNEnvironment: "ORDERS_ANALYTICS_DSN",
	})
	if err != nil {
		t.Fatalf("create analytics data source: %v", err)
	}
	created, err := fixture.bindings.ReplaceBindingSet(fixture.ctx, sourcebinding.ReplaceBindingSet{
		RepositoryID: repository.ID, Context: "Production", DataSourceIDs: []int64{analytics.ID, primary.ID},
		ExpectedRevisionNo: 0, Principal: adminPrincipal(),
		Reason: "Bind the workspace to its production databases.", RequestID: "binding-create-1",
	})
	if err != nil {
		t.Fatalf("replace binding set: %v", err)
	}
	return exactBindingScenario{fixture: fixture, repository: repository, primary: primary, analytics: analytics, created: created}
}

func assertExactBindingRevision(t *testing.T, scenario exactBindingScenario) {
	t.Helper()
	created := scenario.created
	if created.ID <= 0 || created.RepositoryID != scenario.repository.ID || created.Context != "production" ||
		created.RevisionNo != 1 || !created.CreatedAt.Equal(sourceBindingTestTime) {
		t.Fatalf("created binding = %#v", created)
	}
	assertDataSources(t, created.DataSources, map[int64]string{
		scenario.primary.ID: "orders-primary", scenario.analytics.ID: "orders-analytics",
	})
}

func assertExactBindingResolution(t *testing.T, scenario exactBindingScenario) {
	t.Helper()
	resolved, err := scenario.fixture.bindings.ResolveWorkspace(scenario.fixture.ctx, sourcebinding.WorkspaceEvidence{
		Remotes: []string{"https://github.com/acme/orders-service.git"}, Context: "production",
	})
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	if resolved.Status != sourcebinding.StatusResolved || resolved.RepositoryID != scenario.repository.ID ||
		resolved.RepositoryName != "orders-service" || resolved.BindingRevisionID != scenario.created.ID ||
		resolved.BindingRevisionNo != 1 {
		t.Fatalf("resolution = %#v", resolved)
	}
	assertDataSources(t, resolved.DataSources, map[int64]string{
		scenario.primary.ID: "orders-primary", scenario.analytics.ID: "orders-analytics",
	})
}

func assertExactBindingAudit(t *testing.T, scenario exactBindingScenario) {
	t.Helper()
	events, err := sqlite.NewAuditRepository(scenario.fixture.store).ListAuditEvents(scenario.fixture.ctx, 10)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("audit event count = %d, want 1", len(events))
	}
	event := events[0]
	if event.Action != "SOURCE_BINDING_SET_REPLACED" || event.SubjectType != "SOURCE_BINDING_REVISION" ||
		event.SubjectID != scenario.created.ID || event.ExpectedRevision == nil || *event.ExpectedRevision != 0 ||
		event.Actor != "admin@example.test" || event.Reason != "Bind the workspace to its production databases." ||
		event.RequestID != "binding-create-1" {
		t.Fatalf("audit event = %#v", event)
	}
	var details map[string]any
	if err := json.Unmarshal(event.Details, &details); err != nil {
		t.Fatalf("decode audit details: %v", err)
	}
	dataSourceIDs, _ := details["dataSourceIds"].([]any)
	if details["repositoryId"] != formatTestID(scenario.repository.ID) || details["context"] != "production" ||
		details["revisionNo"] != float64(1) || len(dataSourceIDs) != 2 ||
		dataSourceIDs[0] != formatTestID(scenario.primary.ID) || dataSourceIDs[1] != formatTestID(scenario.analytics.ID) {
		t.Fatalf("audit details = %#v", details)
	}
	encodedDetails := string(event.Details)
	for _, forbidden := range []string{"ORDERS_PRIMARY_DSN", "ORDERS_ANALYTICS_DSN", "dsnEnvironment", "credential"} {
		if contains(encodedDetails, forbidden) {
			t.Fatalf("audit details contain %q: %s", forbidden, encodedDetails)
		}
	}
}

func formatTestID(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestResolveWorkspaceRejectsUnsafeRemoteEvidence(t *testing.T) {
	t.Parallel()

	service := sourcebinding.NewService(unusedSourceBindingRepository{}, nil, nil)
	for _, remote := range []string{
		"https://token@github.com/acme/orders-service.git",
		"https://github.com/acme/orders-service.git?token=secret",
		"file:///root/workspace/orders-service",
		"https://github.com/acme/../orders-service.git",
		"ssh://alice@git.example.test/platform/orders-service.git",
		"bob@git.example.test:platform/orders-service.git",
		"ssh://git.example.test/platform/orders-service.git",
		"git.example.test:platform/orders-service.git",
		"git@git.example.test:/platform/orders-service.git",
		"https://git.example.test/platform%2Forders-service.git",
		"https://git.example.test/platform//orders-service.git",
	} {
		_, err := service.ResolveWorkspace(context.Background(), sourcebinding.WorkspaceEvidence{
			Remotes: []string{remote}, Context: "production",
		})
		if !errors.Is(err, sourcebinding.ErrInvalidWorkspaceEvidence) {
			t.Errorf("ResolveWorkspace(%q) error = %v, want invalid workspace evidence", remote, err)
		}
	}
}

func TestResolveWorkspaceRecognizesARegisteredRepositoryBeforeItsFirstBinding(t *testing.T) {
	t.Parallel()

	fixture := newSourceBindingFixture(t)
	repository, err := fixture.repositories.Create(fixture.ctx, catalog.CreateCodeRepository{
		Name: "reporting-service", RemoteURL: "https://github.com/acme/reporting.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	for _, test := range []struct {
		context string
		status  sourcebinding.ResolutionStatus
	}{
		{context: "production", status: sourcebinding.StatusUnbound},
		{context: "", status: sourcebinding.StatusContextRequired},
	} {
		resolution, err := fixture.bindings.ResolveWorkspace(fixture.ctx, sourcebinding.WorkspaceEvidence{
			Remotes: []string{"https://github.com/acme/reporting.git"}, Context: test.context,
		})
		if err != nil {
			t.Fatalf("resolve registered repository: %v", err)
		}
		if resolution.Status != test.status || resolution.RepositoryID != repository.ID ||
			resolution.RepositoryName != repository.Name || len(resolution.DataSources) != 0 {
			t.Fatalf("resolution = %#v, want status %s and repository %d", resolution, test.status, repository.ID)
		}
	}
}

func TestResolveWorkspaceDoesNotGuessCrossTransportRemoteEquivalence(t *testing.T) {
	t.Parallel()

	fixture := newSourceBindingFixture(t)
	fixture.createBoundRepository(
		t, "ledger-service", "https://git.example.test/platform/ledger.git", "ledger-primary", "ledger-production-1",
	)
	resolution, err := fixture.bindings.ResolveWorkspace(fixture.ctx, sourcebinding.WorkspaceEvidence{
		Remotes: []string{"ssh://git@git.example.test/platform/ledger.git"}, Context: "production",
	})
	if err != nil {
		t.Fatalf("resolve cross-transport evidence: %v", err)
	}
	if resolution.Status != sourcebinding.StatusRepositoryNotFound || resolution.RepositoryID != 0 ||
		len(resolution.DataSources) != 0 {
		t.Fatalf("cross-transport resolution = %#v", resolution)
	}
}

func TestResolveWorkspaceDoesNotGuessDotGitSuffixEquivalence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		registered string
		evidence   string
	}{
		{name: "HTTPS", registered: "https://git.example.test/platform/ledger", evidence: "https://git.example.test/platform/ledger.git"},
		{name: "SSH URL", registered: "ssh://git@git.example.test/platform/ledger", evidence: "ssh://git@git.example.test/platform/ledger.git"},
		{name: "scp style", registered: "git@git.example.test:platform/ledger", evidence: "git@git.example.test:platform/ledger.git"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSourceBindingFixture(t)
			fixture.createBoundRepository(
				t, "ledger-service", test.registered, "ledger-primary", "ledger-production-1",
			)
			resolution, err := fixture.bindings.ResolveWorkspace(fixture.ctx, sourcebinding.WorkspaceEvidence{
				Remotes: []string{test.evidence}, Context: "production",
			})
			if err != nil {
				t.Fatalf("resolve suffix evidence: %v", err)
			}
			if resolution.Status != sourcebinding.StatusRepositoryNotFound || resolution.RepositoryID != 0 ||
				len(resolution.DataSources) != 0 {
				t.Fatalf("suffix resolution = %#v", resolution)
			}
		})
	}
}

func TestServiceRegistersBindsAndResolvesASafeSSHURL(t *testing.T) {
	t.Parallel()

	fixture := newSourceBindingFixture(t)
	repository, source := fixture.createBoundRepository(
		t, "shipping-service", "ssh://git@git.example.test/platform/shipping.git",
		"shipping-primary", "shipping-production-1",
	)
	resolution, err := fixture.bindings.ResolveWorkspace(fixture.ctx, sourcebinding.WorkspaceEvidence{
		Remotes: []string{"ssh://git@git.example.test/platform/shipping.git"}, Context: "production",
	})
	if err != nil {
		t.Fatalf("resolve SSH binding: %v", err)
	}
	if resolution.Status != sourcebinding.StatusResolved || resolution.RepositoryID != repository.ID ||
		len(resolution.DataSources) != 1 || resolution.DataSources[0].ID != source.ID {
		t.Fatalf("SSH resolution = %#v", resolution)
	}
}

func TestDeleteDataSourceRefusesAReferencedBindingMember(t *testing.T) {
	t.Parallel()

	fixture := newSourceBindingFixture(t)
	repository, source := fixture.createBoundRepository(
		t, "payments-service", "https://github.com/acme/payments.git", "payments-primary", "payments-production-1",
	)
	if err := fixture.catalog.DeleteDataSource(fixture.ctx, source.ID); !errors.Is(err, catalog.ErrDataSourceInUse) {
		t.Fatalf("DeleteDataSource = %v, want ErrDataSourceInUse", err)
	}
	resolution, err := fixture.bindings.ResolveWorkspace(fixture.ctx, sourcebinding.WorkspaceEvidence{
		Remotes: []string{"https://github.com/acme/payments.git"}, Context: "production",
	})
	if err != nil {
		t.Fatalf("resolve retained binding: %v", err)
	}
	if resolution.Status != sourcebinding.StatusResolved || resolution.RepositoryID != repository.ID ||
		len(resolution.DataSources) != 1 || resolution.DataSources[0].ID != source.ID {
		t.Fatalf("resolution after refused delete = %#v", resolution)
	}
}

func TestReplaceBindingSetIsIdempotentForTheSameRequest(t *testing.T) {
	t.Parallel()

	fixture := newSourceBindingFixture(t)
	repository, err := fixture.repositories.Create(fixture.ctx, catalog.CreateCodeRepository{
		Name: "billing-service", RemoteURL: "https://git.example.test/platform/billing.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	source, err := fixture.catalog.CreateDataSource(fixture.ctx, catalog.CreateDataSource{
		Name: "billing-primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "BILLING_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}
	command := sourcebinding.ReplaceBindingSet{
		RepositoryID: repository.ID, Context: "production", DataSourceIDs: []int64{source.ID},
		ExpectedRevisionNo: 0,
		Principal: relations.Principal{
			Actor: "admin@example.test", Role: relations.RoleAdmin, Origin: audit.OriginWeb,
		},
		Reason: "Bind billing production.", RequestID: "binding-idempotent-1",
	}
	first, err := fixture.bindings.ReplaceBindingSet(fixture.ctx, command)
	if err != nil {
		t.Fatalf("first replace: %v", err)
	}
	repeated, err := fixture.bindings.ReplaceBindingSet(fixture.ctx, command)
	if err != nil {
		t.Fatalf("repeat replace: %v", err)
	}
	if repeated.ID != first.ID || repeated.RevisionNo != first.RevisionNo ||
		!repeated.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("repeated binding = %#v, want original %#v", repeated, first)
	}

	conflicting := command
	conflicting.DataSourceIDs = nil
	if _, err := fixture.bindings.ReplaceBindingSet(fixture.ctx, conflicting); !errors.Is(err, sourcebinding.ErrBindingConflict) {
		t.Fatalf("conflicting retry error = %v, want binding conflict", err)
	}
	assertSingleBindingAuditEvent(t, fixture)
}

func assertSingleBindingAuditEvent(t *testing.T, fixture sourceBindingFixture) {
	t.Helper()
	events, err := sqlite.NewAuditRepository(fixture.store).ListAuditEvents(fixture.ctx, 10)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("audit event count = %d, want 1", len(events))
	}
}

func TestReplaceBindingSetRejectsUnauthorizedCallersBeforeValidation(t *testing.T) {
	t.Parallel()

	service := sourcebinding.NewService(unusedSourceBindingRepository{}, panicIDGenerator{}, nil)
	_, err := service.ReplaceBindingSet(context.Background(), sourcebinding.ReplaceBindingSet{
		Context: "not valid context!",
		Principal: relations.Principal{
			Actor: "reviewer@example.test", Role: relations.RoleReviewer, Origin: audit.OriginWeb,
		},
	})
	if !errors.Is(err, sourcebinding.ErrForbidden) {
		t.Fatalf("ReplaceBindingSet error = %v, want forbidden", err)
	}
}

func TestReplaceBindingSetRejectsAnOversizedExpectedRevisionBeforePersistence(t *testing.T) {
	t.Parallel()

	service := sourcebinding.NewService(unusedSourceBindingRepository{}, panicIDGenerator{}, nil)
	_, err := service.ReplaceBindingSet(context.Background(), sourcebinding.ReplaceBindingSet{
		RepositoryID: 1, Context: "production", ExpectedRevisionNo: int(^uint(0) >> 1),
		Principal: adminPrincipal(), Reason: "Invalid oversized revision.", RequestID: "oversized-revision-1",
	})
	if !errors.Is(err, sourcebinding.ErrInvalidBinding) {
		t.Fatalf("ReplaceBindingSet error = %v, want invalid binding", err)
	}
}

func TestResolveWorkspaceReturnsExplicitNonResolvedStatuses(t *testing.T) {
	t.Parallel()

	fixture, firstRepositoryID := createNonResolvedStatusScenario(t)
	for _, test := range nonResolvedStatusCases(firstRepositoryID) {
		t.Run(test.name, func(t *testing.T) {
			resolution, err := fixture.bindings.ResolveWorkspace(fixture.ctx, test.evidence)
			if err != nil {
				t.Fatalf("resolve workspace: %v", err)
			}
			if resolution.Status != test.wantStatus || resolution.RepositoryID != test.wantRepositoryID ||
				resolution.BindingRevisionNo != test.wantBindingRevisionNo {
				t.Fatalf("resolution = %#v", resolution)
			}
			if test.wantStatus != sourcebinding.StatusResolved && len(resolution.DataSources) != 0 {
				t.Fatalf("non-resolved data sources = %#v", resolution.DataSources)
			}
		})
	}
}

type nonResolvedStatusCase struct {
	name                  string
	evidence              sourcebinding.WorkspaceEvidence
	wantStatus            sourcebinding.ResolutionStatus
	wantRepositoryID      int64
	wantBindingRevisionNo int
}

func createNonResolvedStatusScenario(t *testing.T) (sourceBindingFixture, int64) {
	t.Helper()
	fixture := newSourceBindingFixture(t)
	firstRepository, _ := fixture.createBoundRepository(
		t, "orders-service", "https://github.com/acme/orders-service.git", "orders-primary", "orders-production-1",
	)
	fixture.createBoundRepository(
		t, "billing-service", "https://git.example.test/platform/billing.git", "billing-primary", "billing-production-1",
	)
	_, err := fixture.bindings.ReplaceBindingSet(fixture.ctx, sourcebinding.ReplaceBindingSet{
		RepositoryID: firstRepository.ID, Context: "staging", DataSourceIDs: nil, ExpectedRevisionNo: 0,
		Principal: adminPrincipal(), Reason: "Explicitly unbind staging.", RequestID: "orders-staging-unbind-1",
	})
	if err != nil {
		t.Fatalf("create unbound context: %v", err)
	}
	return fixture, firstRepository.ID
}

func nonResolvedStatusCases(firstRepositoryID int64) []nonResolvedStatusCase {
	return []nonResolvedStatusCase{
		{
			name: "repository not found",
			evidence: sourcebinding.WorkspaceEvidence{
				Remotes: []string{"https://github.com/acme/orders-service-copy.git"}, Context: "production",
			},
			wantStatus: sourcebinding.StatusRepositoryNotFound,
		},
		{
			name: "context required",
			evidence: sourcebinding.WorkspaceEvidence{
				Remotes: []string{"https://github.com/acme/orders-service.git"},
			},
			wantStatus: sourcebinding.StatusContextRequired, wantRepositoryID: firstRepositoryID,
		},
		{
			name: "explicitly unbound",
			evidence: sourcebinding.WorkspaceEvidence{
				Remotes: []string{"https://github.com/acme/orders-service.git"}, Context: "staging",
			},
			wantStatus: sourcebinding.StatusUnbound, wantRepositoryID: firstRepositoryID, wantBindingRevisionNo: 1,
		},
		{
			name: "ambiguous repository",
			evidence: sourcebinding.WorkspaceEvidence{
				Remotes: []string{
					"https://github.com/acme/orders-service.git",
					"https://git.example.test/platform/billing.git",
				},
				Context: "production",
			},
			wantStatus: sourcebinding.StatusAmbiguousRepository,
		},
	}
}

func TestReplaceBindingSetUsesOptimisticRevisions(t *testing.T) {
	t.Parallel()

	fixture := newSourceBindingFixture(t)
	repository, source := fixture.createBoundRepository(
		t, "inventory-service", "https://github.com/acme/inventory.git", "inventory-primary", "inventory-production-1",
	)
	second, err := fixture.bindings.ReplaceBindingSet(fixture.ctx, sourcebinding.ReplaceBindingSet{
		RepositoryID: repository.ID, Context: "production", DataSourceIDs: nil, ExpectedRevisionNo: 1,
		Principal: adminPrincipal(), Reason: "Unbind the retired database.", RequestID: "inventory-production-2",
	})
	if err != nil {
		t.Fatalf("replace revision 2: %v", err)
	}
	if second.RevisionNo != 2 || len(second.DataSources) != 0 {
		t.Fatalf("second revision = %#v", second)
	}
	_, err = fixture.bindings.ReplaceBindingSet(fixture.ctx, sourcebinding.ReplaceBindingSet{
		RepositoryID: repository.ID, Context: "production", DataSourceIDs: []int64{source.ID}, ExpectedRevisionNo: 1,
		Principal: adminPrincipal(), Reason: "Stale retry.", RequestID: "inventory-production-stale",
	})
	var conflict *sourcebinding.RevisionConflictError
	if !errors.Is(err, sourcebinding.ErrRevisionConflict) || !errors.As(err, &conflict) ||
		conflict.CurrentRevisionNo != 2 || err.Error() != "source binding revision conflict" {
		t.Fatalf("stale replace error = %#v, want current revision 2", err)
	}
	resolved, err := fixture.bindings.ResolveWorkspace(fixture.ctx, sourcebinding.WorkspaceEvidence{
		Remotes: []string{"https://github.com/acme/inventory.git"}, Context: "production",
	})
	if err != nil {
		t.Fatalf("resolve current binding: %v", err)
	}
	if resolved.Status != sourcebinding.StatusUnbound || resolved.BindingRevisionID != second.ID ||
		resolved.BindingRevisionNo != 2 {
		t.Fatalf("current resolution = %#v", resolved)
	}
}

func TestReplaceBindingSetRejectsADuplicateRepositoryIdentity(t *testing.T) {
	t.Parallel()

	fixture := newSourceBindingFixture(t)
	first, err := fixture.repositories.Create(fixture.ctx, catalog.CreateCodeRepository{
		Name: "first-orders", RemoteURL: "https://github.com/acme/duplicate-orders.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create first repository: %v", err)
	}
	if _, err := fixture.repositories.Create(fixture.ctx, catalog.CreateCodeRepository{
		Name: "second-orders", RemoteURL: "https://github.com/acme/duplicate-orders.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("create duplicate repository: %v", err)
	}
	resolution, err := fixture.bindings.ResolveWorkspace(fixture.ctx, sourcebinding.WorkspaceEvidence{
		Remotes: []string{"https://github.com/acme/duplicate-orders.git"}, Context: "production",
	})
	if err != nil {
		t.Fatalf("resolve duplicate identity: %v", err)
	}
	if resolution.Status != sourcebinding.StatusAmbiguousRepository || resolution.RepositoryID != 0 {
		t.Fatalf("duplicate identity resolution = %#v", resolution)
	}
	_, err = fixture.bindings.ReplaceBindingSet(fixture.ctx, sourcebinding.ReplaceBindingSet{
		RepositoryID: first.ID, Context: "production", ExpectedRevisionNo: 0,
		Principal: adminPrincipal(), Reason: "Ambiguous identity must not bind.", RequestID: "duplicate-identity-1",
	})
	if !errors.Is(err, sourcebinding.ErrBindingConflict) {
		t.Fatalf("ReplaceBindingSet error = %v, want binding conflict", err)
	}
}

type panicIDGenerator struct{}

func (panicIDGenerator) Next(context.Context) (int64, error) {
	panic("unauthorized command generated an ID")
}

type sourceBindingFixture struct {
	ctx          context.Context
	store        *sqlite.Store
	repositories *catalog.CodeRepositoryService
	catalog      *catalog.Service
	bindings     *sourcebinding.Service
}

func (f sourceBindingFixture) createBoundRepository(
	t *testing.T,
	repositoryName string,
	remote string,
	dataSourceName string,
	requestID string,
) (catalog.CodeRepository, catalog.DataSource) {
	t.Helper()
	repository, err := f.repositories.Create(f.ctx, catalog.CreateCodeRepository{
		Name: repositoryName, RemoteURL: remote, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	source, err := f.catalog.CreateDataSource(f.ctx, catalog.CreateDataSource{
		Name: dataSourceName, Kind: catalog.DataSourceMySQL,
		DSNEnvironment: strings.ToUpper(strings.ReplaceAll(dataSourceName, "-", "_")) + "_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}
	if _, err := f.bindings.ReplaceBindingSet(f.ctx, sourcebinding.ReplaceBindingSet{
		RepositoryID: repository.ID, Context: "production", DataSourceIDs: []int64{source.ID}, ExpectedRevisionNo: 0,
		Principal: adminPrincipal(), Reason: "Bind production data source.", RequestID: requestID,
	}); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	return repository, source
}

func adminPrincipal() relations.Principal {
	return relations.Principal{Actor: "admin@example.test", Role: relations.RoleAdmin, Origin: audit.OriginWeb}
}

func newSourceBindingFixture(t *testing.T) sourceBindingFixture {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ids, err := id.NewGenerator(18, func() time.Time { return sourceBindingTestTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	return sourceBindingFixture{
		ctx:          ctx,
		store:        store,
		repositories: catalog.NewCodeRepositoryService(sqlite.NewCodeRepository(store), ids, func() time.Time { return sourceBindingTestTime }),
		catalog:      catalog.NewService(sqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return sourceBindingTestTime }),
		bindings:     sourcebinding.NewService(sqlite.NewSourceBindingRepository(store), ids, func() time.Time { return sourceBindingTestTime }),
	}
}

type unusedSourceBindingRepository struct{}

func (unusedSourceBindingRepository) GetRepository(context.Context, int64) (sourcebinding.RepositoryRecord, error) {
	panic("unexpected GetRepository call")
}

func (unusedSourceBindingRepository) FindRepositoriesByCanonicalRemotes(
	context.Context,
	[]string,
	int,
) ([]sourcebinding.RepositoryRecord, error) {
	panic("unsafe remote reached repository lookup")
}

func (unusedSourceBindingRepository) GetCurrentBinding(
	context.Context,
	int64,
	string,
) (sourcebinding.BindingRevision, error) {
	panic("unexpected GetCurrentBinding call")
}

func (unusedSourceBindingRepository) ReplaceBinding(
	context.Context,
	sourcebinding.PersistBindingRevision,
	audit.Event,
) (sourcebinding.BindingRevision, error) {
	panic("unexpected ReplaceBinding call")
}

func assertDataSources(t *testing.T, actual []sourcebinding.DataSource, expected map[int64]string) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("data sources = %#v, want %d", actual, len(expected))
	}
	for _, source := range actual {
		if expected[source.ID] != source.Name || source.Kind != "MYSQL" {
			t.Fatalf("data source = %#v, expected names = %#v", source, expected)
		}
	}
}

func contains(value string, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
