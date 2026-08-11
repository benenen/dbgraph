package conditions_test

import (
	"encoding/json"
	"testing"

	"github.com/benenen/dbgraph/internal/conditions"
)

func TestEvaluateBooleanUsesThreeValuedLogic(t *testing.T) {
	t.Parallel()

	expression := conditions.Boolean{
		Kind: conditions.BooleanAnd,
		Children: []conditions.Boolean{
			{
				Kind:     conditions.BooleanCompare,
				Operator: conditions.CompareEqual,
				Left:     &conditions.Value{Kind: conditions.ValueColumn, NodeID: 1001},
				Right: &conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
					Type: conditions.LiteralInteger, Value: json.RawMessage(`1`),
				}},
			},
			{
				Kind: conditions.BooleanIn,
				Left: &conditions.Value{Kind: conditions.ValueParameter, Parameter: "status"},
				Values: []conditions.Value{
					{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{Type: conditions.LiteralString, Value: json.RawMessage(`"ACTIVE"`)}},
					{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{Type: conditions.LiteralString, Value: json.RawMessage(`"READY"`)}},
				},
			},
		},
	}

	unknown, err := conditions.EvaluateBoolean(expression, conditions.Context{
		Columns: map[int64]json.RawMessage{1001: json.RawMessage(`1`)},
	})
	if err != nil {
		t.Fatalf("evaluate unknown expression: %v", err)
	}
	if unknown.Truth != conditions.TruthUnknown || len(unknown.Missing) != 1 || unknown.Missing[0].Parameter != "status" {
		t.Fatalf("unknown evaluation = %#v", unknown)
	}

	trueResult, err := conditions.EvaluateBoolean(expression, conditions.Context{
		Columns:    map[int64]json.RawMessage{1001: json.RawMessage(`1`)},
		Parameters: map[string]json.RawMessage{"status": json.RawMessage(`"READY"`)},
	})
	if err != nil {
		t.Fatalf("evaluate true expression: %v", err)
	}
	if trueResult.Truth != conditions.TruthTrue || len(trueResult.Missing) != 0 {
		t.Fatalf("true evaluation = %#v", trueResult)
	}

	falseResult, err := conditions.EvaluateBoolean(expression, conditions.Context{
		Columns:    map[int64]json.RawMessage{1001: json.RawMessage(`2`)},
		Parameters: map[string]json.RawMessage{"status": json.RawMessage(`"READY"`)},
	})
	if err != nil {
		t.Fatalf("evaluate false expression: %v", err)
	}
	if falseResult.Truth != conditions.TruthFalse {
		t.Fatalf("false evaluation = %#v", falseResult)
	}
}

func TestEvaluateValueReportsMissingCaseDependencies(t *testing.T) {
	t.Parallel()

	transform := conditions.Value{
		Kind: conditions.ValueCase,
		Cases: []conditions.Case{{
			When: conditions.Boolean{
				Kind:     conditions.BooleanCompare,
				Operator: conditions.CompareEqual,
				Left:     &conditions.Value{Kind: conditions.ValueParameter, Parameter: "route"},
				Right: &conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
					Type: conditions.LiteralString, Value: json.RawMessage(`"primary"`),
				}},
			},
			Then: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 77},
		}},
		Else: &conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
			Type: conditions.LiteralString, Value: json.RawMessage(`"fallback"`),
		}},
	}

	missingBranch, err := conditions.EvaluateValue(transform, conditions.Context{})
	if err != nil {
		t.Fatalf("evaluate transform with missing CASE branch dependency: %v", err)
	}
	if missingBranch.Truth != conditions.TruthUnknown || len(missingBranch.Missing) != 1 ||
		missingBranch.Missing[0].Parameter != "route" {
		t.Fatalf("missing CASE branch evaluation = %#v", missingBranch)
	}

	missingResult, err := conditions.EvaluateValue(transform, conditions.Context{
		Parameters: map[string]json.RawMessage{"route": json.RawMessage(`"primary"`)},
	})
	if err != nil {
		t.Fatalf("evaluate transform with missing selected result: %v", err)
	}
	if missingResult.Truth != conditions.TruthUnknown || len(missingResult.Missing) != 1 ||
		missingResult.Missing[0].NodeID != 77 {
		t.Fatalf("missing CASE result evaluation = %#v", missingResult)
	}

	known, err := conditions.EvaluateValue(transform, conditions.Context{
		Parameters: map[string]json.RawMessage{"route": json.RawMessage(`"secondary"`)},
	})
	if err != nil {
		t.Fatalf("evaluate known transform: %v", err)
	}
	if known.Truth != conditions.TruthTrue || len(known.Missing) != 0 {
		t.Fatalf("known transform evaluation = %#v", known)
	}
}
