package mcpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type beginRelationInitInput struct {
	ProjectID    string          `json:"projectId"`
	RepositoryID string          `json:"repositoryId"`
	Mode         string          `json:"mode"`
	SourceCommit string          `json:"sourceCommit"`
	Scope        json.RawMessage `json:"scope,omitempty"`
	RequestID    string          `json:"requestId"`
}

type relationInitIDInput struct {
	SessionID string `json:"sessionId"`
}

type initSessionOutput struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"projectId"`
	RepositoryID string          `json:"repositoryId"`
	Mode         string          `json:"mode"`
	SourceCommit string          `json:"sourceCommit"`
	Scope        json.RawMessage `json:"scope"`
	Status       string          `json:"status"`
	Actor        string          `json:"actor"`
	RequestID    string          `json:"requestId"`
	CreatedAt    string          `json:"createdAt"`
	CompletedAt  string          `json:"completedAt,omitempty"`
}

type batchProposalInput struct {
	Type string `json:"type"`
	relationContentInput
	Reason string `json:"reason"`
}

type unresolvedInput struct {
	Type     string          `json:"type"`
	Summary  string          `json:"summary"`
	Evidence json.RawMessage `json:"evidence"`
}

type proposeRelationsInput struct {
	SessionID      string               `json:"sessionId"`
	BatchNo        int                  `json:"batchNo"`
	IdempotencyKey string               `json:"idempotencyKey"`
	Proposals      []batchProposalInput `json:"proposals,omitempty"`
	Unresolved     []unresolvedInput    `json:"unresolved,omitempty"`
	RequestID      string               `json:"requestId"`
}

type batchItemOutput struct {
	RelationID string `json:"relationId"`
	Status     string `json:"status"`
}

type batchResultOutput struct {
	BatchID       string            `json:"batchId"`
	SessionID     string            `json:"sessionId"`
	BatchNo       int               `json:"batchNo"`
	Items         []batchItemOutput `json:"items"`
	UnresolvedIDs []string          `json:"unresolvedIds"`
	AcceptedAt    string            `json:"acceptedAt"`
}

type completeRelationInitInput struct {
	SessionID          string `json:"sessionId"`
	ExpectedBatchCount int    `json:"expectedBatchCount"`
	Reason             string `json:"reason"`
	RequestID          string `json:"requestId"`
}

type completionOutput struct {
	Session              initSessionOutput `json:"session"`
	CandidateRelationIDs []string          `json:"candidateRelationIds"`
}

type listUnresolvedInput struct {
	ProjectID string `json:"projectId"`
	Limit     int    `json:"limit,omitempty"`
}

type unresolvedOutput struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"projectId"`
	RepositoryID string          `json:"repositoryId"`
	SessionID    string          `json:"sessionId"`
	BatchID      string          `json:"batchId"`
	Fingerprint  string          `json:"fingerprint"`
	Type         string          `json:"type"`
	Summary      string          `json:"summary"`
	Evidence     json.RawMessage `json:"evidence"`
	Status       string          `json:"status"`
	Actor        string          `json:"actor"`
	CreatedAt    string          `json:"createdAt"`
}

type unresolvedListOutput struct {
	Findings  []unresolvedOutput `json:"findings"`
	Truncated bool               `json:"truncated"`
}

func registerReconcileReadTools(server *mcp.Server, services Services) {
	registerTool(server, objectTool("dbgraph_get_relation_init", "Get an Agent relation-initialization session."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input relationInitIDInput) (*mcp.CallToolResult, initSessionOutput, error) {
			if services.Reconcile == nil {
				return nil, initSessionOutput{}, errServiceUnavailable
			}
			sessionID, err := parseID(input.SessionID)
			if err != nil {
				return nil, initSessionOutput{}, err
			}
			session, err := services.Reconcile.Get(ctx, sessionID)
			return nil, mapInitSession(session), safeToolError(err)
		})

	registerTool(server, objectTool("dbgraph_list_unresolved", "List findings that an external Agent could not resolve."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input listUnresolvedInput) (*mcp.CallToolResult, unresolvedListOutput, error) {
			if services.Reconcile == nil {
				return nil, unresolvedListOutput{}, errServiceUnavailable
			}
			projectID, err := parseID(input.ProjectID)
			if err != nil {
				return nil, unresolvedListOutput{}, safeToolError(err)
			}
			if input.Limit == 0 {
				input.Limit = 20
			}
			if input.Limit < 1 || input.Limit > 100 {
				return nil, unresolvedListOutput{}, errInvalidToolInput
			}
			responseLimit := min(input.Limit, maximumMCPListResponseCount)
			findings, err := services.Reconcile.ListUnresolved(ctx, projectID, responseLimit+1)
			if err != nil {
				return nil, unresolvedListOutput{}, safeToolError(err)
			}
			output, err := boundedUnresolvedOutput(findings, responseLimit)
			if err != nil {
				return nil, unresolvedListOutput{}, err
			}
			return nil, output, nil
		})
}

func boundedUnresolvedOutput(found []reconcile.Unresolved, countLimit int) (unresolvedListOutput, error) {
	result := make([]unresolvedOutput, 0, min(len(found), countLimit))
	completeBaseBytes, err := structuredOutputBytes(unresolvedListOutput{Findings: []unresolvedOutput{}, Truncated: false})
	if err != nil {
		return unresolvedListOutput{}, err
	}
	truncatedBaseBytes, err := structuredOutputBytes(unresolvedListOutput{Findings: []unresolvedOutput{}, Truncated: true})
	if err != nil {
		return unresolvedListOutput{}, err
	}
	itemsBytes := 0
	for _, finding := range found {
		if len(result) >= countLimit {
			return unresolvedListOutput{Findings: result, Truncated: true}, nil
		}
		mapped := mapUnresolved(finding)
		encoded, err := json.Marshal(mapped)
		if err != nil {
			return unresolvedListOutput{}, errOperation
		}
		candidateItemsBytes := itemsBytes + len(encoded)
		if len(result) > 0 {
			candidateItemsBytes++
		}
		truncated := len(found) > len(result)+1
		baseBytes := completeBaseBytes
		if truncated {
			baseBytes = truncatedBaseBytes
		}
		if baseBytes+candidateItemsBytes > maximumMCPToolResultBytes {
			return unresolvedListOutput{Findings: result, Truncated: true}, nil
		}
		result = append(result, mapped)
		itemsBytes = candidateItemsBytes
	}
	return unresolvedListOutput{Findings: result, Truncated: false}, nil
}

func registerReconcileWriteTools(server *mcp.Server, services Services, principal relations.Principal) {
	registerTool(server, objectTool("dbgraph_begin_relation_init", "Begin a FULL or INCREMENTAL relation analysis session. Source analysis remains external to dbgraph."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input beginRelationInitInput) (*mcp.CallToolResult, initSessionOutput, error) {
			if !canInitialize(principal) {
				return nil, initSessionOutput{}, errForbidden
			}
			if services.Reconcile == nil {
				return nil, initSessionOutput{}, errServiceUnavailable
			}
			projectID, err := parseID(input.ProjectID)
			if err != nil {
				return nil, initSessionOutput{}, err
			}
			repositoryID, err := parseID(input.RepositoryID)
			if err != nil {
				return nil, initSessionOutput{}, err
			}
			mode, err := parseInitMode(input.Mode)
			if err != nil {
				return nil, initSessionOutput{}, err
			}
			scope, err := marshalObject(input.Scope)
			if err != nil {
				return nil, initSessionOutput{}, err
			}
			session, err := services.Reconcile.Begin(ctx, reconcile.Begin{
				ProjectID: projectID, RepositoryID: repositoryID, Mode: mode, SourceCommit: input.SourceCommit,
				Scope: scope, Principal: principal, RequestID: input.RequestID,
			})
			return nil, mapInitSession(session), safeToolError(err)
		})

	registerTool(server, objectTool("dbgraph_propose_relations", "Submit one bounded idempotent batch of relations and unresolved findings."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input proposeRelationsInput) (*mcp.CallToolResult, batchResultOutput, error) {
			if !canInitialize(principal) {
				return nil, batchResultOutput{}, errForbidden
			}
			if services.Reconcile == nil {
				return nil, batchResultOutput{}, errServiceUnavailable
			}
			command, err := parseBatch(input, principal)
			if err != nil {
				return nil, batchResultOutput{}, err
			}
			result, err := services.Reconcile.SubmitBatch(ctx, command)
			return nil, mapBatchResult(result), safeToolError(err)
		})

	registerTool(server, objectTool("dbgraph_complete_relation_init", "Complete an init session; FULL omissions create reviewable tombstone proposals only."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input completeRelationInitInput) (*mcp.CallToolResult, completionOutput, error) {
			if !canInitialize(principal) {
				return nil, completionOutput{}, errForbidden
			}
			if services.Reconcile == nil {
				return nil, completionOutput{}, errServiceUnavailable
			}
			sessionID, err := parseID(input.SessionID)
			if err != nil {
				return nil, completionOutput{}, err
			}
			result, err := services.Reconcile.Complete(ctx, reconcile.Complete{
				SessionID: sessionID, ExpectedBatchCount: input.ExpectedBatchCount,
				Principal: principal, Reason: input.Reason, RequestID: input.RequestID,
			})
			return nil, mapCompletion(result), safeToolError(err)
		})
}

func parseBatch(input proposeRelationsInput, principal relations.Principal) (reconcile.SubmitBatch, error) {
	sessionID, err := parseID(input.SessionID)
	if err != nil {
		return reconcile.SubmitBatch{}, err
	}
	itemCount := len(input.Proposals) + len(input.Unresolved)
	if itemCount < 1 || itemCount > 100 {
		return reconcile.SubmitBatch{}, fmt.Errorf("%w: batch must contain 1 to 100 items", errInvalidToolInput)
	}
	proposals := make([]reconcile.Proposal, len(input.Proposals))
	for index, item := range input.Proposals {
		relationType, err := parseRelationType(item.Type)
		if err != nil {
			return reconcile.SubmitBatch{}, err
		}
		content, err := parseRelationContent(item.relationContentInput)
		if err != nil {
			return reconcile.SubmitBatch{}, err
		}
		proposals[index] = reconcile.Proposal{
			Type: relationType, SourceNodeID: content.sourceNodeID, TargetNodeID: content.targetNodeID,
			Guard: content.guard, Selector: content.selector, Transform: content.transform,
			Confidence: item.Confidence, Evidence: content.evidence, Reason: item.Reason,
		}
	}
	unresolved := make([]reconcile.UnresolvedInput, len(input.Unresolved))
	for index, item := range input.Unresolved {
		evidence := append(json.RawMessage(nil), item.Evidence...)
		if !json.Valid(evidence) {
			return reconcile.SubmitBatch{}, errInvalidToolInput
		}
		unresolved[index] = reconcile.UnresolvedInput{Type: item.Type, Summary: item.Summary, Evidence: evidence}
	}
	return reconcile.SubmitBatch{
		SessionID: sessionID, BatchNo: input.BatchNo, IdempotencyKey: input.IdempotencyKey,
		Proposals: proposals, Unresolved: unresolved, Principal: principal, RequestID: input.RequestID,
	}, nil
}

func parseInitMode(value string) (reconcile.Mode, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "FULL":
		return reconcile.ModeFull, nil
	case "INCREMENTAL":
		return reconcile.ModeIncremental, nil
	default:
		return 0, fmt.Errorf("%w: mode must be FULL or INCREMENTAL", errInvalidToolInput)
	}
}

func marshalObject(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return json.RawMessage(`{}`), nil
	}
	encoded := append(json.RawMessage(nil), value...)
	if !json.Valid(encoded) || len(encoded) == 0 || encoded[0] != '{' {
		return nil, fmt.Errorf("%w: expected a JSON object", errInvalidToolInput)
	}
	return encoded, nil
}

func mapInitSession(session reconcile.Session) initSessionOutput {
	result := initSessionOutput{
		ID: formatID(session.ID), ProjectID: formatID(session.ProjectID), RepositoryID: formatID(session.RepositoryID),
		Mode: initModeName(session.Mode), SourceCommit: session.SourceCommit,
		Scope:  append(json.RawMessage(nil), session.Scope...),
		Status: initStatusName(session.Status), Actor: session.Principal.Actor,
		RequestID: session.RequestID, CreatedAt: session.CreatedAt.UTC().Format(timeFormat),
	}
	if session.CompletedAt != nil {
		result.CompletedAt = session.CompletedAt.UTC().Format(timeFormat)
	}
	return result
}

func mapBatchResult(result reconcile.BatchResult) batchResultOutput {
	items := make([]batchItemOutput, len(result.Items))
	for index, item := range result.Items {
		items[index] = batchItemOutput{RelationID: formatID(item.RelationID), Status: string(item.Status)}
	}
	unresolvedIDs := make([]string, len(result.UnresolvedIDs))
	for index, id := range result.UnresolvedIDs {
		unresolvedIDs[index] = formatID(id)
	}
	return batchResultOutput{
		BatchID: formatID(result.BatchID), SessionID: formatID(result.SessionID), BatchNo: result.BatchNo,
		Items: items, UnresolvedIDs: unresolvedIDs, AcceptedAt: result.AcceptedAt.UTC().Format(timeFormat),
	}
}

func mapCompletion(result reconcile.Completion) completionOutput {
	ids := make([]string, len(result.CandidateRelationIDs))
	for index, id := range result.CandidateRelationIDs {
		ids[index] = formatID(id)
	}
	return completionOutput{Session: mapInitSession(result.Session), CandidateRelationIDs: ids}
}

func mapUnresolved(finding reconcile.Unresolved) unresolvedOutput {
	status := "OPEN"
	if finding.Status != 1 {
		status = "RESOLVED"
	}
	return unresolvedOutput{
		ID: formatID(finding.ID), ProjectID: formatID(finding.ProjectID), RepositoryID: formatID(finding.RepositoryID),
		SessionID: formatID(finding.SessionID), BatchID: formatID(finding.BatchID), Fingerprint: finding.Fingerprint,
		Type: finding.Type, Summary: finding.Summary,
		Evidence: append(json.RawMessage(nil), finding.Evidence...), Status: status,
		Actor: finding.Principal.Actor, CreatedAt: finding.CreatedAt.UTC().Format(timeFormat),
	}
}

func initModeName(value reconcile.Mode) string {
	if value == reconcile.ModeFull {
		return "FULL"
	}
	if value == reconcile.ModeIncremental {
		return "INCREMENTAL"
	}
	return "UNKNOWN"
}

func initStatusName(value reconcile.Status) string {
	switch value {
	case reconcile.StatusOpen:
		return "OPEN"
	case reconcile.StatusCompleted:
		return "COMPLETED"
	case reconcile.StatusFailed:
		return "FAILED"
	case reconcile.StatusCancelled:
		return "CANCELLED"
	default:
		return "UNKNOWN"
	}
}
