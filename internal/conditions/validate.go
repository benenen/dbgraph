package conditions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

var ErrInvalidExpression = errors.New("invalid condition expression")

type validator struct {
	limits Limits
	nodes  int
}

func ValidateBoolean(expression Boolean, limits Limits) error {
	if err := validateLimits(limits); err != nil {
		return err
	}
	v := &validator{limits: limits}
	return v.boolean(expression, 1)
}

func ValidateValue(expression Value, limits Limits) error {
	if err := validateLimits(limits); err != nil {
		return err
	}
	v := &validator{limits: limits}
	return v.value(expression, 1)
}

func validateLimits(limits Limits) error {
	if limits.MaxDepth <= 0 || limits.MaxNodes <= 0 || limits.MaxStringLength <= 0 || limits.MaxListItems <= 0 {
		return fmt.Errorf("%w: limits must be positive", ErrInvalidExpression)
	}
	return nil
}

func (v *validator) enter(depth int) error {
	v.nodes++
	if depth > v.limits.MaxDepth {
		return fmt.Errorf("%w: maximum depth exceeded", ErrInvalidExpression)
	}
	if v.nodes > v.limits.MaxNodes {
		return fmt.Errorf("%w: maximum node count exceeded", ErrInvalidExpression)
	}
	return nil
}

func (v *validator) boolean(expression Boolean, depth int) error {
	if err := v.enter(depth); err != nil {
		return err
	}

	switch expression.Kind {
	case BooleanAnd, BooleanOr:
		if expression.Operator != "" || expression.Operand != nil || expression.Left != nil ||
			expression.Right != nil || len(expression.Values) != 0 {
			return fmt.Errorf("%w: %s contains unrelated fields", ErrInvalidExpression, expression.Kind)
		}
		if len(expression.Children) < 2 || len(expression.Children) > v.limits.MaxListItems {
			return fmt.Errorf("%w: %s requires bounded children", ErrInvalidExpression, expression.Kind)
		}
		for _, child := range expression.Children {
			if err := v.boolean(child, depth+1); err != nil {
				return err
			}
		}
	case BooleanNot:
		if expression.Operand == nil || expression.Operator != "" || len(expression.Children) != 0 ||
			expression.Left != nil || expression.Right != nil || len(expression.Values) != 0 {
			return fmt.Errorf("%w: not requires an operand", ErrInvalidExpression)
		}
		return v.boolean(*expression.Operand, depth+1)
	case BooleanCompare:
		if !validCompareOperator(expression.Operator) || expression.Left == nil || expression.Right == nil ||
			len(expression.Children) != 0 || expression.Operand != nil || len(expression.Values) != 0 {
			return fmt.Errorf("%w: compare requires operator, left, and right", ErrInvalidExpression)
		}
		if err := v.value(*expression.Left, depth+1); err != nil {
			return err
		}
		return v.value(*expression.Right, depth+1)
	case BooleanIn, BooleanNotIn:
		if expression.Left == nil || len(expression.Values) == 0 || len(expression.Values) > v.limits.MaxListItems ||
			expression.Operator != "" || len(expression.Children) != 0 || expression.Operand != nil || expression.Right != nil {
			return fmt.Errorf("%w: %s requires left and bounded values", ErrInvalidExpression, expression.Kind)
		}
		if err := v.value(*expression.Left, depth+1); err != nil {
			return err
		}
		for _, item := range expression.Values {
			if err := v.value(item, depth+1); err != nil {
				return err
			}
		}
	case BooleanIsNull, BooleanIsNotNull:
		if expression.Left == nil || expression.Operator != "" || len(expression.Children) != 0 ||
			expression.Operand != nil || expression.Right != nil || len(expression.Values) != 0 {
			return fmt.Errorf("%w: %s requires a value", ErrInvalidExpression, expression.Kind)
		}
		return v.value(*expression.Left, depth+1)
	default:
		return fmt.Errorf("%w: unknown boolean kind %q", ErrInvalidExpression, expression.Kind)
	}
	return nil
}

func (v *validator) value(expression Value, depth int) error {
	if err := v.enter(depth); err != nil {
		return err
	}

	switch expression.Kind {
	case ValueColumn, ValueColumnCopy:
		if expression.NodeID <= 0 || expression.Parameter != "" || expression.Literal != nil ||
			len(expression.Cases) != 0 || expression.Else != nil {
			return fmt.Errorf("%w: column node ID must be positive", ErrInvalidExpression)
		}
	case ValueParameter:
		name := strings.TrimSpace(expression.Parameter)
		if name == "" || len(name) > v.limits.MaxStringLength || expression.NodeID != 0 ||
			expression.Literal != nil || len(expression.Cases) != 0 || expression.Else != nil {
			return fmt.Errorf("%w: invalid parameter", ErrInvalidExpression)
		}
	case ValueLiteral:
		if expression.Literal == nil || expression.NodeID != 0 || expression.Parameter != "" ||
			len(expression.Cases) != 0 || expression.Else != nil {
			return fmt.Errorf("%w: literal payload is required", ErrInvalidExpression)
		}
		if err := validateLiteral(*expression.Literal, v.limits); err != nil {
			return err
		}
	case ValueCase:
		if len(expression.Cases) == 0 || len(expression.Cases) > v.limits.MaxListItems || expression.Else == nil ||
			expression.NodeID != 0 || expression.Parameter != "" || expression.Literal != nil {
			return fmt.Errorf("%w: case requires bounded branches and else", ErrInvalidExpression)
		}
		for _, branch := range expression.Cases {
			if err := v.boolean(branch.When, depth+1); err != nil {
				return err
			}
			if err := v.value(branch.Then, depth+1); err != nil {
				return err
			}
		}
		return v.value(*expression.Else, depth+1)
	default:
		return fmt.Errorf("%w: unknown value kind %q", ErrInvalidExpression, expression.Kind)
	}
	return nil
}

func validCompareOperator(operator CompareOperator) bool {
	switch operator {
	case CompareEqual,
		CompareNotEqual,
		CompareGreaterThan,
		CompareGreaterThanOrEqual,
		CompareLessThan,
		CompareLessThanOrEqual:
		return true
	default:
		return false
	}
}

func validateLiteral(literal Literal, limits Limits) error {
	if !json.Valid(literal.Value) {
		return fmt.Errorf("%w: literal is not valid JSON", ErrInvalidExpression)
	}

	switch literal.Type {
	case LiteralInteger:
		text := string(literal.Value)
		if strings.ContainsAny(text, ".eE") {
			return fmt.Errorf("%w: integer literal is not integral", ErrInvalidExpression)
		}
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return fmt.Errorf("%w: integer literal is out of range", ErrInvalidExpression)
		}
	case LiteralDecimal:
		decoder := json.NewDecoder(bytes.NewReader(literal.Value))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("%w: invalid decimal literal", ErrInvalidExpression)
		}
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("%w: decimal literal is not a number", ErrInvalidExpression)
		}
		if _, ok := new(big.Rat).SetString(number.String()); !ok {
			return fmt.Errorf("%w: invalid decimal literal", ErrInvalidExpression)
		}
	case LiteralString:
		var value string
		if err := json.Unmarshal(literal.Value, &value); err != nil || len(value) > limits.MaxStringLength {
			return fmt.Errorf("%w: invalid string literal", ErrInvalidExpression)
		}
	case LiteralBoolean:
		var value bool
		if err := json.Unmarshal(literal.Value, &value); err != nil {
			return fmt.Errorf("%w: invalid boolean literal", ErrInvalidExpression)
		}
	case LiteralNull:
		if string(literal.Value) != "null" {
			return fmt.Errorf("%w: invalid null literal", ErrInvalidExpression)
		}
	default:
		return fmt.Errorf("%w: unknown literal type %q", ErrInvalidExpression, literal.Type)
	}
	return nil
}
