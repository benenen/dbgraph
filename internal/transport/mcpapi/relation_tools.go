package mcpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type relationIDInput struct {
	RelationID string `json:"relationId" jsonschema:"relation Snowflake ID as a decimal string"`
}

type listProposalsInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum number of proposals from 1 to 100; defaults to 20"`
}

type relationsOutput struct {
	Relations []relationOutput `json:"relations"`
	Truncated bool             `json:"truncated"`
}

type explainRelationOutput struct {
	Relation relationOutput `json:"relation"`
	Summary  string         `json:"summary"`
}

type relationContentInput struct {
	SourceNodeID string          `json:"sourceNodeId"`
	TargetNodeID string          `json:"targetNodeId"`
	Guard        *booleanInput   `json:"guard,omitempty"`
	Selector     *booleanInput   `json:"selector,omitempty"`
	Transform    valueInput      `json:"transform"`
	Confidence   float64         `json:"confidence"`
	Evidence     []evidenceInput `json:"evidence"`
}

type proposeRelationInput struct {
	Type string `json:"type"`
	relationContentInput
	Reason    string `json:"reason"`
	RequestID string `json:"requestId"`
}

type proposeRevisionInput struct {
	RelationID         string `json:"relationId"`
	ExpectedRevisionNo int    `json:"expectedRevisionNo"`
	relationContentInput
	Reason    string `json:"reason"`
	RequestID string `json:"requestId"`
}

type changeRelationInput struct {
	RelationID         string `json:"relationId"`
	ExpectedRevisionNo int    `json:"expectedRevisionNo"`
	Reason             string `json:"reason"`
	RequestID          string `json:"requestId"`
}

type reviewRelationInput struct {
	RelationID         string `json:"relationId"`
	ExpectedRevisionNo int    `json:"expectedRevisionNo"`
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	RequestID          string `json:"requestId"`
}

func registerRelationReadTools(server *mcp.Server, services Services) {
	registerTool(server, objectTool("dbgraph_get_relation", "Get current and proposed relation revisions."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input relationIDInput) (*mcp.CallToolResult, relationOutput, error) {
			relation, err := getRelation(ctx, services, input.RelationID)
			return nil, mapRelation(relation), err
		})

	registerTool(server, objectTool("dbgraph_explain_relation", "Explain whether a relation is effective and which revision controls it."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input relationIDInput) (*mcp.CallToolResult, explainRelationOutput, error) {
			relation, err := getRelation(ctx, services, input.RelationID)
			if err != nil {
				return nil, explainRelationOutput{}, err
			}
			summary := fmt.Sprintf("Relation is %s and is not part of the effective graph.", relationDisplayStatus(relation))
			if relation.Effective && relation.Active != nil {
				summary = fmt.Sprintf("Approved revision %d is part of the effective graph.", relation.Active.RevisionNo)
			}
			return nil, explainRelationOutput{Relation: mapRelation(relation), Summary: summary}, nil
		})

	registerTool(server, objectTool("dbgraph_list_proposals", "List pending relation proposals for review."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input listProposalsInput) (*mcp.CallToolResult, relationsOutput, error) {
			if services.Relations == nil {
				return nil, relationsOutput{}, errServiceUnavailable
			}
			if input.Limit == 0 {
				input.Limit = 20
			}
			if input.Limit < 1 || input.Limit > 100 {
				return nil, relationsOutput{}, errInvalidToolInput
			}
			responseLimit := min(input.Limit, maximumMCPListResponseCount)
			relationsFound, err := services.Relations.ListProposals(ctx, responseLimit+1)
			if err != nil {
				return nil, relationsOutput{}, err
			}
			output, err := boundedRelationsOutput(relationsFound, responseLimit)
			if err != nil {
				return nil, relationsOutput{}, err
			}
			return nil, output, nil
		})
}

func boundedRelationsOutput(found []relations.Relation, countLimit int) (relationsOutput, error) {
	result := make([]relationOutput, 0, min(len(found), countLimit))
	completeBaseBytes, err := structuredOutputBytes(relationsOutput{Relations: []relationOutput{}, Truncated: false})
	if err != nil {
		return relationsOutput{}, err
	}
	truncatedBaseBytes, err := structuredOutputBytes(relationsOutput{Relations: []relationOutput{}, Truncated: true})
	if err != nil {
		return relationsOutput{}, err
	}
	itemsBytes := 0
	for _, relation := range found {
		if len(result) >= countLimit {
			return relationsOutput{Relations: result, Truncated: true}, nil
		}
		mapped := mapRelation(relation)
		encoded, err := json.Marshal(mapped)
		if err != nil {
			return relationsOutput{}, errOperation
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
			return relationsOutput{Relations: result, Truncated: true}, nil
		}
		result = append(result, mapped)
		itemsBytes = candidateItemsBytes
	}
	return relationsOutput{Relations: result, Truncated: false}, nil
}

func registerWriteTools(server *mcp.Server, services Services, principal relations.Principal) {
	registerRelationWriteTools(server, services, principal)
	registerReconcileWriteTools(server, services, principal)
	registerJobWriteTools(server, services, principal)
}

func registerRelationWriteTools(server *mcp.Server, services Services, principal relations.Principal) {
	registerTool(server, objectTool("dbgraph_propose_relation", "Propose a new evidence-backed relation. It is not effective until approved."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input proposeRelationInput) (*mcp.CallToolResult, relationOutput, error) {
			if !canCreateOrTombstone(principal) {
				return nil, relationOutput{}, errForbidden
			}
			if services.Relations == nil {
				return nil, relationOutput{}, errServiceUnavailable
			}
			relationType, content, err := parseCreateContent(input)
			if err != nil {
				return nil, relationOutput{}, err
			}
			relation, err := services.Relations.ProposeCreate(ctx, relations.ProposeCreate{
				Type:         relationType,
				TargetNodeID: content.targetNodeID, Guard: content.guard, Selector: content.selector,
				Transform: content.transform, Confidence: input.Confidence, Evidence: content.evidence,
				Principal: principal, Reason: input.Reason, RequestID: input.RequestID,
			})
			return nil, mapRelation(relation), err
		})

	registerTool(server, objectTool("dbgraph_propose_relation_revision", "Propose a revision using optimistic revision control."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input proposeRevisionInput) (*mcp.CallToolResult, relationOutput, error) {
			if !canRevise(principal) {
				return nil, relationOutput{}, errForbidden
			}
			if services.Relations == nil {
				return nil, relationOutput{}, errServiceUnavailable
			}
			relationID, err := parseID(input.RelationID)
			if err != nil {
				return nil, relationOutput{}, err
			}
			content, err := parseRelationContent(input.relationContentInput)
			if err != nil {
				return nil, relationOutput{}, err
			}
			relation, err := services.Relations.ProposeRevision(ctx, relations.ProposeRevision{
				RelationID: relationID, ExpectedRevisionNo: input.ExpectedRevisionNo,
				SourceNodeID: content.sourceNodeID, TargetNodeID: content.targetNodeID,
				Guard: content.guard, Selector: content.selector, Transform: content.transform,
				Confidence: input.Confidence, Evidence: content.evidence, Principal: principal,
				Reason: input.Reason, RequestID: input.RequestID,
			})
			return nil, mapRelation(relation), err
		})

	registerTool(server, objectTool("dbgraph_propose_relation_tombstone", "Propose a tombstone without deleting relation history."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input changeRelationInput) (*mcp.CallToolResult, relationOutput, error) {
			if !canCreateOrTombstone(principal) {
				return nil, relationOutput{}, errForbidden
			}
			if services.Relations == nil {
				return nil, relationOutput{}, errServiceUnavailable
			}
			relationID, err := parseID(input.RelationID)
			if err != nil {
				return nil, relationOutput{}, err
			}
			relation, err := services.Relations.ProposeTombstone(ctx, relations.ProposeTombstone{
				RelationID: relationID, ExpectedRevisionNo: input.ExpectedRevisionNo,
				Principal: principal, Reason: input.Reason, RequestID: input.RequestID,
			})
			return nil, mapRelation(relation), err
		})

	registerTool(server, objectTool("dbgraph_review_relation", "Approve or reject a pending revision. Requires Reviewer or Admin."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input reviewRelationInput) (*mcp.CallToolResult, relationOutput, error) {
			if !canReview(principal) {
				return nil, relationOutput{}, errForbidden
			}
			if services.Relations == nil {
				return nil, relationOutput{}, errServiceUnavailable
			}
			relationID, err := parseID(input.RelationID)
			if err != nil {
				return nil, relationOutput{}, err
			}
			decision, err := parseDecision(input.Decision)
			if err != nil {
				return nil, relationOutput{}, err
			}
			relation, err := services.Relations.Review(ctx, relations.Review{
				RelationID: relationID, ExpectedRevisionNo: input.ExpectedRevisionNo, Decision: decision,
				Principal: principal, Reason: input.Reason, RequestID: input.RequestID,
			})
			return nil, mapRelation(relation), err
		})

	registerStateTool(server, "dbgraph_suppress_relation", "Suppress an approved relation. Requires Reviewer or Admin.", services, principal, false)
	registerStateTool(server, "dbgraph_restore_relation", "Restore a suppressed relation. Requires Reviewer or Admin.", services, principal, true)
}

func registerStateTool(server *mcp.Server, name string, description string, services Services, principal relations.Principal, restore bool) {
	registerTool(server, objectTool(name, description),
		func(ctx context.Context, _ *mcp.CallToolRequest, input changeRelationInput) (*mcp.CallToolResult, relationOutput, error) {
			if !canReview(principal) {
				return nil, relationOutput{}, errForbidden
			}
			if services.Relations == nil {
				return nil, relationOutput{}, errServiceUnavailable
			}
			relationID, err := parseID(input.RelationID)
			if err != nil {
				return nil, relationOutput{}, err
			}
			command := relations.ChangeState{
				RelationID: relationID, ExpectedRevisionNo: input.ExpectedRevisionNo,
				Principal: principal, Reason: input.Reason, RequestID: input.RequestID,
			}
			var relation relations.Relation
			if restore {
				relation, err = services.Relations.Restore(ctx, command)
			} else {
				relation, err = services.Relations.Suppress(ctx, command)
			}
			return nil, mapRelation(relation), err
		})
}

func canCreateOrTombstone(principal relations.Principal) bool {
	return principal.Role == relations.RoleAgent || principal.Role == relations.RoleEditor ||
		principal.Role == relations.RoleAdmin
}

func canRevise(principal relations.Principal) bool {
	return canCreateOrTombstone(principal) || principal.Role == relations.RoleReviewer
}

func canInitialize(principal relations.Principal) bool {
	return principal.Role == relations.RoleAgent || principal.Role == relations.RoleAdmin
}

func canReview(principal relations.Principal) bool {
	return principal.Role == relations.RoleReviewer || principal.Role == relations.RoleAdmin
}

type parsedRelationContent struct {
	sourceNodeID int64
	targetNodeID int64
	guard        *conditions.Boolean
	selector     *conditions.Boolean
	transform    conditions.Value
	evidence     []relations.EvidenceInput
}

func parseCreateContent(input proposeRelationInput) (relations.Type, parsedRelationContent, error) {
	relationType, err := parseRelationType(input.Type)
	if err != nil {
		return 0, parsedRelationContent{}, err
	}
	content, err := parseRelationContent(input.relationContentInput)
	return relationType, content, err
}

func parseRelationContent(input relationContentInput) (parsedRelationContent, error) {
	sourceNodeID, err := parseID(input.SourceNodeID)
	if err != nil {
		return parsedRelationContent{}, err
	}
	targetNodeID, err := parseID(input.TargetNodeID)
	if err != nil {
		return parsedRelationContent{}, err
	}
	guard, err := convertBoolean(input.Guard)
	if err != nil {
		return parsedRelationContent{}, err
	}
	selector, err := convertBoolean(input.Selector)
	if err != nil {
		return parsedRelationContent{}, err
	}
	transform, err := convertValue(&input.Transform)
	if err != nil {
		return parsedRelationContent{}, err
	}
	evidence, err := convertEvidence(input.Evidence)
	if err != nil {
		return parsedRelationContent{}, err
	}
	return parsedRelationContent{sourceNodeID, targetNodeID, guard, selector, *transform, evidence}, nil
}

func getRelation(ctx context.Context, services Services, value string) (relations.Relation, error) {
	if services.Relations == nil {
		return relations.Relation{}, errServiceUnavailable
	}
	relationID, err := parseID(value)
	if err != nil {
		return relations.Relation{}, err
	}
	return services.Relations.Get(ctx, relationID)
}

func parseDecision(value string) (relations.Decision, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "APPROVE":
		return relations.DecisionApprove, nil
	case "REJECT":
		return relations.DecisionReject, nil
	default:
		return 0, fmt.Errorf("%w: decision must be APPROVE or REJECT", errInvalidToolInput)
	}
}
