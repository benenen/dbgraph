package mcpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	appauth "github.com/benenen/dbgraph/internal/auth"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/benenen/dbgraph/internal/sourcebinding"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	testBindingRevisionID int64 = 9_007_199_254_741_001
	testRepositoryID      int64 = 9_007_199_254_741_002
	testBoundSourceID     int64 = 9_007_199_254_741_003
)

type sourceBindingToolStub struct {
	resolveInput sourcebinding.WorkspaceEvidence
	replaceInput sourcebinding.ReplaceBindingSet
	resolveCalls int
	replaceCalls int
	resolveError error
	replaceError error
}

func (s *sourceBindingToolStub) ResolveWorkspace(
	_ context.Context,
	input sourcebinding.WorkspaceEvidence,
) (sourcebinding.Resolution, error) {
	s.resolveInput = input
	s.resolveCalls++
	return sourcebinding.Resolution{
		Status:       sourcebinding.StatusResolved,
		RepositoryID: testRepositoryID, RepositoryName: "orders-service", Context: "production",
		BindingRevisionID: testBindingRevisionID, BindingRevisionNo: 2,
		DataSources: []sourcebinding.DataSource{{ID: testBoundSourceID, Name: "orders-primary", Kind: "MYSQL"}},
	}, s.resolveError
}

func (s *sourceBindingToolStub) ReplaceBindingSet(
	_ context.Context,
	input sourcebinding.ReplaceBindingSet,
) (sourcebinding.BindingRevision, error) {
	s.replaceInput = input
	s.replaceCalls++
	return sourcebinding.BindingRevision{
		ID: testBindingRevisionID, RepositoryID: testRepositoryID, RepositoryName: "orders-service",
		Context: "production", RevisionNo: 2,
		DataSources: []sourcebinding.DataSource{{ID: testBoundSourceID, Name: "orders-primary", Kind: "MYSQL"}},
		CreatedAt:   testMCPTime,
	}, s.replaceError
}

func TestSourceBindingToolsMapCommandsAndStringIDs(t *testing.T) {
	t.Parallel()

	stub := &sourceBindingToolStub{}
	agent := relations.Principal{Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent}
	agentSession := connectInMemoryClient(t, Services{SourceBindings: stub}, agent)
	resolved := callToolOK(t, agentSession, "dbgraph_resolve_workspace_data_sources", `{
		"remotes":["git@github.com:acme/orders-service.git"],"context":"production"
	}`)
	if stub.resolveCalls != 1 || len(stub.resolveInput.Remotes) != 1 ||
		stub.resolveInput.Remotes[0] != "git@github.com:acme/orders-service.git" || stub.resolveInput.Context != "production" {
		t.Fatalf("resolve input = %#v", stub.resolveInput)
	}
	if resolved["status"] != "RESOLVED" || resolved["repositoryId"] != "9007199254741002" ||
		resolved["bindingRevisionId"] != "9007199254741001" {
		t.Fatalf("resolve output = %#v", resolved)
	}
	resolvedSource := firstArrayObject(t, resolved, "dataSources")
	if resolvedSource["id"] != "9007199254741003" || resolvedSource["name"] != "orders-primary" {
		t.Fatalf("resolved data source = %#v", resolvedSource)
	}
	encodedResolution, _ := json.Marshal(resolved)
	if strings.Contains(strings.ToLower(string(encodedResolution)), "dsn") || strings.Contains(string(encodedResolution), "credential") {
		t.Fatalf("resolution leaked deployment detail: %s", encodedResolution)
	}

	admin := relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginAgent}
	adminSession := connectInMemoryClient(t, Services{SourceBindings: stub}, admin)
	replaced := callToolOK(t, adminSession, "dbgraph_replace_source_binding", `{
		"repositoryId":"9007199254741002","context":"production",
		"dataSourceIds":["9007199254741003"],"expectedRevisionNo":1,
		"reason":"Update production binding.","requestId":"binding-mcp-2"
	}`)
	if stub.replaceCalls != 1 || stub.replaceInput.RepositoryID != testRepositoryID ||
		len(stub.replaceInput.DataSourceIDs) != 1 || stub.replaceInput.DataSourceIDs[0] != testBoundSourceID ||
		stub.replaceInput.ExpectedRevisionNo != 1 || stub.replaceInput.Principal != admin {
		t.Fatalf("replace input = %#v", stub.replaceInput)
	}
	if replaced["id"] != "9007199254741001" || replaced["repositoryId"] != "9007199254741002" ||
		replaced["revisionNo"] != float64(2) {
		t.Fatalf("replace output = %#v", replaced)
	}
}

func TestResolveSourceBindingRequiresAgentOrAdminBeforeService(t *testing.T) {
	t.Parallel()

	for _, role := range []relations.Role{
		relations.RoleViewer, relations.RoleEditor, relations.RoleReviewer,
	} {
		t.Run(roleName(role), func(t *testing.T) {
			stub := &sourceBindingToolStub{}
			principal := relations.Principal{Actor: "unauthorized", Role: role, Origin: audit.OriginAgent}
			session := connectInMemoryClient(t, Services{SourceBindings: stub}, principal)
			result := callTool(t, session, "dbgraph_resolve_workspace_data_sources", `{
				"remotes":["git@github.com:acme/orders-service.git"],"context":"production"
			}`)
			if !result.IsError || stub.resolveCalls != 0 || toolResultText(t, result) != "dbgraph permission denied" {
				t.Fatalf("role=%d result=%#v resolve calls=%d", role, result, stub.resolveCalls)
			}
		})
	}
}

func TestSourceBindingToolsRejectMalformedInputsBeforeService(t *testing.T) {
	t.Parallel()

	stub := &sourceBindingToolStub{}
	admin := relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginAgent}
	session := connectInMemoryClient(t, Services{SourceBindings: stub}, admin)
	result := callTool(t, session, "dbgraph_replace_source_binding", `{
		"repositoryId":"9007199254741002","context":"production",
		"dataSourceIds":["9007199254741003","9007199254741003"],"expectedRevisionNo":1,
		"reason":"Invalid duplicate.","requestId":"binding-duplicate"
	}`)
	if !result.IsError || stub.replaceCalls != 0 || toolResultText(t, result) != "dbgraph rejected the request" {
		t.Fatalf("duplicate result=%#v replace calls=%d", result, stub.replaceCalls)
	}
	missing := callTool(t, session, "dbgraph_replace_source_binding", `{
		"repositoryId":"9007199254741002","context":"production","expectedRevisionNo":1,
		"reason":"Missing data sources.","requestId":"binding-missing"
	}`)
	if !missing.IsError || stub.replaceCalls != 0 || toolResultText(t, missing) != "dbgraph rejected the request" {
		t.Fatalf("missing array result=%#v replace calls=%d", missing, stub.replaceCalls)
	}
	missingRemotes := callTool(t, session, "dbgraph_resolve_workspace_data_sources", `{"context":"production"}`)
	if !missingRemotes.IsError || stub.resolveCalls != 0 || toolResultText(t, missingRemotes) != "dbgraph rejected the request" {
		t.Fatalf("missing remotes result=%#v resolve calls=%d", missingRemotes, stub.resolveCalls)
	}
	missingRevision := callTool(t, session, "dbgraph_replace_source_binding", `{
		"repositoryId":"9007199254741002","context":"production","dataSourceIds":[],
		"reason":"Missing revision.","requestId":"binding-missing-revision"
	}`)
	if !missingRevision.IsError || stub.replaceCalls != 0 || toolResultText(t, missingRevision) != "dbgraph rejected the request" {
		t.Fatalf("missing revision result=%#v replace calls=%d", missingRevision, stub.replaceCalls)
	}
}

func TestSourceBindingRevisionConflictReturnsCurrentRevision(t *testing.T) {
	t.Parallel()

	stub := &sourceBindingToolStub{replaceError: &sourcebinding.RevisionConflictError{CurrentRevisionNo: 7}}
	admin := relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginAgent}
	session := connectInMemoryClient(t, Services{SourceBindings: stub}, admin)
	result := callTool(t, session, "dbgraph_replace_source_binding", `{
		"repositoryId":"9007199254741002","context":"production",
		"dataSourceIds":["9007199254741003"],"expectedRevisionNo":2,
		"reason":"Stale replacement.","requestId":"binding-stale"
	}`)
	if !result.IsError {
		t.Fatal("source binding conflict was returned as success")
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["code"] != "REVISION_CONFLICT" || structured["currentRevisionNo"] != float64(7) {
		t.Fatalf("structured conflict = %#v", result.StructuredContent)
	}
	if len(structured) != 2 || toolResultText(t, result) != "dbgraph resource changed or conflicts with existing state" {
		t.Fatalf("source binding conflict leaked extra data: %#v", result)
	}
}

func TestReplaceSourceBindingRequiresAdminBeforeService(t *testing.T) {
	t.Parallel()

	for _, role := range []relations.Role{
		relations.RoleViewer, relations.RoleAgent, relations.RoleEditor, relations.RoleReviewer,
	} {
		t.Run(roleName(role), func(t *testing.T) {
			stub := &sourceBindingToolStub{}
			principal := relations.Principal{Actor: "non-admin", Role: role, Origin: audit.OriginAgent}
			session := connectInMemoryClient(t, Services{SourceBindings: stub}, principal)
			result := callTool(t, session, "dbgraph_replace_source_binding", `{
				"repositoryId":"9007199254741002","context":"production",
				"dataSourceIds":["9007199254741003"],"expectedRevisionNo":1,
				"reason":"Unauthorized replacement.","requestId":"binding-denied"
			}`)
			if !result.IsError || stub.replaceCalls != 0 || toolResultText(t, result) != "dbgraph permission denied" {
				t.Fatalf("role=%d result=%#v replace calls=%d", role, result, stub.replaceCalls)
			}
		})
	}
}

func TestHTTPMCPSourceBindingToolsUseExplicitRateClasses(t *testing.T) {
	t.Parallel()

	stub, session := newRateLimitedSourceBindingSession(t)
	assertSourceBindingResolveRateLimit(t, session, stub)
	assertSourceBindingReplaceRateLimit(t, session, stub)
}

func newRateLimitedSourceBindingSession(t *testing.T) (*sourceBindingToolStub, *mcp.ClientSession) {
	t.Helper()
	stub := &sourceBindingToolStub{}
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testMCPAgentToken, Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginAgent,
	}})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	handler := NewHTTPHandlerWithOptions(
		Services{SourceBindings: stub}, authenticator,
		HTTPOptions{Now: time.Now, limits: mcpRateLimits{
			cheap: 10, expensive: 1, writes: 1, authentication: 10, maximumKeys: 100,
		}},
	)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return stub, connectHTTPClient(t, httpServer.URL, testMCPAgentToken)
}

func assertSourceBindingResolveRateLimit(
	t *testing.T,
	session *mcp.ClientSession,
	stub *sourceBindingToolStub,
) {
	t.Helper()
	resolveArguments := json.RawMessage(`{"remotes":["git@github.com:acme/orders-service.git"],"context":"production"}`)
	firstResolve, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "dbgraph_resolve_workspace_data_sources", Arguments: resolveArguments,
	})
	if err != nil || firstResolve.IsError {
		t.Fatalf("first resolve result=%#v error=%v", firstResolve, err)
	}
	secondResolve, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "dbgraph_resolve_workspace_data_sources", Arguments: resolveArguments,
	})
	if err != nil || !secondResolve.IsError || stub.resolveCalls != 1 {
		t.Fatalf("limited resolve result=%#v error=%v calls=%d", secondResolve, err, stub.resolveCalls)
	}
}

func assertSourceBindingReplaceRateLimit(
	t *testing.T,
	session *mcp.ClientSession,
	stub *sourceBindingToolStub,
) {
	t.Helper()
	replaceArguments := json.RawMessage(`{
		"repositoryId":"9007199254741002","context":"production",
		"dataSourceIds":["9007199254741003"],"expectedRevisionNo":1,
		"reason":"Update binding.","requestId":"binding-rate"
	}`)
	firstReplace, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "dbgraph_replace_source_binding", Arguments: replaceArguments,
	})
	if err != nil || firstReplace.IsError {
		t.Fatalf("first replace result=%#v error=%v", firstReplace, err)
	}
	secondReplace, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "dbgraph_replace_source_binding", Arguments: replaceArguments,
	})
	if err != nil || !secondReplace.IsError || stub.replaceCalls != 1 {
		t.Fatalf("limited replace result=%#v error=%v calls=%d", secondReplace, err, stub.replaceCalls)
	}
}

type oversizedRevisionConflictService struct{ RelationService }

func (oversizedRevisionConflictService) ProposeRevision(
	context.Context,
	relations.ProposeRevision,
) (relations.Relation, error) {
	current := testRelation()
	current.Active.Evidence[0].File = strings.Repeat("x", maximumMCPHTTPResponseBytes+1)
	current.Proposed = nil
	return relations.Relation{}, &relations.RevisionConflictError{CurrentRevisionNo: 7, Current: &current}
}

func TestHTTPMCPRevisionConflictFallsBackWithinResponseBudget(t *testing.T) {
	t.Parallel()

	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testMCPAgentToken, Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent,
	}})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	handler := NewHTTPHandler(Services{Relations: oversizedRevisionConflictService{}}, authenticator)
	request := httptest.NewRequest(http.MethodPost, "https://localhost/mcp", strings.NewReader(`{
		"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
			"name":"dbgraph_propose_relation_revision","arguments":{
				"relationId":"9007199254740994","expectedRevisionNo":2,
				"sourceNodeId":"11","targetNodeId":"12","confidence":0.9,
				"transform":{"kind":"column_copy","nodeId":"11"},
				"evidence":[{"kind":"CODE","repository":"r","commit":"c","file":"f","startLine":1,"endLine":1}],
				"reason":"Refresh evidence.","requestId":"oversized-conflict"
			}
		}
	}`))
	request.RemoteAddr = "127.0.0.1:41000"
	request.Header.Set("Authorization", "Bearer "+testMCPAgentToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.Len() > expectedMaximumMCPResponseBytes {
		t.Fatalf("conflict status=%d bytes=%d body-prefix=%q", response.Code, response.Body.Len(), response.Body.String()[:min(200, response.Body.Len())])
	}
	var envelope struct {
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	structured := envelope.Result.StructuredContent
	if !envelope.Result.IsError || structured["code"] != "REVISION_CONFLICT" ||
		structured["currentRevisionNo"] != float64(7) || structured["currentRelationOmitted"] != true ||
		structured["currentRelation"] != nil {
		t.Fatalf("bounded conflict = %#v", envelope.Result)
	}
}

func roleName(role relations.Role) string {
	switch role {
	case relations.RoleViewer:
		return "viewer"
	case relations.RoleAgent:
		return "agent"
	case relations.RoleEditor:
		return "editor"
	case relations.RoleReviewer:
		return "reviewer"
	default:
		return "unknown"
	}
}
