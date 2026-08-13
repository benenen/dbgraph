package webapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/relations"
)

type recordingSchemaScanService struct {
	calls int
}

func (s *recordingSchemaScanService) Start(context.Context, jobs.StartSchemaScan) (jobs.Job, error) {
	s.calls++
	return jobs.Job{ID: 1}, nil
}

func (s *recordingSchemaScanService) Get(context.Context, int64) (jobs.Job, error) {
	return jobs.Job{}, nil
}

func TestSchemaScanRejectsAnInvalidDataSourceIDBeforeCallingTheService(t *testing.T) {
	jobService := &recordingSchemaScanService{}
	client := newWebTestClient(t, Services{Jobs: jobService}, relations.RoleAdmin)

	response := client.request(
		http.MethodPost,
		"/api/v1/data-sources/not-an-id/schema-scan-jobs",
		`{"reason":"scan"}`,
		true,
	)

	assertWebStatus(t, response, http.StatusBadRequest, "INVALID_REQUEST")
	if jobService.calls != 0 {
		t.Fatalf("schema scan service called %d times", jobService.calls)
	}
}
