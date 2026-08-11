package conditions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
)

var ErrEvaluation = errors.New("condition evaluation failed")

type Truth int

const (
	TruthUnknown Truth = iota
	TruthFalse
	TruthTrue
)

type MissingReference struct {
	NodeID    int64  `json:"nodeId,omitempty"`
	Parameter string `json:"parameter,omitempty"`
}

type Context struct {
	Columns    map[int64]json.RawMessage
	Parameters map[string]json.RawMessage
}

type ContextLimits struct {
	MaxColumns         int
	MaxParameters      int
	MaxTotalBytes      int
	MaxValueBytes      int
	MaxNumberBytes     int
	MaxParameterLength int
}

func DefaultContextLimits() ContextLimits {
	return ContextLimits{
		MaxColumns: 1000, MaxParameters: 1000, MaxTotalBytes: 256 << 10,
		MaxValueBytes: 8 << 10, MaxNumberBytes: 256, MaxParameterLength: 2000,
	}
}

// PreparedContext owns decoded scalar copies of untrusted context input. Its
// fields remain private so callers cannot mutate values during evaluation.
type PreparedContext struct {
	columns    map[int64]runtimeValue
	parameters map[string]runtimeValue
}

type Evaluation struct {
	Truth   Truth              `json:"truth"`
	Missing []MissingReference `json:"missing,omitempty"`
}

type runtimeValue struct {
	known   bool
	value   any
	missing []MissingReference
}

type runtimeNumber struct {
	value *big.Rat
}

func PrepareContext(input Context, limits ContextLimits) (PreparedContext, error) {
	if limits.MaxColumns < 1 || limits.MaxParameters < 1 || limits.MaxTotalBytes < 1 ||
		limits.MaxValueBytes < 1 || limits.MaxNumberBytes < 1 || limits.MaxParameterLength < 1 ||
		len(input.Columns) > limits.MaxColumns || len(input.Parameters) > limits.MaxParameters {
		return PreparedContext{}, fmt.Errorf("%w: invalid context limits", ErrEvaluation)
	}
	prepared := PreparedContext{
		columns:    make(map[int64]runtimeValue, len(input.Columns)),
		parameters: make(map[string]runtimeValue, len(input.Parameters)),
	}
	totalBytes := 0
	for nodeID, raw := range input.Columns {
		if nodeID <= 0 {
			return PreparedContext{}, fmt.Errorf("%w: invalid context column", ErrEvaluation)
		}
		value, err := prepareContextValue(raw, limits, 8, &totalBytes)
		if err != nil {
			return PreparedContext{}, err
		}
		prepared.columns[nodeID] = runtimeValue{known: true, value: value}
	}
	for parameter, raw := range input.Parameters {
		if strings.TrimSpace(parameter) == "" || len(parameter) > limits.MaxParameterLength {
			return PreparedContext{}, fmt.Errorf("%w: invalid context parameter", ErrEvaluation)
		}
		value, err := prepareContextValue(raw, limits, len(parameter), &totalBytes)
		if err != nil {
			return PreparedContext{}, err
		}
		prepared.parameters[parameter] = runtimeValue{known: true, value: value}
	}
	return prepared, nil
}

func prepareContextValue(raw json.RawMessage, limits ContextLimits, keyBytes int, totalBytes *int) (any, error) {
	if len(raw) == 0 || len(raw) > limits.MaxValueBytes || keyBytes > limits.MaxTotalBytes-len(raw) ||
		*totalBytes > limits.MaxTotalBytes-keyBytes-len(raw) {
		return nil, fmt.Errorf("%w: context byte budget exceeded", ErrEvaluation)
	}
	value, err := decodeRuntimeValueWithNumberLimit(append(json.RawMessage(nil), raw...), limits.MaxNumberBytes)
	if err != nil {
		return nil, err
	}
	*totalBytes += keyBytes + len(raw)
	return value, nil
}

func EvaluateBoolean(expression Boolean, context Context) (Evaluation, error) {
	prepared, err := PrepareContext(context, DefaultContextLimits())
	if err != nil {
		return Evaluation{}, err
	}
	return EvaluateBooleanPrepared(expression, prepared)
}

func EvaluateBooleanPrepared(expression Boolean, context PreparedContext) (Evaluation, error) {
	if err := ValidateBoolean(expression, DefaultLimits()); err != nil {
		return Evaluation{}, err
	}
	evaluation, err := evaluateBooleanPrepared(expression, context)
	if err != nil {
		return Evaluation{}, err
	}
	evaluation.Missing = normalizeMissing(evaluation.Missing)
	return evaluation, nil
}

// EvaluateValue reports whether every context dependency needed to resolve a
// value expression is available. A resolved value is TRUE; an unresolved value
// is UNKNOWN with the missing column and parameter references.
func EvaluateValue(expression Value, context Context) (Evaluation, error) {
	prepared, err := PrepareContext(context, DefaultContextLimits())
	if err != nil {
		return Evaluation{}, err
	}
	return EvaluateValuePrepared(expression, prepared)
}

func EvaluateValuePrepared(expression Value, context PreparedContext) (Evaluation, error) {
	if err := ValidateValue(expression, DefaultLimits()); err != nil {
		return Evaluation{}, err
	}
	value, err := evaluateValuePrepared(expression, context)
	if err != nil {
		return Evaluation{}, err
	}
	if value.known {
		return Evaluation{Truth: TruthTrue}, nil
	}
	return Evaluation{Truth: TruthUnknown, Missing: normalizeMissing(value.missing)}, nil
}

func evaluateBoolean(expression Boolean, context Context) (Evaluation, error) {
	prepared, err := PrepareContext(context, DefaultContextLimits())
	if err != nil {
		return Evaluation{}, err
	}
	return evaluateBooleanPrepared(expression, prepared)
}

func evaluateBooleanPrepared(expression Boolean, context PreparedContext) (Evaluation, error) {
	switch expression.Kind {
	case BooleanAnd:
		missing := make([]MissingReference, 0)
		for _, child := range expression.Children {
			result, err := evaluateBooleanPrepared(child, context)
			if err != nil {
				return Evaluation{}, err
			}
			if result.Truth == TruthFalse {
				return Evaluation{Truth: TruthFalse}, nil
			}
			if result.Truth == TruthUnknown {
				missing = append(missing, result.Missing...)
			}
		}
		if len(missing) > 0 {
			return Evaluation{Truth: TruthUnknown, Missing: missing}, nil
		}
		return Evaluation{Truth: TruthTrue}, nil
	case BooleanOr:
		missing := make([]MissingReference, 0)
		for _, child := range expression.Children {
			result, err := evaluateBooleanPrepared(child, context)
			if err != nil {
				return Evaluation{}, err
			}
			if result.Truth == TruthTrue {
				return Evaluation{Truth: TruthTrue}, nil
			}
			if result.Truth == TruthUnknown {
				missing = append(missing, result.Missing...)
			}
		}
		if len(missing) > 0 {
			return Evaluation{Truth: TruthUnknown, Missing: missing}, nil
		}
		return Evaluation{Truth: TruthFalse}, nil
	case BooleanNot:
		result, err := evaluateBooleanPrepared(*expression.Operand, context)
		if err != nil || result.Truth == TruthUnknown {
			return result, err
		}
		if result.Truth == TruthTrue {
			result.Truth = TruthFalse
		} else {
			result.Truth = TruthTrue
		}
		return result, nil
	case BooleanCompare:
		left, err := evaluateValuePrepared(*expression.Left, context)
		if err != nil {
			return Evaluation{}, err
		}
		right, err := evaluateValuePrepared(*expression.Right, context)
		if err != nil {
			return Evaluation{}, err
		}
		if !left.known || !right.known {
			return unknownFromValues(left, right), nil
		}
		truth, err := compareRuntimeValues(left.value, right.value, expression.Operator)
		if err != nil {
			return Evaluation{}, err
		}
		return Evaluation{Truth: truth}, nil
	case BooleanIn, BooleanNotIn:
		left, err := evaluateValuePrepared(*expression.Left, context)
		if err != nil {
			return Evaluation{}, err
		}
		if !left.known {
			return Evaluation{Truth: TruthUnknown, Missing: left.missing}, nil
		}
		missing := make([]MissingReference, 0)
		matched := false
		for _, item := range expression.Values {
			candidate, err := evaluateValuePrepared(item, context)
			if err != nil {
				return Evaluation{}, err
			}
			if !candidate.known {
				missing = append(missing, candidate.missing...)
				continue
			}
			equal, err := compareRuntimeValues(left.value, candidate.value, CompareEqual)
			if err != nil {
				return Evaluation{}, err
			}
			if equal == TruthTrue {
				matched = true
				break
			}
		}
		if !matched && len(missing) > 0 {
			return Evaluation{Truth: TruthUnknown, Missing: missing}, nil
		}
		if expression.Kind == BooleanNotIn {
			matched = !matched
		}
		if matched {
			return Evaluation{Truth: TruthTrue}, nil
		}
		return Evaluation{Truth: TruthFalse}, nil
	case BooleanIsNull, BooleanIsNotNull:
		value, err := evaluateValuePrepared(*expression.Left, context)
		if err != nil {
			return Evaluation{}, err
		}
		if !value.known {
			return Evaluation{Truth: TruthUnknown, Missing: value.missing}, nil
		}
		isNull := value.value == nil
		if expression.Kind == BooleanIsNotNull {
			isNull = !isNull
		}
		if isNull {
			return Evaluation{Truth: TruthTrue}, nil
		}
		return Evaluation{Truth: TruthFalse}, nil
	default:
		return Evaluation{}, ErrEvaluation
	}
}

func evaluateValue(expression Value, context Context) (runtimeValue, error) {
	prepared, err := PrepareContext(context, DefaultContextLimits())
	if err != nil {
		return runtimeValue{}, err
	}
	return evaluateValuePrepared(expression, prepared)
}

func evaluateValuePrepared(expression Value, context PreparedContext) (runtimeValue, error) {
	switch expression.Kind {
	case ValueColumn, ValueColumnCopy:
		value, found := context.columns[expression.NodeID]
		if !found {
			return runtimeValue{missing: []MissingReference{{NodeID: expression.NodeID}}}, nil
		}
		return value, nil
	case ValueParameter:
		value, found := context.parameters[expression.Parameter]
		if !found {
			return runtimeValue{missing: []MissingReference{{Parameter: expression.Parameter}}}, nil
		}
		return value, nil
	case ValueLiteral:
		value, err := decodeRuntimeValue(expression.Literal.Value)
		return runtimeValue{known: err == nil, value: value}, err
	case ValueCase:
		for _, branch := range expression.Cases {
			condition, err := evaluateBooleanPrepared(branch.When, context)
			if err != nil {
				return runtimeValue{}, err
			}
			if condition.Truth == TruthUnknown {
				return runtimeValue{missing: condition.Missing}, nil
			}
			if condition.Truth == TruthTrue {
				return evaluateValuePrepared(branch.Then, context)
			}
		}
		return evaluateValuePrepared(*expression.Else, context)
	default:
		return runtimeValue{}, ErrEvaluation
	}
}

func decodeRuntimeValue(raw json.RawMessage) (any, error) {
	return decodeRuntimeValueWithNumberLimit(raw, 0)
}

func decodeRuntimeValueWithNumberLimit(raw json.RawMessage, maximumNumberBytes int) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: invalid context value", ErrEvaluation)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: context value contains trailing data", ErrEvaluation)
	}
	switch typed := value.(type) {
	case nil, string, bool:
		return typed, nil
	case json.Number:
		if maximumNumberBytes > 0 && len(typed.String()) > maximumNumberBytes {
			return nil, fmt.Errorf("%w: context number is too large", ErrEvaluation)
		}
		parsed, ok := new(big.Rat).SetString(typed.String())
		if !ok {
			return nil, fmt.Errorf("%w: invalid context number", ErrEvaluation)
		}
		return runtimeNumber{value: parsed}, nil
	default:
		return nil, fmt.Errorf("%w: context value must be a JSON scalar", ErrEvaluation)
	}
}

func compareRuntimeValues(left any, right any, operator CompareOperator) (Truth, error) {
	if left == nil || right == nil {
		return TruthUnknown, nil
	}
	comparison, comparable := compareValues(left, right)
	if !comparable {
		if operator == CompareEqual {
			return TruthFalse, nil
		}
		if operator == CompareNotEqual {
			return TruthTrue, nil
		}
		return TruthUnknown, nil
	}
	matched := false
	switch operator {
	case CompareEqual:
		matched = comparison == 0
	case CompareNotEqual:
		matched = comparison != 0
	case CompareGreaterThan:
		matched = comparison > 0
	case CompareGreaterThanOrEqual:
		matched = comparison >= 0
	case CompareLessThan:
		matched = comparison < 0
	case CompareLessThanOrEqual:
		matched = comparison <= 0
	default:
		return TruthUnknown, ErrEvaluation
	}
	if matched {
		return TruthTrue, nil
	}
	return TruthFalse, nil
}

func compareValues(left any, right any) (int, bool) {
	switch typedLeft := left.(type) {
	case runtimeNumber:
		typedRight, ok := right.(runtimeNumber)
		if !ok {
			return 0, false
		}
		return typedLeft.value.Cmp(typedRight.value), true
	case json.Number:
		typedRight, ok := right.(json.Number)
		if !ok {
			return 0, false
		}
		leftNumber, leftOK := new(big.Rat).SetString(typedLeft.String())
		rightNumber, rightOK := new(big.Rat).SetString(typedRight.String())
		if !leftOK || !rightOK {
			return 0, false
		}
		return leftNumber.Cmp(rightNumber), true
	case string:
		typedRight, ok := right.(string)
		if !ok {
			return 0, false
		}
		if typedLeft < typedRight {
			return -1, true
		}
		if typedLeft > typedRight {
			return 1, true
		}
		return 0, true
	case bool:
		typedRight, ok := right.(bool)
		if !ok {
			return 0, false
		}
		if typedLeft == typedRight {
			return 0, true
		}
		if !typedLeft && typedRight {
			return -1, true
		}
		return 1, true
	default:
		return 0, false
	}
}

func unknownFromValues(values ...runtimeValue) Evaluation {
	missing := make([]MissingReference, 0)
	for _, value := range values {
		missing = append(missing, value.missing...)
	}
	return Evaluation{Truth: TruthUnknown, Missing: missing}
}

func normalizeMissing(values []MissingReference) []MissingReference {
	sort.Slice(values, func(i, j int) bool {
		if values[i].NodeID == values[j].NodeID {
			return values[i].Parameter < values[j].Parameter
		}
		return values[i].NodeID < values[j].NodeID
	})
	deduplicated := values[:0]
	for _, value := range values {
		if len(deduplicated) > 0 && deduplicated[len(deduplicated)-1] == value {
			continue
		}
		deduplicated = append(deduplicated, value)
	}
	return append([]MissingReference(nil), deduplicated...)
}
