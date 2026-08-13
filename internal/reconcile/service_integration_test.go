package reconcile_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestRelationInitBatchIsIdempotentAndCompletionOnlyProposesOmissions(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "dbgraph.sqlite")
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{
		Path: databasePath,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	fixedTime := time.Date(2026, time.August, 11, 15, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(12, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	repository, sourceNode, targetNode := createInitFixture(t, ctx, store, ids, fixedTime)
	relationRepository := dbsqlite.NewRelationRepository(store)
	relationCommands := relations.NewCommands(relationRepository, ids, func() time.Time { return fixedTime })
	service := reconcile.NewService(
		dbsqlite.NewReconcileRepository(store),
		relationCommands,
		ids,
		func() time.Time { return fixedTime },
	)
	agent := relations.Principal{Actor: "agent@example.test", Role: relations.RoleAgent, Origin: audit.OriginAgent}
	reviewer := relations.Principal{Actor: "reviewer@example.test", Role: relations.RoleReviewer, Origin: audit.OriginWeb}

	firstSession, err := service.Begin(ctx, reconcile.Begin{

		RepositoryID: repository.ID,
		Mode:         reconcile.ModeFull,
		SourceCommit: "abc1234",
		Scope:        json.RawMessage(`{"module":"service"}`),
		Principal:    agent,
		RequestID:    "init-begin-001",
	})
	if err != nil {
		t.Fatalf("begin first relation init: %v", err)
	}
	batchCommand := reconcile.SubmitBatch{
		SessionID:      firstSession.ID,
		BatchNo:        1,
		IdempotencyKey: "batch-key-001",
		Principal:      agent,
		RequestID:      "init-batch-001",
		Proposals: []reconcile.Proposal{{
			Type:         relations.TypeConditionalValueCopy,
			SourceNodeID: sourceNode.ID,
			TargetNodeID: targetNode.ID,
			Transform:    conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: sourceNode.ID},
			Confidence:   0.91,
			Evidence: []relations.EvidenceInput{{
				Kind:       relations.EvidenceCode,
				Repository: repository.Name,
				Commit:     "abc1234",
				File:       "src/Service.java",
				Symbol:     "Service.copy",
				StartLine:  10,
				EndLine:    12,
			}},
			Reason: "Agent observed a direct assignment.",
		}},
		Unresolved: []reconcile.UnresolvedInput{{
			Type:     "DYNAMIC_SQL",
			Summary:  "Runtime SQL target could not be resolved.",
			Evidence: json.RawMessage(`{"file":"src/DynamicMapper.java"}`),
		}},
	}
	firstBatch, err := service.SubmitBatch(ctx, batchCommand)
	if err != nil {
		t.Fatalf("submit first relation init batch: %v", err)
	}
	if len(firstBatch.Items) != 1 || firstBatch.Items[0].Status != reconcile.ItemCreated || len(firstBatch.UnresolvedIDs) != 1 {
		t.Fatalf("first batch result = %#v", firstBatch)
	}
	auditEvents, err := dbsqlite.NewAuditRepository(store).ListAuditEvents(ctx, 20)
	if err != nil {
		t.Fatalf("list batch audit events: %v", err)
	}
	batchAuditFound := false
	for _, event := range auditEvents {
		if event.Action != "RELATION_INIT_BATCH_ACCEPTED" {
			continue
		}
		batchAuditFound = true
		if event.RequestID != batchCommand.RequestID {
			t.Fatalf("batch audit request ID = %q, want %q", event.RequestID, batchCommand.RequestID)
		}
	}
	if !batchAuditFound {
		t.Fatal("relation init batch audit event was not recorded")
	}
	retriedBatch, err := service.SubmitBatch(ctx, batchCommand)
	if err != nil {
		t.Fatalf("retry identical relation init batch: %v", err)
	}
	if retriedBatch.BatchID != firstBatch.BatchID || retriedBatch.Items[0].RelationID != firstBatch.Items[0].RelationID ||
		retriedBatch.UnresolvedIDs[0] != firstBatch.UnresolvedIDs[0] {
		t.Fatalf("idempotent batch changed result: first=%#v retry=%#v", firstBatch, retriedBatch)
	}
	conflictingBatch := batchCommand
	conflictingBatch.Unresolved = []reconcile.UnresolvedInput{{
		Type: "DYNAMIC_SQL", Summary: "Different payload under the same key.", Evidence: json.RawMessage(`{}`),
	}}
	if _, err := service.SubmitBatch(ctx, conflictingBatch); !errors.Is(err, reconcile.ErrIdempotencyConflict) {
		t.Fatalf("conflicting idempotency key error = %v", err)
	}

	if _, err := service.Complete(ctx, reconcile.Complete{
		SessionID: firstSession.ID, ExpectedBatchCount: 2, Principal: agent,
		Reason: "The full analysis finished.", RequestID: "init-complete-missing",
	}); !errors.Is(err, reconcile.ErrIncompleteBatches) {
		t.Fatalf("incomplete relation init completion error = %v", err)
	}
	stillOpen, err := service.Get(ctx, firstSession.ID)
	if err != nil || stillOpen.Status != reconcile.StatusOpen {
		t.Fatalf("incomplete session state = %#v, error = %v", stillOpen, err)
	}
	firstCompletion, err := service.Complete(ctx, reconcile.Complete{
		SessionID: firstSession.ID, ExpectedBatchCount: 1, Principal: agent,
		Reason: "The full analysis finished.", RequestID: "init-complete-001",
	})
	if err != nil {
		t.Fatalf("complete first relation init: %v", err)
	}
	if firstCompletion.Session.Status != reconcile.StatusCompleted || len(firstCompletion.CandidateRelationIDs) != 0 {
		t.Fatalf("first completion = %#v", firstCompletion)
	}

	relationID := firstBatch.Items[0].RelationID
	approved, err := relationCommands.Review(ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 1, Decision: relations.DecisionApprove,
		Principal: reviewer, Reason: "Initial evidence reviewed.", RequestID: "review-init-001",
	})
	if err != nil || !approved.Effective {
		t.Fatalf("approve initialized relation = %#v, error = %v", approved, err)
	}

	secondSession, err := service.Begin(ctx, reconcile.Begin{
		RepositoryID: repository.ID, Mode: reconcile.ModeFull,
		SourceCommit: "def5678", Scope: json.RawMessage(`{"module":"service"}`),
		Principal: agent, RequestID: "init-begin-002",
	})
	if err != nil {
		t.Fatalf("begin second relation init: %v", err)
	}
	if _, err := service.SubmitBatch(ctx, reconcile.SubmitBatch{
		SessionID: secondSession.ID, BatchNo: 1, IdempotencyKey: "batch-key-002",
		Principal: agent, RequestID: "init-batch-002",
		Unresolved: []reconcile.UnresolvedInput{{
			Type: "REFLECTION", Summary: "Reflection prevented a complete mapping.", Evidence: json.RawMessage(`{}`),
		}},
	}); err != nil {
		t.Fatalf("submit second relation init batch: %v", err)
	}
	secondCompletion, err := service.Complete(ctx, reconcile.Complete{
		SessionID: secondSession.ID, ExpectedBatchCount: 1, Principal: agent,
		Reason: "Relation was omitted from the complete scope.", RequestID: "init-complete-002",
	})
	if err != nil {
		t.Fatalf("complete second relation init: %v", err)
	}
	if len(secondCompletion.CandidateRelationIDs) != 1 || secondCompletion.CandidateRelationIDs[0] != relationID {
		t.Fatalf("omission candidates = %#v", secondCompletion.CandidateRelationIDs)
	}
	pendingStale, err := relationCommands.Get(ctx, relationID)
	if err != nil {
		t.Fatalf("get omission candidate: %v", err)
	}
	if pendingStale.Proposed == nil || pendingStale.Proposed.Kind != relations.ProposalStale || !pendingStale.Effective {
		t.Fatalf("completion changed graph before review: %#v", pendingStale)
	}
	stale, err := relationCommands.Review(ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 2, Decision: relations.DecisionApprove,
		Principal: reviewer, Reason: "Omission confirmed as a removal.", RequestID: "review-init-002",
	})
	if err != nil || stale.Effective || stale.Status != relations.StatusStale {
		t.Fatalf("approve omission candidate = %#v, error = %v", stale, err)
	}
	readOnly, err := sql.Open("sqlite", "file:"+databasePath+"?mode=ro")
	if err != nil {
		t.Fatalf("open relation event verification: %v", err)
	}
	defer func() {
		if err := readOnly.Close(); err != nil {
			t.Errorf("close relation event verification: %v", err)
		}
	}()
	var staleEvents int
	if err := readOnly.QueryRowContext(ctx, `
SELECT COUNT(*) FROM relation_events WHERE relation_id = ? AND event_type = ?
`, relationID, relations.EventStale).Scan(&staleEvents); err != nil {
		t.Fatalf("query stale relation event: %v", err)
	}
	if staleEvents != 1 {
		t.Fatalf("stale relation event count = %d, want 1", staleEvents)
	}

	findings, err := service.ListUnresolved(ctx, 10)
	if err != nil {
		t.Fatalf("list unresolved findings: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("unresolved finding count = %d, want 2", len(findings))
	}
}

func TestIncrementalRelationInitOnlyProposesOmissionsInsideExplicitRelationScope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fixedTime := time.Date(2026, time.August, 11, 20, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(20, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	repository, sourceNode, targetNode := createInitFixture(t, ctx, store, ids, fixedTime)
	commands := relations.NewCommands(dbsqlite.NewRelationRepository(store), ids, func() time.Time { return fixedTime })
	service := reconcile.NewService(
		dbsqlite.NewReconcileRepository(store), commands, ids, func() time.Time { return fixedTime },
	)
	agent := relations.Principal{Actor: "agent@example.test", Role: relations.RoleAgent, Origin: audit.OriginAgent}
	reviewer := relations.Principal{Actor: "reviewer@example.test", Role: relations.RoleReviewer, Origin: audit.OriginWeb}

	initial, err := service.Begin(ctx, reconcile.Begin{
		RepositoryID: repository.ID, Mode: reconcile.ModeFull,
		SourceCommit: "initial", Scope: json.RawMessage(`{}`), Principal: agent, RequestID: "scope-initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := func(discriminator int64) reconcile.Proposal {
		return reconcile.Proposal{
			Type: relations.TypeConditionalValueCopy, SourceNodeID: sourceNode.ID, TargetNodeID: targetNode.ID,
			Guard: &conditions.Boolean{
				Kind: conditions.BooleanCompare, Operator: conditions.CompareEqual,
				Left: &conditions.Value{Kind: conditions.ValueColumn, NodeID: sourceNode.ID},
				Right: &conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
					Type: conditions.LiteralInteger, Value: json.RawMessage(strconv.FormatInt(discriminator, 10)),
				}},
			},
			Transform: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: sourceNode.ID}, Confidence: 0.9,
			Evidence: []relations.EvidenceInput{{
				Kind: relations.EvidenceCode, Repository: repository.Name, Commit: "initial",
				File: "src/Service.java", Symbol: "Service.copy", StartLine: int(discriminator), EndLine: int(discriminator),
			}},
			Reason: "Create a relation in the initial repository analysis.",
		}
	}
	initialBatch, err := service.SubmitBatch(ctx, reconcile.SubmitBatch{
		SessionID: initial.ID, BatchNo: 1, IdempotencyKey: "scope-initial-batch",
		Proposals: []reconcile.Proposal{proposal(1), proposal(2), proposal(3)}, Principal: agent, RequestID: "scope-initial-batch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(ctx, reconcile.Complete{
		SessionID: initial.ID, ExpectedBatchCount: 1, Principal: agent,
		Reason: "Complete the initial repository analysis.", RequestID: "scope-initial-complete",
	}); err != nil {
		t.Fatal(err)
	}
	if len(initialBatch.Items) != 3 {
		t.Fatalf("initial items = %#v", initialBatch.Items)
	}
	for _, item := range initialBatch.Items {
		if _, err := commands.Review(ctx, relations.Review{
			RelationID: item.RelationID, ExpectedRevisionNo: 1, Decision: relations.DecisionApprove,
			Principal: reviewer, Reason: "Approve initial relation.",
			RequestID: "scope-approve-" + strconv.FormatInt(item.RelationID, 10),
		}); err != nil {
			t.Fatal(err)
		}
	}

	seenInScopeID := initialBatch.Items[0].RelationID
	omittedInScopeID := initialBatch.Items[1].RelationID
	outOfScopeID := initialBatch.Items[2].RelationID
	scope := json.RawMessage(`{"relationIds":["` + strconv.FormatInt(seenInScopeID, 10) + `","` +
		strconv.FormatInt(omittedInScopeID, 10) + `"]}`)
	incremental, err := service.Begin(ctx, reconcile.Begin{
		RepositoryID: repository.ID, Mode: reconcile.ModeIncremental,
		SourceCommit: "incremental", Scope: scope, Principal: agent, RequestID: "scope-incremental",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitBatch(ctx, reconcile.SubmitBatch{
		SessionID: incremental.ID, BatchNo: 1, IdempotencyKey: "scope-incremental-batch",
		Proposals: []reconcile.Proposal{proposal(1)},
		Principal: agent, RequestID: "scope-incremental-batch",
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := service.Complete(ctx, reconcile.Complete{
		SessionID: incremental.ID, ExpectedBatchCount: 1, Principal: agent,
		Reason: "Complete the scoped incremental analysis.", RequestID: "scope-incremental-complete",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(completed.CandidateRelationIDs) != 1 || completed.CandidateRelationIDs[0] != omittedInScopeID {
		t.Fatalf("incremental candidates = %#v, want only %d", completed.CandidateRelationIDs, omittedInScopeID)
	}
	omittedInScope, err := commands.Get(ctx, omittedInScopeID)
	if err != nil {
		t.Fatal(err)
	}
	if omittedInScope.Proposed == nil || omittedInScope.Proposed.Kind != relations.ProposalStale || !omittedInScope.Effective {
		t.Fatalf("omitted in-scope relation = %#v", omittedInScope)
	}
	seenInScope, err := commands.Get(ctx, seenInScopeID)
	if err != nil {
		t.Fatal(err)
	}
	if seenInScope.Proposed != nil || !seenInScope.Effective || seenInScope.LatestRevisionNo != 1 {
		t.Fatalf("seen in-scope relation changed = %#v", seenInScope)
	}
	outOfScope, err := commands.Get(ctx, outOfScopeID)
	if err != nil {
		t.Fatal(err)
	}
	if outOfScope.Proposed != nil || !outOfScope.Effective || outOfScope.LatestRevisionNo != 1 {
		t.Fatalf("out-of-scope relation changed = %#v", outOfScope)
	}
}

func createInitFixture(
	t *testing.T,
	ctx context.Context,
	store *dbsqlite.Store,
	ids *id.Generator,
	fixedTime time.Time,
) (catalog.CodeRepository, catalog.Node, catalog.Node) {
	t.Helper()
	repositoryService := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), ids, func() time.Time { return fixedTime })
	repository, err := repositoryService.Create(ctx, catalog.CreateCodeRepository{
		Name: "example-service", RemoteURL: "https://example.test/repository.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create code repository: %v", err)
	}
	catalogService := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })
	dataSource, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{

		Name: "primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "DBGRAPH_INIT_TEST_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}
	inputs := []catalog.NodeInput{
		{StableKey: "database:primary", Kind: catalog.NodeDatabase, Name: "primary", QualifiedName: "primary"},
		{StableKey: "schema:learn", ParentStableKey: "database:primary", Kind: catalog.NodeSchema, Name: "learn", QualifiedName: "learn"},
		{StableKey: "table:source", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "source", QualifiedName: "learn.source"},
		{StableKey: "table:target", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "target", QualifiedName: "learn.target"},
		{StableKey: "column:source.value", ParentStableKey: "table:source", Kind: catalog.NodeColumn, Name: "value", QualifiedName: "learn.source.value", DataType: "text", Ordinal: 1},
		{StableKey: "column:target.value", ParentStableKey: "table:target", Kind: catalog.NodeColumn, Name: "value", QualifiedName: "learn.target.value", DataType: "text", Ordinal: 1},
	}
	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		DataSourceID: dataSource.ID, Nodes: inputs,
	}); err != nil {
		t.Fatalf("publish catalog: %v", err)
	}
	source, err := catalogService.FindCurrentNode(ctx, dataSource.ID, "learn.source.value")
	if err != nil {
		t.Fatalf("find source node: %v", err)
	}
	target, err := catalogService.FindCurrentNode(ctx, dataSource.ID, "learn.target.value")
	if err != nil {
		t.Fatalf("find target node: %v", err)
	}
	return repository, source, target
}
