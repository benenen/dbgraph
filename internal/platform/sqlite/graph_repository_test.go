package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
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

func TestGraphRepositoryLoadsRecursivePathsAcrossMultipleDepths(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "graph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixedTime := time.Date(2026, time.August, 11, 20, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(23, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	project, err := catalog.NewProjectService(
		dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime },
	).Create(ctx, catalog.CreateProject{Name: "Recursive graph"})
	if err != nil {
		t.Fatal(err)
	}
	catalogService := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime },
	)
	dataSource, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{
		ProjectID: project.ID,
		Name:      "recursive", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "RECURSIVE_GRAPH_MYSQL_DSN",
	})
	if err != nil {
		t.Fatal(err)
	}
	nodes := []catalog.NodeInput{
		{StableKey: "database:g", Kind: catalog.NodeDatabase, Name: "g", QualifiedName: "mysql://g"},
		{StableKey: "schema:g", ParentStableKey: "database:g", Kind: catalog.NodeSchema, Name: "g", QualifiedName: "g"},
		{StableKey: "table:g.a", ParentStableKey: "schema:g", Kind: catalog.NodeTable, Name: "a", QualifiedName: "g.a"},
		{StableKey: "column:g.a.id", ParentStableKey: "table:g.a", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "g.a.id", DataType: "bigint", Ordinal: 1},
		{StableKey: "table:g.b", ParentStableKey: "schema:g", Kind: catalog.NodeTable, Name: "b", QualifiedName: "g.b"},
		{StableKey: "column:g.b.id", ParentStableKey: "table:g.b", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "g.b.id", DataType: "bigint", Ordinal: 1},
		{StableKey: "table:g.c", ParentStableKey: "schema:g", Kind: catalog.NodeTable, Name: "c", QualifiedName: "g.c"},
		{StableKey: "column:g.c.id", ParentStableKey: "table:g.c", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "g.c.id", DataType: "bigint", Ordinal: 1},
	}
	_, err = catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project.ID, DataSourceID: dataSource.ID, Nodes: nodes,
		ForeignKeys: []catalog.DeclaredForeignKey{
			{ConstraintSchema: "g", Name: "fk_a_b", SourceColumn: "g.a.id", TargetColumn: "g.b.id", Ordinal: 1},
			{ConstraintSchema: "g", Name: "fk_b_c", SourceColumn: "g.b.id", TargetColumn: "g.c.id", Ordinal: 1},
			{ConstraintSchema: "g", Name: "fk_c_a", SourceColumn: "g.c.id", TargetColumn: "g.a.id", Ordinal: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	start, err := catalogService.FindCurrentNode(ctx, project.ID, dataSource.ID, "g.a.id")
	if err != nil {
		t.Fatal(err)
	}
	target, err := catalogService.FindCurrentNode(ctx, project.ID, dataSource.ID, "g.c.id")
	if err != nil {
		t.Fatal(err)
	}

	states := make([]graph.RecursiveEdgeState, 0, 2)
	truncated, _, err := dbsqlite.NewGraphRepository(store).LoadRecursiveEdges(
		ctx,
		graph.RecursiveTraceRequest{
			ProjectID: project.ID, StartNodeID: start.ID, TargetNodeID: target.ID,
			Direction: graph.DirectionDownstream, MaxDepth: 4,
			MaxEdgeExpansions: 100, MaxLoadedBytes: 1 << 20,
		},
		func(state graph.RecursiveEdgeState) error {
			states = append(states, state)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("load recursive paths: %v", err)
	}
	if truncated || len(states) != 2 {
		t.Fatalf("recursive states=%#v truncated=%v, want two complete states", states, truncated)
	}
	if states[0].Depth != 1 || states[0].ParentStateKey != "" ||
		states[1].Depth != 2 || states[1].ParentStateKey != states[0].StateKey ||
		states[1].NextNodeID != target.ID {
		t.Fatalf("recursive state chain = %#v", states)
	}
	upstreamStates := make([]graph.RecursiveEdgeState, 0, 2)
	upstreamTruncated, _, err := dbsqlite.NewGraphRepository(store).LoadRecursiveEdges(
		ctx,
		graph.RecursiveTraceRequest{
			ProjectID: project.ID, StartNodeID: target.ID, TargetNodeID: start.ID,
			Direction: graph.DirectionUpstream, MaxDepth: 4,
			MaxEdgeExpansions: 100, MaxLoadedBytes: 1 << 20,
		},
		func(state graph.RecursiveEdgeState) error {
			upstreamStates = append(upstreamStates, state)
			return nil
		},
	)
	if err != nil || upstreamTruncated || len(upstreamStates) != 2 ||
		upstreamStates[1].NextNodeID != start.ID {
		t.Fatalf("upstream recursive states=%#v truncated=%v error=%v", upstreamStates, upstreamTruncated, err)
	}

	expansionCount := 0
	expansionTruncated, _, err := dbsqlite.NewGraphRepository(store).LoadRecursiveEdges(
		ctx,
		graph.RecursiveTraceRequest{
			ProjectID: project.ID, StartNodeID: start.ID, Direction: graph.DirectionDownstream,
			MaxDepth: 4, MaxEdgeExpansions: 1, MaxLoadedBytes: 1 << 20,
		},
		func(graph.RecursiveEdgeState) error { expansionCount++; return nil },
	)
	if err != nil || !expansionTruncated || expansionCount != 1 {
		t.Fatalf("edge-limited recursive load count=%d truncated=%v error=%v", expansionCount, expansionTruncated, err)
	}

	byteCount := 0
	byteTruncated, consumed, err := dbsqlite.NewGraphRepository(store).LoadRecursiveEdges(
		ctx,
		graph.RecursiveTraceRequest{
			ProjectID: project.ID, StartNodeID: start.ID, Direction: graph.DirectionDownstream,
			MaxDepth: 4, MaxEdgeExpansions: 100, MaxLoadedBytes: 1,
		},
		func(graph.RecursiveEdgeState) error { byteCount++; return nil },
	)
	if err != nil || !byteTruncated || byteCount != 0 || consumed != 0 {
		t.Fatalf("byte-limited recursive load count=%d consumed=%d truncated=%v error=%v", byteCount, consumed, byteTruncated, err)
	}

	cancelledContext, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = dbsqlite.NewGraphRepository(store).LoadRecursiveEdges(
		cancelledContext,
		graph.RecursiveTraceRequest{
			ProjectID: project.ID, StartNodeID: start.ID, Direction: graph.DirectionDownstream,
			MaxDepth: 4, MaxEdgeExpansions: 100, MaxLoadedBytes: 1 << 20,
		},
		func(graph.RecursiveEdgeState) error { return nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recursive load error = %v, want context.Canceled", err)
	}

	cycleResult, err := graph.NewService(dbsqlite.NewGraphRepository(store)).Trace(ctx, graph.TraceRequest{
		ProjectID: project.ID, StartNodeID: start.ID, Direction: graph.DirectionDownstream,
		Limits: graph.Limits{MaxDepth: 4, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace recursive cycle: %v", err)
	}
	if !cycleResult.CycleDetected || len(cycleResult.Paths) != 2 ||
		cycleResult.Paths[1].Nodes[len(cycleResult.Paths[1].Nodes)-1] != target.ID {
		t.Fatalf("recursive cycle result = %#v", cycleResult)
	}
	for name, limits := range map[string]graph.Limits{
		"depth": {MaxDepth: 1, MaxNodes: 10, MaxPaths: 10},
		"nodes": {MaxDepth: 4, MaxNodes: 2, MaxPaths: 10},
		"paths": {MaxDepth: 4, MaxNodes: 10, MaxPaths: 1},
	} {
		t.Run(name+" limit", func(t *testing.T) {
			limited, err := graph.NewService(dbsqlite.NewGraphRepository(store)).Trace(ctx, graph.TraceRequest{
				ProjectID: project.ID, StartNodeID: start.ID, Direction: graph.DirectionDownstream,
				Limits: limits,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !limited.Truncated || len(limited.Paths) != 1 {
				t.Fatalf("%s-limited recursive result = %#v", name, limited)
			}
		})
	}
}

func TestGraphRepositoryBoundsDenseCyclicLargeASTTraversal(t *testing.T) {
	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dense-graph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixedTime := time.Date(2026, time.August, 11, 21, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(24, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	project, err := catalog.NewProjectService(
		dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime },
	).Create(ctx, catalog.CreateProject{Name: "Dense recursive graph"})
	if err != nil {
		t.Fatal(err)
	}
	catalogService := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime },
	)
	dataSource, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{
		ProjectID: project.ID,
		Name:      "dense", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "DENSE_GRAPH_MYSQL_DSN",
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeInputs := []catalog.NodeInput{
		{StableKey: "database:dense", Kind: catalog.NodeDatabase, Name: "dense", QualifiedName: "mysql://dense"},
		{StableKey: "schema:dense", ParentStableKey: "database:dense", Kind: catalog.NodeSchema, Name: "dense", QualifiedName: "dense"},
		{StableKey: "table:dense.nodes", ParentStableKey: "schema:dense", Kind: catalog.NodeTable, Name: "nodes", QualifiedName: "dense.nodes"},
	}
	const nodeCount = 5
	for index := range nodeCount {
		name := fmt.Sprintf("c%d", index+1)
		nodeInputs = append(nodeInputs, catalog.NodeInput{
			StableKey: "column:dense.nodes." + name, ParentStableKey: "table:dense.nodes",
			Kind: catalog.NodeColumn, Name: name, QualifiedName: "dense.nodes." + name,
			DataType: "varchar", Ordinal: index + 1,
		})
	}
	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project.ID, DataSourceID: dataSource.ID, Nodes: nodeInputs,
	}); err != nil {
		t.Fatal(err)
	}
	nodes := make([]catalog.Node, 0, nodeCount)
	for index := range nodeCount {
		node, err := catalogService.FindCurrentNode(
			ctx, project.ID, dataSource.ID, fmt.Sprintf("dense.nodes.c%d", index+1),
		)
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, node)
	}

	commands := relations.NewCommands(
		dbsqlite.NewRelationRepository(store), ids, func() time.Time { return fixedTime },
	)
	agent := relations.Principal{Actor: "dense-agent", Role: relations.RoleAgent, Origin: audit.OriginAgent}
	reviewer := relations.Principal{Actor: "dense-reviewer", Role: relations.RoleReviewer, Origin: audit.OriginWeb}
	firstEdgeLoadedBytes := 0
	for sourceIndex, source := range nodes {
		guard := denseGraphGuard(source.ID)
		for targetIndex, target := range nodes {
			if sourceIndex == targetIndex {
				continue
			}
			created, err := commands.ProposeCreate(ctx, relations.ProposeCreate{
				ProjectID: project.ID, Type: relations.TypeConditionalValueCopy,
				SourceNodeID: source.ID, TargetNodeID: target.ID,
				Guard: guard, Transform: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: source.ID},
				Confidence: 0.9,
				Evidence: []relations.EvidenceInput{{
					Kind: relations.EvidenceCode, Repository: "dense-service", Commit: "dense-commit",
					File: "src/Dense.java", Symbol: "Dense.copy", StartLine: 1, EndLine: 2,
				}},
				Principal: agent, Reason: "Exercise a dense cyclic graph with a large condition AST.",
				RequestID: fmt.Sprintf("dense-create-%d-%d", sourceIndex, targetIndex),
			})
			if err != nil {
				t.Fatalf("create dense edge %d -> %d: %v", sourceIndex, targetIndex, err)
			}
			approved, err := commands.Review(ctx, relations.Review{
				RelationID: created.ID, ExpectedRevisionNo: 1, Decision: relations.DecisionApprove,
				Principal: reviewer, Reason: "Approve the dense graph test edge.",
				RequestID: fmt.Sprintf("dense-review-%d-%d", sourceIndex, targetIndex),
			})
			if err != nil {
				t.Fatalf("approve dense edge %d -> %d: %v", sourceIndex, targetIndex, err)
			}
			if firstEdgeLoadedBytes == 0 {
				firstEdgeLoadedBytes = 128 + revisionASTBytes(t, approved.Active)
			}
		}
	}
	if firstEdgeLoadedBytes < 100_000 {
		t.Fatalf("large edge loaded bytes = %d, want at least 100000", firstEdgeLoadedBytes)
	}

	repository := dbsqlite.NewGraphRepository(store)
	expansionCallbacks := 0
	expansionTruncated, expansionBytes, err := repository.LoadRecursiveEdges(
		ctx,
		graph.RecursiveTraceRequest{
			ProjectID: project.ID, StartNodeID: nodes[0].ID, Direction: graph.DirectionDownstream,
			MaxDepth: nodeCount, MaxEdgeExpansions: 3, MaxLoadedBytes: 16 << 20,
		},
		func(graph.RecursiveEdgeState) error { expansionCallbacks++; return nil },
	)
	if err != nil || !expansionTruncated || expansionCallbacks != 3 || expansionBytes != 3*firstEdgeLoadedBytes {
		t.Fatalf(
			"edge-bounded dense traversal callbacks=%d bytes=%d truncated=%v error=%v, want 3/%d/true/nil",
			expansionCallbacks, expansionBytes, expansionTruncated, err, 3*firstEdgeLoadedBytes,
		)
	}

	byteCallbacks := 0
	byteTruncated, loadedBytes, err := repository.LoadRecursiveEdges(
		ctx,
		graph.RecursiveTraceRequest{
			ProjectID: project.ID, StartNodeID: nodes[0].ID, Direction: graph.DirectionDownstream,
			MaxDepth: nodeCount, MaxEdgeExpansions: 300, MaxLoadedBytes: 2 * firstEdgeLoadedBytes,
		},
		func(graph.RecursiveEdgeState) error { byteCallbacks++; return nil },
	)
	if err != nil || !byteTruncated || byteCallbacks != 2 || loadedBytes != 2*firstEdgeLoadedBytes {
		t.Fatalf(
			"byte-bounded dense traversal callbacks=%d bytes=%d truncated=%v error=%v, want 2/%d/true/nil",
			byteCallbacks, loadedBytes, byteTruncated, err, 2*firstEdgeLoadedBytes,
		)
	}
}

func denseGraphGuard(sourceNodeID int64) *conditions.Boolean {
	values := make([]conditions.Value, 100)
	literalJSON := json.RawMessage(strconv.Quote(strings.Repeat("x", 1900)))
	for index := range values {
		values[index] = conditions.Value{
			Kind: conditions.ValueLiteral,
			Literal: &conditions.Literal{
				Type: conditions.LiteralString, Value: append(json.RawMessage(nil), literalJSON...),
			},
		}
	}
	return &conditions.Boolean{
		Kind:   conditions.BooleanIn,
		Left:   &conditions.Value{Kind: conditions.ValueColumn, NodeID: sourceNodeID},
		Values: values,
	}
}

func revisionASTBytes(t *testing.T, revision *relations.Revision) int {
	t.Helper()
	if revision == nil {
		t.Fatal("approved dense edge has no active revision")
	}
	total := 0
	values := []any{revision.Transform}
	if revision.Guard != nil {
		values = append(values, revision.Guard)
	}
	if revision.Selector != nil {
		values = append(values, revision.Selector)
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		total += len(encoded)
	}
	return total
}
