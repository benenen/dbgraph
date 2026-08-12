package webapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/relations"
)

const (
	expectedProposalResponseCountBudget = 50
	expectedProposalResponseByteBudget  = 1 << 20
)

func TestProposalListPublishesCountAndByteTruncation(t *testing.T) {
	t.Run("count budget", func(t *testing.T) {
		proposals := make([]relations.Relation, 75)
		for index := range proposals {
			proposals[index] = proposalForResponseBudget(int64(index+1), false)
		}
		response, service := proposalListResponse(t, proposals)
		data := decodeWebEnvelope(t, response)["data"].(map[string]any)
		items := data["relations"].([]any)
		if len(items) != expectedProposalResponseCountBudget || data["truncated"] != true {
			t.Fatalf("relations=%d truncated=%#v", len(items), data["truncated"])
		}
		if service.listLimit != expectedProposalResponseCountBudget+1 {
			t.Fatalf("repository limit=%d, want %d", service.listLimit, expectedProposalResponseCountBudget+1)
		}
	})

	t.Run("byte budget", func(t *testing.T) {
		proposals := make([]relations.Relation, 10)
		for index := range proposals {
			proposals[index] = proposalForResponseBudget(int64(index+1), true)
		}
		response, _ := proposalListResponse(t, proposals)
		data := decodeWebEnvelope(t, response)["data"].(map[string]any)
		items := data["relations"].([]any)
		if len(items) == 0 || len(items) >= len(proposals) || data["truncated"] != true {
			t.Fatalf("relations=%d truncated=%#v", len(items), data["truncated"])
		}
		if response.Body.Len() > expectedProposalResponseByteBudget {
			t.Fatalf("response bytes=%d, budget=%d", response.Body.Len(), expectedProposalResponseByteBudget)
		}
	})
}

func TestAuditListPublishesAnExactEnvelopeBudgetAndRequestsOnlyOneExtraRecord(t *testing.T) {
	events := make([]audit.Event, 75)
	for index := range events {
		events[index] = audit.Event{
			ID: int64(index + 1), ProjectID: 10, Actor: strings.Repeat("a", 200), Origin: audit.OriginWeb,
			Action: strings.Repeat("A", 100), SubjectType: strings.Repeat("S", 100), SubjectID: int64(index + 100),
			Reason: strings.Repeat("r", 2_000), RequestID: strings.Repeat("q", 200),
			Details:    json.RawMessage(`{"payload":"` + strings.Repeat("d", 19_980) + `"}`),
			OccurredAt: testWebTime,
		}
	}
	service := &auditHTTPStub{events: events}
	client := newWebTestClient(t, Services{Audit: service}, relations.RoleViewer)
	response := client.request(http.MethodGet, "/api/v1/projects/10/audit-events?limit=1000", "", false)
	assertWebStatus(t, response, http.StatusOK, "")
	if response.Body.Len() > 1<<20 {
		t.Fatalf("audit response bytes=%d, budget=%d", response.Body.Len(), 1<<20)
	}
	data := decodeWebEnvelope(t, response)["data"].(map[string]any)
	items := data["events"].([]any)
	if len(items) == 0 || len(items) >= len(events) || data["truncated"] != true {
		t.Fatalf("events=%d truncated=%#v", len(items), data["truncated"])
	}
	if service.limit != 51 {
		t.Fatalf("repository limit=%d, want 51", service.limit)
	}
}

func loginResourceLimitClient(t *testing.T, handler http.Handler, token string, remoteAddress string) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "https://localhost/login", bytes.NewBufferString(`{"token":"`+token+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remoteAddress
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	return response.Result().Cookies()[0]
}

func authenticatedResourceLimitGET(handler http.Handler, cookie *http.Cookie, remoteAddress string, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "https://localhost"+path, nil)
	request.RemoteAddr = remoteAddress
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func proposalListResponse(t *testing.T, proposals []relations.Relation) (*httptest.ResponseRecorder, *relationHTTPStub) {
	t.Helper()
	service := &relationHTTPStub{proposals: proposals}
	client := newWebTestClient(t, Services{Relations: service}, relations.RoleViewer)
	response := client.request(http.MethodGet, "/api/v1/projects/10/relation-proposals?limit=100", "", false)
	assertWebStatus(t, response, http.StatusOK, "")
	return response, service
}

func proposalForResponseBudget(id int64, large bool) relations.Relation {
	evidence := []relations.EvidenceInput{{
		Kind: relations.EvidenceCode, Repository: "repo", Commit: "commit", File: "Mapper.java",
		Symbol: "map", StartLine: 1, EndLine: 2,
	}}
	reason := "proposal"
	if large {
		evidence = make([]relations.EvidenceInput, 20)
		for index := range evidence {
			value := strings.Repeat(strconv.Itoa(index%10), 2_000)
			evidence[index] = relations.EvidenceInput{
				Kind: relations.EvidenceCode, Repository: value, Commit: value,
				File: value, Symbol: value, StartLine: 1, EndLine: 2,
			}
		}
		reason = strings.Repeat("r", 2_000)
	}
	revision := &relations.Revision{
		ID: id + 1_000, RelationID: id, RevisionNo: 1, Kind: relations.ProposalContent,
		SourceNodeID: 11, TargetNodeID: 12,
		Transform: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 11},
		Evidence:  evidence, Reason: reason, Actor: "agent", RequestID: "request",
		CreatedAt: testWebTime,
	}
	return relations.Relation{
		ID: id, ProjectID: 10, Type: relations.TypeConditionalValueCopy,
		LatestRevisionNo: 1, Status: relations.StatusPending, Proposed: revision,
		CreatedAt: testWebTime,
	}
}
