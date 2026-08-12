package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	appauth "github.com/benenen/dbgraph/internal/auth"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/relations"
)

type relationStub struct {
	RelationService
	calls     int
	principal relations.Principal
}

type adminCatalogStub struct {
	CatalogService
	command catalog.AdminCreateDataSource
}

type adminProjectStub struct {
	ProjectService
	command catalog.AdminCreateProject
}

func (s *adminProjectStub) CreateAsAdmin(_ context.Context, command catalog.AdminCreateProject) (catalog.Project, error) {
	s.command = command
	return catalog.Project{ID: 21, Name: command.Name, Description: command.Description}, nil
}

type adminCodeRepositoryStub struct {
	CodeRepositoryService
	command catalog.AdminCreateCodeRepository
}

func (s *adminCodeRepositoryStub) CreateAsAdmin(_ context.Context, command catalog.AdminCreateCodeRepository) (catalog.CodeRepository, error) {
	s.command = command
	return catalog.CodeRepository{ID: 35, ProjectID: command.ProjectID, Name: command.Name, DefaultBranch: command.DefaultBranch}, nil
}

func (s *adminCatalogStub) CreateDataSourceAsAdmin(_ context.Context, command catalog.AdminCreateDataSource) (catalog.DataSource, error) {
	s.command = command
	return catalog.DataSource{
		ID: 31, Name: command.Name,
		Kind: command.Kind, DSNEnvironment: command.DSNEnvironment,
	}, nil
}

type adminJobStub struct {
	JobService
	command jobs.StartSchemaScan
}

func (s *adminJobStub) Start(_ context.Context, command jobs.StartSchemaScan) (jobs.Job, error) {
	s.command = command
	return jobs.Job{ID: 41, ProjectID: command.ProjectID, Type: jobs.TypeSchemaScan, Status: jobs.StatusPending, RevisionNo: 1}, nil
}

func (s *relationStub) ProposeCreate(_ context.Context, command relations.ProposeCreate) (relations.Relation, error) {
	s.calls++
	s.principal = command.Principal
	return relations.Relation{
		ID: 9_007_199_254_740_993, ProjectID: command.ProjectID, Type: command.Type,
		LatestRevisionNo: 1, Status: relations.StatusPending,
	}, nil
}

func TestWebLoginCreatesSecureSessionAndCSRFProtectsRelationProposal(t *testing.T) {
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testWebToken, Actor: "alice", Role: relations.RoleEditor, Origin: audit.OriginWeb,
	}})
	if err != nil {
		t.Fatal(err)
	}
	sessions := appauth.NewSessionManager(authenticator, func() time.Time {
		return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	}, bytes.NewReader(bytes.Repeat([]byte{3}, 128)))
	relationService := &relationStub{}
	handler := NewHandler(Services{Relations: relationService}, sessions)

	login := httptest.NewRequest(http.MethodPost, "https://localhost/login", bytes.NewBufferString(`{"token":"`+testWebToken+`"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var loginBody struct {
		Data struct {
			CSRFToken string `json:"csrfToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginBody); err != nil {
		t.Fatal(err)
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookies = %#v", cookies)
	}

	body := `{
		"type":"CONDITIONAL_VALUE_COPY",
		"sourceNodeId":"11",
		"targetNodeId":"12",
		"transform":{"kind":"column_copy","nodeId":"11"},
		"confidence":0.9,
		"evidence":[{"kind":"CODE","repository":"repo","commit":"abc","file":"Mapper.java","startLine":1,"endLine":2}],
		"reason":"Mapped by source assignment"
	}`
	withoutCSRF := httptest.NewRequest(http.MethodPost, "https://localhost/api/v1/projects/9007199254740993/relations", bytes.NewBufferString(body))
	withoutCSRF.Header.Set("Content-Type", "application/json")
	withoutCSRF.AddCookie(cookies[0])
	withoutCSRFResponse := httptest.NewRecorder()
	handler.ServeHTTP(withoutCSRFResponse, withoutCSRF)
	if withoutCSRFResponse.Code != http.StatusForbidden || relationService.calls != 0 {
		t.Fatalf("without CSRF status=%d calls=%d", withoutCSRFResponse.Code, relationService.calls)
	}

	request := httptest.NewRequest(http.MethodPost, "https://localhost/api/v1/projects/9007199254740993/relations", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", loginBody.Data.CSRFToken)
	request.AddCookie(cookies[0])
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || relationService.calls != 1 {
		t.Fatalf("proposal status=%d calls=%d body=%s", response.Code, relationService.calls, response.Body.String())
	}
	if relationService.principal.Actor != "alice" || relationService.principal.Role != relations.RoleEditor ||
		relationService.principal.Origin != audit.OriginWeb {
		t.Fatalf("principal = %#v", relationService.principal)
	}
}

func TestRelationResponseUsesBrowserSafeCamelCaseDTOs(t *testing.T) {
	revision := &relations.Revision{
		ID: 21, RelationID: 20, RevisionNo: 1, Kind: relations.ProposalContent,
		SourceNodeID: 11, TargetNodeID: 12,
		Transform: conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
			Type: conditions.LiteralInteger, Value: json.RawMessage(`9007199254740993`),
		}},
		Evidence: []relations.EvidenceInput{{
			Kind: relations.EvidenceCode, Repository: "repo", Commit: "abc", File: "Mapper.java",
			Symbol: "map", StartLine: 3, EndLine: 5,
		}},
		CreatedAt: time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC),
	}

	encoded, err := json.Marshal(mapRelation(relations.Relation{
		ID: 20, ProjectID: 10, Type: relations.TypeConditionalValueCopy,
		LatestRevisionNo: 1, Status: relations.StatusApproved, Active: revision,
		CreatedAt: revision.CreatedAt,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&output); err != nil {
		t.Fatal(err)
	}
	active := output["active"].(map[string]any)
	transform := active["transform"].(map[string]any)
	if transform["valueType"] != "integer" || transform["value"] != "9007199254740993" {
		t.Fatalf("integer literal = %#v, want canonical flat decimal string", transform)
	}
	evidence := active["evidence"].([]any)[0].(map[string]any)
	if evidence["startLine"] != json.Number("3") || evidence["repository"] != "repo" {
		t.Fatalf("evidence DTO = %#v", evidence)
	}
	if _, leaked := evidence["StartLine"]; leaked {
		t.Fatalf("evidence leaked Go field names: %#v", evidence)
	}
}

func TestRejectedCreateHasExplicitAPIStatus(t *testing.T) {
	mapped := mapRelation(relations.Relation{
		ID: 20, ProjectID: 10, Type: relations.TypeConditionalValueCopy,
		LatestRevisionNo: 1, Status: relations.StatusPending,
	})
	if mapped["status"] != "REJECTED" || mapped["proposed"] != nil || mapped["active"] != nil {
		t.Fatalf("rejected relation DTO = %#v", mapped)
	}
}

func TestNodeKindNameHandlesUnknownValues(t *testing.T) {
	for _, kind := range []catalog.NodeKind{-1, 0, 99} {
		if got := nodeKindName(kind); got != "UNKNOWN" {
			t.Fatalf("nodeKindName(%d) = %q, want UNKNOWN", kind, got)
		}
	}
}

func TestWebAdminCreatesDataSourceAndStartsRealScanUseCase(t *testing.T) {
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testWebToken, Actor: "sre", Role: relations.RoleAdmin, Origin: audit.OriginWeb,
	}})
	if err != nil {
		t.Fatal(err)
	}
	sessions := appauth.NewSessionManager(authenticator, time.Now, bytes.NewReader(bytes.Repeat([]byte{7}, 128)))
	catalogService := &adminCatalogStub{}
	projectService := &adminProjectStub{}
	codeRepositoryService := &adminCodeRepositoryStub{}
	jobService := &adminJobStub{}
	handler := NewHandler(Services{
		Projects: projectService, CodeRepositories: codeRepositoryService,
		Catalog: catalogService, Jobs: jobService,
	}, sessions)

	login := httptest.NewRequest(http.MethodPost, "https://localhost/login", bytes.NewBufferString(`{"token":"`+testWebToken+`"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	var loginBody struct {
		Data struct {
			CSRF string `json:"csrfToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginBody); err != nil {
		t.Fatal(err)
	}
	cookie := loginResponse.Result().Cookies()[0]
	postAdmin := func(path string, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "https://localhost"+path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", loginBody.Data.CSRF)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	projectResponse := postAdmin("/api/v1/projects", `{"name":"Learning","description":"Lineage","reason":"Initialize"}`)
	if projectResponse.Code != http.StatusCreated || projectService.command.Principal.Actor != "sre" || projectService.command.RequestID == "" {
		t.Fatalf("create project status=%d body=%s command=%#v", projectResponse.Code, projectResponse.Body.String(), projectService.command)
	}
	repositoryResponse := postAdmin("/api/v1/projects/21/repositories", `{"name":"service","remoteUrl":"https://example.test/service.git","defaultBranch":"main","reason":"Register evidence"}`)
	if repositoryResponse.Code != http.StatusCreated || codeRepositoryService.command.ProjectID != 21 || codeRepositoryService.command.Principal.Actor != "sre" {
		t.Fatalf("create repository status=%d body=%s command=%#v", repositoryResponse.Code, repositoryResponse.Body.String(), codeRepositoryService.command)
	}

	dataSourceRequest := httptest.NewRequest(
		http.MethodPost,
		"https://localhost/api/v1/projects/21/data-sources",
		bytes.NewBufferString(`{"name":"primary","kind":"MYSQL","dsnEnvironment":"PRIMARY_MYSQL_DSN","reason":"Configure source"}`),
	)
	dataSourceRequest.Header.Set("Content-Type", "application/json")
	dataSourceRequest.Header.Set("X-CSRF-Token", loginBody.Data.CSRF)
	dataSourceRequest.AddCookie(cookie)
	dataSourceResponse := httptest.NewRecorder()
	handler.ServeHTTP(dataSourceResponse, dataSourceRequest)
	if dataSourceResponse.Code != http.StatusCreated {
		t.Fatalf("create data source status=%d body=%s", dataSourceResponse.Code, dataSourceResponse.Body.String())
	}
	if catalogService.command.Principal.Actor != "sre" || catalogService.command.RequestID == "" || catalogService.command.DSNEnvironment != "PRIMARY_MYSQL_DSN" {
		t.Fatalf("data source command = %#v", catalogService.command)
	}

	scanRequest := httptest.NewRequest(
		http.MethodPost,
		"https://localhost/api/v1/projects/21/data-sources/31/schema-scan-jobs",
		bytes.NewBufferString(`{"reason":"Refresh metadata"}`),
	)
	scanRequest.Header.Set("Content-Type", "application/json")
	scanRequest.Header.Set("X-CSRF-Token", loginBody.Data.CSRF)
	scanRequest.AddCookie(cookie)
	scanResponse := httptest.NewRecorder()
	handler.ServeHTTP(scanResponse, scanRequest)
	if scanResponse.Code != http.StatusCreated {
		t.Fatalf("start scan status=%d body=%s", scanResponse.Code, scanResponse.Body.String())
	}
	if jobService.command.ProjectID != 21 || jobService.command.DataSourceID != 31 ||
		jobService.command.Principal.Actor != "sre" || jobService.command.RequestID == "" {
		t.Fatalf("schema scan command = %#v", jobService.command)
	}
}

func TestWebLoginIsRateLimitedByRemoteAddress(t *testing.T) {
	authenticator, err := appauth.NewTokenAuthenticator(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Services{}, appauth.NewSessionManager(authenticator, time.Now, nil))
	for attempt := 1; attempt <= maximumLoginAttemptsPerMinute+1; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "https://localhost/login", bytes.NewBufferString(`{"token":"wrong"}`))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "192.0.2.44:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if attempt > maximumLoginAttemptsPerMinute {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d status=%d want=%d body=%s", attempt, response.Code, want, response.Body.String())
		}
	}
}

func TestWebWriteRateLimitIsSharedAcrossSessionsForPrincipal(t *testing.T) {
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testWebToken, Actor: "alice", Role: relations.RoleEditor, Origin: audit.OriginWeb,
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(
		Services{Relations: &relationHTTPStub{relation: relations.Relation{ID: 20, ProjectID: 10}}},
		appauth.NewSessionManager(authenticator, time.Now, nil),
	)
	login := func(remoteAddress string) (*http.Cookie, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "https://localhost/login", bytes.NewBufferString(`{"token":"`+testWebToken+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = remoteAddress
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("login from %s status=%d body=%s", remoteAddress, response.Code, response.Body.String())
		}
		data := decodeWebEnvelope(t, response)["data"].(map[string]any)
		return response.Result().Cookies()[0], data["csrfToken"].(string)
	}
	requestWrite := func(remoteAddress string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(
			http.MethodPost,
			"https://localhost/api/v1/projects/10/relations",
			bytes.NewBufferString(validRelationBody),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.RemoteAddr = remoteAddress
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	firstCookie, firstCSRF := login("192.0.2.10:41000")
	secondCookie, secondCSRF := login("192.0.2.11:41000")
	for attempt := 0; attempt < maximumWritesPerMinute; attempt++ {
		response := requestWrite("192.0.2.10:41000", firstCookie, firstCSRF)
		if response.Code != http.StatusCreated {
			t.Fatalf("principal write %d status=%d body=%s", attempt+1, response.Code, response.Body.String())
		}
	}
	limited := requestWrite("192.0.2.11:41000", secondCookie, secondCSRF)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("second session status=%d, want %d; body=%s", limited.Code, http.StatusTooManyRequests, limited.Body.String())
	}
}

func TestWebWriteRateLimitIsSharedAcrossPrincipalsForClientIP(t *testing.T) {
	const secondToken = "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{
		{Token: testWebToken, Actor: "alice", Role: relations.RoleEditor, Origin: audit.OriginWeb},
		{Token: secondToken, Actor: "bob", Role: relations.RoleEditor, Origin: audit.OriginWeb},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(
		Services{Relations: &relationHTTPStub{relation: relations.Relation{ID: 20, ProjectID: 10}}},
		appauth.NewSessionManager(authenticator, time.Now, nil),
	)
	login := func(token string) (*http.Cookie, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "https://localhost/login", bytes.NewBufferString(`{"token":"`+token+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "192.0.2.20:42000"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
		}
		data := decodeWebEnvelope(t, response)["data"].(map[string]any)
		return response.Result().Cookies()[0], data["csrfToken"].(string)
	}
	requestWrite := func(cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(
			http.MethodPost,
			"https://localhost/api/v1/projects/10/relations",
			bytes.NewBufferString(validRelationBody),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.RemoteAddr = "192.0.2.20:42000"
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	firstCookie, firstCSRF := login(testWebToken)
	secondCookie, secondCSRF := login(secondToken)
	for attempt := 0; attempt < maximumWritesPerMinute; attempt++ {
		response := requestWrite(firstCookie, firstCSRF)
		if response.Code != http.StatusCreated {
			t.Fatalf("IP write %d status=%d body=%s", attempt+1, response.Code, response.Body.String())
		}
	}
	limited := requestWrite(secondCookie, secondCSRF)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("second principal status=%d, want %d; body=%s", limited.Code, http.StatusTooManyRequests, limited.Body.String())
	}
}
