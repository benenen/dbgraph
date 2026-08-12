package webapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	appauth "github.com/benenen/dbgraph/internal/auth"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestPublicLoginSessionLogoutAndSecurityHeaders(t *testing.T) {
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testWebToken, Actor: "alice", Role: relations.RoleEditor, Origin: audit.OriginWeb,
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Services{}, appauth.NewSessionManager(authenticator, func() time.Time { return testWebTime }, nil))

	loginPage := httptest.NewRecorder()
	handler.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "https://localhost/login", nil))
	assertWebStatus(t, loginPage, http.StatusOK, "")
	if !strings.Contains(loginPage.Body.String(), "Sign in to dbgraph") {
		t.Fatalf("login page body=%q", loginPage.Body.String())
	}
	assertSecurityHeaders(t, loginPage)

	loginRequest := httptest.NewRequest(http.MethodPost, "https://localhost/login", strings.NewReader(`{"token":"`+testWebToken+`"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.RemoteAddr = "192.0.2.15:5544"
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	assertWebStatus(t, loginResponse, http.StatusOK, "")
	assertSecurityHeaders(t, loginResponse)
	if loginResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("login Cache-Control=%q, want no-store", loginResponse.Header().Get("Cache-Control"))
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].Secure || !cookies[0].HttpOnly ||
		cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" {
		t.Fatalf("login cookies=%#v", cookies)
	}
	loginEnvelope := decodeWebEnvelope(t, loginResponse)
	loginData := loginEnvelope["data"].(map[string]any)
	csrf := loginData["csrfToken"].(string)
	if loginData["actor"] != "alice" || loginData["role"] != "EDITOR" || csrf == "" {
		t.Fatalf("login data=%#v", loginData)
	}

	client := webTestClient{t: t, handler: handler, cookie: cookies[0], csrf: csrf}
	sessionResponse := client.request(http.MethodGet, "/api/v1/session", "", false)
	assertWebStatus(t, sessionResponse, http.StatusOK, "")
	if sessionResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("session Cache-Control=%q, want no-store", sessionResponse.Header().Get("Cache-Control"))
	}
	sessionData := decodeWebEnvelope(t, sessionResponse)["data"].(map[string]any)
	if sessionData["actor"] != "alice" || sessionData["role"] != "EDITOR" || sessionData["csrfToken"] != csrf {
		t.Fatalf("session data=%#v", sessionData)
	}

	withoutCSRF := client.request(http.MethodPost, "/logout", `{}`, false)
	assertWebStatus(t, withoutCSRF, http.StatusForbidden, "CSRF_REJECTED")
	logout := client.request(http.MethodPost, "/logout", `{}`, true)
	assertWebStatus(t, logout, http.StatusOK, "")
	deleted := logout.Result().Cookies()
	if len(deleted) != 1 || deleted[0].MaxAge != -1 {
		t.Fatalf("logout cookies=%#v", deleted)
	}
	afterLogout := client.request(http.MethodGet, "/api/v1/session", "", false)
	assertWebStatus(t, afterLogout, http.StatusUnauthorized, "UNAUTHENTICATED")
}

func TestPublicAssetsAreVendoredAndSafelyServed(t *testing.T) {
	authenticator, err := appauth.NewTokenAuthenticator(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Services{}, appauth.NewSessionManager(authenticator, time.Now, nil))
	tests := []struct {
		name          string
		path          string
		status        int
		contentType   string
		bodyContains  string
		cacheContains string
	}{
		{name: "application JavaScript", path: "/assets/app.js", status: http.StatusOK, contentType: "javascript", bodyContains: "renderConditionNode"},
		{name: "editor controls JavaScript", path: "/assets/editor_controls.js", status: http.StatusOK, contentType: "javascript", bodyContains: "initializeStructuredEditors"},
		{name: "application CSS", path: "/assets/app.css", status: http.StatusOK, contentType: "text/css", bodyContains: ":root"},
		{name: "workflow CSS", path: "/assets/workflows.css", status: http.StatusOK, contentType: "text/css"},
		{name: "SVG favicon", path: "/assets/favicon.svg", status: http.StatusOK, contentType: "image/svg+xml", bodyContains: "<svg"},
		{name: "Cytoscape", path: "/assets/cytoscape.min.js", status: http.StatusOK, contentType: "javascript", bodyContains: "cytoscape", cacheContains: "max-age=3600"},
		{name: "missing asset", path: "/assets/missing.js", status: http.StatusNotFound},
		{name: "path traversal", path: "/assets/../handler.go", status: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://localhost"+test.path, nil))
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.status, response.Body.String())
			}
			assertSecurityHeaders(t, response)
			if test.contentType != "" && !strings.Contains(response.Header().Get("Content-Type"), test.contentType) {
				t.Fatalf("Content-Type=%q", response.Header().Get("Content-Type"))
			}
			if test.bodyContains != "" && !strings.Contains(response.Body.String(), test.bodyContains) {
				t.Fatalf("asset body does not contain %q", test.bodyContains)
			}
			if test.cacheContains != "" && !strings.Contains(response.Header().Get("Cache-Control"), test.cacheContains) {
				t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestAuthenticatedGETRoutesReturnProjectScopedResources(t *testing.T) {
	now := testWebTime
	revision := &relations.Revision{
		ID: 201, RelationID: 20, RevisionNo: 1, Kind: relations.ProposalContent,
		SourceNodeID: 11, TargetNodeID: 12,
		Transform: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 11},
		Evidence:  []relations.EvidenceInput{{Kind: relations.EvidenceManual, Repository: "repo", Commit: "abc", File: "README.md", StartLine: 1, EndLine: 1}},
		Actor:     "agent", Origin: audit.OriginAgent, Reason: "evidence", RequestID: "req", CreatedAt: now,
	}
	relation := relations.Relation{
		ID: 20, ProjectID: 10, Type: relations.TypeConditionalValueCopy,
		LatestRevisionNo: 1, Status: relations.StatusApproved, Active: revision,
		Effective: true, CreatedAt: now,
	}
	catalogNode := catalog.Node{
		ID: 11, VersionID: 111, ProjectID: 10, DataSourceID: 2, Kind: catalog.NodeColumn,
		Status: catalog.NodeActive, Name: "student_id", QualifiedName: "school.student.id", DataType: "BIGINT",
	}
	catalogService := &catalogHTTPStub{nodes: []catalog.Node{catalogNode}, node: catalogNode}
	relationService := &relationHTTPStub{relation: relation, proposals: []relations.Relation{relation}}
	reconcileService := &reconcileHTTPStub{
		session:  reconcile.Session{ID: 40, ProjectID: 10, RepositoryID: 3, Mode: reconcile.ModeFull, Status: reconcile.StatusOpen, Scope: json.RawMessage(`{}`), CreatedAt: now},
		findings: []reconcile.Unresolved{{ID: 50, ProjectID: 10, RepositoryID: 3, SessionID: 40, Type: "AMBIGUOUS_MAPPING", Summary: "Two columns match", Evidence: json.RawMessage(`{}`), CreatedAt: now}},
	}
	jobService := &jobHTTPStub{job: jobs.Job{ID: 30, ProjectID: 10, Type: jobs.TypeSchemaScan, Status: jobs.StatusPending, Payload: json.RawMessage(`{"dataSourceId":"2"}`), CreatedAt: now, RevisionNo: 1}}
	auditService := &auditHTTPStub{events: []audit.Event{{ID: 60, ProjectID: 10, Actor: "alice", Origin: audit.OriginWeb, Action: "RELATION_PROPOSED", SubjectType: "RELATION", SubjectID: 20, Reason: "update", RequestID: "web-1", Details: json.RawMessage(`{}`), OccurredAt: now}}}
	client := newWebTestClient(t, Services{
		Catalog: catalogService, Relations: relationService, Reconcile: reconcileService,
		Jobs: jobService, Audit: auditService,
	}, relations.RoleViewer)

	tests := []struct {
		name     string
		path     string
		contains string
	}{
		{name: "index", path: "/", contains: "Conditional data lineage"},
		{name: "nodes", path: "/api/v1/projects/10/nodes?q=student&limit=5", contains: "school.student.id"},
		{name: "node details", path: "/api/v1/projects/10/nodes/11", contains: "school.student.id"},
		{name: "relation", path: "/api/v1/projects/10/relations/20", contains: `"relationId":"20"`},
		{name: "proposals", path: "/api/v1/projects/10/relation-proposals?limit=5", contains: `"relations"`},
		{name: "unresolved", path: "/api/v1/projects/10/unresolved-findings?limit=5", contains: "Two columns match"},
		{name: "job", path: "/api/v1/projects/10/schema-scan-jobs/30", contains: `"SCHEMA_SCAN"`},
		{name: "relation init", path: "/api/v1/projects/10/relation-init-sessions/40", contains: `"FULL"`},
		{name: "audit", path: "/api/v1/projects/10/audit-events?limit=5", contains: "RELATION_PROPOSED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := client.request(http.MethodGet, test.path, "", false)
			assertWebStatus(t, response, http.StatusOK, "")
			if !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("body=%s does not contain %q", response.Body.String(), test.contains)
			}
		})
	}
}

func TestUnauthenticatedApplicationRequestIsRejected(t *testing.T) {
	authenticator, err := appauth.NewTokenAuthenticator(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Services{}, appauth.NewSessionManager(authenticator, time.Now, nil))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://localhost/", nil))
	assertWebStatus(t, response, http.StatusUnauthorized, "UNAUTHENTICATED")
}

func TestUnauthenticatedPageNavigationRedirectsToLogin(t *testing.T) {
	const browserAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	authenticator, err := appauth.NewTokenAuthenticator(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Services{}, appauth.NewSessionManager(authenticator, time.Now, nil))
	staleCookie := &http.Cookie{Name: sessionCookieName, Value: "expired-session-token"}

	tests := []struct {
		name     string
		method   string
		path     string
		accept   string
		cookie   *http.Cookie
		status   int
		location string
		code     string
	}{
		{
			name: "page navigation without a session", method: http.MethodGet, path: "/",
			accept: browserAccept, status: http.StatusSeeOther, location: "/login",
		},
		{
			name: "page navigation with an expired session", method: http.MethodGet, path: "/",
			accept: browserAccept, cookie: staleCookie, status: http.StatusSeeOther, location: "/login",
		},
		{
			name: "HEAD page navigation", method: http.MethodHead, path: "/",
			accept: browserAccept, status: http.StatusSeeOther, location: "/login",
		},
		{
			name: "API read stays a JSON error", method: http.MethodGet, path: "/api/v1/session",
			accept: browserAccept, status: http.StatusUnauthorized, code: "UNAUTHENTICATED",
		},
		{
			name: "API write stays a JSON error", method: http.MethodPost, path: "/api/v1/projects",
			accept: browserAccept, status: http.StatusUnauthorized, code: "UNAUTHENTICATED",
		},
		{
			name: "non-browser client stays a JSON error", method: http.MethodGet, path: "/",
			accept: "application/json", status: http.StatusUnauthorized, code: "UNAUTHENTICATED",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "https://localhost"+test.path, nil)
			request.Header.Set("Accept", test.accept)
			if test.cookie != nil {
				request.AddCookie(test.cookie)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if test.code != "" {
				assertWebStatus(t, response, test.status, test.code)
				return
			}
			if response.Code != test.status {
				t.Fatalf("status=%d body=%q, want %d", response.Code, response.Body.String(), test.status)
			}
			if location := response.Header().Get("Location"); location != test.location {
				t.Fatalf("Location=%q, want %q", location, test.location)
			}
		})
	}
}

func TestGraphTraceMapsContextualPathsAndPreservesThreeValuedResults(t *testing.T) {
	column := conditions.Value{Kind: conditions.ValueColumn, NodeID: 11}
	parameter := conditions.Value{Kind: conditions.ValueParameter, Parameter: "tenant"}
	integer := conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{Type: conditions.LiteralInteger, Value: json.RawMessage(`9007199254740993`)}}
	stringValue := conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{Type: conditions.LiteralString, Value: json.RawMessage(`"north"`)}}
	isNull := conditions.Boolean{Kind: conditions.BooleanIsNull, Left: &parameter}
	guard := &conditions.Boolean{
		Kind: conditions.BooleanAnd,
		Children: []conditions.Boolean{
			{Kind: conditions.BooleanCompare, Operator: conditions.CompareEqual, Left: &column, Right: &integer},
			{Kind: conditions.BooleanNot, Operand: &isNull},
		},
	}
	selector := &conditions.Boolean{Kind: conditions.BooleanIn, Left: &parameter, Values: []conditions.Value{stringValue}}
	otherwise := conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 11}
	transform := conditions.Value{
		Kind: conditions.ValueCase,
		Cases: []conditions.Case{{
			When: conditions.Boolean{Kind: conditions.BooleanCompare, Operator: conditions.CompareEqual, Left: &column, Right: &integer},
			Then: stringValue,
		}},
		Else: &otherwise,
	}
	step := graph.Step{
		Edge: graph.Edge{
			RelationID: 20, SourceNodeID: 11, TargetNodeID: 12, Type: relations.TypeDeclaredForeignKey,
			Status: relations.StatusApproved, HasPendingProposal: true,
			Guard: guard, Selector: selector, Transform: transform, Confidence: 0.75,
		},
		Evaluation: conditions.Evaluation{
			Truth:   conditions.TruthUnknown,
			Missing: []conditions.MissingReference{{NodeID: 11}, {Parameter: "tenant"}},
		},
	}
	service := &graphHTTPStub{result: graph.TraceResult{
		Paths: []graph.Path{
			{Nodes: []int64{11, 12}, Steps: []graph.Step{step}},
			{Nodes: []int64{11, 13}, Steps: []graph.Step{{Edge: step.Edge, Evaluation: conditions.Evaluation{Truth: conditions.TruthTrue}}}},
			{Nodes: []int64{11, 14}, Steps: []graph.Step{{Edge: step.Edge, Evaluation: conditions.Evaluation{Truth: conditions.TruthFalse}}}},
		},
		VisitedNodes: 4, CycleDetected: true, Truncated: true,
	}}
	client := newWebTestClient(t, Services{Graph: service}, relations.RoleViewer)
	response := client.request(http.MethodPost, "/api/v1/projects/10/graph-traces", `{
		"startNodeId":"11","targetNodeId":"12","direction":"UPSTREAM",
		"context":{"columns":{"11":9007199254740993},"parameters":{"tenant":"north"}},
		"maxDepth":4,"maxNodes":50,"maxPaths":6
	}`, true)
	assertWebStatus(t, response, http.StatusOK, "")
	if service.request.ProjectID != 10 || service.request.StartNodeID != 11 || service.request.TargetNodeID != 12 ||
		service.request.Direction != graph.DirectionUpstream || service.request.Limits.MaxDepth != 4 ||
		string(service.request.Context.Columns[11]) != "9007199254740993" || string(service.request.Context.Parameters["tenant"]) != `"north"` {
		t.Fatalf("trace request=%#v", service.request)
	}
	body := response.Body.String()
	for _, expected := range []string{`"truth":"UNKNOWN"`, `"truth":"TRUE"`, `"truth":"FALSE"`, `"status":"APPROVED"`, `"proposalStatus":"PROPOSED"`, `"parameter":"tenant"`, `"nodeId":"11"`, `"DECLARED_FOREIGN_KEY"`, `"cycleDetected":true`, `"truncated":true`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("trace response missing %q: %s", expected, body)
		}
	}
}

func assertSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	csp := response.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'self'", "script-src 'self'", "style-src-elem 'self' 'sha256-pgvDUBa4IjFA2yuSJ2cqcyxmNYJMborsd0ORcRv9vw8='",
		"style-src-attr 'unsafe-inline'", "frame-ancestors 'none'", "form-action 'self'",
	} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("CSP=%q missing %q", csp, directive)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("CSP=%q permits inline scripts", csp)
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" ||
		response.Header().Get("X-Frame-Options") != "DENY" ||
		response.Header().Get("Referrer-Policy") != "no-referrer" ||
		response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("security headers=%v", response.Header())
	}
}
