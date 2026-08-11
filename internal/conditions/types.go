package conditions

import (
	"encoding/json"
	"errors"
)

type BooleanKind string

const (
	BooleanAnd       BooleanKind = "and"
	BooleanOr        BooleanKind = "or"
	BooleanNot       BooleanKind = "not"
	BooleanCompare   BooleanKind = "compare"
	BooleanIn        BooleanKind = "in"
	BooleanNotIn     BooleanKind = "not_in"
	BooleanIsNull    BooleanKind = "is_null"
	BooleanIsNotNull BooleanKind = "is_not_null"
)

type CompareOperator string

const (
	CompareEqual              CompareOperator = "eq"
	CompareNotEqual           CompareOperator = "ne"
	CompareGreaterThan        CompareOperator = "gt"
	CompareGreaterThanOrEqual CompareOperator = "gte"
	CompareLessThan           CompareOperator = "lt"
	CompareLessThanOrEqual    CompareOperator = "lte"
)

type ValueKind string

const (
	ValueColumn     ValueKind = "column"
	ValueLiteral    ValueKind = "literal"
	ValueParameter  ValueKind = "parameter"
	ValueColumnCopy ValueKind = "column_copy"
	ValueCase       ValueKind = "case"
)

type LiteralType string

const (
	LiteralInteger LiteralType = "integer"
	LiteralDecimal LiteralType = "decimal"
	LiteralString  LiteralType = "string"
	LiteralBoolean LiteralType = "boolean"
	LiteralNull    LiteralType = "null"
)

type Boolean struct {
	Kind     BooleanKind     `json:"kind"`
	Operator CompareOperator `json:"operator,omitempty"`
	Children []Boolean       `json:"children,omitempty"`
	Operand  *Boolean        `json:"operand,omitempty"`
	Left     *Value          `json:"left,omitempty"`
	Right    *Value          `json:"right,omitempty"`
	Values   []Value         `json:"values,omitempty"`
}

type Value struct {
	Kind      ValueKind `json:"kind"`
	NodeID    int64     `json:"nodeId,omitempty"`
	Parameter string    `json:"parameter,omitempty"`
	Literal   *Literal  `json:"literal,omitempty"`
	Cases     []Case    `json:"cases,omitempty"`
	Else      *Value    `json:"else,omitempty"`
}

type Literal struct {
	Type  LiteralType     `json:"type"`
	Value json.RawMessage `json:"value"`
}

type Case struct {
	When Boolean `json:"when"`
	Then Value   `json:"then"`
}

type valueJSON struct {
	Kind      ValueKind       `json:"kind"`
	NodeID    int64           `json:"nodeId,omitempty"`
	Parameter string          `json:"parameter,omitempty"`
	ValueType LiteralType     `json:"valueType,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	Literal   *Literal        `json:"literal,omitempty"`
	Cases     []Case          `json:"cases,omitempty"`
	Else      *Value          `json:"else,omitempty"`
}

func (v Value) MarshalJSON() ([]byte, error) {
	wire := valueJSON{
		Kind: v.Kind, NodeID: v.NodeID, Parameter: v.Parameter,
		Cases: append([]Case(nil), v.Cases...), Else: v.Else,
	}
	if v.Literal != nil {
		wire.ValueType = v.Literal.Type
		wire.Value = append(json.RawMessage(nil), v.Literal.Value...)
	}
	return json.Marshal(wire)
}

func (v *Value) UnmarshalJSON(data []byte) error {
	var wire valueJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Literal != nil && (wire.ValueType != "" || wire.Value != nil) {
		return errors.New("literal value uses conflicting wire shapes")
	}
	if (wire.ValueType == "") != (wire.Value == nil) {
		return errors.New("literal valueType and value must be provided together")
	}
	result := Value{
		Kind: wire.Kind, NodeID: wire.NodeID, Parameter: wire.Parameter,
		Cases: append([]Case(nil), wire.Cases...), Else: wire.Else,
	}
	if wire.Literal != nil {
		result.Literal = &Literal{
			Type: wire.Literal.Type, Value: append(json.RawMessage(nil), wire.Literal.Value...),
		}
	} else if wire.ValueType != "" {
		result.Literal = &Literal{Type: wire.ValueType, Value: append(json.RawMessage(nil), wire.Value...)}
	}
	*v = result
	return nil
}

type Limits struct {
	MaxDepth        int
	MaxNodes        int
	MaxStringLength int
	MaxListItems    int
}

func DefaultLimits() Limits {
	return Limits{
		MaxDepth:        16,
		MaxNodes:        256,
		MaxStringLength: 2000,
		MaxListItems:    100,
	}
}
