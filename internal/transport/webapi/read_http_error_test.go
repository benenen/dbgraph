package webapi

import (
	"net/http"
	"testing"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestReadRoutesDistinguishKnownAndUnexpectedServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		services   Services
		method     string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "node search rejected", services: Services{Catalog: &catalogHTTPStub{searchErr: catalog.ErrInvalidSnapshot}}, method: http.MethodGet, path: "/api/v1/projects/10/nodes", wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "node search unexpected", services: Services{Catalog: &catalogHTTPStub{searchErr: errUnexpectedWebFailure}}, method: http.MethodGet, path: "/api/v1/projects/10/nodes", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "trace rejected", services: Services{Graph: &graphHTTPStub{err: graph.ErrInvalidTrace}}, method: http.MethodPost, path: "/api/v1/projects/10/graph-traces", body: `{"startNodeId":"11"}`, wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_TRACE"},
		{name: "trace unexpected", services: Services{Graph: &graphHTTPStub{err: errUnexpectedWebFailure}}, method: http.MethodPost, path: "/api/v1/projects/10/graph-traces", body: `{"startNodeId":"11"}`, wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "unresolved rejected", services: Services{Reconcile: &reconcileHTTPStub{unresolvedErr: reconcile.ErrInvalidInit}}, method: http.MethodGet, path: "/api/v1/projects/10/unresolved-findings", wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "unresolved unexpected", services: Services{Reconcile: &reconcileHTTPStub{unresolvedErr: errUnexpectedWebFailure}}, method: http.MethodGet, path: "/api/v1/projects/10/unresolved-findings", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "job missing", services: Services{Jobs: &jobHTTPStub{getErr: jobs.ErrJobNotFound}}, method: http.MethodGet, path: "/api/v1/projects/10/schema-scan-jobs/30", wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "job unexpected", services: Services{Jobs: &jobHTTPStub{getErr: errUnexpectedWebFailure}}, method: http.MethodGet, path: "/api/v1/projects/10/schema-scan-jobs/30", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "init missing", services: Services{Reconcile: &reconcileHTTPStub{getErr: reconcile.ErrInitNotFound}}, method: http.MethodGet, path: "/api/v1/projects/10/relation-init-sessions/40", wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "init unexpected", services: Services{Reconcile: &reconcileHTTPStub{getErr: errUnexpectedWebFailure}}, method: http.MethodGet, path: "/api/v1/projects/10/relation-init-sessions/40", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "audit rejected", services: Services{Audit: &auditHTTPStub{err: audit.ErrInvalidEvent}}, method: http.MethodGet, path: "/api/v1/projects/10/audit-events", wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "audit unexpected", services: Services{Audit: &auditHTTPStub{err: errUnexpectedWebFailure}}, method: http.MethodGet, path: "/api/v1/projects/10/audit-events", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "relation missing", services: Services{Relations: &relationHTTPStub{getErr: relations.ErrRelationNotFound}}, method: http.MethodGet, path: "/api/v1/projects/10/relations/20", wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "relation unexpected", services: Services{Relations: &relationHTTPStub{getErr: errUnexpectedWebFailure}}, method: http.MethodGet, path: "/api/v1/projects/10/relations/20", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "proposal list invalid", services: Services{Relations: &relationHTTPStub{listErr: relations.ErrInvalidCommand}}, method: http.MethodGet, path: "/api/v1/projects/10/relation-proposals", wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_RELATION"},
		{name: "proposal list unexpected", services: Services{Relations: &relationHTTPStub{listErr: errUnexpectedWebFailure}}, method: http.MethodGet, path: "/api/v1/projects/10/relation-proposals", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newWebTestClient(t, test.services, relations.RoleViewer)
			response := client.request(test.method, test.path, test.body, test.method == http.MethodPost)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func TestProjectScopedReadsHideResourcesFromOtherProjects(t *testing.T) {
	tests := []struct {
		name     string
		services Services
		path     string
	}{
		{name: "relation", services: Services{Relations: &relationHTTPStub{relation: relations.Relation{ID: 20, ProjectID: 99}}}, path: "/api/v1/projects/10/relations/20"},
		{name: "job", services: Services{Jobs: &jobHTTPStub{job: jobs.Job{ID: 30, ProjectID: 99}}}, path: "/api/v1/projects/10/schema-scan-jobs/30"},
		{name: "relation init", services: Services{Reconcile: &reconcileHTTPStub{session: reconcile.Session{ID: 40, ProjectID: 99}}}, path: "/api/v1/projects/10/relation-init-sessions/40"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newWebTestClient(t, test.services, relations.RoleViewer)
			response := client.request(http.MethodGet, test.path, "", false)
			assertWebStatus(t, response, http.StatusNotFound, "NOT_FOUND")
		})
	}
}

func TestApplicationRoutesReturnUnavailableWhenReadUseCaseIsMissing(t *testing.T) {
	client := newWebTestClient(t, Services{}, relations.RoleViewer)
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/v1/projects/10/nodes"},
		{method: http.MethodGet, path: "/api/v1/projects/10/relations/20"},
		{method: http.MethodGet, path: "/api/v1/projects/10/relation-proposals"},
		{method: http.MethodPost, path: "/api/v1/projects/10/graph-traces", body: `{"startNodeId":"11"}`},
		{method: http.MethodGet, path: "/api/v1/projects/10/unresolved-findings"},
		{method: http.MethodGet, path: "/api/v1/projects/10/schema-scan-jobs/30"},
		{method: http.MethodGet, path: "/api/v1/projects/10/relation-init-sessions/40"},
		{method: http.MethodGet, path: "/api/v1/projects/10/audit-events"},
	}
	for _, test := range tests {
		response := client.request(test.method, test.path, test.body, test.method == http.MethodPost)
		assertWebStatus(t, response, http.StatusServiceUnavailable, "UNAVAILABLE")
	}
}

func TestHTTPBoundaryRejectsInvalidPathsQueriesAndPayloads(t *testing.T) {
	client := newWebTestClient(t, Services{
		Catalog: &catalogHTTPStub{}, Relations: &relationHTTPStub{}, Graph: &graphHTTPStub{},
		Reconcile: &reconcileHTTPStub{}, Jobs: &jobHTTPStub{}, Audit: &auditHTTPStub{},
	}, relations.RoleEditor)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "invalid project ID", method: http.MethodGet, path: "/api/v1/projects/not-an-id/nodes"},
		{name: "node limit too large", method: http.MethodGet, path: "/api/v1/projects/10/nodes?limit=101"},
		{name: "proposal limit not numeric", method: http.MethodGet, path: "/api/v1/projects/10/relation-proposals?limit=x"},
		{name: "proposal limit zero", method: http.MethodGet, path: "/api/v1/projects/10/relation-proposals?limit=0"},
		{name: "proposal limit too large", method: http.MethodGet, path: "/api/v1/projects/10/relation-proposals?limit=101"},
		{name: "audit limit zero", method: http.MethodGet, path: "/api/v1/projects/10/audit-events?limit=0"},
		{name: "invalid relation ID", method: http.MethodGet, path: "/api/v1/projects/10/relations/0"},
		{name: "invalid graph direction", method: http.MethodPost, path: "/api/v1/projects/10/graph-traces", body: `{"startNodeId":"11","direction":"SIDEWAYS"}`},
		{name: "unknown JSON field", method: http.MethodPost, path: "/api/v1/projects/10/graph-traces", body: `{"startNodeId":"11","unknown":true}`},
		{name: "invalid relation type", method: http.MethodPost, path: "/api/v1/projects/10/relations", body: `{"type":"FOREIGN_KEY","sourceNodeId":"11","targetNodeId":"12","transform":{"kind":"column_copy","nodeId":"11"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := client.request(test.method, test.path, test.body, test.method == http.MethodPost)
			assertWebStatus(t, response, http.StatusBadRequest, "INVALID_REQUEST")
		})
	}
}
