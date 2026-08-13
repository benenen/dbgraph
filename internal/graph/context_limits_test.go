package graph_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestTraceRejectsUnboundedOrCompositeContextBeforeLoadingEdges(t *testing.T) {
	largeValue, err := json.Marshal(strings.Repeat("x", 8<<10))
	if err != nil {
		t.Fatal(err)
	}
	aggregate := make(map[string]json.RawMessage, 40)
	for index := 0; index < 40; index++ {
		aggregate[fmt.Sprintf("p_%02d", index)] = json.RawMessage(`"` + strings.Repeat("x", 7_000) + `"`)
	}

	tests := []struct {
		name    string
		context conditions.Context
	}{
		{name: "object", context: conditions.Context{Parameters: map[string]json.RawMessage{"tenant": json.RawMessage(`{"nested":true}`)}}},
		{name: "array", context: conditions.Context{Parameters: map[string]json.RawMessage{"tenant": json.RawMessage(`[1,2]`)}}},
		{name: "invalid JSON", context: conditions.Context{Parameters: map[string]json.RawMessage{"tenant": json.RawMessage(`{`)}}},
		{name: "oversized value", context: conditions.Context{Parameters: map[string]json.RawMessage{"tenant": largeValue}}},
		{name: "oversized number", context: conditions.Context{Parameters: map[string]json.RawMessage{"tenant": json.RawMessage(strings.Repeat("9", 257))}}},
		{name: "aggregate bytes", context: conditions.Context{Parameters: aggregate}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeGraphRepository{}
			_, err := graph.NewService(repository).Trace(context.Background(), graph.TraceRequest{
				Context: test.context,
				Limits:  graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10},
			})
			if !errors.Is(err, graph.ErrInvalidTrace) {
				t.Fatalf("Trace error=%v, want ErrInvalidTrace", err)
			}
			if len(repository.requestedLimits) != 0 {
				t.Fatalf("repository was called with limits %v", repository.requestedLimits)
			}
		})
	}
}

func TestTracePreparesAnImmutableContextBeforeRepositoryWork(t *testing.T) {
	raw := json.RawMessage(`1`)
	repository := &contextMutatingRepository{raw: raw}
	result, err := graph.NewService(repository).Trace(context.Background(), graph.TraceRequest{
		Context: conditions.Context{Columns: map[int64]json.RawMessage{100: raw}},
		Limits:  graph.Limits{MaxDepth: 2, MaxNodes: 10, MaxPaths: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Paths) != 1 || result.Paths[0].Steps[0].Evaluation.Truth != conditions.TruthTrue {
		t.Fatalf("prepared-context result=%#v", result)
	}
}

type contextMutatingRepository struct {
	raw json.RawMessage
}

func (r *contextMutatingRepository) LoadEdges(
	context.Context,
	[]int64,
	graph.Direction,
	int,
	int,
) ([]graph.Edge, bool, int, error) {
	r.raw[0] = '2'
	column := conditions.Value{Kind: conditions.ValueColumn, NodeID: 100}
	one := conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
		Type: conditions.LiteralInteger, Value: json.RawMessage(`1`),
	}}
	guard := conditions.Boolean{
		Kind: conditions.BooleanCompare, Operator: conditions.CompareEqual, Left: &column, Right: &one,
	}
	return []graph.Edge{{
		RelationID: 1, VersionID: 1, SourceNodeID: 1, TargetNodeID: 2,
		Type: relations.TypeConditionalValueCopy, Guard: &guard,
	}}, false, 128, nil
}
