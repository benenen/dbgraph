package webapi

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/relations"
)

type booleanDTO struct {
	Kind     string       `json:"kind"`
	Operator string       `json:"operator,omitempty"`
	Children []booleanDTO `json:"children,omitempty"`
	Operand  *booleanDTO  `json:"operand,omitempty"`
	Left     *valueDTO    `json:"left,omitempty"`
	Right    *valueDTO    `json:"right,omitempty"`
	Values   []valueDTO   `json:"values,omitempty"`
}

type valueDTO struct {
	Kind      string          `json:"kind"`
	NodeID    string          `json:"nodeId,omitempty"`
	Parameter string          `json:"parameter,omitempty"`
	ValueType string          `json:"valueType,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	Literal   *literalDTO     `json:"literal,omitempty"`
	Cases     []caseDTO       `json:"cases,omitempty"`
	Else      *valueDTO       `json:"else,omitempty"`
}

type literalDTO struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type caseDTO struct {
	When booleanDTO `json:"when"`
	Then valueDTO   `json:"then"`
}

type evidenceDTO struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	File       string `json:"file"`
	Symbol     string `json:"symbol,omitempty"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
}

type proposeRelationDTO struct {
	Type         string        `json:"type"`
	SourceNodeID string        `json:"sourceNodeId"`
	TargetNodeID string        `json:"targetNodeId"`
	Guard        *booleanDTO   `json:"guard,omitempty"`
	Selector     *booleanDTO   `json:"selector,omitempty"`
	Transform    valueDTO      `json:"transform"`
	Confidence   float64       `json:"confidence"`
	Evidence     []evidenceDTO `json:"evidence"`
	Reason       string        `json:"reason"`
}

type proposeRevisionDTO struct {
	SourceNodeID       string        `json:"sourceNodeId"`
	TargetNodeID       string        `json:"targetNodeId"`
	Guard              *booleanDTO   `json:"guard,omitempty"`
	Selector           *booleanDTO   `json:"selector,omitempty"`
	Transform          valueDTO      `json:"transform"`
	Confidence         float64       `json:"confidence"`
	Evidence           []evidenceDTO `json:"evidence"`
	ExpectedRevisionNo int           `json:"expectedRevisionNo"`
	Reason             string        `json:"reason"`
}

type relationStateDTO struct {
	ExpectedRevisionNo int    `json:"expectedRevisionNo"`
	Reason             string `json:"reason"`
}

type relationReviewDTO struct {
	ExpectedRevisionNo int    `json:"expectedRevisionNo"`
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
}

func (h *handler) proposeRelation(response http.ResponseWriter, request *http.Request) {
	principal := currentSession(request).session.Principal
	if !canCreateOrTombstone(principal.Role) {
		writeError(response, http.StatusForbidden, "FORBIDDEN", "permission denied", nil)
		return
	}
	if h.services.Relations == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	var input proposeRelationDTO
	if err := decodeJSON(response, request, maximumJSONRequestBytes, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid relation proposal", nil)
		return
	}
	projectID, err := parseID(request.PathValue("projectID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid project ID", nil)
		return
	}
	command, err := input.toCommand(projectID, principal, currentRequestID(request))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid relation proposal", nil)
		return
	}
	relation, err := h.services.Relations.ProposeCreate(request.Context(), command)
	if err != nil {
		writeDomainError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, mapRelation(relation))
}

func (input proposeRelationDTO) toCommand(projectID int64, principal relations.Principal, requestID string) (relations.ProposeCreate, error) {
	sourceNodeID, err := parseID(input.SourceNodeID)
	if err != nil {
		return relations.ProposeCreate{}, err
	}
	targetNodeID, err := parseID(input.TargetNodeID)
	if err != nil {
		return relations.ProposeCreate{}, err
	}
	relationType, err := parseRelationType(input.Type)
	if err != nil {
		return relations.ProposeCreate{}, err
	}
	guard, err := convertBoolean(input.Guard)
	if err != nil {
		return relations.ProposeCreate{}, err
	}
	selector, err := convertBoolean(input.Selector)
	if err != nil {
		return relations.ProposeCreate{}, err
	}
	transform, err := convertValue(&input.Transform)
	if err != nil {
		return relations.ProposeCreate{}, err
	}
	evidence, err := convertEvidence(input.Evidence)
	if err != nil {
		return relations.ProposeCreate{}, err
	}
	if math.IsNaN(input.Confidence) || math.IsInf(input.Confidence, 0) {
		return relations.ProposeCreate{}, errors.New("invalid confidence")
	}
	return relations.ProposeCreate{
		ProjectID: projectID, Type: relationType, SourceNodeID: sourceNodeID, TargetNodeID: targetNodeID,
		Guard: guard, Selector: selector, Transform: *transform, Confidence: input.Confidence,
		Evidence: evidence, Principal: principal, Reason: input.Reason, RequestID: requestID,
	}, nil
}

func (h *handler) getRelation(response http.ResponseWriter, request *http.Request) {
	projectID, relationID, ok := pathRelationIDs(response, request)
	if !ok {
		return
	}
	if h.services.Relations == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	relation, err := h.services.Relations.Get(request.Context(), relationID)
	if err != nil {
		writeDomainError(response, err)
		return
	}
	if relation.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "NOT_FOUND", "relation not found", nil)
		return
	}
	writeJSON(response, http.StatusOK, mapRelation(relation))
}

func (h *handler) listProposals(response http.ResponseWriter, request *http.Request) {
	if h.services.Relations == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	projectID, err := parseID(request.PathValue("projectID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid project ID", nil)
		return
	}
	responseCountLimit := maximumProposalResponseCount
	repositoryLimit := responseCountLimit + 1
	relationsFound, err := h.services.Relations.ListProposals(request.Context(), projectID, repositoryLimit)
	if err != nil {
		writeDomainError(response, err)
		return
	}
	result, truncated, err := boundedProposalResponse(relationsFound, responseCountLimit)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"relations": result, "truncated": truncated})
}

func (h *handler) proposeRevision(response http.ResponseWriter, request *http.Request) {
	projectID, relationID, ok := h.authorizedRelationMutation(response, request, canRevise)
	if !ok {
		return
	}
	var input proposeRevisionDTO
	if err := decodeJSON(response, request, maximumJSONRequestBytes, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid relation revision", nil)
		return
	}
	content, err := parseRevisionContent(input)
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid relation revision", nil)
		return
	}
	relation, err := h.services.Relations.ProposeRevision(request.Context(), relations.ProposeRevision{
		RelationID: relationID, ExpectedRevisionNo: input.ExpectedRevisionNo,
		SourceNodeID: content.SourceNodeID, TargetNodeID: content.TargetNodeID,
		Guard: content.Guard, Selector: content.Selector, Transform: content.Transform,
		Confidence: input.Confidence, Evidence: content.Evidence,
		Principal: currentSession(request).session.Principal, Reason: input.Reason, RequestID: currentRequestID(request),
	})
	writeRelationResult(response, relation, err, projectID, http.StatusCreated)
}

func (h *handler) proposeTombstone(response http.ResponseWriter, request *http.Request) {
	projectID, relationID, ok := h.authorizedRelationMutation(response, request, canCreateOrTombstone)
	if !ok {
		return
	}
	var input relationStateDTO
	if err := decodeJSON(response, request, maximumJSONRequestBytes, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid tombstone proposal", nil)
		return
	}
	relation, err := h.services.Relations.ProposeTombstone(request.Context(), relations.ProposeTombstone{
		RelationID: relationID, ExpectedRevisionNo: input.ExpectedRevisionNo,
		Principal: currentSession(request).session.Principal, Reason: input.Reason, RequestID: currentRequestID(request),
	})
	writeRelationResult(response, relation, err, projectID, http.StatusCreated)
}

func (h *handler) reviewRelation(response http.ResponseWriter, request *http.Request) {
	projectID, relationID, ok := h.authorizedRelationMutation(response, request, canReview)
	if !ok {
		return
	}
	var input relationReviewDTO
	if err := decodeJSON(response, request, maximumJSONRequestBytes, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid relation review", nil)
		return
	}
	decision, err := parseDecision(input.Decision)
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid review decision", nil)
		return
	}
	relation, err := h.services.Relations.Review(request.Context(), relations.Review{
		RelationID: relationID, ExpectedRevisionNo: input.ExpectedRevisionNo, Decision: decision,
		Principal: currentSession(request).session.Principal, Reason: input.Reason, RequestID: currentRequestID(request),
	})
	writeRelationResult(response, relation, err, projectID, http.StatusOK)
}

func (h *handler) suppressRelation(response http.ResponseWriter, request *http.Request) {
	h.changeRelationState(response, request, false)
}

func (h *handler) restoreRelation(response http.ResponseWriter, request *http.Request) {
	h.changeRelationState(response, request, true)
}

func (h *handler) changeRelationState(response http.ResponseWriter, request *http.Request, restore bool) {
	projectID, relationID, ok := h.authorizedRelationMutation(response, request, canReview)
	if !ok {
		return
	}
	var input relationStateDTO
	if err := decodeJSON(response, request, maximumJSONRequestBytes, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid relation state change", nil)
		return
	}
	command := relations.ChangeState{
		RelationID: relationID, ExpectedRevisionNo: input.ExpectedRevisionNo,
		Principal: currentSession(request).session.Principal, Reason: input.Reason, RequestID: currentRequestID(request),
	}
	var relation relations.Relation
	var err error
	if restore {
		relation, err = h.services.Relations.Restore(request.Context(), command)
	} else {
		relation, err = h.services.Relations.Suppress(request.Context(), command)
	}
	writeRelationResult(response, relation, err, projectID, http.StatusOK)
}

type revisionContent struct {
	SourceNodeID int64
	TargetNodeID int64
	Guard        *conditions.Boolean
	Selector     *conditions.Boolean
	Transform    conditions.Value
	Evidence     []relations.EvidenceInput
}

func parseRevisionContent(input proposeRevisionDTO) (revisionContent, error) {
	sourceID, err := parseID(input.SourceNodeID)
	if err != nil {
		return revisionContent{}, err
	}
	targetID, err := parseID(input.TargetNodeID)
	if err != nil {
		return revisionContent{}, err
	}
	guard, err := convertBoolean(input.Guard)
	if err != nil {
		return revisionContent{}, err
	}
	selector, err := convertBoolean(input.Selector)
	if err != nil {
		return revisionContent{}, err
	}
	transform, err := convertValue(&input.Transform)
	if err != nil {
		return revisionContent{}, err
	}
	evidence, err := convertEvidence(input.Evidence)
	if err != nil {
		return revisionContent{}, err
	}
	return revisionContent{sourceID, targetID, guard, selector, *transform, evidence}, nil
}

func (h *handler) authorizedRelationMutation(
	response http.ResponseWriter,
	request *http.Request,
	allowed func(relations.Role) bool,
) (int64, int64, bool) {
	role := currentSession(request).session.Principal.Role
	if !allowed(role) {
		writeError(response, http.StatusForbidden, "FORBIDDEN", "permission denied", nil)
		return 0, 0, false
	}
	projectID, relationID, ok := pathRelationIDs(response, request)
	if !ok {
		return 0, 0, false
	}
	if h.services.Relations == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return 0, 0, false
	}
	current, err := h.services.Relations.Get(request.Context(), relationID)
	if err != nil || current.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "NOT_FOUND", "relation not found", nil)
		return 0, 0, false
	}
	return projectID, relationID, true
}

func pathRelationIDs(response http.ResponseWriter, request *http.Request) (int64, int64, bool) {
	projectID, err := parseID(request.PathValue("projectID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid project ID", nil)
		return 0, 0, false
	}
	relationID, err := parseID(request.PathValue("relationID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid relation ID", nil)
		return 0, 0, false
	}
	return projectID, relationID, true
}

func writeRelationResult(response http.ResponseWriter, relation relations.Relation, err error, projectID int64, statusCode int) {
	if err != nil {
		writeDomainError(response, err)
		return
	}
	if relation.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "NOT_FOUND", "relation not found", nil)
		return
	}
	writeJSON(response, statusCode, mapRelation(relation))
}

func parseDecision(value string) (relations.Decision, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "APPROVE":
		return relations.DecisionApprove, nil
	case "REJECT":
		return relations.DecisionReject, nil
	default:
		return 0, errors.New("invalid decision")
	}
}

func canReview(role relations.Role) bool {
	return role == relations.RoleReviewer || role == relations.RoleAdmin
}

func convertBoolean(input *booleanDTO) (*conditions.Boolean, error) {
	if input == nil {
		return nil, nil
	}
	result := conditions.Boolean{Kind: conditions.BooleanKind(input.Kind), Operator: conditions.CompareOperator(input.Operator)}
	result.Children = make([]conditions.Boolean, len(input.Children))
	for index := range input.Children {
		child, err := convertBoolean(&input.Children[index])
		if err != nil {
			return nil, err
		}
		result.Children[index] = *child
	}
	var err error
	result.Operand, err = convertBoolean(input.Operand)
	if err != nil {
		return nil, err
	}
	result.Left, err = convertValue(input.Left)
	if err != nil {
		return nil, err
	}
	result.Right, err = convertValue(input.Right)
	if err != nil {
		return nil, err
	}
	result.Values = make([]conditions.Value, len(input.Values))
	for index := range input.Values {
		value, err := convertValue(&input.Values[index])
		if err != nil {
			return nil, err
		}
		result.Values[index] = *value
	}
	return &result, nil
}

func convertValue(input *valueDTO) (*conditions.Value, error) {
	if input == nil {
		return nil, nil
	}
	result := conditions.Value{Kind: conditions.ValueKind(input.Kind), Parameter: input.Parameter}
	if input.NodeID != "" {
		nodeID, err := parseID(input.NodeID)
		if err != nil {
			return nil, err
		}
		result.NodeID = nodeID
	}
	flatLiteral := input.ValueType != "" || input.Value != nil
	if input.Literal != nil && flatLiteral {
		return nil, errors.New("conflicting literal wire shapes")
	}
	if flatLiteral && (input.ValueType == "" || input.Value == nil) {
		return nil, errors.New("literal valueType and value are required")
	}
	if input.Literal != nil || flatLiteral {
		literalType := input.ValueType
		literalValue := input.Value
		if input.Literal != nil {
			literalType = input.Literal.Type
			literalValue = input.Literal.Value
		}
		encoded, err := normalizeLiteral(literalType, literalValue)
		if err != nil {
			return nil, err
		}
		result.Literal = &conditions.Literal{Type: conditions.LiteralType(literalType), Value: encoded}
	}
	result.Cases = make([]conditions.Case, len(input.Cases))
	for index := range input.Cases {
		when, err := convertBoolean(&input.Cases[index].When)
		if err != nil {
			return nil, err
		}
		then, err := convertValue(&input.Cases[index].Then)
		if err != nil {
			return nil, err
		}
		result.Cases[index] = conditions.Case{When: *when, Then: *then}
	}
	var err error
	result.Else, err = convertValue(input.Else)
	return &result, err
}

func normalizeLiteral(literalType string, raw json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(raw) {
		return nil, errors.New("invalid literal")
	}
	switch conditions.LiteralType(literalType) {
	case conditions.LiteralInteger:
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			if _, ok := new(big.Int).SetString(value, 10); !ok {
				return nil, errors.New("invalid integer literal")
			}
			return json.RawMessage(value), nil
		}
	case conditions.LiteralDecimal:
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			if _, ok := new(big.Rat).SetString(value); !ok {
				return nil, errors.New("invalid decimal literal")
			}
			return json.RawMessage(value), nil
		}
	case conditions.LiteralString:
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return append(json.RawMessage(nil), raw...), nil
		}
	case conditions.LiteralBoolean:
		var value bool
		if json.Unmarshal(raw, &value) == nil {
			return append(json.RawMessage(nil), raw...), nil
		}
	case conditions.LiteralNull:
		if string(raw) == "null" {
			return json.RawMessage(`null`), nil
		}
	}
	return nil, errors.New("literal value does not match its type")
}

func convertEvidence(input []evidenceDTO) ([]relations.EvidenceInput, error) {
	result := make([]relations.EvidenceInput, len(input))
	for index, item := range input {
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

func parseID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid ID")
	}
	return id, nil
}

func parseRelationType(value string) (relations.Type, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "CONDITIONAL_VALUE_COPY":
		return relations.TypeConditionalValueCopy, nil
	default:
		return 0, errors.New("invalid relation type")
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
		return 0, errors.New("invalid evidence kind")
	}
}

func mapRelation(relation relations.Relation) map[string]any {
	return map[string]any{
		"id": strconv.FormatInt(relation.ID, 10), "projectId": strconv.FormatInt(relation.ProjectID, 10),
		"type": relationTypeName(relation.Type), "latestRevisionNo": relation.LatestRevisionNo,
		"status": relationDisplayStatus(relation), "effective": relation.Effective,
		"active": mapRevision(relation.Active), "proposed": mapRevision(relation.Proposed),
		"createdAt": relation.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapRevision(revision *relations.Revision) any {
	if revision == nil {
		return nil
	}
	return map[string]any{
		"id": strconv.FormatInt(revision.ID, 10), "relationId": strconv.FormatInt(revision.RelationID, 10),
		"revisionNo": revision.RevisionNo, "kind": proposalKindName(revision.Kind),
		"sourceNodeId": strconv.FormatInt(revision.SourceNodeID, 10),
		"targetNodeId": strconv.FormatInt(revision.TargetNodeID, 10),
		"guard":        mapBoolean(revision.Guard), "selector": mapBoolean(revision.Selector),
		"transform": mapValue(revision.Transform), "confidence": revision.Confidence,
		"evidence": mapEvidence(revision.Evidence), "actor": revision.Actor, "reason": revision.Reason,
		"requestId": revision.RequestID, "expectedRevisionNo": revision.ExpectedRevisionNo,
		"createdAt": revision.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapBoolean(value *conditions.Boolean) any {
	if value == nil {
		return nil
	}
	mapped := map[string]any{"kind": value.Kind}
	if value.Operator != "" {
		mapped["operator"] = value.Operator
	}
	if len(value.Children) > 0 {
		children := make([]any, len(value.Children))
		for index := range value.Children {
			children[index] = mapBoolean(&value.Children[index])
		}
		mapped["children"] = children
	}
	if value.Operand != nil {
		mapped["operand"] = mapBoolean(value.Operand)
	}
	if value.Left != nil {
		mapped["left"] = mapValue(*value.Left)
	}
	if value.Right != nil {
		mapped["right"] = mapValue(*value.Right)
	}
	if len(value.Values) > 0 {
		values := make([]any, len(value.Values))
		for index := range value.Values {
			values[index] = mapValue(value.Values[index])
		}
		mapped["values"] = values
	}
	return mapped
}

func mapValue(value conditions.Value) map[string]any {
	mapped := map[string]any{"kind": value.Kind}
	if value.NodeID > 0 {
		mapped["nodeId"] = strconv.FormatInt(value.NodeID, 10)
	}
	if value.Parameter != "" {
		mapped["parameter"] = value.Parameter
	}
	if value.Literal != nil {
		mapped["valueType"] = value.Literal.Type
		mapped["value"] = mapLiteralValue(*value.Literal)
	}
	if len(value.Cases) > 0 {
		cases := make([]map[string]any, len(value.Cases))
		for index, item := range value.Cases {
			cases[index] = map[string]any{"when": mapBoolean(&item.When), "then": mapValue(item.Then)}
		}
		mapped["cases"] = cases
	}
	if value.Else != nil {
		mapped["else"] = mapValue(*value.Else)
	}
	return mapped
}

func mapLiteralValue(literal conditions.Literal) any {
	if literal.Type == conditions.LiteralInteger || literal.Type == conditions.LiteralDecimal {
		return string(literal.Value)
	}
	return append(json.RawMessage(nil), literal.Value...)
}

func mapEvidence(evidence []relations.EvidenceInput) []map[string]any {
	mapped := make([]map[string]any, len(evidence))
	for index, item := range evidence {
		mapped[index] = map[string]any{
			"kind": evidenceKindName(item.Kind), "repository": item.Repository,
			"commit": item.Commit, "file": item.File, "symbol": item.Symbol,
			"startLine": item.StartLine, "endLine": item.EndLine,
		}
		if item.DataSourceID > 0 {
			mapped[index]["dataSourceId"] = strconv.FormatInt(item.DataSourceID, 10)
		}
		if item.ConstraintSchema != "" {
			mapped[index]["constraintSchema"] = item.ConstraintSchema
		}
		if item.ConstraintName != "" {
			mapped[index]["constraintName"] = item.ConstraintName
		}
		if item.ScanRunID > 0 {
			mapped[index]["scanRunId"] = strconv.FormatInt(item.ScanRunID, 10)
		}
	}
	return mapped
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

func relationTypeName(value relations.Type) string {
	switch value {
	case relations.TypeDeclaredForeignKey:
		return "DECLARED_FOREIGN_KEY"
	case relations.TypeConditionalValueCopy:
		return "CONDITIONAL_VALUE_COPY"
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

func canCreateOrTombstone(role relations.Role) bool {
	return role == relations.RoleEditor || role == relations.RoleAdmin
}

func canRevise(role relations.Role) bool {
	return canCreateOrTombstone(role) || role == relations.RoleReviewer
}

func writeDomainError(response http.ResponseWriter, err error) {
	var conflict *relations.RevisionConflictError
	switch {
	case errors.Is(err, relations.ErrForbidden):
		writeError(response, http.StatusForbidden, "FORBIDDEN", "permission denied", nil)
	case errors.As(err, &conflict):
		details := map[string]any{"currentRevisionNo": conflict.CurrentRevisionNo}
		if conflict.Current != nil {
			details["currentRelation"] = mapRelation(*conflict.Current)
		}
		writeError(response, http.StatusConflict, "REVISION_CONFLICT", "relation revision changed", details)
	case errors.Is(err, relations.ErrRelationNotFound):
		writeError(response, http.StatusNotFound, "NOT_FOUND", "relation not found", nil)
	case errors.Is(err, relations.ErrPendingProposal), errors.Is(err, relations.ErrDuplicateRelation):
		writeError(response, http.StatusConflict, "RELATION_CONFLICT", "relation command conflicts with current state", nil)
	case errors.Is(err, relations.ErrInvalidCommand), errors.Is(err, relations.ErrInvalidTransition):
		writeError(response, http.StatusUnprocessableEntity, "INVALID_RELATION", "relation command was rejected", nil)
	default:
		writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
	}
}
