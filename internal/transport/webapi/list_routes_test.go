package webapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestListRoutesApplyRoleScopedFields(t *testing.T) {
	createdAt := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	services := func() (Services, *catalogHTTPStub, *repositoryHTTPStub) {
		catalogService := &catalogHTTPStub{sources: []catalog.DataSource{
			{ID: 30, Name: "orders-primary", Kind: catalog.DataSourceMySQL,
				DSNEnvironment: "ORDERS_DSN", CreatedAt: createdAt, UpdatedAt: createdAt},
		}}
		repositoryService := &repositoryHTTPStub{repositories: []catalog.CodeRepository{
			{ID: 40, Name: "orders-api", DefaultBranch: "main", CreatedAt: createdAt, UpdatedAt: createdAt},
		}}
		return Services{
			Catalog: catalogService, CodeRepositories: repositoryService,
		}, catalogService, repositoryService
	}

	adminServices, adminCatalog, adminRepositories := services()
	viewerServices, _, _ := services()
	admin := newWebTestClient(t, adminServices, relations.RoleAdmin)
	viewer := newWebTestClient(t, viewerServices, relations.RoleViewer)

	adminSources := admin.request(http.MethodGet, "/api/v1/data-sources", "", false)
	assertWebStatus(t, adminSources, http.StatusOK, "")
	adminSource := decodeWebEnvelope(t, adminSources)["data"].([]any)[0].(map[string]any)
	if adminSource["dsnEnvironment"] != "ORDERS_DSN" {
		t.Fatalf("admin data source = %#v, want dsnEnvironment", adminSource)
	}

	viewerSources := viewer.request(http.MethodGet, "/api/v1/data-sources", "", false)
	assertWebStatus(t, viewerSources, http.StatusOK, "")
	viewerSource := decodeWebEnvelope(t, viewerSources)["data"].([]any)[0].(map[string]any)
	if _, present := viewerSource["dsnEnvironment"]; present {
		t.Fatalf("viewer data source = %#v, must not expose dsnEnvironment", viewerSource)
	}
	if viewerSource["name"] != "orders-primary" || viewerSource["kind"] != "MYSQL" {
		t.Fatalf("viewer data source = %#v", viewerSource)
	}

	assertWebStatus(t, admin.request(http.MethodGet, "/api/v1/repositories", "", false), http.StatusOK, "")
	assertWebStatus(t, viewer.request(http.MethodGet, "/api/v1/repositories", "", false), http.StatusForbidden, "FORBIDDEN")
	if adminCatalog.listSrcLimit != catalog.DefaultListLimit || adminRepositories.listLimit != catalog.DefaultListLimit {
		t.Fatalf("list limits: data sources=%d repositories=%d", adminCatalog.listSrcLimit, adminRepositories.listLimit)
	}
}

func TestListRoutesMapMissingAndFailedServices(t *testing.T) {
	tests := []struct {
		name       string
		services   Services
		path       string
		wantStatus int
		wantCode   string
	}{
		{name: "missing data source service", services: Services{}, path: "/api/v1/data-sources", wantStatus: http.StatusServiceUnavailable, wantCode: "UNAVAILABLE"},
		{name: "failed data source list", services: Services{Catalog: &catalogHTTPStub{listSrcErr: errUnexpectedWebFailure}}, path: "/api/v1/data-sources", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "missing repository service", services: Services{}, path: "/api/v1/repositories", wantStatus: http.StatusServiceUnavailable, wantCode: "UNAVAILABLE"},
		{name: "failed repository list", services: Services{CodeRepositories: &repositoryHTTPStub{listErr: errUnexpectedWebFailure}}, path: "/api/v1/repositories", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newWebTestClient(t, test.services, relations.RoleAdmin)
			response := client.request(http.MethodGet, test.path, "", false)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func TestNodeSearchRejectsAnUnparseableDataSourceID(t *testing.T) {
	client := newWebTestClient(t, Services{Catalog: &catalogHTTPStub{}}, relations.RoleViewer)
	response := client.request(http.MethodGet, "/api/v1/nodes?q=user&dataSourceId=abc", "", false)
	assertWebStatus(t, response, http.StatusBadRequest, "INVALID_REQUEST")
}
