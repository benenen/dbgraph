package conditions_test

import (
	"encoding/json"
	"testing"

	"github.com/benenen/dbgraph/internal/conditions"
)

func TestValidateBooleanAcceptsStructuredComparison(t *testing.T) {
	t.Parallel()

	expression := conditions.Boolean{
		Kind:     conditions.BooleanCompare,
		Operator: conditions.CompareEqual,
		Left: &conditions.Value{
			Kind:   conditions.ValueColumn,
			NodeID: 1001,
		},
		Right: &conditions.Value{
			Kind: conditions.ValueLiteral,
			Literal: &conditions.Literal{
				Type:  conditions.LiteralInteger,
				Value: json.RawMessage(`1`),
			},
		},
	}

	if err := conditions.ValidateBoolean(expression, conditions.DefaultLimits()); err != nil {
		t.Fatalf("ValidateBoolean: %v", err)
	}
}

func TestValidateBooleanRejectsInvalidColumnReference(t *testing.T) {
	t.Parallel()

	expression := conditions.Boolean{
		Kind:     conditions.BooleanCompare,
		Operator: conditions.CompareEqual,
		Left: &conditions.Value{
			Kind:   conditions.ValueColumn,
			NodeID: 0,
		},
		Right: &conditions.Value{
			Kind: conditions.ValueLiteral,
			Literal: &conditions.Literal{
				Type:  conditions.LiteralInteger,
				Value: json.RawMessage(`1`),
			},
		},
	}

	if err := conditions.ValidateBoolean(expression, conditions.DefaultLimits()); err == nil {
		t.Fatal("ValidateBoolean returned nil for an invalid column reference")
	}
}

func TestValidateRejectsFieldsThatDoNotBelongToExpressionKind(t *testing.T) {
	t.Parallel()

	literal := conditions.Value{
		Kind: conditions.ValueLiteral,
		Literal: &conditions.Literal{
			Type:  conditions.LiteralInteger,
			Value: json.RawMessage(`1`),
		},
	}
	comparison := conditions.Boolean{
		Kind: conditions.BooleanAnd,
		Children: []conditions.Boolean{
			{Kind: conditions.BooleanIsNull, Left: &literal},
			{Kind: conditions.BooleanIsNotNull, Left: &literal},
		},
		Left: &conditions.Value{Kind: conditions.ValueColumn, NodeID: 99},
	}
	if err := conditions.ValidateBoolean(comparison, conditions.DefaultLimits()); err == nil {
		t.Fatal("ValidateBoolean accepted an and expression with an unrelated left field")
	}

	column := conditions.Value{Kind: conditions.ValueColumn, NodeID: 99, Parameter: "ignored"}
	if err := conditions.ValidateValue(column, conditions.DefaultLimits()); err == nil {
		t.Fatal("ValidateValue accepted a column expression with an unrelated parameter field")
	}
}
