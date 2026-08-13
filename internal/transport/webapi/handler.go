package webapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	appauth "github.com/benenen/dbgraph/internal/auth"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
)

const (
	sessionCookieName = "__Host-dbgraph-session"
	// The __Host- prefix is only valid on a Secure cookie, so cleartext mode
	// has to fall back to an unprefixed name or the browser rejects it.
	cleartextSessionCookieName = "dbgraph-session"
	loginPath                  = "/login"
	maximumJSONRequestBytes    = 256 << 10
)

type CatalogService interface {
	CreateDataSourceAsAdmin(context.Context, catalog.AdminCreateDataSource) (catalog.DataSource, error)
	FindCurrentNode(context.Context, int64, string) (catalog.Node, error)
	GetCurrentNode(context.Context, int64) (catalog.Node, error)
	SearchCurrentNodes(context.Context, int64, string, int) ([]catalog.Node, error)
	ListAllDataSources(context.Context, int) ([]catalog.DataSource, error)
	DeleteDataSource(context.Context, int64) error
	UpdateDataSourceAsAdmin(context.Context, catalog.AdminUpdateDataSource) (catalog.DataSource, error)
	ListTables(context.Context, int64, string, int) ([]catalog.TableSummary, error)
	TableDetail(context.Context, int64) (catalog.TableDetail, error)
}

type CodeRepositoryService interface {
	CreateAsAdmin(context.Context, catalog.AdminCreateCodeRepository) (catalog.CodeRepository, error)
	List(context.Context, int) ([]catalog.CodeRepository, error)
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
	DataSourceGraph(context.Context, int64) (graph.DataSourceGraph, error)
}

type ReconcileService interface {
	Get(context.Context, int64) (reconcile.Session, error)
	ListUnresolved(context.Context, int) ([]reconcile.Unresolved, error)
}

type JobService interface {
	Start(context.Context, jobs.StartSchemaScan) (jobs.Job, error)
	Get(context.Context, int64) (jobs.Job, error)
}

type AuditService interface {
	ListProject(context.Context, int) ([]audit.Event, error)
}

type Services struct {
	CodeRepositories CodeRepositoryService
	Catalog          CatalogService
	Relations        RelationService
	Graph            GraphService
	Reconcile        ReconcileService
	Jobs             JobService
	Audit            AuditService
}

type contextKey int

const sessionContextKey contextKey = 1

const requestIDContextKey contextKey = 2

type requestSession struct {
	token   string
	session appauth.Session
}

type handler struct {
	services   Services
	sessions   *appauth.SessionManager
	mux        *http.ServeMux
	cookieName string
	secure     bool
}

// Option adjusts how the Web adapter issues session cookies.
type Option func(*handler)

// WithCleartextCookies issues an unprefixed, non-Secure session cookie so a
// browser keeps it over plain HTTP. Callers must restrict this to a loopback
// listener: the session cookie, tokens, and every response travel in the clear.
// CSRF validation, HttpOnly, and SameSite=Strict are unchanged.
func WithCleartextCookies() Option {
	return func(h *handler) {
		h.cookieName = cleartextSessionCookieName
		h.secure = false
	}
}

func NewHandler(services Services, sessions *appauth.SessionManager, options ...Option) http.Handler {
	h := &handler{
		services: services, sessions: sessions, mux: http.NewServeMux(),
		cookieName: sessionCookieName, secure: true,
	}
	for _, option := range options {
		option(h)
	}
	h.mux.HandleFunc("POST /api/v1/relations", h.proposeRelation)
	h.mux.HandleFunc("POST /api/v1/repositories", h.createCodeRepository)
	h.mux.HandleFunc("GET /api/v1/relations/{relationID}", h.getRelation)
	h.mux.HandleFunc("POST /api/v1/relations/{relationID}/revisions", h.proposeRevision)
	h.mux.HandleFunc("POST /api/v1/relations/{relationID}/tombstones", h.proposeTombstone)
	h.mux.HandleFunc("POST /api/v1/relations/{relationID}/reviews", h.reviewRelation)
	h.mux.HandleFunc("POST /api/v1/relations/{relationID}/suppress", h.suppressRelation)
	h.mux.HandleFunc("POST /api/v1/relations/{relationID}/restore", h.restoreRelation)
	h.mux.HandleFunc("GET /api/v1/relation-proposals", h.listProposals)
	h.mux.HandleFunc("POST /api/v1/data-sources", h.createDataSource)
	h.mux.HandleFunc("POST /api/v1/data-sources/{dataSourceID}/schema-scan-jobs", h.startSchemaScan)
	h.mux.HandleFunc("GET /api/v1/nodes", h.searchNodes)
	h.mux.HandleFunc("GET /api/v1/nodes/{nodeID}", h.getNode)
	h.mux.HandleFunc("POST /api/v1/graph-traces", h.traceGraph)
	h.mux.HandleFunc("GET /api/v1/unresolved-findings", h.listUnresolved)
	h.mux.HandleFunc("GET /api/v1/schema-scan-jobs/{jobID}", h.getJob)
	h.mux.HandleFunc("GET /api/v1/relation-init-sessions/{sessionID}", h.getInitSession)
	h.mux.HandleFunc("GET /api/v1/audit-events", h.listAuditEvents)
	h.mux.HandleFunc("GET /api/v1/data-sources", h.listAllDataSources)
	h.mux.HandleFunc("POST /api/v1/data-sources/{dataSourceID}/delete", h.deleteDataSource)
	h.mux.HandleFunc("POST /api/v1/data-sources/{dataSourceID}/update", h.updateDataSource)
	h.mux.HandleFunc("GET /api/v1/data-sources/{dataSourceID}/tables", h.listTables)
	h.mux.HandleFunc("GET /api/v1/data-sources/{dataSourceID}/relation-graph", h.dataSourceGraph)
	h.mux.HandleFunc("GET /api/v1/tables/{tableID}", h.tableDetail)
	h.mux.HandleFunc("GET /api/v1/repositories", h.listRepositories)
	h.mux.HandleFunc("GET /api/v1/session", h.getSession)
	h.mux.HandleFunc("POST /logout", h.logout)
	protection := http.NewCrossOriginProtection()
	return protection.Handler(h)
}

func (h *handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(response)
	requestID := newRequestID()
	response.Header().Set("X-Request-ID", requestID)
	request = request.WithContext(context.WithValue(request.Context(), requestIDContextKey, requestID))
	if request.URL.Path == loginPath && request.Method == http.MethodPost {
		h.login(response, request)
		return
	}
	cookie, err := request.Cookie(h.cookieName)
	if err != nil {
		rejectUnauthenticated(response, request)
		return
	}
	session, ok := h.sessions.Get(cookie.Value)
	if !ok {
		rejectUnauthenticated(response, request)
		return
	}
	if isStateChanging(request.Method) {
		if !h.sessions.ValidateCSRF(cookie.Value, request.Header.Get("X-CSRF-Token")) {
			writeError(response, http.StatusForbidden, "CSRF_REJECTED", "request could not be verified", nil)
			return
		}
	}
	ctx := context.WithValue(request.Context(), sessionContextKey, requestSession{token: cookie.Value, session: session})
	h.mux.ServeHTTP(response, request.WithContext(ctx))
}

func (h *handler) login(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(response, request, 8<<10, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid login request", nil)
		return
	}
	token, session, err := h.sessions.Create(strings.TrimSpace(input.Token))
	if err != nil {
		writeError(response, http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid credentials", nil)
		return
	}
	http.SetCookie(response, &http.Cookie{
		Name: h.cookieName, Value: token, Path: "/", Secure: h.secure, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt, MaxAge: int(time.Until(session.ExpiresAt).Seconds()),
	})
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, map[string]any{
		"actor": session.Principal.Actor, "role": roleName(session.Principal.Role), "csrfToken": session.CSRFToken,
	})
}

func (h *handler) getSession(response http.ResponseWriter, request *http.Request) {
	session := currentSession(request)
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, map[string]any{
		"actor":     session.session.Principal.Actor,
		"role":      roleName(session.session.Principal.Role),
		"csrfToken": session.session.CSRFToken,
	})
}

func (h *handler) logout(response http.ResponseWriter, request *http.Request) {
	session := currentSession(request)
	h.sessions.Delete(session.token)
	http.SetCookie(response, &http.Cookie{
		Name: h.cookieName, Value: "", Path: "/", Secure: h.secure, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	writeJSON(response, http.StatusOK, map[string]any{"loggedOut": true})
}

func currentSession(request *http.Request) requestSession {
	session, _ := request.Context().Value(sessionContextKey).(requestSession)
	return session
}

func currentRequestID(request *http.Request) string {
	requestID, _ := request.Context().Value(requestIDContextKey).(string)
	return requestID
}

func newRequestID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("web-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", value)
}

func decodeJSON(response http.ResponseWriter, request *http.Request, limit int64, target any) error {
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON content type is required")
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeJSON(response http.ResponseWriter, statusCode int, data any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(statusCode)
	_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "data": data, "error": nil})
}

func writeError(response http.ResponseWriter, statusCode int, code string, message string, details any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(statusCode)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"success": false, "data": nil,
		"error": map[string]any{"code": code, "message": message, "details": details},
	})
}

func setSecurityHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; style-src-elem 'self' 'sha256-pgvDUBa4IjFA2yuSJ2cqcyxmNYJMborsd0ORcRv9vw8='; style-src-attr 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
}

// rejectUnauthenticated sends a browser page navigation to the console, which
// owns the sign-in form, and keeps the JSON error contract for every other
// caller: the console's fetch layer routes a 401 to its own login route.
func rejectUnauthenticated(response http.ResponseWriter, request *http.Request) {
	if isPageNavigation(request) {
		response.Header().Set("Cache-Control", "no-store")
		http.Redirect(response, request, ConsolePathPrefix+"login", http.StatusSeeOther)
		return
	}
	writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required", nil)
}

func isPageNavigation(request *http.Request) bool {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return false
	}
	if strings.HasPrefix(request.URL.Path, "/api/") {
		return false
	}
	for _, mediaRange := range strings.Split(request.Header.Get("Accept"), ",") {
		mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(mediaRange))
		if err != nil {
			continue
		}
		if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
			return true
		}
	}
	return false
}

func isStateChanging(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func roleName(role relations.Role) string {
	switch role {
	case relations.RoleViewer:
		return "VIEWER"
	case relations.RoleAgent:
		return "AGENT"
	case relations.RoleEditor:
		return "EDITOR"
	case relations.RoleReviewer:
		return "REVIEWER"
	case relations.RoleAdmin:
		return "ADMIN"
	default:
		return "UNKNOWN"
	}
}
