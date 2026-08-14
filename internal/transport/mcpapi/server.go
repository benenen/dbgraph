package mcpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/benenen/dbgraph/internal/sourcebinding"
	appstatus "github.com/benenen/dbgraph/internal/status"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName                        = "dbgraph"
	serverVersion                     = "0.1.0"
	maximumMCPHTTPResponseBytes       = 1 << 20
	maximumMCPJSONRPCEnvelopeOverhead = 4 << 10
	maximumMCPToolResultBytes         = maximumMCPHTTPResponseBytes - maximumMCPJSONRPCEnvelopeOverhead
	maximumMCPListResponseCount       = 50
)

var errServiceUnavailable = errors.New("dbgraph service is unavailable")

type StatusService interface {
	Status(context.Context) (appstatus.Snapshot, error)
}

type CatalogService interface {
	FindCurrentNode(context.Context, int64, string) (catalog.Node, error)
	SearchCurrentNodes(context.Context, int64, string, int) ([]catalog.Node, error)
}

type RelationService interface {
	ProposeCreate(context.Context, relations.ProposeCreate) (relations.Relation, error)
	ProposeRevision(context.Context, relations.ProposeRevision) (relations.Relation, error)
	ProposeTombstone(context.Context, relations.ProposeTombstone) (relations.Relation, error)
	Review(context.Context, relations.Review) (relations.Relation, error)
	Suppress(context.Context, relations.ChangeState) (relations.Relation, error)
	Restore(context.Context, relations.ChangeState) (relations.Relation, error)
	Get(context.Context, int64) (relations.Relation, error)
	ListProposals(context.Context, int) ([]relations.Relation, error)
}

type GraphService interface {
	Trace(context.Context, graph.TraceRequest) (graph.TraceResult, error)
	Impact(context.Context, graph.TraceRequest) (graph.TraceResult, error)
}

type ReconcileService interface {
	Begin(context.Context, reconcile.Begin) (reconcile.Session, error)
	SubmitBatch(context.Context, reconcile.SubmitBatch) (reconcile.BatchResult, error)
	Complete(context.Context, reconcile.Complete) (reconcile.Completion, error)
	Get(context.Context, int64) (reconcile.Session, error)
	ListUnresolved(context.Context, int) ([]reconcile.Unresolved, error)
}

type JobService interface {
	Start(context.Context, jobs.StartSchemaScan) (jobs.Job, error)
	Get(context.Context, int64) (jobs.Job, error)
}

type SourceBindingService interface {
	ResolveWorkspace(context.Context, sourcebinding.WorkspaceEvidence) (sourcebinding.Resolution, error)
	ReplaceBindingSet(context.Context, sourcebinding.ReplaceBindingSet) (sourcebinding.BindingRevision, error)
}

type Services struct {
	Status         StatusService
	Catalog        CatalogService
	Relations      RelationService
	Graph          GraphService
	Reconcile      ReconcileService
	Jobs           JobService
	SourceBindings SourceBindingService
}

func ViewerPrincipal() relations.Principal {
	return relations.Principal{Actor: "anonymous", Role: relations.RoleViewer}
}

func NewServer(services Services, principal relations.Principal) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	registerReadTools(server, services, principal)
	registerWriteTools(server, services, principal)
	return server
}

func objectTool(name string, description string) *mcp.Tool {
	return &mcp.Tool{
		Name:         name,
		Description:  description,
		InputSchema:  inputSchema(name),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func registerTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		class := classifyTool(tool.Name)
		if identity, ok := ctx.Value(mcpIdentityContextKey).(mcpRequestIdentity); ok &&
			!identity.limiter.Allow(identity.principal, identity.clientIP, class, identity.now()) {
			return toolErrorResult(errRateLimited), nil
		}
		var input In
		arguments := request.Params.Arguments
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		decoder := json.NewDecoder(bytes.NewReader(arguments))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&input); err != nil {
			return toolErrorResult(errInvalidRequest), nil
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return toolErrorResult(errInvalidRequest), nil
		}
		result, output, err := handler(ctx, request, input)
		if err != nil {
			return toolErrorResult(err), nil
		}
		result, err = structuredToolResult(result, output, true)
		if err != nil {
			return toolErrorResult(err), nil
		}
		return result, nil
	})
}

func structuredToolResult(result *mcp.CallToolResult, output any, enforceBudget bool) (*mcp.CallToolResult, error) {
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, errOperation
	}
	if result == nil {
		result = &mcp.CallToolResult{}
	}
	result.StructuredContent = json.RawMessage(encoded)
	if result.Content == nil {
		result.Content = []mcp.Content{}
	}
	if !enforceBudget {
		return result, nil
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, errOperation
	}
	if len(resultJSON) > maximumMCPToolResultBytes {
		return nil, errResponseBudget
	}
	return result, nil
}

func structuredOutputBytes(output any) (int, error) {
	result, err := structuredToolResult(nil, output, false)
	if err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return 0, errOperation
	}
	return len(encoded), nil
}

func toolErrorResult(err error) *mcp.CallToolResult {
	result := &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: safeToolError(err).Error()}},
	}
	var conflict *relations.RevisionConflictError
	if errors.As(err, &conflict) {
		payload := map[string]any{
			"code":              "REVISION_CONFLICT",
			"currentRevisionNo": conflict.CurrentRevisionNo,
		}
		if conflict.Current != nil {
			payload["currentRelation"] = mapRelation(*conflict.Current)
		}
		setBudgetedConflict(result, payload, map[string]any{
			"code": "REVISION_CONFLICT", "currentRevisionNo": conflict.CurrentRevisionNo,
			"currentRelationOmitted": true,
		})
		return result
	}
	var bindingConflict *sourcebinding.RevisionConflictError
	if errors.As(err, &bindingConflict) {
		setBudgetedConflict(result, map[string]any{
			"code": "REVISION_CONFLICT", "currentRevisionNo": bindingConflict.CurrentRevisionNo,
		}, nil)
	}
	return result
}

func setBudgetedConflict(result *mcp.CallToolResult, payload map[string]any, fallback map[string]any) {
	if _, err := structuredToolResult(result, payload, true); err == nil {
		return
	}
	if fallback == nil {
		result.StructuredContent = nil
		return
	}
	_, _ = structuredToolResult(result, fallback, false)
}
