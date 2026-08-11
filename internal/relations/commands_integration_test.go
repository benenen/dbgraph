package relations_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestRelationCommandsPublishOnlyApprovedRevisions(t *testing.T) {
	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{
		Path: filepath.Join(t.TempDir(), "dbgraph.sqlite"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	fixedTime := time.Date(2026, time.August, 11, 14, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(11, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	project, dataSource, nodes := createRelationCatalogFixture(t, ctx, store, ids, fixedTime)
	repository := dbsqlite.NewRelationRepository(store)
	commands := relations.NewCommands(repository, ids, func() time.Time { return fixedTime })
	editor := relations.Principal{
		Actor:  "agent@example.test",
		Role:   relations.RoleEditor,
		Origin: audit.OriginAgent,
	}
	reviewer := relations.Principal{
		Actor:  "reviewer@example.test",
		Role:   relations.RoleReviewer,
		Origin: audit.OriginWeb,
	}

	created, err := commands.ProposeCreate(ctx, relations.ProposeCreate{
		ProjectID:    project.ID,
		Type:         relations.TypeConditionalValueCopy,
		SourceNodeID: nodes["source"].ID,
		TargetNodeID: nodes["target"].ID,
		Guard: &conditions.Boolean{
			Kind:     conditions.BooleanCompare,
			Operator: conditions.CompareEqual,
			Left:     &conditions.Value{Kind: conditions.ValueColumn, NodeID: nodes["guard"].ID},
			Right: &conditions.Value{
				Kind: conditions.ValueLiteral,
				Literal: &conditions.Literal{
					Type:  conditions.LiteralInteger,
					Value: json.RawMessage(`1`),
				},
			},
		},
		Selector: &conditions.Boolean{
			Kind:     conditions.BooleanCompare,
			Operator: conditions.CompareEqual,
			Left:     &conditions.Value{Kind: conditions.ValueColumn, NodeID: nodes["selector_left"].ID},
			Right:    &conditions.Value{Kind: conditions.ValueColumn, NodeID: nodes["selector_right"].ID},
		},
		Transform:  conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: nodes["source"].ID},
		Confidence: 0.96,
		Evidence: []relations.EvidenceInput{{
			Kind:       relations.EvidenceCode,
			Repository: "example-service",
			Commit:     "abc1234",
			File:       "src/main/java/ExampleService.java",
			Symbol:     "ExampleService.save",
			StartLine:  82,
			EndLine:    88,
		}},
		Principal: editor,
		Reason:    "Observed guarded assignment in service code.",
		RequestID: "request-create-001",
	})
	if err != nil {
		t.Fatalf("propose relation: %v", err)
	}
	if created.ProjectID != project.ID || created.LatestRevisionNo != 1 || created.Proposed == nil {
		t.Fatalf("created relation = %#v", created)
	}
	if created.Active != nil || created.Effective {
		t.Fatalf("unreviewed proposal changed effective graph: %#v", created)
	}

	approved, err := commands.Review(ctx, relations.Review{
		RelationID:         created.ID,
		ExpectedRevisionNo: 1,
		Decision:           relations.DecisionApprove,
		Principal:          reviewer,
		Reason:             "Evidence and condition match the implementation.",
		RequestID:          "request-review-001",
	})
	if err != nil {
		t.Fatalf("approve relation: %v", err)
	}
	if approved.Active == nil || approved.Active.RevisionNo != 1 || approved.Proposed != nil || !approved.Effective {
		t.Fatalf("approved relation = %#v", approved)
	}
	symbolMatches, err := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime },
	).SearchCurrentNodes(ctx, project.ID, "ExampleService.save", 10)
	if err != nil {
		t.Fatalf("search catalog by evidence symbol: %v", err)
	}
	matchedIDs := make(map[int64]struct{}, len(symbolMatches))
	for _, node := range symbolMatches {
		matchedIDs[node.ID] = struct{}{}
	}
	for _, key := range []string{"source", "target"} {
		if _, found := matchedIDs[nodes[key].ID]; !found {
			t.Fatalf("symbol search did not return %s node %d: %#v", key, nodes[key].ID, symbolMatches)
		}
	}
	graphService := graph.NewService(dbsqlite.NewGraphRepository(store))
	trace, err := graphService.Trace(ctx, graph.TraceRequest{
		ProjectID:   project.ID,
		StartNodeID: nodes["source"].ID,
		Direction:   graph.DirectionDownstream,
		Context: conditions.Context{Columns: map[int64]json.RawMessage{
			nodes["guard"].ID:          json.RawMessage(`1`),
			nodes["selector_left"].ID:  json.RawMessage(`99`),
			nodes["selector_right"].ID: json.RawMessage(`99`),
		}},
		Limits: graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace approved relation: %v", err)
	}
	if len(trace.Paths) != 1 || trace.Paths[0].Nodes[1] != nodes["target"].ID {
		t.Fatalf("approved relation trace = %#v", trace)
	}

	guardRevision := *created.Proposed.Guard
	guardRevision.Right = &conditions.Value{
		Kind: conditions.ValueLiteral,
		Literal: &conditions.Literal{
			Type:  conditions.LiteralInteger,
			Value: json.RawMessage(`2`),
		},
	}
	revised, err := commands.ProposeRevision(ctx, relations.ProposeRevision{
		RelationID:         created.ID,
		ExpectedRevisionNo: 1,
		SourceNodeID:       nodes["source"].ID,
		TargetNodeID:       nodes["target"].ID,
		Guard:              &guardRevision,
		Selector:           created.Proposed.Selector,
		Transform:          created.Proposed.Transform,
		Confidence:         0.94,
		Evidence:           created.Proposed.Evidence,
		Principal:          editor,
		Reason:             "New branch uses discriminator value 2.",
		RequestID:          "request-revision-002",
	})
	if err != nil {
		t.Fatalf("propose revision: %v", err)
	}
	if revised.Active == nil || revised.Active.RevisionNo != 1 || revised.Proposed == nil || revised.Proposed.RevisionNo != 2 || !revised.Effective {
		t.Fatalf("pending revision replaced active graph: %#v", revised)
	}
	pendingTrace, err := graphService.Trace(ctx, graph.TraceRequest{
		ProjectID: project.ID, StartNodeID: nodes["source"].ID,
		Direction: graph.DirectionDownstream,
		Limits:    graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace relation with pending revision: %v", err)
	}
	if len(pendingTrace.Paths) != 1 || pendingTrace.Paths[0].Steps[0].Edge.Status != relations.StatusApproved ||
		!pendingTrace.Paths[0].Steps[0].Edge.HasPendingProposal {
		t.Fatalf("pending relation trace status = %#v", pendingTrace.Paths)
	}

	_, err = commands.ProposeRevision(ctx, relations.ProposeRevision{
		RelationID:         created.ID,
		ExpectedRevisionNo: 1,
		SourceNodeID:       nodes["source"].ID,
		TargetNodeID:       nodes["target"].ID,
		Transform:          created.Proposed.Transform,
		Confidence:         0.9,
		Evidence:           created.Proposed.Evidence,
		Principal:          editor,
		Reason:             "Stale concurrent proposal.",
		RequestID:          "request-stale-003",
	})
	var conflict *relations.RevisionConflictError
	if !errors.As(err, &conflict) || conflict.CurrentRevisionNo != 2 {
		t.Fatalf("stale proposal error = %v, want current revision 2", err)
	}

	rejected, err := commands.Review(ctx, relations.Review{
		RelationID:         created.ID,
		ExpectedRevisionNo: 2,
		Decision:           relations.DecisionReject,
		Principal:          reviewer,
		Reason:             "The new branch is not deployed.",
		RequestID:          "request-review-002",
	})
	if err != nil {
		t.Fatalf("reject revision: %v", err)
	}
	if rejected.Active == nil || rejected.Active.RevisionNo != 1 || rejected.Proposed != nil || !rejected.Effective {
		t.Fatalf("rejected revision changed effective graph: %#v", rejected)
	}

	suppressed, err := commands.Suppress(ctx, relations.ChangeState{
		RelationID:         created.ID,
		ExpectedRevisionNo: 2,
		Principal:          reviewer,
		Reason:             "Temporarily hide this edge during incident review.",
		RequestID:          "request-suppress-001",
	})
	if err != nil {
		t.Fatalf("suppress relation: %v", err)
	}
	if suppressed.Status != relations.StatusSuppressed || suppressed.Effective || suppressed.Active == nil {
		t.Fatalf("suppressed relation = %#v", suppressed)
	}
	restored, err := commands.Restore(ctx, relations.ChangeState{
		RelationID:         created.ID,
		ExpectedRevisionNo: 2,
		Principal:          reviewer,
		Reason:             "Incident review confirmed the relation is valid.",
		RequestID:          "request-restore-001",
	})
	if err != nil {
		t.Fatalf("restore relation: %v", err)
	}
	if restored.Status != relations.StatusApproved || !restored.Effective || restored.Active == nil || restored.Active.RevisionNo != 1 {
		t.Fatalf("restored relation = %#v", restored)
	}

	tombstone, err := commands.ProposeTombstone(ctx, relations.ProposeTombstone{
		RelationID:         created.ID,
		ExpectedRevisionNo: 2,
		Principal:          editor,
		Reason:             "The assignment was removed from the source repository.",
		RequestID:          "request-tombstone-003",
	})
	if err != nil {
		t.Fatalf("propose tombstone: %v", err)
	}
	if tombstone.Proposed == nil || tombstone.Proposed.Kind != relations.ProposalTombstone || !tombstone.Effective {
		t.Fatalf("unreviewed tombstone changed effective graph: %#v", tombstone)
	}

	tombstoned, err := commands.Review(ctx, relations.Review{
		RelationID:         created.ID,
		ExpectedRevisionNo: 3,
		Decision:           relations.DecisionApprove,
		Principal:          reviewer,
		Reason:             "Removal confirmed in the reviewed commit.",
		RequestID:          "request-review-003",
	})
	if err != nil {
		t.Fatalf("approve tombstone: %v", err)
	}
	if tombstoned.Status != relations.StatusTombstoned || tombstoned.Effective {
		t.Fatalf("approved tombstone left effective graph active: %#v", tombstoned)
	}
	trace, err = graphService.Trace(ctx, graph.TraceRequest{
		ProjectID: project.ID, StartNodeID: nodes["source"].ID,
		Direction: graph.DirectionDownstream,
		Limits:    graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace tombstoned relation: %v", err)
	}
	if len(trace.Paths) != 0 {
		t.Fatalf("tombstoned relation remained traversable: %#v", trace.Paths)
	}

	auditEvents, err := dbsqlite.NewAuditRepository(store).ListAuditEvents(ctx, project.ID, 20)
	if err != nil {
		t.Fatalf("list relation audit events: %v", err)
	}
	if len(auditEvents) != 8 {
		t.Fatalf("relation audit event count = %d, want 8", len(auditEvents))
	}
	_ = dataSource
}

func createRelationCatalogFixture(
	t *testing.T,
	ctx context.Context,
	store *dbsqlite.Store,
	ids *id.Generator,
	fixedTime time.Time,
) (catalog.Project, catalog.DataSource, map[string]catalog.Node) {
	t.Helper()
	projectService := catalog.NewProjectService(
		dbsqlite.NewProjectRepository(store),
		ids,
		func() time.Time { return fixedTime },
	)
	project, err := projectService.Create(ctx, catalog.CreateProject{Name: "Relation Test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	catalogService := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, ids),
		ids,
		func() time.Time { return fixedTime },
	)
	dataSource, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{
		ProjectID:      project.ID,
		Name:           "primary",
		Kind:           catalog.DataSourceMySQL,
		DSNEnvironment: "DBGRAPH_RELATION_TEST_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}
	inputs := []catalog.NodeInput{
		{StableKey: "database:primary", Kind: catalog.NodeDatabase, Name: "primary", QualifiedName: "primary"},
		{StableKey: "schema:learn", ParentStableKey: "database:primary", Kind: catalog.NodeSchema, Name: "learn", QualifiedName: "learn"},
		{StableKey: "table:learn.a", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "a", QualifiedName: "learn.a"},
		{StableKey: "table:learn.b", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "b", QualifiedName: "learn.b"},
		{StableKey: "column:learn.a.x1", ParentStableKey: "table:learn.a", Kind: catalog.NodeColumn, Name: "x1", QualifiedName: "learn.a.x1", DataType: "int", Ordinal: 1},
		{StableKey: "column:learn.a.x2", ParentStableKey: "table:learn.a", Kind: catalog.NodeColumn, Name: "x2", QualifiedName: "learn.a.x2", DataType: "varchar(255)", Ordinal: 2},
		{StableKey: "column:learn.a.b_id", ParentStableKey: "table:learn.a", Kind: catalog.NodeColumn, Name: "b_id", QualifiedName: "learn.a.b_id", DataType: "bigint", Ordinal: 3},
		{StableKey: "column:learn.b.id", ParentStableKey: "table:learn.b", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "learn.b.id", DataType: "bigint", Ordinal: 1},
		{StableKey: "column:learn.b.x", ParentStableKey: "table:learn.b", Kind: catalog.NodeColumn, Name: "x", QualifiedName: "learn.b.x", DataType: "varchar(255)", Ordinal: 2},
	}
	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID:    project.ID,
		DataSourceID: dataSource.ID,
		Nodes:        inputs,
	}); err != nil {
		t.Fatalf("publish catalog fixture: %v", err)
	}
	nodes := map[string]catalog.Node{}
	for key, qualifiedName := range map[string]string{
		"source":         "learn.b.x",
		"target":         "learn.a.x2",
		"guard":          "learn.a.x1",
		"selector_left":  "learn.a.b_id",
		"selector_right": "learn.b.id",
	} {
		node, err := catalogService.FindCurrentNode(ctx, project.ID, dataSource.ID, qualifiedName)
		if err != nil {
			t.Fatalf("find fixture node %q: %v", qualifiedName, err)
		}
		nodes[key] = node
	}
	return project, dataSource, nodes
}
