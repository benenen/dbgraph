package conditions

import (
	"encoding/json"
	"errors"
	"testing"
)

func literal(kind LiteralType, raw string) Value {
	return Value{Kind: ValueLiteral, Literal: &Literal{Type: kind, Value: json.RawMessage(raw)}}
}

func comparison(operator CompareOperator, left, right Value) Boolean {
	return Boolean{Kind: BooleanCompare, Operator: operator, Left: &left, Right: &right}
}

func TestEvaluateBooleanCoversLogicalAndPredicateKinds(t *testing.T) {
	t.Parallel()

	column := Value{Kind: ValueColumn, NodeID: 7}
	parameter := Value{Kind: ValueParameter, Parameter: "state"}
	one := literal(LiteralInteger, `1`)
	two := literal(LiteralInteger, `2`)
	nullValue := literal(LiteralNull, `null`)

	tests := []struct {
		name       string
		expression Boolean
		context    Context
		want       Truth
		missing    int
	}{
		{"or true", Boolean{Kind: BooleanOr, Children: []Boolean{comparison(CompareEqual, one, two), comparison(CompareEqual, one, one)}}, Context{}, TruthTrue, 0},
		{"or false", Boolean{Kind: BooleanOr, Children: []Boolean{comparison(CompareEqual, one, two), comparison(CompareNotEqual, one, one)}}, Context{}, TruthFalse, 0},
		{"or unknown", Boolean{Kind: BooleanOr, Children: []Boolean{comparison(CompareEqual, column, one), comparison(CompareEqual, parameter, one)}}, Context{}, TruthUnknown, 2},
		{"and unknown", Boolean{Kind: BooleanAnd, Children: []Boolean{comparison(CompareEqual, column, one), comparison(CompareEqual, one, one)}}, Context{}, TruthUnknown, 1},
		{"not true", Boolean{Kind: BooleanNot, Operand: pointerBoolean(comparison(CompareEqual, one, one))}, Context{}, TruthFalse, 0},
		{"not false", Boolean{Kind: BooleanNot, Operand: pointerBoolean(comparison(CompareEqual, one, two))}, Context{}, TruthTrue, 0},
		{"not unknown", Boolean{Kind: BooleanNot, Operand: pointerBoolean(comparison(CompareEqual, column, one))}, Context{}, TruthUnknown, 1},
		{"in match", Boolean{Kind: BooleanIn, Left: &one, Values: []Value{two, one}}, Context{}, TruthTrue, 0},
		{"in no match", Boolean{Kind: BooleanIn, Left: &one, Values: []Value{two}}, Context{}, TruthFalse, 0},
		{"not in", Boolean{Kind: BooleanNotIn, Left: &one, Values: []Value{two}}, Context{}, TruthTrue, 0},
		{"in missing left", Boolean{Kind: BooleanIn, Left: &column, Values: []Value{one}}, Context{}, TruthUnknown, 1},
		{"in missing candidate", Boolean{Kind: BooleanIn, Left: &one, Values: []Value{column, two}}, Context{}, TruthUnknown, 1},
		{"is null", Boolean{Kind: BooleanIsNull, Left: &nullValue}, Context{}, TruthTrue, 0},
		{"is not null", Boolean{Kind: BooleanIsNotNull, Left: &one}, Context{}, TruthTrue, 0},
		{"is null missing", Boolean{Kind: BooleanIsNull, Left: &column}, Context{}, TruthUnknown, 1},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := EvaluateBoolean(test.expression, test.context)
			if err != nil {
				t.Fatalf("EvaluateBoolean: %v", err)
			}
			if result.Truth != test.want || len(result.Missing) != test.missing {
				t.Fatalf("result = %#v, want truth %v and %d missing", result, test.want, test.missing)
			}
		})
	}
}

func TestEvaluateBooleanComparesNumbersStringsBooleansAndMismatchedTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		operator CompareOperator
		left     Value
		right    Value
		want     Truth
	}{
		{"number equal", CompareEqual, literal(LiteralDecimal, `1.00`), literal(LiteralInteger, `1`), TruthTrue},
		{"number not equal", CompareNotEqual, literal(LiteralInteger, `1`), literal(LiteralInteger, `2`), TruthTrue},
		{"number greater", CompareGreaterThan, literal(LiteralInteger, `2`), literal(LiteralInteger, `1`), TruthTrue},
		{"number greater equal", CompareGreaterThanOrEqual, literal(LiteralInteger, `2`), literal(LiteralInteger, `2`), TruthTrue},
		{"number less", CompareLessThan, literal(LiteralInteger, `1`), literal(LiteralInteger, `2`), TruthTrue},
		{"number less equal", CompareLessThanOrEqual, literal(LiteralInteger, `2`), literal(LiteralInteger, `2`), TruthTrue},
		{"string less", CompareLessThan, literal(LiteralString, `"a"`), literal(LiteralString, `"b"`), TruthTrue},
		{"string greater", CompareGreaterThan, literal(LiteralString, `"b"`), literal(LiteralString, `"a"`), TruthTrue},
		{"boolean less", CompareLessThan, literal(LiteralBoolean, `false`), literal(LiteralBoolean, `true`), TruthTrue},
		{"boolean greater", CompareGreaterThan, literal(LiteralBoolean, `true`), literal(LiteralBoolean, `false`), TruthTrue},
		{"mismatch equal", CompareEqual, literal(LiteralString, `"1"`), literal(LiteralInteger, `1`), TruthFalse},
		{"mismatch not equal", CompareNotEqual, literal(LiteralString, `"1"`), literal(LiteralInteger, `1`), TruthTrue},
		{"mismatch ordered", CompareGreaterThan, literal(LiteralString, `"1"`), literal(LiteralInteger, `1`), TruthUnknown},
		{"null", CompareEqual, literal(LiteralNull, `null`), literal(LiteralNull, `null`), TruthUnknown},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := EvaluateBoolean(comparison(test.operator, test.left, test.right), Context{})
			if err != nil {
				t.Fatalf("EvaluateBoolean: %v", err)
			}
			if result.Truth != test.want {
				t.Fatalf("truth = %v, want %v", result.Truth, test.want)
			}
		})
	}
}

func TestEvaluateCaseAndRuntimeInputFailures(t *testing.T) {
	t.Parallel()

	caseValue := Value{
		Kind: ValueCase,
		Cases: []Case{{
			When: comparison(CompareEqual, Value{Kind: ValueParameter, Parameter: "choose"}, literal(LiteralString, `"first"`)),
			Then: literal(LiteralInteger, `10`),
		}},
		Else: pointerValue(literal(LiteralInteger, `20`)),
	}

	for name, input := range map[string]string{"branch": `"first"`, "else": `"other"`} {
		name, input := name, input
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want := "10"
			if name == "else" {
				want = "20"
			}
			result, err := EvaluateBoolean(comparison(CompareEqual, caseValue, literal(LiteralInteger, want)), Context{
				Parameters: map[string]json.RawMessage{"choose": json.RawMessage(input)},
			})
			if err != nil || result.Truth != TruthTrue {
				t.Fatalf("result = %#v, err = %v", result, err)
			}
		})
	}

	unknown, err := EvaluateBoolean(comparison(CompareEqual, caseValue, literal(LiteralInteger, `10`)), Context{})
	if err != nil || unknown.Truth != TruthUnknown || len(unknown.Missing) != 1 {
		t.Fatalf("unknown case = %#v, err = %v", unknown, err)
	}

	for _, raw := range []string{`{`, `1 2`} {
		_, err := EvaluateBoolean(comparison(CompareEqual, Value{Kind: ValueColumnCopy, NodeID: 8}, literal(LiteralInteger, `1`)), Context{
			Columns: map[int64]json.RawMessage{8: json.RawMessage(raw)},
		})
		if !errors.Is(err, ErrEvaluation) {
			t.Fatalf("raw %q error = %v, want ErrEvaluation", raw, err)
		}
	}
}

func TestEvaluateNormalizesDuplicateMissingReferences(t *testing.T) {
	t.Parallel()

	column := Value{Kind: ValueColumn, NodeID: 9}
	parameter := Value{Kind: ValueParameter, Parameter: "p"}
	expression := Boolean{Kind: BooleanAnd, Children: []Boolean{
		comparison(CompareEqual, parameter, literal(LiteralInteger, `1`)),
		Boolean{Kind: BooleanOr, Children: []Boolean{
			comparison(CompareEqual, column, literal(LiteralInteger, `1`)),
			comparison(CompareEqual, parameter, literal(LiteralInteger, `2`)),
		}},
	}}
	result, err := EvaluateBoolean(expression, Context{})
	if err != nil {
		t.Fatalf("EvaluateBoolean: %v", err)
	}
	if result.Truth != TruthUnknown || len(result.Missing) != 2 || result.Missing[0].Parameter != "p" || result.Missing[1].NodeID != 9 {
		t.Fatalf("normalized missing = %#v", result.Missing)
	}
}

func TestValidatorRejectsEveryStructuralBoundary(t *testing.T) {
	t.Parallel()

	validLiteral := literal(LiteralInteger, `1`)
	validCompare := comparison(CompareEqual, validLiteral, validLiteral)
	invalidCases := []struct {
		name    string
		value   *Value
		boolean *Boolean
		limits  Limits
	}{
		{"nonpositive limits", nil, &validCompare, Limits{}},
		{"and needs children", nil, &Boolean{Kind: BooleanAnd}, DefaultLimits()},
		{"not needs only operand", nil, &Boolean{Kind: BooleanNot, Operand: &validCompare, Left: &validLiteral}, DefaultLimits()},
		{"compare operator", nil, &Boolean{Kind: BooleanCompare, Operator: "bad", Left: &validLiteral, Right: &validLiteral}, DefaultLimits()},
		{"in needs values", nil, &Boolean{Kind: BooleanIn, Left: &validLiteral}, DefaultLimits()},
		{"is null needs only left", nil, &Boolean{Kind: BooleanIsNull, Left: &validLiteral, Right: &validLiteral}, DefaultLimits()},
		{"unknown boolean", nil, &Boolean{Kind: "future"}, DefaultLimits()},
		{"invalid column", pointerValue(Value{Kind: ValueColumn, NodeID: -1}), nil, DefaultLimits()},
		{"invalid parameter", pointerValue(Value{Kind: ValueParameter, Parameter: "  "}), nil, DefaultLimits()},
		{"missing literal", pointerValue(Value{Kind: ValueLiteral}), nil, DefaultLimits()},
		{"case needs else", pointerValue(Value{Kind: ValueCase, Cases: []Case{{When: validCompare, Then: validLiteral}}}), nil, DefaultLimits()},
		{"unknown value", pointerValue(Value{Kind: "future"}), nil, DefaultLimits()},
	}

	for _, test := range invalidCases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var err error
			if test.value != nil {
				err = ValidateValue(*test.value, test.limits)
			} else {
				err = ValidateBoolean(*test.boolean, test.limits)
			}
			if !errors.Is(err, ErrInvalidExpression) {
				t.Fatalf("error = %v, want ErrInvalidExpression", err)
			}
		})
	}

	deep := Boolean{Kind: BooleanNot, Operand: &validCompare}
	if err := ValidateBoolean(deep, Limits{MaxDepth: 1, MaxNodes: 100, MaxStringLength: 10, MaxListItems: 10}); !errors.Is(err, ErrInvalidExpression) {
		t.Fatalf("depth error = %v", err)
	}
	if err := ValidateBoolean(validCompare, Limits{MaxDepth: 10, MaxNodes: 1, MaxStringLength: 10, MaxListItems: 10}); !errors.Is(err, ErrInvalidExpression) {
		t.Fatalf("node count error = %v", err)
	}
}

func TestValidateLiteralKindsAndFailures(t *testing.T) {
	t.Parallel()

	limits := DefaultLimits()
	valid := []Value{
		literal(LiteralInteger, `-9223372036854775808`),
		literal(LiteralDecimal, `1.25e2`),
		literal(LiteralString, `"text"`),
		literal(LiteralBoolean, `true`),
		literal(LiteralNull, `null`),
	}
	for _, value := range valid {
		if err := ValidateValue(value, limits); err != nil {
			t.Fatalf("valid literal %#v: %v", value.Literal, err)
		}
	}

	invalid := []Value{
		literal(LiteralInteger, `1.2`),
		literal(LiteralInteger, `9223372036854775808`),
		literal(LiteralDecimal, `"1"`),
		literal(LiteralString, `1`),
		literal(LiteralBoolean, `1`),
		literal(LiteralNull, `false`),
		literal("future", `1`),
		literal(LiteralInteger, `{`),
	}
	for _, value := range invalid {
		if err := ValidateValue(value, limits); !errors.Is(err, ErrInvalidExpression) {
			t.Fatalf("invalid literal %#v error = %v", value.Literal, err)
		}
	}

	tooLong := literal(LiteralString, `"ab"`)
	if err := ValidateValue(tooLong, Limits{MaxDepth: 3, MaxNodes: 3, MaxStringLength: 1, MaxListItems: 3}); !errors.Is(err, ErrInvalidExpression) {
		t.Fatalf("long string error = %v", err)
	}
}

func TestUnreachableEvaluationDefensesReturnStableError(t *testing.T) {
	t.Parallel()

	if _, err := evaluateBoolean(Boolean{Kind: "future"}, Context{}); !errors.Is(err, ErrEvaluation) {
		t.Fatalf("boolean error = %v", err)
	}
	if _, err := evaluateValue(Value{Kind: "future"}, Context{}); !errors.Is(err, ErrEvaluation) {
		t.Fatalf("value error = %v", err)
	}
	if _, err := compareRuntimeValues(json.Number("1"), json.Number("1"), "future"); !errors.Is(err, ErrEvaluation) {
		t.Fatalf("comparison error = %v", err)
	}
}

func pointerBoolean(value Boolean) *Boolean { return &value }

func pointerValue(value Value) *Value { return &value }
