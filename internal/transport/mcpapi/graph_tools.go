package mcpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type traceContextInput struct {
	Columns    map[string]json.RawMessage `json:"columns,omitempty"`
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
}

type traceInput struct {
	StartNodeID  string            `json:"startNodeId"`
	TargetNodeID string            `json:"targetNodeId,omitempty"`
	Direction    string            `json:"direction,omitempty"`
	Context      traceContextInput `json:"context,omitempty"`
	MaxDepth     int               `json:"maxDepth,omitempty"`
	MaxNodes     int               `json:"maxNodes,omitempty"`
	MaxPaths     int               `json:"maxPaths,omitempty"`
}

type missingReferenceOutput struct {
	NodeID    string `json:"nodeId,omitempty"`
	Parameter string `json:"parameter,omitempty"`
}

type graphEvaluationOutput struct {
	Truth   string                   `json:"truth"`
	Missing []missingReferenceOutput `json:"missing,omitempty"`
}

type graphEdgeOutput struct {
	RelationID     string        `json:"relationId"`
	VersionID      string        `json:"versionId"`
	SourceNodeID   string        `json:"sourceNodeId"`
	TargetNodeID   string        `json:"targetNodeId"`
	Type           string        `json:"type"`
	Status         string        `json:"status"`
	ProposalStatus string        `json:"proposalStatus,omitempty"`
	Guard          *booleanInput `json:"guard,omitempty"`
	Selector       *booleanInput `json:"selector,omitempty"`
	Transform      valueInput    `json:"transform"`
	Confidence     float64       `json:"confidence"`
}

type graphStepOutput struct {
	Edge       graphEdgeOutput       `json:"edge"`
	Evaluation graphEvaluationOutput `json:"evaluation"`
}

type graphPathOutput struct {
	Nodes []string          `json:"nodes"`
	Steps []graphStepOutput `json:"steps"`
}

type traceOutput struct {
	Paths         []graphPathOutput `json:"paths"`
	VisitedNodes  int               `json:"visitedNodes"`
	CycleDetected bool              `json:"cycleDetected"`
	Truncated     bool              `json:"truncated"`
}

func registerGraphReadTools(server *mcp.Server, services Services) {
	registerTool(server, objectTool("dbgraph_trace", "Trace conditional upstream or downstream graph paths using three-valued context evaluation."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input traceInput) (*mcp.CallToolResult, traceOutput, error) {
			request, err := parseTraceInput(input, false)
			if err != nil {
				return nil, traceOutput{}, err
			}
			if services.Graph == nil {
				return nil, traceOutput{}, errServiceUnavailable
			}
			result, err := services.Graph.Trace(ctx, request)
			return nil, mapTrace(result), safeToolError(err)
		})

	registerTool(server, objectTool("dbgraph_impact", "Analyze downstream impact from a catalog node."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input traceInput) (*mcp.CallToolResult, traceOutput, error) {
			request, err := parseTraceInput(input, true)
			if err != nil {
				return nil, traceOutput{}, err
			}
			if services.Graph == nil {
				return nil, traceOutput{}, errServiceUnavailable
			}
			result, err := services.Graph.Impact(ctx, request)
			return nil, mapTrace(result), safeToolError(err)
		})
}

func parseTraceInput(input traceInput, forceDownstream bool) (graph.TraceRequest, error) {
	startNodeID, err := parseID(input.StartNodeID)
	if err != nil {
		return graph.TraceRequest{}, err
	}
	var targetNodeID int64
	if input.TargetNodeID != "" {
		targetNodeID, err = parseID(input.TargetNodeID)
		if err != nil {
			return graph.TraceRequest{}, err
		}
	}
	direction := graph.DirectionDownstream
	if !forceDownstream {
		switch strings.ToUpper(strings.TrimSpace(input.Direction)) {
		case "", "DOWNSTREAM":
			direction = graph.DirectionDownstream
		case "UPSTREAM":
			direction = graph.DirectionUpstream
		default:
			return graph.TraceRequest{}, fmt.Errorf("%w: direction must be UPSTREAM or DOWNSTREAM", errInvalidToolInput)
		}
	}
	limits := graph.DefaultLimits()
	if input.MaxDepth != 0 {
		limits.MaxDepth = input.MaxDepth
	}
	if input.MaxNodes != 0 {
		limits.MaxNodes = input.MaxNodes
	}
	if input.MaxPaths != 0 {
		limits.MaxPaths = input.MaxPaths
	}
	conditionContext, err := convertTraceContext(input.Context)
	if err != nil {
		return graph.TraceRequest{}, err
	}
	return graph.TraceRequest{
		StartNodeID: startNodeID, TargetNodeID: targetNodeID,
		Direction: direction, Context: conditionContext, Limits: limits,
	}, nil
}

func convertTraceContext(input traceContextInput) (conditions.Context, error) {
	result := conditions.Context{
		Columns:    make(map[int64]json.RawMessage, len(input.Columns)),
		Parameters: make(map[string]json.RawMessage, len(input.Parameters)),
	}
	for key, value := range input.Columns {
		id, err := parseID(key)
		if err != nil {
			return conditions.Context{}, err
		}
		encoded := append(json.RawMessage(nil), value...)
		if !json.Valid(encoded) {
			return conditions.Context{}, errInvalidToolInput
		}
		result.Columns[id] = encoded
	}
	for key, value := range input.Parameters {
		encoded := append(json.RawMessage(nil), value...)
		if !json.Valid(encoded) {
			return conditions.Context{}, errInvalidToolInput
		}
		result.Parameters[key] = encoded
	}
	return result, nil
}

func mapTrace(result graph.TraceResult) traceOutput {
	paths := make([]graphPathOutput, len(result.Paths))
	for pathIndex, path := range result.Paths {
		nodes := make([]string, len(path.Nodes))
		for nodeIndex, nodeID := range path.Nodes {
			nodes[nodeIndex] = formatID(nodeID)
		}
		steps := make([]graphStepOutput, len(path.Steps))
		for stepIndex, step := range path.Steps {
			proposalStatus := ""
			if step.Edge.HasPendingProposal {
				proposalStatus = "PROPOSED"
			}
			missing := make([]missingReferenceOutput, len(step.Evaluation.Missing))
			for index, reference := range step.Evaluation.Missing {
				missing[index] = missingReferenceOutput{NodeID: formatID(reference.NodeID), Parameter: reference.Parameter}
			}
			steps[stepIndex] = graphStepOutput{
				Edge: graphEdgeOutput{
					RelationID: formatID(step.Edge.RelationID), VersionID: formatID(step.Edge.VersionID),
					SourceNodeID: formatID(step.Edge.SourceNodeID), TargetNodeID: formatID(step.Edge.TargetNodeID),
					Type:   relationTypeName(step.Edge.Type),
					Status: relationStatusName(step.Edge.Status), ProposalStatus: proposalStatus,
					Guard: mapBoolean(step.Edge.Guard), Selector: mapBoolean(step.Edge.Selector),
					Transform: mapValue(step.Edge.Transform), Confidence: step.Edge.Confidence,
				},
				Evaluation: graphEvaluationOutput{Truth: truthName(step.Evaluation.Truth), Missing: missing},
			}
		}
		paths[pathIndex] = graphPathOutput{Nodes: nodes, Steps: steps}
	}
	return traceOutput{
		Paths: paths, VisitedNodes: result.VisitedNodes, CycleDetected: result.CycleDetected, Truncated: result.Truncated,
	}
}

func truthName(value conditions.Truth) string {
	switch value {
	case conditions.TruthTrue:
		return "TRUE"
	case conditions.TruthFalse:
		return "FALSE"
	default:
		return "UNKNOWN"
	}
}
