package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	appauth "github.com/benenen/dbgraph/internal/auth"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
)

const testWebToken = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

var testWebTime = time.Date(2026, 8, 11, 12, 30, 0, 0, time.UTC)

type webTestClient struct {
	t       *testing.T
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
}

func newWebTestClient(t *testing.T, services Services, role relations.Role) webTestClient {
	t.Helper()
	authenticator, err := appauth.NewTokenAuthenticator([]appauth.Credential{{
		Token: testWebToken, Actor: "web-test-user", Role: role, Origin: audit.OriginWeb,
	}})
	if err != nil {
		t.Fatalf("configure Web authenticator: %v", err)
	}
	sessions := appauth.NewSessionManager(authenticator, func() time.Time { return testWebTime }, nil)
	handler := NewHandler(services, sessions)
	login := httptest.NewRequest(http.MethodPost, "https://localhost/login", bytes.NewBufferString(`{"token":"`+testWebToken+`"}`))
	login.Header.Set("Content-Type", "application/json")
	login.RemoteAddr = "192.0.2.10:44000"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, login)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			CSRF string `json:"csrfToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies=%d, want 1", len(cookies))
	}
	return webTestClient{t: t, handler: handler, cookie: cookies[0], csrf: envelope.Data.CSRF}
}

func (c webTestClient) request(method string, path string, body string, includeCSRF bool) *httptest.ResponseRecorder {
	c.t.Helper()
	request := httptest.NewRequest(method, "https://localhost"+path, bytes.NewBufferString(body))
	request.RemoteAddr = "192.0.2.10:44000"
	request.AddCookie(c.cookie)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if includeCSRF {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, request)
	return response
}

func decodeWebEnvelope(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope map[string]any
	decoder := json.NewDecoder(bytes.NewReader(response.Body.Bytes()))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode response status=%d body=%q: %v", response.Code, response.Body.String(), err)
	}
	return envelope
}

func webErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	envelope := decodeWebEnvelope(t, response)
	errorValue, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope=%#v", envelope)
	}
	code, _ := errorValue["code"].(string)
	return code
}

type catalogHTTPStub struct {
	tables         []catalog.TableSummary
	tableDetail    catalog.TableDetail
	tableDetailErr error
	tableDetailID  int64
	tablesSourceID int64
	tablesFilter   string
	tablesLimit    int
	tablesErr      error
	createCommand  catalog.AdminCreateDataSource
	createResult   catalog.DataSource
	createErr      error
	updateCommand  catalog.AdminUpdateDataSource
	updateErr      error
	nodes          []catalog.Node
	node           catalog.Node
	searchErr      error
	getNodeErr     error
	sources        []catalog.DataSource
	listSrcErr     error
	listSrcLimit   int
	linkErr        error
	deleted        []int64
}

func (s *catalogHTTPStub) CreateDataSourceAsAdmin(_ context.Context, command catalog.AdminCreateDataSource) (catalog.DataSource, error) {
	s.createCommand = command
	return s.createResult, s.createErr
}

func (s *catalogHTTPStub) FindCurrentNode(context.Context, int64, string) (catalog.Node, error) {
	return catalog.Node{}, catalog.ErrNodeNotFound
}

func (s *catalogHTTPStub) SearchCurrentNodes(context.Context, int64, string, int) ([]catalog.Node, error) {
	return append([]catalog.Node(nil), s.nodes...), s.searchErr
}

func (s *catalogHTTPStub) GetCurrentNode(context.Context, int64) (catalog.Node, error) {
	return s.node, s.getNodeErr
}

func (s *catalogHTTPStub) ListDataSources(context.Context, int64, int) ([]catalog.DataSource, error) {
	return append([]catalog.DataSource(nil), s.sources...), s.listSrcErr
}

func (s *catalogHTTPStub) ListAllDataSources(_ context.Context, limit int) ([]catalog.DataSource, error) {
	s.listSrcLimit = limit
	return append([]catalog.DataSource(nil), s.sources...), s.listSrcErr
}

func (s *catalogHTTPStub) UpdateDataSourceAsAdmin(_ context.Context, command catalog.AdminUpdateDataSource) (catalog.DataSource, error) {
	s.updateCommand = command
	return s.createResult, s.updateErr
}

func (s *catalogHTTPStub) DeleteDataSource(_ context.Context, dataSourceID int64) error {
	s.deleted = append(s.deleted, dataSourceID)
	return s.linkErr
}

type relationHTTPStub struct {
	relation      relations.Relation
	proposals     []relations.Relation
	getErr        error
	listErr       error
	listLimit     int
	mutationErr   error
	operation     string
	create        relations.ProposeCreate
	revision      relations.ProposeRevision
	tombstone     relations.ProposeTombstone
	review        relations.Review
	stateChange   relations.ChangeState
	mutationCalls int
}

func (s *relationHTTPStub) ProposeCreate(_ context.Context, command relations.ProposeCreate) (relations.Relation, error) {
	s.operation, s.create = "create", command
	s.mutationCalls++
	return s.relation, s.mutationErr
}

func (s *relationHTTPStub) ProposeRevision(_ context.Context, command relations.ProposeRevision) (relations.Relation, error) {
	s.operation, s.revision = "revision", command
	s.mutationCalls++
	return s.relation, s.mutationErr
}

func (s *relationHTTPStub) ProposeTombstone(_ context.Context, command relations.ProposeTombstone) (relations.Relation, error) {
	s.operation, s.tombstone = "tombstone", command
	s.mutationCalls++
	return s.relation, s.mutationErr
}

func (s *relationHTTPStub) Review(_ context.Context, command relations.Review) (relations.Relation, error) {
	s.operation, s.review = "review", command
	s.mutationCalls++
	return s.relation, s.mutationErr
}

func (s *relationHTTPStub) Suppress(_ context.Context, command relations.ChangeState) (relations.Relation, error) {
	s.operation, s.stateChange = "suppress", command
	s.mutationCalls++
	return s.relation, s.mutationErr
}

func (s *relationHTTPStub) Restore(_ context.Context, command relations.ChangeState) (relations.Relation, error) {
	s.operation, s.stateChange = "restore", command
	s.mutationCalls++
	return s.relation, s.mutationErr
}

func (s *relationHTTPStub) Get(context.Context, int64) (relations.Relation, error) {
	return s.relation, s.getErr
}

func (s *relationHTTPStub) ListProposals(_ context.Context, limit int) ([]relations.Relation, error) {
	s.listLimit = limit
	end := min(limit, len(s.proposals))
	return append([]relations.Relation(nil), s.proposals[:end]...), s.listErr
}

type graphHTTPStub struct {
	result           graph.TraceResult
	err              error
	calls            int
	request          graph.TraceRequest
	dataSourceGraph  graph.DataSourceGraph
	dataSourceErr    error
	dataSourceCalls  int
	dataSourceSource int64
}

func (s *graphHTTPStub) DataSourceGraph(
	_ context.Context,
	dataSourceID int64,
) (graph.DataSourceGraph, error) {
	s.dataSourceCalls++
	s.dataSourceSource = dataSourceID
	return s.dataSourceGraph, s.dataSourceErr
}

func (s *catalogHTTPStub) TableDetail(
	_ context.Context,
	tableID int64,
) (catalog.TableDetail, error) {
	s.tableDetailID = tableID
	return s.tableDetail, s.tableDetailErr
}

func (s *catalogHTTPStub) ListTables(
	_ context.Context,
	dataSourceID int64,
	filter string,
	limit int,
) ([]catalog.TableSummary, error) {
	s.tablesSourceID = dataSourceID
	s.tablesFilter = filter
	s.tablesLimit = limit
	return s.tables, s.tablesErr
}

func (s *graphHTTPStub) Trace(_ context.Context, request graph.TraceRequest) (graph.TraceResult, error) {
	s.calls++
	s.request = request
	return s.result, s.err
}

type reconcileHTTPStub struct {
	session       reconcile.Session
	findings      []reconcile.Unresolved
	getErr        error
	unresolvedErr error
}

func (s *reconcileHTTPStub) Get(context.Context, int64) (reconcile.Session, error) {
	return s.session, s.getErr
}

func (s *reconcileHTTPStub) ListUnresolved(context.Context, int) ([]reconcile.Unresolved, error) {
	return append([]reconcile.Unresolved(nil), s.findings...), s.unresolvedErr
}

type jobHTTPStub struct {
	job          jobs.Job
	getErr       error
	startErr     error
	startCommand jobs.StartSchemaScan
}

func (s *jobHTTPStub) Start(_ context.Context, command jobs.StartSchemaScan) (jobs.Job, error) {
	s.startCommand = command
	return s.job, s.startErr
}

func (s *jobHTTPStub) Get(context.Context, int64) (jobs.Job, error) {
	return s.job, s.getErr
}

type auditHTTPStub struct {
	events []audit.Event
	err    error
	limit  int
}

func (s *auditHTTPStub) ListEvents(_ context.Context, limit int) ([]audit.Event, error) {
	s.limit = limit
	return append([]audit.Event(nil), s.events[:min(limit, len(s.events))]...), s.err
}

func assertWebStatus(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	if code != "" && webErrorCode(t, response) != code {
		t.Fatalf("error code=%q want=%q body=%s", webErrorCode(t, response), code, response.Body.String())
	}
}

var errUnexpectedWebFailure = errors.New("unexpected Web service failure")
