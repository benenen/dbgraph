package webapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/relations"
)

type repositoryHTTPStub struct {
	result       catalog.CodeRepository
	err          error
	command      catalog.AdminCreateCodeRepository
	calls        int
	repositories []catalog.CodeRepository
	listErr      error
	listLimit    int
}

func (s *repositoryHTTPStub) CreateAsAdmin(_ context.Context, command catalog.AdminCreateCodeRepository) (catalog.CodeRepository, error) {
	s.command = command
	s.calls++
	return s.result, s.err
}

func (s *repositoryHTTPStub) List(_ context.Context, limit int) ([]catalog.CodeRepository, error) {
	s.listLimit = limit
	return append([]catalog.CodeRepository(nil), s.repositories...), s.listErr
}

func TestAdminRoutesRejectNonAdminBeforeCallingServices(t *testing.T) {
	repositories := &repositoryHTTPStub{}
	catalogService := &catalogHTTPStub{}
	jobService := &jobHTTPStub{}
	client := newWebTestClient(t, Services{
		CodeRepositories: repositories, Catalog: catalogService, Jobs: jobService,
	}, relations.RoleReviewer)
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "create repository", path: "/api/v1/repositories", body: `{"name":"service","reason":"register"}`},
		{name: "create data source", path: "/api/v1/data-sources", body: `{"name":"primary","kind":"MYSQL","dsnEnvironment":"PRIMARY_DSN","reason":"register"}`},
		{name: "start schema scan", path: "/api/v1/data-sources/30/schema-scan-jobs", body: `{"reason":"scan"}`},
		{name: "update data source", path: "/api/v1/data-sources/30/update", body: `{"name":"primary","reason":"rename"}`},
		{name: "delete data source", path: "/api/v1/data-sources/30/delete", body: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := client.request(http.MethodPost, test.path, test.body, true)
			assertWebStatus(t, response, http.StatusForbidden, "FORBIDDEN")
		})
	}
	if repositories.calls != 0 || catalogService.createCommand.Name != "" ||
		catalogService.updateCommand.DataSourceID != 0 || len(catalogService.deleted) != 0 ||
		jobService.startCommand.DataSourceID != 0 {
		t.Fatalf("services called: repositories=%d catalog=%#v jobs=%#v",
			repositories.calls, catalogService.createCommand, jobService.startCommand)
	}
}

func TestAdminDataSourceErrorMappings(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "forbidden", err: catalog.ErrForbidden, wantStatus: http.StatusForbidden, wantCode: "FORBIDDEN"},
		{name: "data source missing", err: catalog.ErrDataSourceNotFound, wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "invalid data source", err: catalog.ErrInvalidDataSource, wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_COMMAND"},
		{name: "unexpected", err: errUnexpectedWebFailure, wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &catalogHTTPStub{createErr: test.err}
			client := newWebTestClient(t, Services{Catalog: service}, relations.RoleAdmin)
			response := client.request(http.MethodPost, "/api/v1/data-sources",
				`{"name":"primary","kind":"MYSQL","dsnEnvironment":"PRIMARY_DSN","reason":"register"}`, true)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func TestAdminSchemaScanErrorMappings(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "forbidden", err: jobs.ErrForbidden, wantStatus: http.StatusForbidden, wantCode: "FORBIDDEN"},
		{name: "invalid job", err: jobs.ErrInvalidJob, wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_COMMAND"},
		{name: "queue full", err: jobs.ErrQueueFull, wantStatus: http.StatusServiceUnavailable, wantCode: "QUEUE_FULL"},
		{name: "unexpected", err: errUnexpectedWebFailure, wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &jobHTTPStub{startErr: test.err}
			client := newWebTestClient(t, Services{Jobs: service}, relations.RoleAdmin)
			response := client.request(http.MethodPost, "/api/v1/data-sources/30/schema-scan-jobs", `{"reason":"scan"}`, true)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
			if test.err == jobs.ErrQueueFull && response.Header().Get("Retry-After") != "30" {
				t.Fatalf("Retry-After=%q", response.Header().Get("Retry-After"))
			}
		})
	}
}

func TestAdminCanQueueIncrementalSchemaScan(t *testing.T) {
	service := &jobHTTPStub{job: jobs.Job{ID: 41}}
	client := newWebTestClient(t, Services{Jobs: service}, relations.RoleAdmin)
	response := client.request(
		http.MethodPost,
		"/api/v1/data-sources/30/schema-scan-jobs",
		`{"mode":"INCREMENTAL","tables":["learn.orders"],"reason":"refresh changed table"}`,
		true,
	)
	assertWebStatus(t, response, http.StatusCreated, "")
	if service.startCommand.Mode != jobs.SchemaScanIncremental || len(service.startCommand.Tables) != 1 ||
		service.startCommand.Tables[0] != "learn.orders" {
		t.Fatalf("incremental schema scan command = %#v", service.startCommand)
	}
}

func TestAdminCanUpdateAndDeleteAnUnusedDataSource(t *testing.T) {
	updatedAt := testWebTime.Add(time.Minute)
	service := &catalogHTTPStub{createResult: catalog.DataSource{
		ID: 30, Name: "orders-primary", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "ORDERS_DSN", CreatedAt: testWebTime, UpdatedAt: updatedAt,
	}}
	client := newWebTestClient(t, Services{Catalog: service}, relations.RoleAdmin)

	updated := client.request(http.MethodPost, "/api/v1/data-sources/30/update",
		`{"name":"orders-primary","dsnEnvironment":"ORDERS_DSN","dsn":"mysql://secret","reason":"rotate credentials"}`, true)
	assertWebStatus(t, updated, http.StatusOK, "")
	if service.updateCommand.DataSourceID != 30 || service.updateCommand.Name != "orders-primary" ||
		service.updateCommand.DSNEnvironment != "ORDERS_DSN" || service.updateCommand.DSN != "mysql://secret" ||
		service.updateCommand.Principal.Actor != "web-test-user" || service.updateCommand.Reason != "rotate credentials" ||
		service.updateCommand.RequestID == "" {
		t.Fatalf("update command=%#v", service.updateCommand)
	}
	if strings.Contains(updated.Body.String(), "mysql://secret") {
		t.Fatalf("update response leaked write-only DSN: %s", updated.Body.String())
	}

	deleted := client.request(http.MethodPost, "/api/v1/data-sources/30/delete", `{}`, true)
	assertWebStatus(t, deleted, http.StatusOK, "")
	if len(service.deleted) != 1 || service.deleted[0] != 30 {
		t.Fatalf("deleted data sources=%v, want [30]", service.deleted)
	}
	if decodeWebEnvelope(t, deleted)["data"].(map[string]any)["deleted"] != "30" {
		t.Fatalf("delete response=%s", deleted.Body.String())
	}
}

func TestAdminDataSourceUpdateAndDeleteErrorMappings(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		service    *catalogHTTPStub
		wantStatus int
		wantCode   string
	}{
		{name: "update missing", path: "/api/v1/data-sources/30/update", service: &catalogHTTPStub{updateErr: catalog.ErrDataSourceNotFound}, wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "update name taken", path: "/api/v1/data-sources/30/update", service: &catalogHTTPStub{updateErr: catalog.ErrDataSourceNameTaken}, wantStatus: http.StatusConflict, wantCode: "NAME_TAKEN"},
		{name: "update unusable DSN", path: "/api/v1/data-sources/30/update", service: &catalogHTTPStub{updateErr: catalog.ErrUnusableDSN}, wantStatus: http.StatusUnprocessableEntity, wantCode: "UNUSABLE_DSN"},
		{name: "update missing secret key", path: "/api/v1/data-sources/30/update", service: &catalogHTTPStub{updateErr: catalog.ErrSealerUnavailable}, wantStatus: http.StatusUnprocessableEntity, wantCode: "SECRET_KEY_REQUIRED"},
		{name: "update unexpected", path: "/api/v1/data-sources/30/update", service: &catalogHTTPStub{updateErr: errUnexpectedWebFailure}, wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "delete missing", path: "/api/v1/data-sources/30/delete", service: &catalogHTTPStub{linkErr: catalog.ErrDataSourceNotFound}, wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "delete in use", path: "/api/v1/data-sources/30/delete", service: &catalogHTTPStub{linkErr: catalog.ErrDataSourceInUse}, wantStatus: http.StatusConflict, wantCode: "IN_USE"},
		{name: "delete unexpected", path: "/api/v1/data-sources/30/delete", service: &catalogHTTPStub{linkErr: errUnexpectedWebFailure}, wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newWebTestClient(t, Services{Catalog: test.service}, relations.RoleAdmin)
			body := `{}`
			if strings.Contains(test.path, "/update") {
				body = `{"name":"orders-primary","reason":"update"}`
			}
			response := client.request(http.MethodPost, test.path, body, true)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func TestAdminRoutesRejectInvalidIDsAndPayloadsBeforeCallingServices(t *testing.T) {
	repositories := &repositoryHTTPStub{}
	catalogService := &catalogHTTPStub{}
	jobService := &jobHTTPStub{}
	client := newWebTestClient(t, Services{
		CodeRepositories: repositories, Catalog: catalogService, Jobs: jobService,
	}, relations.RoleAdmin)
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "malformed repository", path: "/api/v1/repositories", body: `{`, wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "malformed data source", path: "/api/v1/data-sources", body: `{`, wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "unsupported data source kind", path: "/api/v1/data-sources", body: `{"name":"primary","kind":"POSTGRES","reason":"register"}`, wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_DATA_SOURCE"},
		{name: "malformed schema scan", path: "/api/v1/data-sources/30/schema-scan-jobs", body: `{`, wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "invalid schema scan mode", path: "/api/v1/data-sources/30/schema-scan-jobs", body: `{"mode":"DELTA","reason":"scan"}`, wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_COMMAND"},
		{name: "invalid update id", path: "/api/v1/data-sources/nope/update", body: `{"name":"primary"}`, wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "malformed update", path: "/api/v1/data-sources/30/update", body: `{`, wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "invalid delete id", path: "/api/v1/data-sources/0/delete", body: `{}`, wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := client.request(http.MethodPost, test.path, test.body, true)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
		})
	}
	if repositories.calls != 0 || catalogService.createCommand.Name != "" ||
		catalogService.updateCommand.DataSourceID != 0 || len(catalogService.deleted) != 0 ||
		jobService.startCommand.DataSourceID != 0 {
		t.Fatalf("invalid request reached a service: repositories=%d catalog=%#v jobs=%#v",
			repositories.calls, catalogService, jobService.startCommand)
	}
}

func TestAdminRepositoryMapsValidationErrors(t *testing.T) {
	tests := []struct {
		name       string
		services   func(error) Services
		path       string
		body       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name: "invalid repository", services: func(err error) Services { return Services{CodeRepositories: &repositoryHTTPStub{err: err}} },
			path: "/api/v1/repositories", body: `{"name":"service","reason":"register"}`, err: catalog.ErrInvalidRepository,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_COMMAND",
		},
		{
			name: "unexpected repository", services: func(err error) Services { return Services{CodeRepositories: &repositoryHTTPStub{err: err}} },
			path: "/api/v1/repositories", body: `{"name":"service","reason":"register"}`, err: errUnexpectedWebFailure,
			wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newWebTestClient(t, test.services(test.err), relations.RoleAdmin)
			response := client.request(http.MethodPost, test.path, test.body, true)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func TestAdminRoutesReturnUnavailableWhenUseCaseIsMissing(t *testing.T) {
	client := newWebTestClient(t, Services{}, relations.RoleAdmin)
	tests := []struct {
		path string
		body string
	}{
		{path: "/api/v1/repositories", body: `{"name":"service","reason":"register"}`},
		{path: "/api/v1/data-sources", body: `{"name":"primary","kind":"MYSQL","dsnEnvironment":"PRIMARY_DSN","reason":"register"}`},
		{path: "/api/v1/data-sources/30/schema-scan-jobs", body: `{"reason":"scan"}`},
		{path: "/api/v1/data-sources/30/update", body: `{"name":"primary","reason":"rename"}`},
		{path: "/api/v1/data-sources/30/delete", body: `{}`},
	}
	for _, test := range tests {
		response := client.request(http.MethodPost, test.path, test.body, true)
		assertWebStatus(t, response, http.StatusServiceUnavailable, "UNAVAILABLE")
	}
}
