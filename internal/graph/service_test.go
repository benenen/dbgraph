package graph_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestTraceKeepsUnknownConditionalPathsAndDetectsCycles(t *testing.T) {
	t.Parallel()

	guard := conditions.Boolean{
		Kind:     conditions.BooleanCompare,
		Operator: conditions.CompareEqual,
		Left:     &conditions.Value{Kind: conditions.ValueColumn, NodeID: 100},
		Right: &conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
			Type: conditions.LiteralInteger, Value: json.RawMessage(`1`),
		}},
	}
	repository := &fakeGraphRepository{edges: []graph.Edge{
		{RelationID: 10, VersionID: 11, SourceNodeID: 1, TargetNodeID: 2, Type: relations.TypeConditionalValueCopy, Guard: &guard},
		{RelationID: 20, VersionID: 21, SourceNodeID: 2, TargetNodeID: 3, Type: relations.TypeConditionalValueCopy},
		{RelationID: 30, VersionID: 31, SourceNodeID: 3, TargetNodeID: 1, Type: relations.TypeConditionalValueCopy},
	}}
	service := graph.NewService(repository)

	unknown, err := service.Trace(context.Background(), graph.TraceRequest{
		StartNodeID: 1,
		Direction:   graph.DirectionDownstream,
		Limits:      graph.Limits{MaxDepth: 5, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace unknown condition: %v", err)
	}
	if len(unknown.Paths) != 2 {
		t.Fatalf("unknown path count = %d, want paths to nodes 2 and 3", len(unknown.Paths))
	}
	if unknown.Paths[0].Steps[0].Evaluation.Truth != conditions.TruthUnknown ||
		len(unknown.Paths[0].Steps[0].Evaluation.Missing) != 1 {
		t.Fatalf("unknown conditional step = %#v", unknown.Paths[0].Steps[0])
	}
	if !unknown.CycleDetected {
		t.Fatal("trace did not report the 1 -> 2 -> 3 -> 1 cycle")
	}

	falseResult, err := service.Trace(context.Background(), graph.TraceRequest{
		StartNodeID: 1,
		Direction:   graph.DirectionDownstream,
		Context: conditions.Context{
			Columns: map[int64]json.RawMessage{100: json.RawMessage(`2`)},
		},
		Limits: graph.Limits{MaxDepth: 5, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace false condition: %v", err)
	}
	if len(falseResult.Paths) != 0 {
		t.Fatalf("false guard produced paths: %#v", falseResult.Paths)
	}
}

func TestTraceUsesRecursiveTraversalForEmptyContext(t *testing.T) {
	t.Parallel()

	repository := &recursiveGraphRepository{
		states: []graph.RecursiveEdgeState{{
			StateKey: "10", Depth: 1, NextNodeID: 2,
			Edge: graph.Edge{
				RelationID: 10, VersionID: 11, ProjectID: 1,
				SourceNodeID: 1, TargetNodeID: 2,
			},
		}},
	}
	result, err := graph.NewService(repository).Trace(context.Background(), graph.TraceRequest{
		Direction: graph.DirectionDownstream,
		Limits:    graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace without context: %v", err)
	}
	if repository.recursiveCalls != 1 || repository.layerCalls != 0 {
		t.Fatalf("recursive calls=%d layer calls=%d, want 1 and 0", repository.recursiveCalls, repository.layerCalls)
	}
	if len(result.Paths) != 1 || len(result.Paths[0].Steps) != 1 ||
		result.Paths[0].Nodes[1] != 2 {
		t.Fatalf("recursive trace result = %#v", result)
	}
	impact, err := graph.NewService(repository).Impact(context.Background(), graph.TraceRequest{
		Limits: graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil || len(impact.Paths) != 1 || repository.recursiveCalls != 2 || repository.layerCalls != 0 {
		t.Fatalf("recursive impact result=%#v recursive calls=%d layer calls=%d error=%v", impact, repository.recursiveCalls, repository.layerCalls, err)
	}
}

func TestTraceKeepsLayeredBFSWhenContextIsPresent(t *testing.T) {
	t.Parallel()

	repository := &recursiveGraphRepository{layerEdges: []graph.Edge{{
		RelationID: 10, VersionID: 11, ProjectID: 1,
		SourceNodeID: 1, TargetNodeID: 2,
	}}}
	result, err := graph.NewService(repository).Trace(context.Background(), graph.TraceRequest{
		Direction: graph.DirectionDownstream,
		Context: conditions.Context{
			Parameters: map[string]json.RawMessage{"tenant": json.RawMessage(`"north"`)},
		},
		Limits: graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace with context: %v", err)
	}
	if repository.recursiveCalls != 0 || repository.layerCalls != 1 {
		t.Fatalf("recursive calls=%d layer calls=%d, want 0 and 1", repository.recursiveCalls, repository.layerCalls)
	}
	if len(result.Paths) != 1 || result.Paths[0].Nodes[1] != 2 {
		t.Fatalf("contextual BFS result = %#v", result)
	}
}

func TestRecursiveTraceEvaluatesASTsAndPrunesFalseAncestors(t *testing.T) {
	t.Parallel()

	one := conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
		Type: conditions.LiteralInteger, Value: json.RawMessage(`1`),
	}}
	two := conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
		Type: conditions.LiteralInteger, Value: json.RawMessage(`2`),
	}}
	falseGuard := conditions.Boolean{
		Kind: conditions.BooleanCompare, Operator: conditions.CompareEqual, Left: &one, Right: &two,
	}
	parameter := conditions.Value{Kind: conditions.ValueParameter, Parameter: "tenant"}
	unknownGuard := conditions.Boolean{
		Kind: conditions.BooleanCompare, Operator: conditions.CompareEqual, Left: &parameter, Right: &one,
	}
	repository := &recursiveGraphRepository{states: []graph.RecursiveEdgeState{
		{
			StateKey: "1", Depth: 1, NextNodeID: 2,
			Edge: graph.Edge{RelationID: 1, SourceNodeID: 1, TargetNodeID: 2, Guard: &falseGuard},
		},
		{
			StateKey: "3", Depth: 1, NextNodeID: 4,
			Edge: graph.Edge{
				RelationID: 3, SourceNodeID: 1, TargetNodeID: 4, Guard: &unknownGuard,
				Transform: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 5},
			},
		},
		{
			StateKey: "1,2", ParentStateKey: "1", Depth: 2, NextNodeID: 3,
			Edge: graph.Edge{RelationID: 2, SourceNodeID: 2, TargetNodeID: 3},
		},
	}}
	result, err := graph.NewService(repository).Trace(context.Background(), graph.TraceRequest{
		Limits: graph.Limits{MaxDepth: 3, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Paths) != 1 || result.Paths[0].Nodes[1] != 4 || result.VisitedNodes != 2 {
		t.Fatalf("recursive AST-filtered result = %#v", result)
	}
	evaluation := result.Paths[0].Steps[0].Evaluation
	if evaluation.Truth != conditions.TruthUnknown || len(evaluation.Missing) != 2 ||
		evaluation.Missing[0].Parameter != "tenant" || evaluation.Missing[1].NodeID != 5 {
		t.Fatalf("recursive missing context = %#v", evaluation)
	}
}

func TestTraceStopsAtNodeLimit(t *testing.T) {
	t.Parallel()

	repository := &fakeGraphRepository{edges: []graph.Edge{
		{RelationID: 10, SourceNodeID: 1, TargetNodeID: 2},
		{RelationID: 20, SourceNodeID: 2, TargetNodeID: 3},
	}}
	result, err := graph.NewService(repository).Trace(context.Background(), graph.TraceRequest{
		StartNodeID: 1,
		Direction:   graph.DirectionDownstream,
		Limits:      graph.Limits{MaxDepth: 5, MaxNodes: 2, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace with node limit: %v", err)
	}
	if !result.Truncated || len(result.Paths) != 1 || result.Paths[0].Nodes[len(result.Paths[0].Nodes)-1] != 2 {
		t.Fatalf("node-limited trace = %#v", result)
	}
}

func TestTraceMarksEdgeUnknownWhenCaseTransformContextIsMissing(t *testing.T) {
	t.Parallel()

	primary := conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
		Type: conditions.LiteralString, Value: json.RawMessage(`"primary"`),
	}}
	fallback := conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
		Type: conditions.LiteralString, Value: json.RawMessage(`"fallback"`),
	}}
	transform := conditions.Value{
		Kind: conditions.ValueCase,
		Cases: []conditions.Case{{
			When: conditions.Boolean{
				Kind:     conditions.BooleanCompare,
				Operator: conditions.CompareEqual,
				Left:     &conditions.Value{Kind: conditions.ValueParameter, Parameter: "route"},
				Right:    &primary,
			},
			Then: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 77},
		}},
		Else: &fallback,
	}
	repository := &fakeGraphRepository{edges: []graph.Edge{{
		RelationID: 10, SourceNodeID: 1, TargetNodeID: 2,
		Type: relations.TypeConditionalValueCopy, Transform: transform,
	}}}
	service := graph.NewService(repository)

	missingBranch, err := service.Trace(context.Background(), graph.TraceRequest{
		Direction: graph.DirectionDownstream,
		Limits:    graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace with missing CASE branch dependency: %v", err)
	}
	branchEvaluation := missingBranch.Paths[0].Steps[0].Evaluation
	if branchEvaluation.Truth != conditions.TruthUnknown || len(branchEvaluation.Missing) != 1 ||
		branchEvaluation.Missing[0].Parameter != "route" {
		t.Fatalf("missing CASE branch edge evaluation = %#v", branchEvaluation)
	}

	missingResult, err := service.Trace(context.Background(), graph.TraceRequest{
		Direction: graph.DirectionDownstream,
		Context: conditions.Context{
			Parameters: map[string]json.RawMessage{"route": json.RawMessage(`"primary"`)},
		},
		Limits: graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatalf("trace with missing CASE result dependency: %v", err)
	}
	resultEvaluation := missingResult.Paths[0].Steps[0].Evaluation
	if resultEvaluation.Truth != conditions.TruthUnknown || len(resultEvaluation.Missing) != 1 ||
		resultEvaluation.Missing[0].NodeID != 77 {
		t.Fatalf("missing CASE result edge evaluation = %#v", resultEvaluation)
	}
}

func TestTraceStopsWhenContextIsCancelledDuringTraversal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	repository := cancellingGraphRepository{cancel: cancel}
	_, err := graph.NewService(repository).Trace(ctx, graph.TraceRequest{
		Direction: graph.DirectionDownstream,
		Limits:    graph.Limits{MaxDepth: 8, MaxNodes: 100, MaxPaths: 100},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("trace cancellation error = %v, want context.Canceled", err)
	}
}

func TestRecursiveTraceStopsWhenContextIsCancelledDuringTraversal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	repository := cancellingRecursiveRepository{cancel: cancel}
	_, err := graph.NewService(repository).Trace(ctx, graph.TraceRequest{
		Direction: graph.DirectionDownstream,
		Limits:    graph.Limits{MaxDepth: 8, MaxNodes: 100, MaxPaths: 100},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recursive trace cancellation error = %v, want context.Canceled", err)
	}
}

func TestTraceAppliesGlobalTraversalWorkBudgets(t *testing.T) {
	t.Parallel()

	t.Run("frontier states", func(t *testing.T) {
		edges := make([]graph.Edge, 6_000)
		for index := range edges {
			edges[index] = graph.Edge{
				RelationID: int64(index + 1), ProjectID: 1,
				SourceNodeID: 1, TargetNodeID: int64(index + 2),
			}
		}
		repository := &fakeGraphRepository{edges: edges}
		result, err := graph.NewService(repository).Trace(
			context.Background(),
			graph.TraceRequest{
				Direction: graph.DirectionDownstream,
				Limits:    graph.Limits{MaxDepth: 2, MaxNodes: 10_000, MaxPaths: 10_000},
			},
		)
		if err != nil {
			t.Fatalf("trace with frontier budget: %v", err)
		}
		if !result.Truncated || len(result.Paths) != 0 || result.VisitedNodes > 5_002 {
			t.Fatalf("frontier-limited trace = %#v", result)
		}
	})

	t.Run("edge expansions", func(t *testing.T) {
		edges := make([]graph.Edge, 100_001)
		for index := range edges {
			edges[index] = graph.Edge{
				RelationID: int64(index + 1), ProjectID: 1,
				SourceNodeID: 1, TargetNodeID: 1,
			}
		}
		repository := &fakeGraphRepository{edges: edges}
		result, err := graph.NewService(repository).Trace(
			context.Background(),
			graph.TraceRequest{
				Direction: graph.DirectionDownstream,
				Limits:    graph.Limits{MaxDepth: 2, MaxNodes: 10_000, MaxPaths: 10_000},
			},
		)
		if err != nil {
			t.Fatalf("trace with edge expansion budget: %v", err)
		}
		if !result.Truncated || !result.CycleDetected || result.VisitedNodes != 1 {
			t.Fatalf("edge-expansion-limited trace = %#v", result)
		}
		if len(repository.requestedLimits) != 1 || repository.requestedLimits[0] != 100_000 {
			t.Fatalf("repository edge limits = %v, want remaining hard budget", repository.requestedLimits)
		}
	})

	t.Run("expanded response bytes", func(t *testing.T) {
		encodedLiteral, err := json.Marshal(strings.Repeat("x", 2_000))
		if err != nil {
			t.Fatal(err)
		}
		states := make([]graph.RecursiveEdgeState, 6_000)
		for index := range states {
			relationID := int64(index + 1)
			states[index] = graph.RecursiveEdgeState{
				StateKey: strconv.FormatInt(relationID, 10), Depth: 1, NextNodeID: 2,
				Edge: graph.Edge{
					RelationID: relationID, VersionID: relationID + 10_000, ProjectID: 1,
					SourceNodeID: 1, TargetNodeID: 2,
					Transform: conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
						Type: conditions.LiteralString, Value: encodedLiteral,
					}},
				},
			}
		}
		result, err := graph.NewService(&recursiveGraphRepository{states: states}).Trace(
			context.Background(),
			graph.TraceRequest{
				Direction: graph.DirectionDownstream,
				Limits:    graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10_000},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Truncated || len(result.Paths) == 0 || len(result.Paths) >= len(states) || len(encoded) > 9<<20 {
			t.Fatalf("recursive response paths=%d bytes=%d truncated=%v", len(result.Paths), len(encoded), result.Truncated)
		}
	})
}

func TestRecursiveTraceAppliesGlobalTraversalWorkBudgets(t *testing.T) {
	t.Parallel()

	t.Run("frontier states", func(t *testing.T) {
		states := make([]graph.RecursiveEdgeState, 6_000)
		for index := range states {
			relationID := int64(index + 1)
			states[index] = graph.RecursiveEdgeState{
				StateKey: strconv.FormatInt(relationID, 10), Depth: 1, NextNodeID: relationID + 1,
				Edge: graph.Edge{RelationID: relationID, SourceNodeID: 1, TargetNodeID: relationID + 1},
			}
		}
		repository := &recursiveGraphRepository{states: states}
		result, err := graph.NewService(repository).Trace(context.Background(), graph.TraceRequest{
			Direction: graph.DirectionDownstream,
			Limits:    graph.Limits{MaxDepth: 2, MaxNodes: 10_000, MaxPaths: 10_000},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Truncated || len(result.Paths) != 0 || result.VisitedNodes > 5_002 {
			t.Fatalf("recursive frontier-limited trace = %#v", result)
		}
	})

	t.Run("path states", func(t *testing.T) {
		states := make([]graph.RecursiveEdgeState, 0, 10_000)
		for index := 0; index < 5_000; index++ {
			relationID := int64(index + 1)
			firstKey := strconv.FormatInt(relationID, 10)
			middleNode := int64(index + 2)
			states = append(states, graph.RecursiveEdgeState{
				StateKey: firstKey, Depth: 1, NextNodeID: middleNode,
				Edge: graph.Edge{RelationID: relationID, SourceNodeID: 1, TargetNodeID: middleNode},
			})
			secondRelationID := int64(index + 5_001)
			states = append(states, graph.RecursiveEdgeState{
				StateKey:       firstKey + "," + strconv.FormatInt(secondRelationID, 10),
				ParentStateKey: firstKey, Depth: 2, NextNodeID: 9_000,
				Edge: graph.Edge{RelationID: secondRelationID, SourceNodeID: middleNode, TargetNodeID: 9_000},
			})
		}
		sort.SliceStable(states, func(i, j int) bool { return states[i].Depth < states[j].Depth })
		result, err := graph.NewService(&recursiveGraphRepository{states: states}).Trace(
			context.Background(),
			graph.TraceRequest{
				Direction: graph.DirectionDownstream,
				Limits:    graph.Limits{MaxDepth: 3, MaxNodes: 10_000, MaxPaths: 10_000},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Truncated || len(result.Paths) != 0 || result.VisitedNodes > 5_002 {
			t.Fatalf("recursive path-state-limited trace = %#v", result)
		}
	})

	t.Run("edge expansions", func(t *testing.T) {
		states := make([]graph.RecursiveEdgeState, 100_001)
		for index := range states {
			relationID := int64(index + 1)
			states[index] = graph.RecursiveEdgeState{
				StateKey: strconv.FormatInt(relationID, 10), Depth: 1, NextNodeID: 1, Cycle: true,
				Edge: graph.Edge{RelationID: relationID, SourceNodeID: 1, TargetNodeID: 1},
			}
		}
		repository := &recursiveGraphRepository{states: states}
		result, err := graph.NewService(repository).Trace(context.Background(), graph.TraceRequest{
			Direction: graph.DirectionDownstream,
			Limits:    graph.Limits{MaxDepth: 2, MaxNodes: 10_000, MaxPaths: 10_000},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Truncated || !result.CycleDetected || result.VisitedNodes != 1 {
			t.Fatalf("recursive edge-expansion-limited trace = %#v", result)
		}
		if len(repository.recursiveRequests) != 1 ||
			repository.recursiveRequests[0].MaxEdgeExpansions != 100_000 ||
			repository.recursiveRequests[0].MaxLoadedBytes != 16<<20 {
			t.Fatalf("recursive hard limits = %#v", repository.recursiveRequests)
		}
	})
}

func TestTraceBoundsPathStatesForUnreachableTargetInConvergingDAG(t *testing.T) {
	t.Parallel()

	edges := make([]graph.Edge, 0, 72)
	previous := []int64{1}
	nextNodeID := int64(2)
	for layer := 0; layer < 18; layer++ {
		current := []int64{nextNodeID, nextNodeID + 1}
		nextNodeID += 2
		for _, source := range previous {
			for _, target := range current {
				edges = append(edges, graph.Edge{
					RelationID: int64(len(edges) + 1), ProjectID: 1,
					SourceNodeID: source, TargetNodeID: target,
				})
			}
		}
		previous = current
	}

	result, err := graph.NewService(&fakeGraphRepository{edges: edges}).Trace(
		context.Background(),
		graph.TraceRequest{
			Direction: graph.DirectionDownstream,
			Limits:    graph.Limits{MaxDepth: 32, MaxNodes: 100, MaxPaths: 100},
		},
	)
	if err != nil {
		t.Fatalf("trace converging DAG: %v", err)
	}
	if !result.Truncated || len(result.Paths) != 0 || result.VisitedNodes > 37 {
		t.Fatalf("bounded converging DAG trace = %#v", result)
	}
}

func TestTraceBoundsExpandedResponseBytes(t *testing.T) {
	t.Parallel()

	encodedLiteral, err := json.Marshal(strings.Repeat("x", 2_000))
	if err != nil {
		t.Fatal(err)
	}
	edges := make([]graph.Edge, 6_000)
	for index := range edges {
		edges[index] = graph.Edge{
			RelationID: int64(index + 1), VersionID: int64(index + 10_000), ProjectID: 1,
			SourceNodeID: 1, TargetNodeID: 2,
			Transform: conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
				Type: conditions.LiteralString, Value: encodedLiteral,
			}},
		}
	}
	result, err := graph.NewService(&fakeGraphRepository{edges: edges}).Trace(context.Background(), graph.TraceRequest{
		Context: conditions.Context{}, Limits: graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10_000},
	})
	if err != nil {
		t.Fatalf("trace large expanded response: %v", err)
	}
	if !result.Truncated || len(result.Paths) == 0 || len(result.Paths) >= len(edges) {
		t.Fatalf("response-bounded result paths=%d truncated=%v", len(result.Paths), result.Truncated)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 9<<20 {
		t.Fatalf("expanded graph result = %d bytes, want bounded near 8 MiB", len(encoded))
	}
}

func TestTraceSharesRawEdgeByteBudgetAcrossDepths(t *testing.T) {
	t.Parallel()

	repository := &fakeGraphRepository{
		edges: []graph.Edge{
			{RelationID: 1, SourceNodeID: 1, TargetNodeID: 2},
			{RelationID: 2, SourceNodeID: 2, TargetNodeID: 3},
		},
		loadedBytesPerCall: 10 << 20,
	}
	result, err := graph.NewService(repository).Trace(context.Background(), graph.TraceRequest{
		Limits: graph.Limits{MaxDepth: 8, MaxNodes: 100, MaxPaths: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(repository.requestedByteLimits) != 2 ||
		repository.requestedByteLimits[1] >= repository.requestedByteLimits[0] {
		t.Fatalf("result truncated=%v byte limits=%v", result.Truncated, repository.requestedByteLimits)
	}
}

type fakeGraphRepository struct {
	edges               []graph.Edge
	requestedLimits     []int
	requestedByteLimits []int
	loadedBytesPerCall  int
}

type recursiveGraphRepository struct {
	states            []graph.RecursiveEdgeState
	layerEdges        []graph.Edge
	recursiveRequests []graph.RecursiveTraceRequest
	recursiveCalls    int
	layerCalls        int
}

func (r *recursiveGraphRepository) LoadRecursiveEdges(
	_ context.Context,
	request graph.RecursiveTraceRequest,
	visit func(graph.RecursiveEdgeState) error,
) (bool, int, error) {
	r.recursiveCalls++
	r.recursiveRequests = append(r.recursiveRequests, request)
	for _, state := range r.states {
		if err := visit(state); err != nil {
			return false, len(r.states) * 128, err
		}
	}
	return false, len(r.states) * 128, nil
}

func (r *recursiveGraphRepository) LoadEdges(
	_ context.Context,
	_ []int64,
	_ graph.Direction,
	_ int,
	_ int,
) ([]graph.Edge, bool, int, error) {
	r.layerCalls++
	return append([]graph.Edge(nil), r.layerEdges...), false, len(r.layerEdges) * 128, nil
}

type cancellingGraphRepository struct {
	cancel context.CancelFunc
}

type cancellingRecursiveRepository struct {
	cancel context.CancelFunc
}

func (r cancellingRecursiveRepository) LoadRecursiveEdges(
	_ context.Context,
	_ graph.RecursiveTraceRequest,
	visit func(graph.RecursiveEdgeState) error,
) (bool, int, error) {
	r.cancel()
	err := visit(graph.RecursiveEdgeState{
		StateKey: "1", Depth: 1, NextNodeID: 2,
		Edge: graph.Edge{RelationID: 1, SourceNodeID: 1, TargetNodeID: 2},
	})
	return false, 128, err
}

func (cancellingRecursiveRepository) LoadEdges(
	context.Context,
	[]int64,
	graph.Direction,
	int,
	int,
) ([]graph.Edge, bool, int, error) {
	return nil, false, 0, nil
}

func (r cancellingGraphRepository) LoadEdges(
	_ context.Context,
	_ []int64,
	_ graph.Direction,
	_ int,
	_ int,
) ([]graph.Edge, bool, int, error) {
	r.cancel()
	return []graph.Edge{{SourceNodeID: 1, TargetNodeID: 2}}, false, 128, nil
}

func (r *fakeGraphRepository) LoadEdges(
	_ context.Context,
	nodeIDs []int64,
	direction graph.Direction,
	limit int,
	byteLimit int,
) ([]graph.Edge, bool, int, error) {
	r.requestedLimits = append(r.requestedLimits, limit)
	r.requestedByteLimits = append(r.requestedByteLimits, byteLimit)
	if r.loadedBytesPerCall > byteLimit {
		return []graph.Edge{}, true, 0, nil
	}
	wanted := make(map[int64]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		wanted[nodeID] = struct{}{}
	}
	loaded := make([]graph.Edge, 0)
	for _, edge := range r.edges {
		if edge.ProjectID != projectID {
			continue
		}
		candidate := edge.SourceNodeID
		if direction == graph.DirectionUpstream {
			candidate = edge.TargetNodeID
		}
		if _, exists := wanted[candidate]; exists {
			if len(loaded) >= limit {
				return loaded, true, r.loadedBytes(loaded), nil
			}
			loaded = append(loaded, edge)
		}
	}
	return loaded, false, r.loadedBytes(loaded), nil
}

func (r *fakeGraphRepository) loadedBytes(edges []graph.Edge) int {
	if r.loadedBytesPerCall > 0 {
		return r.loadedBytesPerCall
	}
	return len(edges) * 128
}
