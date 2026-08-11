package webapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/relations"
)

type projectHTTPStub struct {
	result  catalog.Project
	err     error
	command catalog.AdminCreateProject
	calls   int
}

func (s *projectHTTPStub) CreateAsAdmin(_ context.Context, command catalog.AdminCreateProject) (catalog.Project, error) {
	s.command = command
	s.calls++
	return s.result, s.err
}

type repositoryHTTPStub struct {
	result  catalog.CodeRepository
	err     error
	command catalog.AdminCreateCodeRepository
	calls   int
}

func (s *repositoryHTTPStub) CreateAsAdmin(_ context.Context, command catalog.AdminCreateCodeRepository) (catalog.CodeRepository, error) {
	s.command = command
	s.calls++
	return s.result, s.err
}

func TestAdminRoutesRejectNonAdminBeforeCallingServices(t *testing.T) {
	projects := &projectHTTPStub{}
	repositories := &repositoryHTTPStub{}
	catalogService := &catalogHTTPStub{}
	jobService := &jobHTTPStub{}
	client := newWebTestClient(t, Services{
		Projects: projects, CodeRepositories: repositories, Catalog: catalogService, Jobs: jobService,
	}, relations.RoleReviewer)
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "create project", path: "/api/v1/projects", body: `{"name":"Learning","reason":"create"}`},
		{name: "create repository", path: "/api/v1/projects/10/repositories", body: `{"name":"service","reason":"register"}`},
		{name: "create data source", path: "/api/v1/projects/10/data-sources", body: `{"name":"primary","kind":"MYSQL","dsnEnvironment":"PRIMARY_DSN","reason":"register"}`},
		{name: "start schema scan", path: "/api/v1/projects/10/data-sources/30/schema-scan-jobs", body: `{"reason":"scan"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := client.request(http.MethodPost, test.path, test.body, true)
			assertWebStatus(t, response, http.StatusForbidden, "FORBIDDEN")
		})
	}
	if projects.calls != 0 || repositories.calls != 0 || catalogService.createCommand.ProjectID != 0 || jobService.startCommand.ProjectID != 0 {
		t.Fatalf("services called: projects=%d repositories=%d catalog=%#v jobs=%#v",
			projects.calls, repositories.calls, catalogService.createCommand, jobService.startCommand)
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
		{name: "project missing", err: catalog.ErrProjectNotFound, wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "data source missing", err: catalog.ErrDataSourceNotFound, wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "invalid data source", err: catalog.ErrInvalidDataSource, wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_COMMAND"},
		{name: "unexpected", err: errUnexpectedWebFailure, wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &catalogHTTPStub{createErr: test.err}
			client := newWebTestClient(t, Services{Catalog: service}, relations.RoleAdmin)
			response := client.request(http.MethodPost, "/api/v1/projects/10/data-sources",
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
			response := client.request(http.MethodPost, "/api/v1/projects/10/data-sources/30/schema-scan-jobs", `{"reason":"scan"}`, true)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
			if test.err == jobs.ErrQueueFull && response.Header().Get("Retry-After") != "30" {
				t.Fatalf("Retry-After=%q", response.Header().Get("Retry-After"))
			}
		})
	}
}

func TestAdminCanQueueIncrementalSchemaScan(t *testing.T) {
	service := &jobHTTPStub{job: jobs.Job{ID: 41, ProjectID: 10}}
	client := newWebTestClient(t, Services{Jobs: service}, relations.RoleAdmin)
	response := client.request(
		http.MethodPost,
		"/api/v1/projects/10/data-sources/30/schema-scan-jobs",
		`{"mode":"INCREMENTAL","tables":["learn.orders"],"reason":"refresh changed table"}`,
		true,
	)
	assertWebStatus(t, response, http.StatusCreated, "")
	if service.startCommand.Mode != jobs.SchemaScanIncremental || len(service.startCommand.Tables) != 1 ||
		service.startCommand.Tables[0] != "learn.orders" {
		t.Fatalf("incremental schema scan command = %#v", service.startCommand)
	}
}

func TestAdminProjectAndRepositoryMapValidationAndUnexpectedErrors(t *testing.T) {
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
			name: "invalid project", services: func(err error) Services { return Services{Projects: &projectHTTPStub{err: err}} },
			path: "/api/v1/projects", body: `{"name":"Learning","reason":"create"}`, err: catalog.ErrInvalidProject,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_COMMAND",
		},
		{
			name: "invalid repository", services: func(err error) Services { return Services{CodeRepositories: &repositoryHTTPStub{err: err}} },
			path: "/api/v1/projects/10/repositories", body: `{"name":"service","reason":"register"}`, err: catalog.ErrInvalidRepository,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_COMMAND",
		},
		{
			name: "unexpected project error", services: func(err error) Services { return Services{Projects: &projectHTTPStub{err: err}} },
			path: "/api/v1/projects", body: `{"name":"Learning","reason":"create"}`, err: errUnexpectedWebFailure,
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
		{path: "/api/v1/projects", body: `{"name":"Learning","reason":"create"}`},
		{path: "/api/v1/projects/10/repositories", body: `{"name":"service","reason":"register"}`},
		{path: "/api/v1/projects/10/data-sources", body: `{"name":"primary","kind":"MYSQL","dsnEnvironment":"PRIMARY_DSN","reason":"register"}`},
		{path: "/api/v1/projects/10/data-sources/30/schema-scan-jobs", body: `{"reason":"scan"}`},
	}
	for _, test := range tests {
		response := client.request(http.MethodPost, test.path, test.body, true)
		assertWebStatus(t, response, http.StatusServiceUnavailable, "UNAVAILABLE")
	}
}
