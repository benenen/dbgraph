package mcpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	appauth "github.com/benenen/dbgraph/internal/auth"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testMCPAgentToken = "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f"

const expectedMaximumMCPResponseBytes = 1 << 20

func TestHTTPMCPRequiresBearerWhenCredentialsAreConfigured(t *testing.T) {
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testMCPAgentToken, Actor: "repository-agent", Role: relations.RoleAgent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()

	NewHTTPHandler(Services{}, authenticator).ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if response.Header().Get("WWW-Authenticate") != `Bearer realm="dbgraph"` {
		t.Fatalf("WWW-Authenticate = %q", response.Header().Get("WWW-Authenticate"))
	}
}

type relationWriteStub struct {
	RelationService
	calls            int
	principal        relations.Principal
	transformLiteral json.RawMessage
}

type proposalListBudgetStub struct {
	RelationService
	proposals []relations.Relation
	listLimit int
	listCalls int
}

type unresolvedListBudgetStub struct {
	ReconcileService
	findings  []reconcile.Unresolved
	listLimit int
	listCalls int
}

type largeWriteOutputStub struct {
	RelationService
	result relations.Relation
	calls  int
}

func (s *largeWriteOutputStub) ProposeCreate(context.Context, relations.ProposeCreate) (relations.Relation, error) {
	s.calls++
	return s.result, nil
}

func (s *proposalListBudgetStub) ListProposals(_ context.Context, limit int) ([]relations.Relation, error) {
	s.listLimit = limit
	s.listCalls++
	return append([]relations.Relation(nil), s.proposals[:min(limit, len(s.proposals))]...), nil
}

func (s *proposalListBudgetStub) Get(context.Context, int64) (relations.Relation, error) {
	return s.proposals[0], nil
}

func (s *unresolvedListBudgetStub) ListUnresolved(_ context.Context, limit int) ([]reconcile.Unresolved, error) {
	s.listLimit = limit
	s.listCalls++
	return append([]reconcile.Unresolved(nil), s.findings[:min(limit, len(s.findings))]...), nil
}

func (s *relationWriteStub) ProposeCreate(_ context.Context, command relations.ProposeCreate) (relations.Relation, error) {
	s.calls++
	s.principal = command.Principal
	if command.Transform.Literal != nil {
		s.transformLiteral = append(json.RawMessage(nil), command.Transform.Literal.Value...)
	}
	return relations.Relation{ID: 9_007_199_254_740_993, Type: command.Type}, nil
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copy)
}

func TestHTTPMCPRejectsForgedRolesAndInjectsAuthenticatedPrincipal(t *testing.T) {
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testMCPAgentToken, Actor: "repository-agent", Role: relations.RoleAgent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	relationsService := &relationWriteStub{}
	httpServer := httptest.NewServer(NewHTTPHandler(Services{Relations: relationsService}, authenticator))
	t.Cleanup(httpServer.Close)

	authenticatedSession := connectHTTPClient(t, httpServer.URL, testMCPAgentToken)
	forged, err := authenticatedSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "dbgraph_propose_relation",
		Arguments: json.RawMessage(`{"type":"CONDITIONAL_VALUE_COPY","role":"ADMIN"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !forged.IsError || relationsService.calls != 0 {
		t.Fatalf("authenticated forged role error=%v calls=%d", forged.IsError, relationsService.calls)
	}
	result, err := authenticatedSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "dbgraph_propose_relation",
		Arguments: json.RawMessage(`{
			"type":"CONDITIONAL_VALUE_COPY",
			"sourceNodeId":"11","targetNodeId":"12","confidence":0.9,
			"transform":{"kind":"literal","literal":{"type":"integer","value":9007199254740993}},
			"evidence":[{"kind":"CODE","repository":"repo","commit":"abc","file":"Mapper.java","startLine":1,"endLine":2}],
			"reason":"Mapped by source assignment","requestId":"req-1"
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || relationsService.calls != 1 {
		t.Fatalf("authenticated write result error=%v calls=%d content=%v", result.IsError, relationsService.calls, result.Content)
	}
	if relationsService.principal.Actor != "repository-agent" || relationsService.principal.Role != relations.RoleAgent {
		t.Fatalf("principal = %#v", relationsService.principal)
	}
	if string(relationsService.transformLiteral) != "9007199254740993" {
		t.Fatalf("integer literal changed to %s", relationsService.transformLiteral)
	}

	request := httptest.NewRequest(http.MethodPost, httpServer.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	request.Header.Set("Authorization", "Bearer invalid-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	NewHTTPHandler(Services{}, authenticator).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d", response.Code)
	}
}

func connectHTTPClient(t *testing.T, endpoint string, token string) *mcp.ClientSession {
	t.Helper()
	httpClient := http.DefaultClient
	if token != "" {
		httpClient = &http.Client{Transport: bearerTransport{base: http.DefaultTransport, token: token}}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestMCPRateLimiterEnforcesPrincipalAndIPBucketsConcurrently(t *testing.T) {
	t.Parallel()

	limiter := newMCPRateLimiter(mcpRateLimits{cheap: 10, expensive: 2, writes: 2, authentication: 2, maximumKeys: 100})
	now := time.Date(2026, 8, 11, 17, 0, 0, 0, time.UTC)
	principal := relations.Principal{Actor: "agent", Role: relations.RoleAgent}
	var wait sync.WaitGroup
	results := make(chan bool, 20)
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results <- limiter.Allow(principal, "192.0.2."+strconv.Itoa(index+1), toolWrite, now)
		}(index)
	}
	wait.Wait()
	close(results)
	allowed := 0
	for result := range results {
		if result {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("concurrent principal allowance = %d, want 2", allowed)
	}
	if !limiter.Allow(principal, "192.0.2.1", toolWrite, now.Add(time.Minute)) {
		t.Fatal("rate limit did not reset after one minute")
	}

	ipLimiter := newMCPRateLimiter(mcpRateLimits{cheap: 10, expensive: 2, writes: 1, authentication: 2, maximumKeys: 100})
	if !ipLimiter.Allow(relations.Principal{Actor: "a", Role: relations.RoleAgent}, "192.0.2.9", toolWrite, now) ||
		ipLimiter.Allow(relations.Principal{Actor: "b", Role: relations.RoleAgent}, "192.0.2.9", toolWrite, now) {
		t.Fatal("IP bucket was bypassed with a second principal")
	}
	if normalizedClientIP("[::ffff:192.0.2.10]:1234") != "192.0.2.10" {
		t.Fatalf("normalized client IP = %q", normalizedClientIP("[::ffff:192.0.2.10]:1234"))
	}
}

func TestHTTPMCPRateLimitStopsWriteBeforeService(t *testing.T) {
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testMCPAgentToken, Actor: "repository-agent", Role: relations.RoleAgent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	relationsService := &relationWriteStub{}
	handler := NewHTTPHandlerWithOptions(
		Services{Relations: relationsService}, authenticator,
		HTTPOptions{Now: time.Now, limits: mcpRateLimits{cheap: 100, expensive: 100, writes: 1, authentication: 10, maximumKeys: 100}},
	)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	session := connectHTTPClient(t, httpServer.URL, testMCPAgentToken)
	arguments := json.RawMessage(`{
		"type":"CONDITIONAL_VALUE_COPY","sourceNodeId":"11","targetNodeId":"12",
		"confidence":0.9,"transform":{"kind":"column_copy","nodeId":"11"},
		"evidence":[{"kind":"CODE","repository":"repo","commit":"abc","file":"Mapper.java","startLine":1,"endLine":2}],
		"reason":"Mapped by source assignment","requestId":"req-rate"
	}`)
	first, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbgraph_propose_relation", Arguments: arguments})
	if err != nil || first.IsError {
		t.Fatalf("first call result=%#v err=%v", first, err)
	}
	second, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbgraph_propose_relation", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if !second.IsError || relationsService.calls != 1 {
		t.Fatalf("limited result=%#v service calls=%d", second, relationsService.calls)
	}
}

func TestHTTPMCPRateLimitSeparatesCheapAndExpensiveTools(t *testing.T) {
	stub := &toolBehaviorStub{}
	handler := NewHTTPHandlerWithOptions(
		testServices(stub), nil,
		HTTPOptions{Now: time.Now, limits: mcpRateLimits{cheap: 10, expensive: 1, writes: 10, authentication: 10, maximumKeys: 100}},
	)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	session := connectHTTPClient(t, httpServer.URL, "")

	for index := 0; index < 2; index++ {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbgraph_status", Arguments: json.RawMessage(`{}`)})
		if err != nil || result.IsError {
			t.Fatalf("cheap status call %d result=%#v err=%v", index+1, result, err)
		}
	}
	arguments := json.RawMessage(`{"query":"orders"}`)
	first, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbgraph_search_nodes", Arguments: arguments})
	if err != nil || first.IsError {
		t.Fatalf("first expensive call result=%#v err=%v", first, err)
	}
	second, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbgraph_search_nodes", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if !second.IsError || stub.searchCalls != 1 {
		t.Fatalf("limited expensive result=%#v search calls=%d", second, stub.searchCalls)
	}
}

func TestHTTPMCPAuthenticationLimitUsesRemoteAddressNotForwardedHeader(t *testing.T) {
	now := time.Date(2026, time.August, 11, 19, 0, 0, 0, time.UTC)
	handler := NewHTTPHandlerWithOptions(
		Services{}, nil,
		HTTPOptions{Now: func() time.Time { return now }, limits: mcpRateLimits{cheap: 10, expensive: 10, writes: 10, authentication: 1, maximumKeys: 100}},
	)

	request := func(forwarded string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
		req.RemoteAddr = "192.0.2.44:1234"
		req.Header.Set("Authorization", "Bearer invalid-token")
		req.Header.Set("X-Forwarded-For", forwarded)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	if first := request("198.51.100.1"); first.Code != http.StatusUnauthorized {
		t.Fatalf("first status = %d", first.Code)
	}
	second := request("203.0.113.99")
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "60" {
		t.Fatalf("second status=%d headers=%v", second.Code, second.Header())
	}
}

func TestHTTPMCPProtocolLimitIsSharedAcrossSessionsPrincipalsAndClientIP(t *testing.T) {
	const secondToken = "606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f"
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{
		{Token: testMCPAgentToken, Actor: "repository-agent", Role: relations.RoleAgent},
		{Token: secondToken, Actor: "second-agent", Role: relations.RoleAgent},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 11, 20, 0, 0, 0, time.UTC)
	newHandler := func() http.Handler {
		return NewHTTPHandlerWithOptions(
			Services{}, authenticator,
			HTTPOptions{Now: func() time.Time { return now }, limits: mcpRateLimits{
				cheap: 1, expensive: 10, writes: 10, authentication: 10, maximumKeys: 100,
			}},
		)
	}
	request := func(handler http.Handler, token string, remoteAddress string, id int) *httptest.ResponseRecorder {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"rate-test","version":"1"}}}`
		req := httptest.NewRequest(http.MethodPost, "https://localhost/mcp", strings.NewReader(body))
		req.RemoteAddr = remoteAddress
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	assertLimited := func(response *httptest.ResponseRecorder) {
		t.Helper()
		if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "60" {
			t.Fatalf("limited response status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
		var envelope struct {
			JSONRPC string `json:"jsonrpc"`
			Error   struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.JSONRPC != "2.0" ||
			envelope.Error.Code == 0 || envelope.Error.Message != "too many requests" ||
			strings.Contains(response.Body.String(), testMCPAgentToken) {
			t.Fatalf("unsafe or invalid JSON-RPC rate-limit envelope = %s, error=%v", response.Body.String(), err)
		}
	}

	principalHandler := newHandler()
	if first := request(principalHandler, testMCPAgentToken, "192.0.2.10:41000", 1); first.Code != http.StatusOK {
		t.Fatalf("first principal session status=%d body=%s", first.Code, first.Body.String())
	}
	assertLimited(request(principalHandler, testMCPAgentToken, "192.0.2.11:42000", 2))

	ipHandler := newHandler()
	if first := request(ipHandler, testMCPAgentToken, "[::ffff:192.0.2.20]:41000", 3); first.Code != http.StatusOK {
		t.Fatalf("first IP session status=%d body=%s", first.Code, first.Body.String())
	}
	assertLimited(request(ipHandler, secondToken, "192.0.2.20:42000", 4))
}

func TestHTTPMCPProposalListHasExactResponseBudgetAndNoDuplicatedTextPayload(t *testing.T) {
	proposals := make([]relations.Relation, 75)
	for index := range proposals {
		proposals[index] = largeMCPProposal(int64(index + 1))
	}
	service := &proposalListBudgetStub{proposals: proposals}
	handler := NewHTTPHandler(Services{Relations: service}, nil)
	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dbgraph_list_proposals","arguments":{"limit":100}}}`,
	))
	request.RemoteAddr = "127.0.0.1:41000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("proposal list status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Body.Len() > expectedMaximumMCPResponseBytes {
		t.Fatalf("proposal response bytes=%d, budget=%d", response.Body.Len(), expectedMaximumMCPResponseBytes)
	}
	var envelope struct {
		Result struct {
			Content           []json.RawMessage `json:"content"`
			StructuredContent struct {
				Relations []json.RawMessage `json:"relations"`
				Truncated bool              `json:"truncated"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode proposal response: %v; body=%s", err, response.Body.String())
	}
	if len(envelope.Result.Content) != 0 {
		t.Fatalf("unstructured content duplicated %d-byte structured result: %s", response.Body.Len(), response.Body.String())
	}
	if len(envelope.Result.StructuredContent.Relations) == 0 ||
		len(envelope.Result.StructuredContent.Relations) >= len(proposals) ||
		!envelope.Result.StructuredContent.Truncated {
		t.Fatalf("relations=%d truncated=%v", len(envelope.Result.StructuredContent.Relations), envelope.Result.StructuredContent.Truncated)
	}
	if service.listLimit != 51 {
		t.Fatalf("repository limit=%d, want 51", service.listLimit)
	}
}

func TestHTTPMCPUnresolvedListHasExactResponseBudgetAndNoDuplicatedTextPayload(t *testing.T) {
	findings := make([]reconcile.Unresolved, 75)
	for index := range findings {
		findings[index] = largeMCPUnresolved(int64(index + 1))
	}
	service := &unresolvedListBudgetStub{findings: findings}
	handler := NewHTTPHandler(Services{Reconcile: service}, nil)
	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dbgraph_list_unresolved","arguments":{"limit":100}}}`,
	))
	request.RemoteAddr = "127.0.0.1:41000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unresolved list status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Body.Len() > expectedMaximumMCPResponseBytes {
		t.Fatalf("unresolved response bytes=%d, budget=%d", response.Body.Len(), expectedMaximumMCPResponseBytes)
	}
	var envelope struct {
		Result struct {
			Content           []json.RawMessage `json:"content"`
			StructuredContent struct {
				Findings  []json.RawMessage `json:"findings"`
				Truncated bool              `json:"truncated"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode unresolved response: %v; body=%s", err, response.Body.String())
	}
	if len(envelope.Result.Content) != 0 {
		t.Fatalf("unstructured content duplicated %d-byte structured result", response.Body.Len())
	}
	if len(envelope.Result.StructuredContent.Findings) == 0 ||
		len(envelope.Result.StructuredContent.Findings) >= len(findings) ||
		!envelope.Result.StructuredContent.Truncated {
		t.Fatalf("findings=%d truncated=%v", len(envelope.Result.StructuredContent.Findings), envelope.Result.StructuredContent.Truncated)
	}
	if service.listLimit != 51 {
		t.Fatalf("repository limit=%d, want 51", service.listLimit)
	}
}

func TestHTTPMCPAmplifiableListsUseTheSharedStrictReadBucket(t *testing.T) {
	proposalService := &proposalListBudgetStub{proposals: []relations.Relation{testRelation()}}
	unresolvedService := &unresolvedListBudgetStub{findings: []reconcile.Unresolved{largeMCPUnresolved(1)}}
	handler := NewHTTPHandlerWithOptions(
		Services{Relations: proposalService, Reconcile: unresolvedService}, nil,
		HTTPOptions{Now: time.Now, limits: mcpRateLimits{
			cheap: 100, expensive: 1, writes: 10, authentication: 10, maximumKeys: 100,
		}},
	)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	session := connectHTTPClient(t, httpServer.URL, "")
	arguments := json.RawMessage(`{}`)
	first, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "dbgraph_list_proposals", Arguments: arguments})
	if err != nil || first.IsError {
		t.Fatalf("first list result=%#v error=%v", first, err)
	}
	second, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "dbgraph_list_unresolved", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.IsError || proposalService.listCalls != 1 || unresolvedService.listCalls != 0 {
		t.Fatalf("second list error=%v proposal calls=%d unresolved calls=%d", second.IsError, proposalService.listCalls, unresolvedService.listCalls)
	}
}

func TestHTTPMCPLimitsSuccessfulWriteOutputsWithoutDuplicatingThePayload(t *testing.T) {
	large := largeMCPProposal(1)
	large.Proposed.Evidence[0].File = strings.Repeat("sensitive-marker-", 100_000)
	service := &largeWriteOutputStub{result: large}
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testMCPAgentToken, Actor: "repository-agent", Role: relations.RoleAgent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(Services{Relations: service}, authenticator)
	request := httptest.NewRequest(http.MethodPost, "https://localhost/mcp", strings.NewReader(`{
		"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
			"name":"dbgraph_propose_relation","arguments":{
				"type":"CONDITIONAL_VALUE_COPY","sourceNodeId":"11","targetNodeId":"12",
				"confidence":0.9,"transform":{"kind":"column_copy","nodeId":"11"},
				"evidence":[{"kind":"CODE","repository":"repo","commit":"abc","file":"Mapper.java","startLine":1,"endLine":2}],
				"reason":"Mapped by source assignment","requestId":"req-output-budget"
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
		t.Fatalf("write response status=%d bytes=%d body-prefix=%q", response.Code, response.Body.Len(), response.Body.String()[:min(200, response.Body.Len())])
	}
	var envelope struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || !envelope.Result.IsError ||
		len(envelope.Result.Content) != 1 || envelope.Result.Content[0].Text != errResponseBudget.Error() ||
		strings.Contains(response.Body.String(), "sensitive-marker-") || service.calls != 1 {
		t.Fatalf("unsafe write budget response=%s error=%v calls=%d", response.Body.String(), err, service.calls)
	}
}

func largeMCPProposal(id int64) relations.Relation {
	largeEvidence := make([]relations.EvidenceInput, 20)
	for index := range largeEvidence {
		largeEvidence[index] = relations.EvidenceInput{
			Kind: relations.EvidenceCode, Repository: strings.Repeat("r", 500), Commit: strings.Repeat("c", 200),
			File: strings.Repeat("f", 2_000), Symbol: strings.Repeat("s", 2_000), StartLine: 1, EndLine: 2,
		}
	}
	revision := &relations.Revision{
		ID: id + 1_000, RelationID: id, RevisionNo: 1, Kind: relations.ProposalContent,
		SourceNodeID: 11, TargetNodeID: 12,
		Transform: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 11},
		Evidence:  largeEvidence, Actor: "agent", Origin: audit.OriginAgent,
		Reason: strings.Repeat("reason", 400), RequestID: "request", CreatedAt: testMCPTime,
	}
	return relations.Relation{
		ID: id, Type: relations.TypeConditionalValueCopy, LatestRevisionNo: 1,
		Status: relations.StatusPending, Proposed: revision, CreatedAt: testMCPTime,
	}
}

func largeMCPUnresolved(id int64) reconcile.Unresolved {
	return reconcile.Unresolved{
		ID: id, RepositoryID: 2, SessionID: 3, BatchID: 4,
		Fingerprint: strings.Repeat("f", 200), Type: strings.Repeat("DYNAMIC_SQL", 10),
		Summary:  strings.Repeat("summary", 300),
		Evidence: json.RawMessage(`{"payload":"` + strings.Repeat("e", 19_980) + `"}`),
		Status:   1,
		Principal: relations.Principal{
			Actor: strings.Repeat("agent", 40), Role: relations.RoleAgent, Origin: audit.OriginAgent,
		},
		CreatedAt: testMCPTime,
	}
}
