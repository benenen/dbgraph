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
	services := func() Services {
		return Services{
			Projects: &projectHTTPStub{projects: []catalog.Project{
				{ID: 10, Name: "orders", Description: "order domain", CreatedAt: createdAt, UpdatedAt: createdAt},
			}},
			Catalog: &catalogHTTPStub{sources: []catalog.DataSource{
				{ID: 30, Name: "orders-primary", Kind: catalog.DataSourceMySQL,
					DSNEnvironment: "ORDERS_DSN", CreatedAt: createdAt, UpdatedAt: createdAt},
			}},
			CodeRepositories: &repositoryHTTPStub{repositories: []catalog.CodeRepository{
				{ID: 40, ProjectID: 10, Name: "orders-api", DefaultBranch: "main", CreatedAt: createdAt, UpdatedAt: createdAt},
			}},
		}
	}

	admin := newWebTestClient(t, services(), relations.RoleAdmin)
	viewer := newWebTestClient(t, services(), relations.RoleViewer)

	projects := admin.request(http.MethodGet, "/api/v1/projects", "", false)
	assertWebStatus(t, projects, http.StatusOK, "")
	rows := decodeWebEnvelope(t, projects)["data"].([]any)
	first := rows[0].(map[string]any)
	if first["id"] != "10" || first["name"] != "orders" {
		t.Fatalf("project row = %#v, want a string id", first)
	}

	viewerProjects := viewer.request(http.MethodGet, "/api/v1/projects", "", false)
	assertWebStatus(t, viewerProjects, http.StatusOK, "")

	adminSources := admin.request(http.MethodGet, "/api/v1/projects/10/data-sources", "", false)
	assertWebStatus(t, adminSources, http.StatusOK, "")
	adminSource := decodeWebEnvelope(t, adminSources)["data"].([]any)[0].(map[string]any)
	if adminSource["dsnEnvironment"] != "ORDERS_DSN" {
		t.Fatalf("admin data source = %#v, want dsnEnvironment", adminSource)
	}

	viewerSources := viewer.request(http.MethodGet, "/api/v1/projects/10/data-sources", "", false)
	assertWebStatus(t, viewerSources, http.StatusOK, "")
	viewerSource := decodeWebEnvelope(t, viewerSources)["data"].([]any)[0].(map[string]any)
	if _, present := viewerSource["dsnEnvironment"]; present {
		t.Fatalf("viewer data source = %#v, must not expose dsnEnvironment", viewerSource)
	}
	if viewerSource["name"] != "orders-primary" || viewerSource["kind"] != "MYSQL" {
		t.Fatalf("viewer data source = %#v", viewerSource)
	}

	assertWebStatus(t, admin.request(http.MethodGet, "/api/v1/projects/10/repositories", "", false), http.StatusOK, "")
	assertWebStatus(t, viewer.request(http.MethodGet, "/api/v1/projects/10/repositories", "", false), http.StatusForbidden, "FORBIDDEN")

	assertWebStatus(t, admin.request(http.MethodGet, "/api/v1/projects/10/data-sources?limit=0", "", false),
		http.StatusBadRequest, "INVALID_REQUEST")
}

func TestNodeSearchRejectsAnUnparseableDataSourceID(t *testing.T) {
	client := newWebTestClient(t, Services{Catalog: &catalogHTTPStub{}}, relations.RoleViewer)
	response := client.request(http.MethodGet, "/api/v1/projects/10/nodes?q=user&dataSourceId=abc", "", false)
	assertWebStatus(t, response, http.StatusBadRequest, "INVALID_REQUEST")
}
