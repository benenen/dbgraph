package webapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/relations"
)

const (
	validRelationBody = `{
		"type":"CONDITIONAL_VALUE_COPY",
		"sourceNodeId":"11",
		"targetNodeId":"12",
		"guard":{"kind":"compare","operator":"eq","left":{"kind":"column","nodeId":"11"},"right":{"kind":"literal","valueType":"integer","value":"9007199254740993"}},
		"selector":{"kind":"in","left":{"kind":"parameter","parameter":"tenant"},"values":[{"kind":"literal","valueType":"string","value":"north"}]},
		"transform":{"kind":"column_copy","nodeId":"11"},
		"confidence":0.9,
		"evidence":[{"kind":"CODE","repository":"repo","commit":"abc","file":"Mapper.java","symbol":"map","startLine":1,"endLine":2}],
		"reason":"Mapped by source assignment"
	}`
	validRevisionBody = `{
		"sourceNodeId":"11",
		"targetNodeId":"12",
		"transform":{"kind":"case","cases":[{"when":{"kind":"compare","operator":"eq","left":{"kind":"column","nodeId":"11"},"right":{"kind":"literal","valueType":"integer","value":"1"}},"then":{"kind":"literal","valueType":"decimal","value":"2.5"}}],"else":{"kind":"literal","valueType":"null","value":null}},
		"confidence":0.8,
		"evidence":[{"kind":"SQL_MAPPING","repository":"repo","commit":"def","file":"Mapper.xml","startLine":3,"endLine":5}],
		"expectedRevisionNo":4,
		"reason":"Revise transform"
	}`
)

func TestRelationMutationRoutesUseSharedCommands(t *testing.T) {
	tests := []struct {
		name       string
		role       relations.Role
		path       string
		body       string
		wantStatus int
		operation  string
	}{
		{name: "create", role: relations.RoleEditor, path: "/api/v1/relations", body: validRelationBody, wantStatus: http.StatusCreated, operation: "create"},
		{name: "revise", role: relations.RoleEditor, path: "/api/v1/relations/20/revisions", body: validRevisionBody, wantStatus: http.StatusCreated, operation: "revision"},
		{name: "tombstone", role: relations.RoleEditor, path: "/api/v1/relations/20/tombstones", body: `{"expectedRevisionNo":4,"reason":"No longer present"}`, wantStatus: http.StatusCreated, operation: "tombstone"},
		{name: "approve", role: relations.RoleReviewer, path: "/api/v1/relations/20/reviews", body: `{"expectedRevisionNo":4,"decision":"APPROVE","reason":"Evidence verified"}`, wantStatus: http.StatusOK, operation: "review"},
		{name: "reject", role: relations.RoleAdmin, path: "/api/v1/relations/20/reviews", body: `{"expectedRevisionNo":4,"decision":"REJECT","reason":"Evidence contradicted"}`, wantStatus: http.StatusOK, operation: "review"},
		{name: "suppress", role: relations.RoleReviewer, path: "/api/v1/relations/20/suppress", body: `{"expectedRevisionNo":4,"reason":"Temporarily hidden"}`, wantStatus: http.StatusOK, operation: "suppress"},
		{name: "restore", role: relations.RoleAdmin, path: "/api/v1/relations/20/restore", body: `{"expectedRevisionNo":4,"reason":"Visibility restored"}`, wantStatus: http.StatusOK, operation: "restore"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &relationHTTPStub{relation: relations.Relation{
				ID: 20, Type: relations.TypeConditionalValueCopy,
				LatestRevisionNo: 4, Status: relations.StatusPending, CreatedAt: testWebTime,
			}}
			client := newWebTestClient(t, Services{Relations: service}, test.role)
			response := client.request(http.MethodPost, test.path, test.body, true)
			assertWebStatus(t, response, test.wantStatus, "")
			if service.operation != test.operation || service.mutationCalls != 1 {
				t.Fatalf("operation=%q calls=%d", service.operation, service.mutationCalls)
			}
			principal, reason, requestID, expectedRevision := capturedMutationMetadata(service)
			if principal.Actor != "web-test-user" || principal.Role != test.role || principal.Origin != audit.OriginWeb {
				t.Fatalf("principal=%#v", principal)
			}
			if reason == "" || requestID == "" {
				t.Fatalf("reason=%q requestID=%q", reason, requestID)
			}
			if test.operation != "create" && expectedRevision != 4 {
				t.Fatalf("expectedRevisionNo=%d, want 4", expectedRevision)
			}
			if test.operation == "create" {
				if service.create.SourceNodeID != 11 || service.create.TargetNodeID != 12 ||
					service.create.Guard == nil || service.create.Selector == nil || len(service.create.Evidence) != 1 {
					t.Fatalf("create command=%#v", service.create)
				}
			}
			if test.operation == "revision" {
				if len(service.revision.Transform.Cases) != 1 || service.revision.Transform.Else == nil ||
					service.revision.Evidence[0].Kind != relations.EvidenceSQLMapping {
					t.Fatalf("revision command=%#v", service.revision)
				}
			}
			if test.name == "approve" && service.review.Decision != relations.DecisionApprove {
				t.Fatalf("review decision=%v", service.review.Decision)
			}
			if test.name == "reject" && service.review.Decision != relations.DecisionReject {
				t.Fatalf("review decision=%v", service.review.Decision)
			}
		})
	}
}

func TestRelationMutationsEnforceRoleAndCSRFBeforeServiceCalls(t *testing.T) {
	tests := []struct {
		name string
		role relations.Role
		path string
		body string
	}{
		{name: "viewer cannot propose", role: relations.RoleViewer, path: "/api/v1/relations", body: validRelationBody},
		{name: "editor cannot review", role: relations.RoleEditor, path: "/api/v1/relations/20/reviews", body: `{"expectedRevisionNo":4,"decision":"APPROVE","reason":"review"}`},
		{name: "reviewer cannot create", role: relations.RoleReviewer, path: "/api/v1/relations", body: validRelationBody},
		{name: "reviewer cannot tombstone", role: relations.RoleReviewer, path: "/api/v1/relations/20/tombstones", body: `{"expectedRevisionNo":4,"reason":"remove"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &relationHTTPStub{relation: relations.Relation{ID: 20}}
			client := newWebTestClient(t, Services{Relations: service}, test.role)
			response := client.request(http.MethodPost, test.path, test.body, true)
			assertWebStatus(t, response, http.StatusForbidden, "FORBIDDEN")
			if service.mutationCalls != 0 {
				t.Fatalf("mutation calls=%d", service.mutationCalls)
			}
		})
	}

	service := &relationHTTPStub{relation: relations.Relation{ID: 20}}
	client := newWebTestClient(t, Services{Relations: service}, relations.RoleAdmin)
	withoutCSRF := client.request(http.MethodPost, "/api/v1/relations/20/restore", `{"expectedRevisionNo":4,"reason":"restore"}`, false)
	assertWebStatus(t, withoutCSRF, http.StatusForbidden, "CSRF_REJECTED")
	if service.mutationCalls != 0 {
		t.Fatalf("mutation calls=%d", service.mutationCalls)
	}
}

func TestRelationDomainErrorsHaveStableHTTPMappings(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "forbidden", err: relations.ErrForbidden, wantStatus: http.StatusForbidden, wantCode: "FORBIDDEN"},
		{name: "stale revision", err: &relations.RevisionConflictError{CurrentRevisionNo: 7}, wantStatus: http.StatusConflict, wantCode: "REVISION_CONFLICT"},
		{name: "not found", err: relations.ErrRelationNotFound, wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "pending proposal", err: relations.ErrPendingProposal, wantStatus: http.StatusConflict, wantCode: "RELATION_CONFLICT"},
		{name: "duplicate", err: relations.ErrDuplicateRelation, wantStatus: http.StatusConflict, wantCode: "RELATION_CONFLICT"},
		{name: "invalid command", err: relations.ErrInvalidCommand, wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_RELATION"},
		{name: "invalid transition", err: relations.ErrInvalidTransition, wantStatus: http.StatusUnprocessableEntity, wantCode: "INVALID_RELATION"},
		{name: "unexpected", err: errUnexpectedWebFailure, wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &relationHTTPStub{
				relation:    relations.Relation{ID: 20, Type: relations.TypeConditionalValueCopy, CreatedAt: testWebTime},
				mutationErr: test.err,
			}
			client := newWebTestClient(t, Services{Relations: service}, relations.RoleEditor)
			response := client.request(http.MethodPost, "/api/v1/relations", validRelationBody, true)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
			if test.wantCode == "REVISION_CONFLICT" {
				details := decodeWebEnvelope(t, response)["error"].(map[string]any)["details"].(map[string]any)
				if details["currentRevisionNo"] != json.Number("7") {
					t.Fatalf("details=%#v", details)
				}
			}
		})
	}
}

func capturedMutationMetadata(service *relationHTTPStub) (relations.Principal, string, string, int) {
	switch service.operation {
	case "create":
		return service.create.Principal, service.create.Reason, service.create.RequestID, 0
	case "revision":
		return service.revision.Principal, service.revision.Reason, service.revision.RequestID, service.revision.ExpectedRevisionNo
	case "tombstone":
		return service.tombstone.Principal, service.tombstone.Reason, service.tombstone.RequestID, service.tombstone.ExpectedRevisionNo
	case "review":
		return service.review.Principal, service.review.Reason, service.review.RequestID, service.review.ExpectedRevisionNo
	default:
		return service.stateChange.Principal, service.stateChange.Reason, service.stateChange.RequestID, service.stateChange.ExpectedRevisionNo
	}
}
