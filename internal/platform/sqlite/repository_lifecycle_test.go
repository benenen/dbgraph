package sqlite_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	appauth "github.com/benenen/dbgraph/internal/auth"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/id"
	"github.com/benenen/dbgraph/internal/jobs"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
)

var sqliteLifecycleTime = time.Date(2026, time.August, 13, 7, 0, 0, 0, time.UTC)

func TestSQLiteRelationRepositoryKeepsApprovedContentEffectiveUntilReview(t *testing.T) {
	t.Parallel()

	ctx, store, ids, _, source, target := newSQLiteLifecycleFixture(t, 71)
	commands := relations.NewCommands(
		dbsqlite.NewRelationRepository(store), ids, func() time.Time { return sqliteLifecycleTime },
	)
	editor := relations.Principal{Actor: "agent@example.test", Role: relations.RoleEditor, Origin: audit.OriginAgent}
	reviewer := relations.Principal{Actor: "reviewer@example.test", Role: relations.RoleReviewer, Origin: audit.OriginWeb}
	evidence := []relations.EvidenceInput{{
		Kind: relations.EvidenceCode, Repository: "orders-service", Commit: "abc1234",
		File: "src/Orders.java", Symbol: "Orders.copy", StartLine: 10, EndLine: 12,
	}}

	created, err := commands.ProposeCreate(ctx, relations.ProposeCreate{
		Type: relations.TypeConditionalValueCopy, SourceNodeID: source.ID, TargetNodeID: target.ID,
		Transform:  conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: source.ID},
		Confidence: 0.9, Evidence: evidence, Principal: editor,
		Reason: "Observed a direct assignment.", RequestID: "relation-create",
	})
	if err != nil {
		t.Fatalf("ProposeCreate: %v", err)
	}
	if created.Active != nil || created.Proposed == nil || created.Effective {
		t.Fatalf("unreviewed create = %#v", created)
	}
	proposals, err := commands.ListProposals(ctx, 10)
	if err != nil || len(proposals) != 1 || proposals[0].ID != created.ID {
		t.Fatalf("ListProposals = %#v, error = %v", proposals, err)
	}

	approved, err := commands.Review(ctx, relations.Review{
		RelationID: created.ID, ExpectedRevisionNo: 1, Decision: relations.DecisionApprove,
		Principal: reviewer, Reason: "Evidence verified.", RequestID: "relation-approve",
	})
	if err != nil {
		t.Fatalf("approve create: %v", err)
	}
	if approved.Active == nil || approved.Active.RevisionNo != 1 || approved.Proposed != nil || !approved.Effective {
		t.Fatalf("approved create = %#v", approved)
	}
	for name, testCase := range map[string]struct {
		nodeIDs   []int64
		direction graph.Direction
	}{
		"downstream": {nodeIDs: []int64{source.ID}, direction: graph.DirectionDownstream},
		"upstream":   {nodeIDs: []int64{target.ID}, direction: graph.DirectionUpstream},
	} {
		t.Run(name+" edge load", func(t *testing.T) {
			edges, truncated, loadedBytes, err := dbsqlite.NewGraphRepository(store).LoadEdges(
				ctx, testCase.nodeIDs, testCase.direction, 10, 1<<20,
			)
			if err != nil || truncated || loadedBytes == 0 || len(edges) != 1 || edges[0].RelationID != created.ID {
				t.Fatalf("LoadEdges = %#v, truncated=%t bytes=%d error=%v", edges, truncated, loadedBytes, err)
			}
		})
	}

	revised, err := commands.ProposeRevision(ctx, relations.ProposeRevision{
		RelationID: created.ID, ExpectedRevisionNo: 1,
		SourceNodeID: source.ID, TargetNodeID: target.ID,
		Transform:  conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: source.ID},
		Confidence: 0.8, Evidence: evidence, Principal: editor,
		Reason: "Refresh the reviewed evidence.", RequestID: "relation-revise",
	})
	if err != nil {
		t.Fatalf("ProposeRevision: %v", err)
	}
	if revised.Active == nil || revised.Active.RevisionNo != 1 || revised.Proposed == nil ||
		revised.Proposed.RevisionNo != 2 || !revised.Effective {
		t.Fatalf("pending revision = %#v", revised)
	}
	_, err = commands.ProposeRevision(ctx, relations.ProposeRevision{
		RelationID: created.ID, ExpectedRevisionNo: 1,
		SourceNodeID: source.ID, TargetNodeID: target.ID,
		Transform:  conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: source.ID},
		Confidence: 0.7, Evidence: evidence, Principal: editor,
		Reason: "This request raced with another proposal.", RequestID: "relation-stale-revise",
	})
	var conflict *relations.RevisionConflictError
	if !errors.As(err, &conflict) || conflict.CurrentRevisionNo != 2 {
		t.Fatalf("stale revision error = %v", err)
	}

	rejected, err := commands.Review(ctx, relations.Review{
		RelationID: created.ID, ExpectedRevisionNo: 2, Decision: relations.DecisionReject,
		Principal: reviewer, Reason: "New evidence is incomplete.", RequestID: "relation-reject",
	})
	if err != nil {
		t.Fatalf("reject revision: %v", err)
	}
	if rejected.Active == nil || rejected.Active.RevisionNo != 1 || rejected.Proposed != nil || !rejected.Effective {
		t.Fatalf("rejected revision = %#v", rejected)
	}

	suppressed, err := commands.Suppress(ctx, relations.ChangeState{
		RelationID: created.ID, ExpectedRevisionNo: 2, Principal: reviewer,
		Reason: "Hide while reviewing an incident.", RequestID: "relation-suppress",
	})
	if err != nil || suppressed.Status != relations.StatusSuppressed || suppressed.Effective {
		t.Fatalf("Suppress = %#v, error = %v", suppressed, err)
	}
	restored, err := commands.Restore(ctx, relations.ChangeState{
		RelationID: created.ID, ExpectedRevisionNo: 2, Principal: reviewer,
		Reason: "The approved relation remains valid.", RequestID: "relation-restore",
	})
	if err != nil || restored.Status != relations.StatusApproved || !restored.Effective {
		t.Fatalf("Restore = %#v, error = %v", restored, err)
	}

	pendingTombstone, err := commands.ProposeTombstone(ctx, relations.ProposeTombstone{
		RelationID: created.ID, ExpectedRevisionNo: 2, Principal: editor,
		Reason: "The assignment was removed.", RequestID: "relation-tombstone",
	})
	if err != nil || pendingTombstone.Proposed == nil ||
		pendingTombstone.Proposed.Kind != relations.ProposalTombstone || !pendingTombstone.Effective {
		t.Fatalf("ProposeTombstone = %#v, error = %v", pendingTombstone, err)
	}
	tombstoned, err := commands.Review(ctx, relations.Review{
		RelationID: created.ID, ExpectedRevisionNo: 3, Decision: relations.DecisionApprove,
		Principal: reviewer, Reason: "Removal verified.", RequestID: "relation-tombstone-approve",
	})
	if err != nil || tombstoned.Status != relations.StatusTombstoned || tombstoned.Effective {
		t.Fatalf("approve tombstone = %#v, error = %v", tombstoned, err)
	}

	events, err := dbsqlite.NewAuditRepository(store).ListAuditEvents(ctx, 20)
	if err != nil || len(events) != 8 {
		t.Fatalf("audit events = %#v, error = %v", events, err)
	}
}

func TestSQLiteReconcileRepositoryMakesCompletionCandidatesReviewable(t *testing.T) {
	t.Parallel()

	ctx, store, ids, codeRepository, source, target := newSQLiteLifecycleFixture(t, 72)
	commands := relations.NewCommands(
		dbsqlite.NewRelationRepository(store), ids, func() time.Time { return sqliteLifecycleTime },
	)
	service := reconcile.NewService(
		dbsqlite.NewReconcileRepository(store), commands, ids, func() time.Time { return sqliteLifecycleTime },
	)
	agent := relations.Principal{Actor: "agent@example.test", Role: relations.RoleAgent, Origin: audit.OriginAgent}
	reviewer := relations.Principal{Actor: "reviewer@example.test", Role: relations.RoleReviewer, Origin: audit.OriginWeb}

	begin := reconcile.Begin{
		RepositoryID: codeRepository.ID, Mode: reconcile.ModeFull, SourceCommit: "abc1234",
		Scope: json.RawMessage(`{"module":"orders"}`), Principal: agent, RequestID: "init-first",
	}
	firstSession, err := service.Begin(ctx, begin)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	retriedSession, err := service.Begin(ctx, begin)
	if err != nil || retriedSession.ID != firstSession.ID {
		t.Fatalf("idempotent Begin = %#v, error = %v", retriedSession, err)
	}
	conflictingBegin := begin
	conflictingBegin.SourceCommit = "different"
	if _, err := service.Begin(ctx, conflictingBegin); !errors.Is(err, reconcile.ErrIdempotencyConflict) {
		t.Fatalf("conflicting Begin error = %v", err)
	}

	batch := reconcile.SubmitBatch{
		SessionID: firstSession.ID, BatchNo: 1, IdempotencyKey: "batch-first",
		Principal: agent, RequestID: "batch-first",
		Proposals: []reconcile.Proposal{{
			Type: relations.TypeConditionalValueCopy, SourceNodeID: source.ID, TargetNodeID: target.ID,
			Transform: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: source.ID}, Confidence: 0.91,
			Evidence: []relations.EvidenceInput{{
				Kind: relations.EvidenceCode, Repository: codeRepository.Name, Commit: "abc1234",
				File: "src/Orders.java", Symbol: "Orders.copy", StartLine: 20, EndLine: 22,
			}},
			Reason: "Agent observed a direct assignment.",
		}},
		Unresolved: []reconcile.UnresolvedInput{{
			Type: "DYNAMIC_SQL", Summary: "Runtime target could not be resolved.",
			Evidence: json.RawMessage(`{"file":"src/DynamicMapper.xml"}`),
		}},
	}
	firstBatch, err := service.SubmitBatch(ctx, batch)
	if err != nil {
		t.Fatalf("SubmitBatch: %v", err)
	}
	if len(firstBatch.Items) != 1 || firstBatch.Items[0].Status != reconcile.ItemCreated ||
		len(firstBatch.UnresolvedIDs) != 1 {
		t.Fatalf("first batch = %#v", firstBatch)
	}
	retriedBatch, err := service.SubmitBatch(ctx, batch)
	if err != nil || retriedBatch.BatchID != firstBatch.BatchID ||
		retriedBatch.Items[0].RelationID != firstBatch.Items[0].RelationID {
		t.Fatalf("idempotent SubmitBatch = %#v, error = %v", retriedBatch, err)
	}
	conflictingBatch := batch
	conflictingBatch.Unresolved = []reconcile.UnresolvedInput{{
		Type: "DYNAMIC_SQL", Summary: "A different payload.", Evidence: json.RawMessage(`{}`),
	}}
	if _, err := service.SubmitBatch(ctx, conflictingBatch); !errors.Is(err, reconcile.ErrIdempotencyConflict) {
		t.Fatalf("conflicting SubmitBatch error = %v", err)
	}

	findings, err := service.ListUnresolved(ctx, 10)
	if err != nil || len(findings) != 1 || findings[0].ID != firstBatch.UnresolvedIDs[0] {
		t.Fatalf("ListUnresolved = %#v, error = %v", findings, err)
	}
	if _, err := service.Complete(ctx, reconcile.Complete{
		SessionID: firstSession.ID, ExpectedBatchCount: 2, Principal: agent,
		Reason: "Claiming an incomplete run.", RequestID: "complete-incomplete",
	}); !errors.Is(err, reconcile.ErrIncompleteBatches) {
		t.Fatalf("incomplete Complete error = %v", err)
	}
	firstCompletion, err := service.Complete(ctx, reconcile.Complete{
		SessionID: firstSession.ID, ExpectedBatchCount: 1, Principal: agent,
		Reason: "The first analysis completed.", RequestID: "complete-first",
	})
	if err != nil || firstCompletion.Session.Status != reconcile.StatusCompleted ||
		len(firstCompletion.CandidateRelationIDs) != 0 {
		t.Fatalf("first Complete = %#v, error = %v", firstCompletion, err)
	}

	relationID := firstBatch.Items[0].RelationID
	approved, err := commands.Review(ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 1, Decision: relations.DecisionApprove,
		Principal: reviewer, Reason: "Initial evidence verified.", RequestID: "approve-initial",
	})
	if err != nil || !approved.Effective {
		t.Fatalf("approve initialized relation = %#v, error = %v", approved, err)
	}

	secondSession, err := service.Begin(ctx, reconcile.Begin{
		RepositoryID: codeRepository.ID, Mode: reconcile.ModeFull, SourceCommit: "def5678",
		Scope: json.RawMessage(`{"module":"orders"}`), Principal: agent, RequestID: "init-second",
	})
	if err != nil {
		t.Fatalf("begin second session: %v", err)
	}
	if _, err := service.SubmitBatch(ctx, reconcile.SubmitBatch{
		SessionID: secondSession.ID, BatchNo: 1, IdempotencyKey: "batch-second",
		Principal: agent, RequestID: "batch-second",
		Unresolved: []reconcile.UnresolvedInput{{
			Type: "NO_RELATION", Summary: "The relation was absent from the complete scope.",
			Evidence: json.RawMessage(`{}`),
		}},
	}); err != nil {
		t.Fatalf("submit second batch: %v", err)
	}
	secondCompletion, err := service.Complete(ctx, reconcile.Complete{
		SessionID: secondSession.ID, ExpectedBatchCount: 1, Principal: agent,
		Reason: "Complete the second analysis.", RequestID: "complete-second",
	})
	if err != nil {
		t.Fatalf("complete second session: %v", err)
	}
	if len(secondCompletion.CandidateRelationIDs) != 1 || secondCompletion.CandidateRelationIDs[0] != relationID {
		t.Fatalf("completion candidates = %#v", secondCompletion.CandidateRelationIDs)
	}
	pendingStale, err := commands.Get(ctx, relationID)
	if err != nil || pendingStale.Proposed == nil ||
		pendingStale.Proposed.Kind != relations.ProposalStale || !pendingStale.Effective {
		t.Fatalf("pending stale relation = %#v, error = %v", pendingStale, err)
	}
	if _, err := service.Complete(ctx, reconcile.Complete{
		SessionID: secondSession.ID, ExpectedBatchCount: 1, Principal: agent,
		Reason: "Retry a completed session.", RequestID: "complete-second-again",
	}); !errors.Is(err, reconcile.ErrInitNotOpen) {
		t.Fatalf("repeat Complete error = %v", err)
	}
	stale, err := commands.Review(ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 2, Decision: relations.DecisionApprove,
		Principal: reviewer, Reason: "The omission was verified.", RequestID: "approve-stale",
	})
	if err != nil || stale.Status != relations.StatusStale || stale.Effective {
		t.Fatalf("approve stale candidate = %#v, error = %v", stale, err)
	}

	thirdSession, err := service.Begin(ctx, reconcile.Begin{
		RepositoryID: codeRepository.ID, Mode: reconcile.ModeFull, SourceCommit: "ghi9012",
		Scope: json.RawMessage(`{"module":"orders"}`), Principal: agent, RequestID: "init-third",
	})
	if err != nil {
		t.Fatalf("begin third session: %v", err)
	}
	reappeared, err := service.SubmitBatch(ctx, reconcile.SubmitBatch{
		SessionID: thirdSession.ID, BatchNo: 1, IdempotencyKey: "batch-third",
		Principal: agent, RequestID: "batch-third", Proposals: batch.Proposals,
	})
	if err != nil || len(reappeared.Items) != 1 || reappeared.Items[0].Status != reconcile.ItemReproposed {
		t.Fatalf("reappeared batch = %#v, error = %v", reappeared, err)
	}
	stillStale, err := commands.Get(ctx, relationID)
	if err != nil || stillStale.Status != relations.StatusStale || stillStale.Proposed != nil || stillStale.Effective {
		t.Fatalf("incomplete reappearance = %#v, error = %v", stillStale, err)
	}
	reappearance, err := service.Complete(ctx, reconcile.Complete{
		SessionID: thirdSession.ID, ExpectedBatchCount: 1, Principal: agent,
		Reason: "Complete the reappearance analysis.", RequestID: "complete-third",
	})
	if err != nil || len(reappearance.CandidateRelationIDs) != 1 || reappearance.CandidateRelationIDs[0] != relationID {
		t.Fatalf("reappearance completion = %#v, error = %v", reappearance, err)
	}
	reproposed, err := commands.Get(ctx, relationID)
	if err != nil || reproposed.Status != relations.StatusStale || reproposed.Proposed == nil ||
		reproposed.Proposed.Kind != relations.ProposalContent || reproposed.Proposed.RevisionNo != 3 || reproposed.Effective {
		t.Fatalf("completed reappearance = %#v, error = %v", reproposed, err)
	}
}

func TestSQLiteAdminWritesAreAuditedAndServiceWide(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ids, err := id.NewGenerator(73, func() time.Time { return sqliteLifecycleTime })
	if err != nil {
		t.Fatalf("create IDs: %v", err)
	}
	admin := relations.Principal{Actor: "admin@example.test", Role: relations.RoleAdmin, Origin: audit.OriginWeb}
	catalogRepository := dbsqlite.NewCatalogRepository(store, ids)
	catalogService := catalog.NewService(catalogRepository, ids, func() time.Time { return sqliteLifecycleTime })

	source, err := catalogService.CreateDataSourceAsAdmin(ctx, catalog.AdminCreateDataSource{
		Name: "orders", Kind: catalog.DataSourceMySQL, DSNEnvironment: "ORDERS_DSN",
		Principal: admin, Reason: "Register the production catalog.", RequestID: "admin-source-create",
	})
	if err != nil {
		t.Fatalf("CreateDataSourceAsAdmin: %v", err)
	}
	updated, err := catalogService.UpdateDataSourceAsAdmin(ctx, catalog.AdminUpdateDataSource{
		DataSourceID: source.ID, Name: "orders-primary", DSNEnvironment: "ORDERS_PRIMARY_DSN",
		Principal: admin, Reason: "Use the canonical source name.", RequestID: "admin-source-update",
	})
	if err != nil || updated.Name != "orders-primary" || updated.DSNEnvironment != "ORDERS_PRIMARY_DSN" {
		t.Fatalf("UpdateDataSourceAsAdmin = %#v, error = %v", updated, err)
	}
	if _, err := catalogService.CreateDataSourceAsAdmin(ctx, catalog.AdminCreateDataSource{
		Name: "orders-primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "DUPLICATE_DSN",
		Principal: admin, Reason: "Duplicate name proof.", RequestID: "admin-source-duplicate",
	}); !errors.Is(err, catalog.ErrDataSourceNameTaken) {
		t.Fatalf("duplicate service-wide source error = %v", err)
	}
	missing := updated
	missing.ID++
	if err := catalogRepository.UpdateDataSourceWithAudit(ctx, missing, false, audit.Event{
		ID: 99, Actor: admin.Actor, Origin: admin.Origin, Action: "DATA_SOURCE_UPDATED",
		SubjectType: "DATA_SOURCE", SubjectID: missing.ID, Reason: "Missing source.",
		RequestID: "admin-source-missing", Details: json.RawMessage(`{}`), OccurredAt: sqliteLifecycleTime,
	}); !errors.Is(err, catalog.ErrDataSourceNotFound) {
		t.Fatalf("update missing source error = %v", err)
	}

	repositoryService := catalog.NewCodeRepositoryService(
		dbsqlite.NewCodeRepository(store), ids, func() time.Time { return sqliteLifecycleTime },
	)
	codeRepository, err := repositoryService.CreateAsAdmin(ctx, catalog.AdminCreateCodeRepository{
		Name: "orders-service", RemoteURL: "https://example.test/orders.git", DefaultBranch: "main",
		Principal: admin, Reason: "Register evidence metadata.", RequestID: "admin-repository-create",
	})
	if err != nil {
		t.Fatalf("CreateAsAdmin code repository: %v", err)
	}
	if loaded, err := repositoryService.Get(ctx, codeRepository.ID); err != nil || loaded.Name != codeRepository.Name {
		t.Fatalf("Get code repository = %#v, error = %v", loaded, err)
	}

	auditService := audit.NewService(
		dbsqlite.NewAuditRepository(store), ids, func() time.Time { return sqliteLifecycleTime.Add(time.Minute) },
	)
	if _, err := auditService.Record(ctx, audit.RecordEvent{
		Actor: admin.Actor, Origin: admin.Origin, Action: "ADMIN_TEST_RECORDED",
		SubjectType: "DATA_SOURCE", SubjectID: source.ID, Reason: "Exercise the append-only audit adapter.",
		RequestID: "admin-audit-record", Details: json.RawMessage(`{"result":"ok"}`),
	}); err != nil {
		t.Fatalf("Record audit event: %v", err)
	}
	events, err := dbsqlite.NewAuditRepository(store).ListAuditEvents(ctx, 20)
	if err != nil || len(events) != 4 || events[0].Action != "ADMIN_TEST_RECORDED" {
		t.Fatalf("audit events = %#v, error = %v", events, err)
	}

	digestA := sha256.Sum256([]byte("admin-token"))
	digestB := sha256.Sum256([]byte("viewer-token"))
	credentials := dbsqlite.NewCredentialRepository(store, func() time.Time { return sqliteLifecycleTime })
	if err := credentials.SyncCredentials(ctx, []appauth.StoredCredential{
		{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb, Digest: digestA[:]},
		{Actor: "viewer", Role: relations.RoleViewer, Origin: audit.OriginWeb, Digest: digestB[:]},
	}); err != nil {
		t.Fatalf("SyncCredentials: %v", err)
	}
	removed, err := credentials.PruneUnknownActors(ctx, []string{"admin"})
	if err != nil || removed != 1 {
		t.Fatalf("PruneUnknownActors removed=%d error=%v", removed, err)
	}
	remaining, err := credentials.ListCredentials(ctx)
	if err != nil || len(remaining) != 1 || remaining[0].Actor != "admin" {
		t.Fatalf("remaining credentials = %#v, error = %v", remaining, err)
	}
}

func TestSQLiteSchemaScanJobTransitionsUseOptimisticRevisions(t *testing.T) {
	t.Parallel()

	ctx, store, ids, _, _, _ := newSQLiteLifecycleFixture(t, 74)
	catalogRepository := dbsqlite.NewCatalogRepository(store, ids)
	sources, err := catalogRepository.ListAllDataSources(ctx, 10)
	if err != nil || len(sources) != 1 {
		t.Fatalf("ListAllDataSources = %#v, error = %v", sources, err)
	}
	jobRepository := dbsqlite.NewJobRepository(store)
	coordinator := jobs.NewSchemaScanCoordinator(
		jobRepository, catalogRepository, nil, ids, func() time.Time { return sqliteLifecycleTime },
	)
	admin := relations.Principal{Actor: "admin@example.test", Role: relations.RoleAdmin, Origin: audit.OriginWeb}
	queued, err := coordinator.Start(ctx, jobs.StartSchemaScan{
		DataSourceID: sources[0].ID, Mode: jobs.SchemaScanFull, Principal: admin,
		Reason: "Refresh the schema.", RequestID: "scan-start",
	})
	if err != nil || queued.Status != jobs.StatusPending || queued.RevisionNo != 1 {
		t.Fatalf("Start schema scan = %#v, error = %v", queued, err)
	}
	startedAt := sqliteLifecycleTime.Add(time.Minute)
	claimed, err := jobRepository.ClaimNextSchemaScan(ctx, startedAt)
	if err != nil || claimed.ID != queued.ID || claimed.Status != jobs.StatusRunning || claimed.RevisionNo != 2 {
		t.Fatalf("ClaimNextSchemaScan = %#v, error = %v", claimed, err)
	}
	completedAt := startedAt.Add(time.Minute)
	finished, err := jobRepository.FinishSchemaScan(ctx, jobs.SchemaScanCompletion{
		JobID: queued.ID, ExpectedRevisionNo: 2, Status: jobs.StatusSucceeded,
		Result: json.RawMessage(`{"nodeCount":6}`), CompletedAt: completedAt,
	})
	if err != nil || finished.Status != jobs.StatusSucceeded || finished.RevisionNo != 3 ||
		string(finished.Result) != `{"nodeCount":6}` {
		t.Fatalf("FinishSchemaScan = %#v, error = %v", finished, err)
	}
	if _, err := jobRepository.FinishSchemaScan(ctx, jobs.SchemaScanCompletion{
		JobID: queued.ID, ExpectedRevisionNo: 2, Status: jobs.StatusFailed,
		ErrorCode: "STALE", ErrorMessage: "stale completion", CompletedAt: completedAt,
	}); !errors.Is(err, jobs.ErrJobConflict) {
		t.Fatalf("stale FinishSchemaScan error = %v", err)
	}
	if _, err := jobRepository.ClaimNextSchemaScan(ctx, startedAt); !errors.Is(err, jobs.ErrNoPendingJob) {
		t.Fatalf("empty ClaimNextSchemaScan error = %v", err)
	}
	if err := jobRepository.CreateSchemaScanJob(ctx, jobs.Job{
		ID: queued.ID + 1, Type: jobs.TypeSchemaScan, Status: jobs.StatusPending,
		Payload: json.RawMessage(`{"dataSourceId":"1"}`), CreatedAt: sqliteLifecycleTime, RevisionNo: 1,
	}, audit.Event{
		ID: queued.ID + 2, Actor: admin.Actor, Origin: admin.Origin, Action: "SCHEMA_SCAN_QUEUED",
		SubjectType: "SCHEMA_SCAN_JOB", SubjectID: queued.ID + 1, Reason: "Queue limit proof.",
		RequestID: "scan-queue-full", Details: json.RawMessage(`{}`), OccurredAt: sqliteLifecycleTime,
	}, 0); !errors.Is(err, jobs.ErrQueueFull) {
		t.Fatalf("queue-full CreateSchemaScanJob error = %v", err)
	}
}

func TestSQLiteCatalogScanLifecyclePublishesAndStalesOnlyItsScope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ids, err := id.NewGenerator(75, func() time.Time { return sqliteLifecycleTime })
	if err != nil {
		t.Fatalf("create IDs: %v", err)
	}
	service := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return sqliteLifecycleTime },
	)
	source, err := service.CreateDataSource(ctx, catalog.CreateDataSource{
		Name: "inventory", Kind: catalog.DataSourceMySQL, DSNEnvironment: "INVENTORY_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}
	ancestors := []catalog.NodeInput{
		{StableKey: "database:inventory", Kind: catalog.NodeDatabase, Name: "inventory", QualifiedName: "mysql://inventory"},
		{StableKey: "schema:inventory", ParentStableKey: "database:inventory", Kind: catalog.NodeSchema, Name: "inventory", QualifiedName: "inventory"},
	}
	fullNodes := append(append([]catalog.NodeInput(nil), ancestors...),
		catalog.NodeInput{StableKey: "table:inventory.a", ParentStableKey: "schema:inventory", Kind: catalog.NodeTable, Name: "a", QualifiedName: "inventory.a"},
		catalog.NodeInput{StableKey: "column:inventory.a.id", ParentStableKey: "table:inventory.a", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "inventory.a.id", DataType: "bigint", Ordinal: 1},
		catalog.NodeInput{StableKey: "table:inventory.b", ParentStableKey: "schema:inventory", Kind: catalog.NodeTable, Name: "b", QualifiedName: "inventory.b"},
		catalog.NodeInput{StableKey: "column:inventory.b.id", ParentStableKey: "table:inventory.b", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "inventory.b.id", DataType: "bigint", Ordinal: 1},
	)
	run, err := service.BeginSchemaScan(ctx, source.ID)
	if err != nil {
		t.Fatalf("BeginSchemaScan: %v", err)
	}
	first, err := service.PublishStartedSnapshot(ctx, run, catalog.PublishSnapshot{
		DataSourceID: source.ID, Nodes: fullNodes,
	})
	if err != nil || first.ScanRunID != run.ID || first.NodeCount != len(fullNodes) {
		t.Fatalf("PublishStartedSnapshot = %#v, error = %v", first, err)
	}
	aBefore, err := service.FindCurrentNode(ctx, source.ID, "inventory.a.id")
	if err != nil {
		t.Fatalf("find table a column: %v", err)
	}
	bBefore, err := service.FindCurrentNode(ctx, source.ID, "inventory.b.id")
	if err != nil {
		t.Fatalf("find table b column: %v", err)
	}
	byID, err := service.GetCurrentNode(ctx, aBefore.ID)
	if err != nil || byID.VersionID != aBefore.VersionID || byID.DataSourceID != source.ID {
		t.Fatalf("GetCurrentNode = %#v, error = %v", byID, err)
	}
	if _, err := service.PublishStartedSnapshot(ctx, run, catalog.PublishSnapshot{
		DataSourceID: source.ID, Nodes: fullNodes,
	}); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("reusing completed scan run error = %v", err)
	}

	incrementalNodes := append(append([]catalog.NodeInput(nil), ancestors...),
		catalog.NodeInput{StableKey: "table:inventory.a", ParentStableKey: "schema:inventory", Kind: catalog.NodeTable, Name: "a", QualifiedName: "inventory.a"},
	)
	second, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		DataSourceID: source.ID, Nodes: incrementalNodes, ScopeTables: []string{"inventory.a"},
	})
	if err != nil || second.StaleCount != 1 {
		t.Fatalf("incremental PublishSnapshot = %#v, error = %v", second, err)
	}
	aAfter, err := service.FindCurrentNode(ctx, source.ID, "inventory.a.id")
	if err != nil || aAfter.Status != catalog.NodeStale || aAfter.VersionID == aBefore.VersionID {
		t.Fatalf("scoped stale node = %#v, error = %v", aAfter, err)
	}
	bAfter, err := service.FindCurrentNode(ctx, source.ID, "inventory.b.id")
	if err != nil || bAfter.Status != catalog.NodeActive || bAfter.VersionID != bBefore.VersionID {
		t.Fatalf("out-of-scope node = %#v, error = %v", bAfter, err)
	}

	failingRun, err := service.BeginSchemaScan(ctx, source.ID)
	if err != nil {
		t.Fatalf("begin failing scan: %v", err)
	}
	if err := service.FailSchemaScan(ctx, failingRun, "CONNECTION_FAILED"); err != nil {
		t.Fatalf("FailSchemaScan: %v", err)
	}
}

func newSQLiteLifecycleFixture(
	t *testing.T,
	nodeID uint16,
) (context.Context, *dbsqlite.Store, *id.Generator, catalog.CodeRepository, catalog.Node, catalog.Node) {
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
	ids, err := id.NewGenerator(nodeID, func() time.Time { return sqliteLifecycleTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}

	repositoryService := catalog.NewCodeRepositoryService(
		dbsqlite.NewCodeRepository(store), ids, func() time.Time { return sqliteLifecycleTime },
	)
	codeRepository, err := repositoryService.Create(ctx, catalog.CreateCodeRepository{
		Name: "orders-service", RemoteURL: "https://example.test/orders.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create code repository: %v", err)
	}
	loadedRepository, err := repositoryService.Get(ctx, codeRepository.ID)
	if err != nil || loadedRepository.Name != codeRepository.Name {
		t.Fatalf("get code repository = %#v, error = %v", loadedRepository, err)
	}

	catalogService := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return sqliteLifecycleTime },
	)
	dataSource, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{
		Name: "orders", Kind: catalog.DataSourceMySQL, DSNEnvironment: "ORDERS_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}
	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		DataSourceID: dataSource.ID,
		Nodes: []catalog.NodeInput{
			{StableKey: "database:orders", Kind: catalog.NodeDatabase, Name: "orders", QualifiedName: "mysql://orders"},
			{StableKey: "schema:orders", ParentStableKey: "database:orders", Kind: catalog.NodeSchema, Name: "orders", QualifiedName: "orders"},
			{StableKey: "table:source", ParentStableKey: "schema:orders", Kind: catalog.NodeTable, Name: "source", QualifiedName: "orders.source"},
			{StableKey: "table:target", ParentStableKey: "schema:orders", Kind: catalog.NodeTable, Name: "target", QualifiedName: "orders.target"},
			{StableKey: "column:source.value", ParentStableKey: "table:source", Kind: catalog.NodeColumn, Name: "value", QualifiedName: "orders.source.value", DataType: "text", Ordinal: 1},
			{StableKey: "column:target.value", ParentStableKey: "table:target", Kind: catalog.NodeColumn, Name: "value", QualifiedName: "orders.target.value", DataType: "text", Ordinal: 1},
		},
	}); err != nil {
		t.Fatalf("publish catalog: %v", err)
	}
	source, err := catalogService.FindCurrentNode(ctx, dataSource.ID, "orders.source.value")
	if err != nil {
		t.Fatalf("find source node: %v", err)
	}
	target, err := catalogService.FindCurrentNode(ctx, dataSource.ID, "orders.target.value")
	if err != nil {
		t.Fatalf("find target node: %v", err)
	}
	return ctx, store, ids, codeRepository, source, target
}
