package mcpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/relations"
)

var errInvalidToolInput = errors.New("invalid dbgraph tool input")

type booleanInput struct {
	Kind     string         `json:"kind"`
	Operator string         `json:"operator,omitempty"`
	Children []booleanInput `json:"children,omitempty"`
	Operand  *booleanInput  `json:"operand,omitempty"`
	Left     *valueInput    `json:"left,omitempty"`
	Right    *valueInput    `json:"right,omitempty"`
	Values   []valueInput   `json:"values,omitempty"`
}

type valueInput struct {
	Kind      string          `json:"kind"`
	NodeID    string          `json:"nodeId,omitempty"`
	Parameter string          `json:"parameter,omitempty"`
	ValueType string          `json:"valueType,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	Literal   *literalInput   `json:"literal,omitempty"`
	Cases     []caseInput     `json:"cases,omitempty"`
	Else      *valueInput     `json:"else,omitempty"`
}

type literalInput struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type caseInput struct {
	When booleanInput `json:"when"`
	Then valueInput   `json:"then"`
}

type evidenceInput struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	File       string `json:"file"`
	Symbol     string `json:"symbol,omitempty"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
}

type revisionOutput struct {
	ID                 string           `json:"id"`
	RelationID         string           `json:"relationId"`
	RevisionNo         int              `json:"revisionNo"`
	Kind               string           `json:"kind"`
	SourceNodeID       string           `json:"sourceNodeId"`
	TargetNodeID       string           `json:"targetNodeId"`
	Guard              *booleanInput    `json:"guard,omitempty"`
	Selector           *booleanInput    `json:"selector,omitempty"`
	Transform          valueInput       `json:"transform"`
	Confidence         float64          `json:"confidence"`
	Evidence           []evidenceOutput `json:"evidence"`
	Actor              string           `json:"actor"`
	Origin             string           `json:"origin"`
	Reason             string           `json:"reason"`
	RequestID          string           `json:"requestId"`
	ExpectedRevisionNo *int             `json:"expectedRevisionNo,omitempty"`
	CreatedAt          string           `json:"createdAt"`
}

type evidenceOutput struct {
	Kind             string `json:"kind"`
	Repository       string `json:"repository,omitempty"`
	Commit           string `json:"commit,omitempty"`
	File             string `json:"file,omitempty"`
	Symbol           string `json:"symbol,omitempty"`
	StartLine        int    `json:"startLine,omitempty"`
	EndLine          int    `json:"endLine,omitempty"`
	DataSourceID     string `json:"dataSourceId,omitempty"`
	ConstraintSchema string `json:"constraintSchema,omitempty"`
	ConstraintName   string `json:"constraintName,omitempty"`
	ScanRunID        string `json:"scanRunId,omitempty"`
}

type relationOutput struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	LatestRevisionNo int             `json:"latestRevisionNo"`
	Status           string          `json:"status"`
	Active           *revisionOutput `json:"active,omitempty"`
	Proposed         *revisionOutput `json:"proposed,omitempty"`
	Effective        bool            `json:"effective"`
	CreatedAt        string          `json:"createdAt"`
}

func convertBoolean(input *booleanInput) (*conditions.Boolean, error) {
	nodes := 0
	return convertBooleanBudget(input, 1, &nodes)
}

func convertBooleanBudget(input *booleanInput, depth int, nodes *int) (*conditions.Boolean, error) {
	if input == nil {
		return nil, nil
	}
	if depth > conditions.DefaultLimits().MaxDepth || *nodes >= conditions.DefaultLimits().MaxNodes {
		return nil, fmt.Errorf("%w: condition exceeds AST limits", errInvalidToolInput)
	}
	*nodes++
	result := conditions.Boolean{
		Kind:     conditions.BooleanKind(input.Kind),
		Operator: conditions.CompareOperator(input.Operator),
	}
	result.Children = make([]conditions.Boolean, len(input.Children))
	for index := range input.Children {
		converted, err := convertBooleanBudget(&input.Children[index], depth+1, nodes)
		if err != nil {
			return nil, err
		}
		result.Children[index] = *converted
	}
	var err error
	result.Operand, err = convertBooleanBudget(input.Operand, depth+1, nodes)
	if err != nil {
		return nil, err
	}
	result.Left, err = convertValueBudget(input.Left, depth+1, nodes)
	if err != nil {
		return nil, err
	}
	result.Right, err = convertValueBudget(input.Right, depth+1, nodes)
	if err != nil {
		return nil, err
	}
	result.Values = make([]conditions.Value, len(input.Values))
	for index := range input.Values {
		converted, err := convertValueBudget(&input.Values[index], depth+1, nodes)
		if err != nil {
			return nil, err
		}
		result.Values[index] = *converted
	}
	return &result, nil
}

func convertValue(input *valueInput) (*conditions.Value, error) {
	nodes := 0
	return convertValueBudget(input, 1, &nodes)
}

func convertValueBudget(input *valueInput, depth int, nodes *int) (*conditions.Value, error) {
	if input == nil {
		return nil, nil
	}
	if depth > conditions.DefaultLimits().MaxDepth || *nodes >= conditions.DefaultLimits().MaxNodes {
		return nil, fmt.Errorf("%w: condition exceeds AST limits", errInvalidToolInput)
	}
	*nodes++
	result := conditions.Value{Kind: conditions.ValueKind(input.Kind), Parameter: input.Parameter}
	if input.NodeID != "" {
		id, err := parseID(input.NodeID)
		if err != nil {
			return nil, err
		}
		result.NodeID = id
	}
	flatLiteral := input.ValueType != "" || input.Value != nil
	if input.Literal != nil && flatLiteral {
		return nil, errInvalidToolInput
	}
	if flatLiteral && (input.ValueType == "" || input.Value == nil) {
		return nil, errInvalidToolInput
	}
	if input.Literal != nil || flatLiteral {
		literalType := input.ValueType
		literalValue := input.Value
		if input.Literal != nil {
			literalType = input.Literal.Type
			literalValue = input.Literal.Value
		}
		value := append(json.RawMessage(nil), literalValue...)
		if !json.Valid(value) {
			return nil, errInvalidToolInput
		}
		result.Literal = &conditions.Literal{Type: conditions.LiteralType(literalType), Value: value}
	}
	result.Cases = make([]conditions.Case, len(input.Cases))
	for index := range input.Cases {
		when, err := convertBooleanBudget(&input.Cases[index].When, depth+1, nodes)
		if err != nil {
			return nil, err
		}
		then, err := convertValueBudget(&input.Cases[index].Then, depth+1, nodes)
		if err != nil {
			return nil, err
		}
		result.Cases[index] = conditions.Case{When: *when, Then: *then}
	}
	var err error
	result.Else, err = convertValueBudget(input.Else, depth+1, nodes)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func convertEvidence(items []evidenceInput) ([]relations.EvidenceInput, error) {
	result := make([]relations.EvidenceInput, len(items))
	for index, item := range items {
		kind, err := parseEvidenceKind(item.Kind)
		if err != nil {
			return nil, err
		}
		result[index] = relations.EvidenceInput{
			Kind: kind, Repository: item.Repository, Commit: item.Commit, File: item.File,
			Symbol: item.Symbol, StartLine: item.StartLine, EndLine: item.EndLine,
		}
	}
	return result, nil
}

func parseRelationType(value string) (relations.Type, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "CONDITIONAL_VALUE_COPY":
		return relations.TypeConditionalValueCopy, nil
	default:
		return 0, fmt.Errorf("%w: unknown relation type", errInvalidToolInput)
	}
}

func parseEvidenceKind(value string) (relations.EvidenceKind, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "CODE":
		return relations.EvidenceCode, nil
	case "SQL_MAPPING":
		return relations.EvidenceSQLMapping, nil
	case "MANUAL":
		return relations.EvidenceManual, nil
	default:
		return 0, fmt.Errorf("%w: unknown evidence kind", errInvalidToolInput)
	}
}

func mapRelation(relation relations.Relation) relationOutput {
	return relationOutput{
		ID: formatID(relation.ID), Type: relationTypeName(relation.Type),
		LatestRevisionNo: relation.LatestRevisionNo, Status: relationDisplayStatus(relation),
		Active: mapRevision(relation.Active), Proposed: mapRevision(relation.Proposed),
		Effective: relation.Effective, CreatedAt: relation.CreatedAt.UTC().Format(timeFormat),
	}
}

func mapRevision(revision *relations.Revision) *revisionOutput {
	if revision == nil {
		return nil
	}
	evidence := make([]evidenceOutput, len(revision.Evidence))
	for index, item := range revision.Evidence {
		evidence[index] = evidenceOutput{
			Kind: evidenceKindName(item.Kind), Repository: item.Repository, Commit: item.Commit,
			File: item.File, Symbol: item.Symbol, StartLine: item.StartLine, EndLine: item.EndLine,
			DataSourceID: optionalFormatID(item.DataSourceID), ConstraintSchema: item.ConstraintSchema,
			ConstraintName: item.ConstraintName, ScanRunID: optionalFormatID(item.ScanRunID),
		}
	}
	return &revisionOutput{
		ID: formatID(revision.ID), RelationID: formatID(revision.RelationID), RevisionNo: revision.RevisionNo,
		Kind: proposalKindName(revision.Kind), SourceNodeID: formatID(revision.SourceNodeID),
		TargetNodeID: formatID(revision.TargetNodeID), Guard: mapBoolean(revision.Guard), Selector: mapBoolean(revision.Selector),
		Transform: mapValue(revision.Transform), Confidence: revision.Confidence, Evidence: evidence,
		Actor: revision.Actor, Origin: originName(revision.Origin), Reason: revision.Reason,
		RequestID: revision.RequestID, ExpectedRevisionNo: revision.ExpectedRevisionNo,
		CreatedAt: revision.CreatedAt.UTC().Format(timeFormat),
	}
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func relationTypeName(value relations.Type) string {
	switch value {
	case relations.TypeConditionalValueCopy:
		return "CONDITIONAL_VALUE_COPY"
	case relations.TypeDeclaredForeignKey:
		return "DECLARED_FOREIGN_KEY"
	default:
		return "UNKNOWN"
	}
}

func relationStatusName(value relations.Status) string {
	switch value {
	case relations.StatusPending:
		return "PROPOSED"
	case relations.StatusApproved:
		return "APPROVED"
	case relations.StatusSuppressed:
		return "SUPPRESSED"
	case relations.StatusTombstoned:
		return "TOMBSTONED"
	case relations.StatusStale:
		return "STALE"
	default:
		return "UNKNOWN"
	}
}

func relationDisplayStatus(relation relations.Relation) string {
	if relation.Status == relations.StatusPending && relation.LatestRevisionNo > 0 &&
		relation.Active == nil && relation.Proposed == nil {
		return "REJECTED"
	}
	return relationStatusName(relation.Status)
}

func proposalKindName(value relations.ProposalKind) string {
	switch value {
	case relations.ProposalTombstone:
		return "TOMBSTONE"
	case relations.ProposalStale:
		return "STALE"
	default:
		return "CONTENT"
	}
}

func evidenceKindName(value relations.EvidenceKind) string {
	switch value {
	case relations.EvidenceCode:
		return "CODE"
	case relations.EvidenceSQLMapping:
		return "SQL_MAPPING"
	case relations.EvidenceManual:
		return "MANUAL"
	case relations.EvidenceDatabaseConstraint:
		return "DATABASE_CONSTRAINT"
	default:
		return "UNKNOWN"
	}
}

func optionalFormatID(value int64) string {
	if value <= 0 {
		return ""
	}
	return formatID(value)
}

func originName(value audit.Origin) string {
	switch value {
	case 1:
		return "AGENT"
	case 2:
		return "WEB"
	case 3:
		return "SYSTEM"
	default:
		return "UNKNOWN"
	}
}

func mapBoolean(value *conditions.Boolean) *booleanInput {
	if value == nil {
		return nil
	}
	result := &booleanInput{Kind: string(value.Kind), Operator: string(value.Operator)}
	result.Children = make([]booleanInput, len(value.Children))
	for index := range value.Children {
		result.Children[index] = *mapBoolean(&value.Children[index])
	}
	result.Operand = mapBoolean(value.Operand)
	if value.Left != nil {
		mapped := mapValue(*value.Left)
		result.Left = &mapped
	}
	if value.Right != nil {
		mapped := mapValue(*value.Right)
		result.Right = &mapped
	}
	result.Values = make([]valueInput, len(value.Values))
	for index := range value.Values {
		result.Values[index] = mapValue(value.Values[index])
	}
	return result
}

func mapValue(value conditions.Value) valueInput {
	result := valueInput{Kind: string(value.Kind), Parameter: value.Parameter}
	if value.NodeID > 0 {
		result.NodeID = formatID(value.NodeID)
	}
	if value.Literal != nil {
		result.ValueType = string(value.Literal.Type)
		result.Value = append(json.RawMessage(nil), value.Literal.Value...)
	}
	result.Cases = make([]caseInput, len(value.Cases))
	for index := range value.Cases {
		result.Cases[index] = caseInput{When: *mapBoolean(&value.Cases[index].When), Then: mapValue(value.Cases[index].Then)}
	}
	if value.Else != nil {
		mapped := mapValue(*value.Else)
		result.Else = &mapped
	}
	return result
}
