package mcpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
	appstatus "github.com/benenen/dbgraph/internal/status"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	testProjectID    int64 = 9_007_199_254_740_993
	testRelationID   int64 = 9_007_199_254_740_994
	testSessionID    int64 = 9_007_199_254_740_995
	testJobID        int64 = 9_007_199_254_740_996
	testDataSourceID int64 = 9_007_199_254_740_997
)

var testMCPTime = time.Date(2026, time.August, 11, 18, 30, 0, 123, time.UTC)

type toolBehaviorStub struct {
	searchProjectID    int64
	searchDataSourceID int64
	searchQuery        string
	searchLimit        int
	searchCalls        int
	findProjectID      int64
	findDataSource     int64
	findQualified      string

	createCommand    relations.ProposeCreate
	revisionCommand  relations.ProposeRevision
	tombstoneCommand relations.ProposeTombstone
	reviewCommand    relations.Review
	suppressCommand  relations.ChangeState
	restoreCommand   relations.ChangeState

	traceRequest  graph.TraceRequest
	impactRequest graph.TraceRequest

	beginCommand    reconcile.Begin
	batchCommand    reconcile.SubmitBatch
	completeCommand reconcile.Complete
	getSessionID    int64
	unresolvedLimit int

	startJobCommand jobs.StartSchemaScan
	getJobID        int64

	writeCalls int
}

func (s *toolBehaviorStub) Status(context.Context) (appstatus.Snapshot, error) {
	return appstatus.Snapshot{
		SchemaVersion: 3, SQLiteVersion: "3.51.4", JournalMode: "wal", ForeignKeysEnabled: true,
	}, nil
}

func (s *toolBehaviorStub) SearchCurrentNodes(_ context.Context, projectID int64, dataSourceID int64, query string, limit int) ([]catalog.Node, error) {
	s.searchProjectID, s.searchDataSourceID, s.searchQuery, s.searchLimit = projectID, dataSourceID, query, limit
	s.searchCalls++
	return []catalog.Node{testNode()}, nil
}

func (s *toolBehaviorStub) FindCurrentNode(_ context.Context, projectID int64, dataSourceID int64, qualifiedName string) (catalog.Node, error) {
	s.findProjectID, s.findDataSource, s.findQualified = projectID, dataSourceID, qualifiedName
	return testNode(), nil
}

func (s *toolBehaviorStub) ProposeCreate(_ context.Context, command relations.ProposeCreate) (relations.Relation, error) {
	s.createCommand = command
	s.writeCalls++
	return testRelation(), nil
}

func (s *toolBehaviorStub) ProposeRevision(_ context.Context, command relations.ProposeRevision) (relations.Relation, error) {
	s.revisionCommand = command
	s.writeCalls++
	return testRelation(), nil
}

func (s *toolBehaviorStub) ProposeTombstone(_ context.Context, command relations.ProposeTombstone) (relations.Relation, error) {
	s.tombstoneCommand = command
	s.writeCalls++
	return testRelation(), nil
}

func (s *toolBehaviorStub) Review(_ context.Context, command relations.Review) (relations.Relation, error) {
	s.reviewCommand = command
	s.writeCalls++
	return testRelation(), nil
}

func (s *toolBehaviorStub) Suppress(_ context.Context, command relations.ChangeState) (relations.Relation, error) {
	s.suppressCommand = command
	s.writeCalls++
	return testRelation(), nil
}

func (s *toolBehaviorStub) Restore(_ context.Context, command relations.ChangeState) (relations.Relation, error) {
	s.restoreCommand = command
	s.writeCalls++
	return testRelation(), nil
}

func (s *toolBehaviorStub) Get(context.Context, int64) (relations.Relation, error) {
	return testRelation(), nil
}

func (s *toolBehaviorStub) ListProposals(context.Context, int64, int) ([]relations.Relation, error) {
	return []relations.Relation{testRelation()}, nil
}

func (s *toolBehaviorStub) Trace(_ context.Context, request graph.TraceRequest) (graph.TraceResult, error) {
	s.traceRequest = request
	return testTraceResult(), nil
}

func (s *toolBehaviorStub) Impact(_ context.Context, request graph.TraceRequest) (graph.TraceResult, error) {
	s.impactRequest = request
	return testTraceResult(), nil
}

func (s *toolBehaviorStub) Begin(_ context.Context, command reconcile.Begin) (reconcile.Session, error) {
	s.beginCommand = command
	s.writeCalls++
	return testInitSession(reconcile.StatusOpen), nil
}

func (s *toolBehaviorStub) SubmitBatch(_ context.Context, command reconcile.SubmitBatch) (reconcile.BatchResult, error) {
	s.batchCommand = command
	s.writeCalls++
	return reconcile.BatchResult{
		BatchID: testSessionID + 10, SessionID: command.SessionID, BatchNo: command.BatchNo,
		Items:         []reconcile.ItemResult{{RelationID: testRelationID, Status: reconcile.ItemCreated}},
		UnresolvedIDs: []int64{testSessionID + 11}, AcceptedAt: testMCPTime,
	}, nil
}

func (s *toolBehaviorStub) Complete(_ context.Context, command reconcile.Complete) (reconcile.Completion, error) {
	s.completeCommand = command
	s.writeCalls++
	return reconcile.Completion{
		Session: testInitSession(reconcile.StatusCompleted), CandidateRelationIDs: []int64{testRelationID},
	}, nil
}

func (s *toolBehaviorStub) GetInit(_ context.Context, sessionID int64) (reconcile.Session, error) {
	s.getSessionID = sessionID
	return testInitSession(reconcile.StatusCompleted), nil
}

func (s *toolBehaviorStub) ListUnresolved(_ context.Context, _ int64, limit int) ([]reconcile.Unresolved, error) {
	s.unresolvedLimit = limit
	return []reconcile.Unresolved{{
		ID: testSessionID + 20, ProjectID: testProjectID, RepositoryID: 41, SessionID: testSessionID,
		BatchID: testSessionID + 10, Fingerprint: "sha256:finding", Type: "DYNAMIC_SQL",
		Summary: "Runtime branch could not be resolved.", Evidence: json.RawMessage(`{"line":9007199254740993}`),
		Status: 1, Principal: relations.Principal{Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent},
		CreatedAt: testMCPTime,
	}}, nil
}

// reconcileGetStub separates reconcile.Get from relations.Get, whose signatures
// otherwise collide when one test double implements every service interface.
type reconcileGetStub struct{ base *toolBehaviorStub }

func (s reconcileGetStub) Begin(ctx context.Context, command reconcile.Begin) (reconcile.Session, error) {
	return s.base.Begin(ctx, command)
}

func (s reconcileGetStub) SubmitBatch(ctx context.Context, command reconcile.SubmitBatch) (reconcile.BatchResult, error) {
	return s.base.SubmitBatch(ctx, command)
}

func (s reconcileGetStub) Complete(ctx context.Context, command reconcile.Complete) (reconcile.Completion, error) {
	return s.base.Complete(ctx, command)
}

func (s reconcileGetStub) Get(ctx context.Context, sessionID int64) (reconcile.Session, error) {
	return s.base.GetInit(ctx, sessionID)
}

func (s reconcileGetStub) ListUnresolved(ctx context.Context, projectID int64, limit int) ([]reconcile.Unresolved, error) {
	return s.base.ListUnresolved(ctx, projectID, limit)
}

func (s *toolBehaviorStub) Start(_ context.Context, command jobs.StartSchemaScan) (jobs.Job, error) {
	s.startJobCommand = command
	s.writeCalls++
	return testJob(), nil
}

func (s *toolBehaviorStub) GetJob(_ context.Context, jobID int64) (jobs.Job, error) {
	s.getJobID = jobID
	return testJob(), nil
}

type jobGetStub struct{ base *toolBehaviorStub }

func (s jobGetStub) Start(ctx context.Context, command jobs.StartSchemaScan) (jobs.Job, error) {
	return s.base.Start(ctx, command)
}

func (s jobGetStub) Get(ctx context.Context, jobID int64) (jobs.Job, error) {
	return s.base.GetJob(ctx, jobID)
}

func testServices(stub *toolBehaviorStub) Services {
	return Services{
		Status: stub, Catalog: stub, Relations: stub, Graph: stub,
		Reconcile: reconcileGetStub{base: stub}, Jobs: jobGetStub{base: stub},
	}
}

func TestReadToolsParseInputsAndMapStructuredOutputs(t *testing.T) {
	stub := &toolBehaviorStub{}
	session := connectInMemoryClient(t, testServices(stub), ViewerPrincipal())

	status := callToolOK(t, session, "dbgraph_status", `{}`)
	if status["status"] != "UP" || status["schemaVersion"] != float64(3) {
		t.Fatalf("status output = %#v", status)
	}

	search := callToolOK(t, session, "dbgraph_search_nodes", `{"projectId":"9007199254740993","query":"orders"}`)
	if stub.searchProjectID != testProjectID || stub.searchDataSourceID != 0 || stub.searchQuery != "orders" || stub.searchLimit != 20 {
		t.Fatalf("search input = project:%d dataSource:%d query:%q limit:%d",
			stub.searchProjectID, stub.searchDataSourceID, stub.searchQuery, stub.searchLimit)
	}
	if firstArrayObject(t, search, "nodes")["id"] != "9007199254740993" {
		t.Fatalf("search output = %#v", search)
	}

	node := callToolOK(t, session, "dbgraph_get_node", `{"projectId":"9007199254740993","dataSourceId":"9007199254740997","qualifiedName":"app.orders.id"}`)
	if stub.findProjectID != testProjectID || stub.findDataSource != testDataSourceID || stub.findQualified != "app.orders.id" || node["kind"] != "COLUMN" {
		t.Fatalf("get node input/output = %#v; %#v", stub, node)
	}

	relation := callToolOK(t, session, "dbgraph_get_relation", `{"relationId":"9007199254740994"}`)
	if relation["id"] != "9007199254740994" || relation["status"] != "APPROVED" || relation["effective"] != true {
		t.Fatalf("relation output = %#v", relation)
	}
	active := relation["active"].(map[string]any)
	if active["origin"] != "AGENT" || active["transform"].(map[string]any)["kind"] != "case" {
		t.Fatalf("active revision output = %#v", active)
	}
	databaseEvidence := active["evidence"].([]any)[1].(map[string]any)
	if databaseEvidence["kind"] != "DATABASE_CONSTRAINT" || databaseEvidence["dataSourceId"] != "9007199254740997" ||
		databaseEvidence["constraintName"] != "fk_orders_customer" || databaseEvidence["scanRunId"] != "9007199254741018" {
		t.Fatalf("database evidence output = %#v", databaseEvidence)
	}

	explained := callToolOK(t, session, "dbgraph_explain_relation", `{"relationId":"9007199254740994"}`)
	if explained["summary"] != "Approved revision 1 is part of the effective graph." {
		t.Fatalf("explain output = %#v", explained)
	}

	proposals := callToolOK(t, session, "dbgraph_list_proposals", `{"projectId":"9007199254740993"}`)
	if firstArrayObject(t, proposals, "relations")["id"] != "9007199254740994" {
		t.Fatalf("proposal output = %#v", proposals)
	}

	trace := callToolOK(t, session, "dbgraph_trace", `{
		"projectId":"9007199254740993","startNodeId":"11","targetNodeId":"12","direction":"UPSTREAM",
		"context":{"columns":{"11":9007199254740993},"parameters":{"tenant":"north"}},
		"maxDepth":4,"maxNodes":30,"maxPaths":5
	}`)
	if stub.traceRequest.Direction != graph.DirectionUpstream || stub.traceRequest.Limits != (graph.Limits{MaxDepth: 4, MaxNodes: 30, MaxPaths: 5}) ||
		string(stub.traceRequest.Context.Columns[11]) != "9007199254740993" {
		t.Fatalf("trace request = %#v", stub.traceRequest)
	}
	path := firstArrayObject(t, trace, "paths")
	step := path["steps"].([]any)[0].(map[string]any)
	if step["evaluation"].(map[string]any)["truth"] != "UNKNOWN" {
		t.Fatalf("trace output = %#v", trace)
	}

	callToolOK(t, session, "dbgraph_impact", `{"projectId":"9007199254740993","startNodeId":"11"}`)
	if stub.impactRequest.Direction != graph.DirectionDownstream || stub.impactRequest.Limits != graph.DefaultLimits() {
		t.Fatalf("impact request = %#v", stub.impactRequest)
	}

	initSession := callToolOK(t, session, "dbgraph_get_relation_init", `{"sessionId":"9007199254740995"}`)
	if stub.getSessionID != testSessionID || initSession["status"] != "COMPLETED" {
		t.Fatalf("init session = %#v", initSession)
	}

	unresolved := callToolOK(t, session, "dbgraph_list_unresolved", `{"projectId":"9007199254740993"}`)
	if stub.unresolvedLimit != 21 || firstArrayObject(t, unresolved, "findings")["status"] != "OPEN" {
		t.Fatalf("unresolved output = %#v", unresolved)
	}

	job := callToolOK(t, session, "dbgraph_get_job", `{"jobId":"9007199254740996"}`)
	if stub.getJobID != testJobID || job["status"] != "SUCCEEDED" || job["errorMessage"] != nil {
		t.Fatalf("job output = %#v", job)
	}
}

func TestAgentToolsForwardCommandsWithAuthenticatedPrincipal(t *testing.T) {
	stub := &toolBehaviorStub{}
	principal := relations.Principal{Actor: "source-agent", Role: relations.RoleAgent, Origin: audit.OriginAgent}
	session := connectInMemoryClient(t, testServices(stub), principal)

	callToolOK(t, session, "dbgraph_propose_relation", relationCreateArguments())
	if stub.createCommand.ProjectID != testProjectID || stub.createCommand.Principal != principal ||
		stub.createCommand.Type != relations.TypeConditionalValueCopy || len(stub.createCommand.Evidence) != 1 ||
		string(stub.createCommand.Transform.Literal.Value) != "9007199254740993" {
		t.Fatalf("create command = %#v", stub.createCommand)
	}

	callToolOK(t, session, "dbgraph_propose_relation_revision", `{
		"relationId":"9007199254740994","expectedRevisionNo":2,
		"sourceNodeId":"11","targetNodeId":"12","confidence":0.75,
		"transform":{"kind":"column_copy","nodeId":"11"},
		"evidence":[{"kind":"SQL_MAPPING","repository":"repo","commit":"abc","file":"Mapper.xml","startLine":3,"endLine":4}],
		"reason":"Revise mapping","requestId":"revision-1"
	}`)
	if stub.revisionCommand.RelationID != testRelationID || stub.revisionCommand.ExpectedRevisionNo != 2 || stub.revisionCommand.Principal != principal {
		t.Fatalf("revision command = %#v", stub.revisionCommand)
	}

	callToolOK(t, session, "dbgraph_propose_relation_tombstone", relationStateArguments("Remove obsolete mapping"))
	if stub.tombstoneCommand.RelationID != testRelationID || stub.tombstoneCommand.Principal != principal {
		t.Fatalf("tombstone command = %#v", stub.tombstoneCommand)
	}

	callToolOK(t, session, "dbgraph_begin_relation_init", `{
		"projectId":"9007199254740993","repositoryId":"41","mode":"FULL","sourceCommit":"abc123",
		"scope":{"module":"service","counter":9007199254740993},"requestId":"init-1"
	}`)
	if stub.beginCommand.Mode != reconcile.ModeFull || stub.beginCommand.Principal != principal ||
		string(stub.beginCommand.Scope) != `{"module":"service","counter":9007199254740993}` {
		t.Fatalf("begin command = %#v", stub.beginCommand)
	}

	callToolOK(t, session, "dbgraph_propose_relations", `{
		"sessionId":"9007199254740995","batchNo":1,"idempotencyKey":"batch-key","requestId":"batch-1",
		"proposals":[{
			"type":"CONDITIONAL_VALUE_COPY","sourceNodeId":"11","targetNodeId":"12","confidence":0.8,
			"transform":{"kind":"column_copy","nodeId":"11"},
			"evidence":[{"kind":"MANUAL","repository":"repo","commit":"abc","file":"notes.md","startLine":1,"endLine":1}],
			"reason":"Batch mapping"
		}],
		"unresolved":[{"type":"DYNAMIC_SQL","summary":"Unknown branch","evidence":{"number":9007199254740993}}]
	}`)
	if stub.batchCommand.SessionID != testSessionID || len(stub.batchCommand.Proposals) != 1 || len(stub.batchCommand.Unresolved) != 1 ||
		string(stub.batchCommand.Unresolved[0].Evidence) != `{"number":9007199254740993}` || stub.batchCommand.Principal != principal {
		t.Fatalf("batch command = %#v", stub.batchCommand)
	}

	completion := callToolOK(t, session, "dbgraph_complete_relation_init", `{
		"sessionId":"9007199254740995","expectedBatchCount":1,"reason":"Analysis complete","requestId":"complete-1"
	}`)
	if stub.completeCommand.ExpectedBatchCount != 1 || stub.completeCommand.Principal != principal ||
		completion["candidateRelationIds"].([]any)[0] != "9007199254740994" {
		t.Fatalf("complete command/output = %#v; %#v", stub.completeCommand, completion)
	}
}

func TestReviewerAndAdminToolsForwardOnlyAuthorizedCommands(t *testing.T) {
	reviewerStub := &toolBehaviorStub{}
	reviewer := relations.Principal{Actor: "reviewer", Role: relations.RoleReviewer, Origin: audit.OriginAgent}
	reviewerSession := connectInMemoryClient(t, testServices(reviewerStub), reviewer)

	callToolOK(t, reviewerSession, "dbgraph_propose_relation_revision", `{
		"relationId":"9007199254740994","expectedRevisionNo":2,
		"sourceNodeId":"11","targetNodeId":"12","confidence":0.9,
		"transform":{"kind":"column_copy","nodeId":"11"},
		"evidence":[{"kind":"CODE","repository":"r","commit":"c","file":"f","startLine":1,"endLine":1}],
		"reason":"Reviewer corrected content","requestId":"review-edit-1"
	}`)
	callToolOK(t, reviewerSession, "dbgraph_review_relation", `{
		"relationId":"9007199254740994","expectedRevisionNo":2,"decision":"APPROVE",
		"reason":"Evidence verified","requestId":"review-1"
	}`)
	callToolOK(t, reviewerSession, "dbgraph_suppress_relation", relationStateArguments("Temporarily suppress"))
	callToolOK(t, reviewerSession, "dbgraph_restore_relation", relationStateArguments("Restore verified relation"))
	writeCalls := reviewerStub.writeCalls
	for _, forbidden := range []struct {
		name      string
		arguments string
	}{
		{"dbgraph_propose_relation", relationCreateArguments()},
		{"dbgraph_propose_relation_tombstone", relationStateArguments("Reviewer must not tombstone")},
		{"dbgraph_begin_relation_init", `{"projectId":"9007199254740993","repositoryId":"41","mode":"FULL","sourceCommit":"abc","requestId":"reviewer-init"}`},
	} {
		result := callTool(t, reviewerSession, forbidden.name, forbidden.arguments)
		if !result.IsError || reviewerStub.writeCalls != writeCalls {
			t.Fatalf("reviewer %s error=%v writeCalls=%d, want forbidden and %d calls", forbidden.name, result.IsError, reviewerStub.writeCalls, writeCalls)
		}
	}
	if reviewerStub.revisionCommand.Principal != reviewer || reviewerStub.reviewCommand.Decision != relations.DecisionApprove || reviewerStub.reviewCommand.Principal != reviewer ||
		reviewerStub.suppressCommand.Principal != reviewer || reviewerStub.restoreCommand.Principal != reviewer {
		t.Fatalf("reviewer commands = revision:%#v review:%#v suppress:%#v restore:%#v", reviewerStub.revisionCommand, reviewerStub.reviewCommand, reviewerStub.suppressCommand, reviewerStub.restoreCommand)
	}

	adminStub := &toolBehaviorStub{}
	admin := relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginAgent}
	adminSession := connectInMemoryClient(t, testServices(adminStub), admin)
	job := callToolOK(t, adminSession, "dbgraph_start_schema_scan", `{
		"projectId":"9007199254740993","dataSourceId":"9007199254740997",
		"mode":"INCREMENTAL","tables":["learn.orders"],
		"reason":"Refresh source schema","requestId":"scan-1"
	}`)
	if adminStub.startJobCommand.ProjectID != testProjectID || adminStub.startJobCommand.DataSourceID != testDataSourceID ||
		adminStub.startJobCommand.Mode != jobs.SchemaScanIncremental || len(adminStub.startJobCommand.Tables) != 1 ||
		adminStub.startJobCommand.Tables[0] != "learn.orders" || adminStub.startJobCommand.Principal != admin ||
		adminStub.startJobCommand.Reason != "Refresh source schema" || job["id"] != "9007199254740996" {
		t.Fatalf("start job command/output = %#v; %#v", adminStub.startJobCommand, job)
	}
}

type revisionConflictRelationService struct{ RelationService }

func (revisionConflictRelationService) ProposeRevision(context.Context, relations.ProposeRevision) (relations.Relation, error) {
	current := testRelation()
	current.LatestRevisionNo = 7
	return relations.Relation{}, &relations.RevisionConflictError{CurrentRevisionNo: 7, Current: &current}
}

func TestRevisionConflictReturnsCurrentRevisionAsStructuredContent(t *testing.T) {
	t.Parallel()

	stub := &toolBehaviorStub{}
	services := testServices(stub)
	services.Relations = revisionConflictRelationService{RelationService: stub}
	session := connectInMemoryClient(t, services, relations.Principal{
		Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent,
	})
	result := callTool(t, session, "dbgraph_propose_relation_revision", `{
		"relationId":"9007199254740994","expectedRevisionNo":2,
		"sourceNodeId":"11","targetNodeId":"12","confidence":0.9,
		"transform":{"kind":"column_copy","nodeId":"11"},
		"evidence":[{"kind":"CODE","repository":"r","commit":"c","file":"f","startLine":1,"endLine":1}],
		"reason":"Refresh code evidence","requestId":"agent-edit-1"
	}`)
	if !result.IsError {
		t.Fatal("revision conflict was returned as success")
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured conflict type = %T", result.StructuredContent)
	}
	if structured["code"] != "REVISION_CONFLICT" || structured["currentRevisionNo"] != float64(7) {
		t.Fatalf("structured conflict = %#v", structured)
	}
	current, ok := structured["currentRelation"].(map[string]any)
	if !ok || current["id"] != "9007199254740994" || current["latestRevisionNo"] != float64(7) {
		t.Fatalf("structured current relation = %#v", structured["currentRelation"])
	}
	if text := toolResultText(t, result); text != "dbgraph resource changed or conflicts with existing state" {
		t.Fatalf("safe conflict text = %q", text)
	}
}

func TestEveryStateChangingToolRejectsAnUnauthorizedRoleBeforeService(t *testing.T) {
	tests := []struct {
		name      string
		principal relations.Principal
		arguments string
	}{
		{"dbgraph_propose_relation", ViewerPrincipal(), relationCreateArguments()},
		{"dbgraph_propose_relation_revision", ViewerPrincipal(), `{"relationId":"9007199254740994","expectedRevisionNo":1,"sourceNodeId":"11","targetNodeId":"12","confidence":1,"transform":{"kind":"column_copy","nodeId":"11"},"evidence":[{"kind":"CODE","repository":"r","commit":"c","file":"f","startLine":1,"endLine":1}],"reason":"r","requestId":"q"}`},
		{"dbgraph_propose_relation_tombstone", ViewerPrincipal(), relationStateArguments("Remove")},
		{"dbgraph_begin_relation_init", ViewerPrincipal(), `{"projectId":"9007199254740993","repositoryId":"41","mode":"FULL","sourceCommit":"abc","requestId":"q"}`},
		{"dbgraph_propose_relations", ViewerPrincipal(), `{"sessionId":"9007199254740995","batchNo":1,"idempotencyKey":"k","unresolved":[{"type":"X","summary":"Y","evidence":{}}],"requestId":"q"}`},
		{"dbgraph_complete_relation_init", ViewerPrincipal(), `{"sessionId":"9007199254740995","expectedBatchCount":1,"reason":"r","requestId":"q"}`},
		{"dbgraph_review_relation", relations.Principal{Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent}, `{"relationId":"9007199254740994","expectedRevisionNo":1,"decision":"REJECT","reason":"r","requestId":"q"}`},
		{"dbgraph_suppress_relation", relations.Principal{Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent}, relationStateArguments("Suppress")},
		{"dbgraph_restore_relation", relations.Principal{Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent}, relationStateArguments("Restore")},
		{"dbgraph_start_schema_scan", relations.Principal{Actor: "reviewer", Role: relations.RoleReviewer, Origin: audit.OriginAgent}, `{"projectId":"9007199254740993","dataSourceId":"9007199254740997","reason":"r","requestId":"q"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &toolBehaviorStub{}
			session := connectInMemoryClient(t, testServices(stub), test.principal)
			result := callTool(t, session, test.name, test.arguments)
			if !result.IsError || stub.writeCalls != 0 {
				t.Fatalf("result error=%v write calls=%d content=%v", result.IsError, stub.writeCalls, result.Content)
			}
		})
	}
}

type failingRelationService struct{ RelationService }

func (failingRelationService) ProposeCreate(context.Context, relations.ProposeCreate) (relations.Relation, error) {
	return relations.Relation{}, errors.New("sqlite path /secret/dbgraph.sqlite should not escape")
}

func TestToolBoundaryRejectsMalformedInputsAndSanitizesServiceErrors(t *testing.T) {
	stub := &toolBehaviorStub{}
	viewer := connectInMemoryClient(t, testServices(stub), ViewerPrincipal())
	tests := []struct {
		name      string
		arguments string
	}{
		{"dbgraph_status", `{"unexpected":true}`},
		{"dbgraph_search_nodes", `{"projectId":"0","query":"orders"}`},
		{"dbgraph_trace", `{"projectId":"9007199254740993","startNodeId":"11","direction":"SIDEWAYS"}`},
	}
	for _, test := range tests {
		result := callTool(t, viewer, test.name, test.arguments)
		if !result.IsError || !strings.Contains(toolResultText(t, result), "dbgraph rejected the request") {
			t.Fatalf("%s result = %#v", test.name, result)
		}
	}

	agentPrincipal := relations.Principal{Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent}
	agent := connectInMemoryClient(t, testServices(stub), agentPrincipal)
	emptyBatch := callTool(t, agent, "dbgraph_propose_relations", `{
		"sessionId":"9007199254740995","batchNo":1,"idempotencyKey":"empty","requestId":"batch-empty"
	}`)
	if !emptyBatch.IsError || stub.writeCalls != 0 {
		t.Fatalf("empty batch result=%#v write calls=%d", emptyBatch, stub.writeCalls)
	}

	reviewer := connectInMemoryClient(t, testServices(stub), relations.Principal{
		Actor: "reviewer", Role: relations.RoleReviewer, Origin: audit.OriginAgent,
	})
	invalidDecision := callTool(t, reviewer, "dbgraph_review_relation", `{
		"relationId":"9007199254740994","expectedRevisionNo":2,"decision":"MAYBE","reason":"r","requestId":"q"
	}`)
	if !invalidDecision.IsError || stub.writeCalls != 0 {
		t.Fatalf("invalid review result=%#v write calls=%d", invalidDecision, stub.writeCalls)
	}

	failing := connectInMemoryClient(t, Services{Relations: failingRelationService{}}, agentPrincipal)
	serviceFailure := callTool(t, failing, "dbgraph_propose_relation", relationCreateArguments())
	text := toolResultText(t, serviceFailure)
	if !serviceFailure.IsError || text != "dbgraph operation failed" || strings.Contains(text, "secret") {
		t.Fatalf("service failure leaked details: %#v", serviceFailure)
	}
}

func connectInMemoryClient(t *testing.T, services Services, principal relations.Principal) *mcp.ClientSession {
	t.Helper()
	server := NewServer(services, principal)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpapi-public-test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func callToolOK(t *testing.T, session *mcp.ClientSession, name string, arguments string) map[string]any {
	t.Helper()
	result := callTool(t, session, name, arguments)
	if result.IsError {
		t.Fatalf("%s returned tool error: %v", name, result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s structured content type = %T", name, result.StructuredContent)
	}
	return structured
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments string) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: json.RawMessage(arguments)})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

func toolResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("tool content type = %T", result.Content[0])
	}
	return text.Text
}

func firstArrayObject(t *testing.T, object map[string]any, key string) map[string]any {
	t.Helper()
	items, ok := object[key].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("%s = %#v", key, object[key])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("%s[0] = %#v", key, items[0])
	}
	return item
}

func relationCreateArguments() string {
	return `{
		"projectId":"9007199254740993","type":"CONDITIONAL_VALUE_COPY","sourceNodeId":"11","targetNodeId":"12",
		"guard":{"kind":"compare","operator":"eq","left":{"kind":"column","nodeId":"11"},"right":{"kind":"literal","valueType":"integer","value":1}},
		"selector":{"kind":"is_not_null","left":{"kind":"parameter","parameter":"tenant"}},
		"transform":{"kind":"literal","valueType":"integer","value":9007199254740993},"confidence":0.9,
		"evidence":[{"kind":"CODE","repository":"repo","commit":"abc","file":"Mapper.java","symbol":"map","startLine":1,"endLine":2}],
		"reason":"Mapped by source assignment","requestId":"create-1"
	}`
}

func relationStateArguments(reason string) string {
	encodedReason, _ := json.Marshal(reason)
	return `{"relationId":"9007199254740994","expectedRevisionNo":2,"reason":` + string(encodedReason) + `,"requestId":"state-1"}`
}

func testNode() catalog.Node {
	return catalog.Node{
		ID: testProjectID, VersionID: testProjectID + 20, ProjectID: testProjectID,
		DataSourceID: testDataSourceID, ScanRunID: testProjectID + 21, ParentNodeID: testProjectID + 22,
		Kind: catalog.NodeColumn, Status: catalog.NodeActive, StableKey: "orders.id", Name: "id",
		QualifiedName: "app.orders.id", DataType: "BIGINT", Nullable: false, Ordinal: 1,
	}
}

func testRelation() relations.Relation {
	expected := 1
	guard := conditions.Boolean{
		Kind: conditions.BooleanCompare, Operator: conditions.CompareEqual,
		Left: &conditions.Value{Kind: conditions.ValueColumn, NodeID: 11},
		Right: &conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
			Type: conditions.LiteralInteger, Value: json.RawMessage(`9007199254740993`),
		}},
	}
	selector := conditions.Boolean{Kind: conditions.BooleanIsNotNull, Left: &conditions.Value{Kind: conditions.ValueParameter, Parameter: "tenant"}}
	transform := conditions.Value{
		Kind: conditions.ValueCase,
		Cases: []conditions.Case{{
			When: conditions.Boolean{Kind: conditions.BooleanIsNull, Left: &conditions.Value{Kind: conditions.ValueColumn, NodeID: 11}},
			Then: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 12},
		}},
		Else: &conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{Type: conditions.LiteralString, Value: json.RawMessage(`"fallback"`)}},
	}
	active := &relations.Revision{
		ID: testRelationID + 10, RelationID: testRelationID, RevisionNo: 1, Kind: relations.ProposalContent,
		SourceNodeID: 11, TargetNodeID: 12, Guard: &guard, Selector: &selector, Transform: transform,
		Confidence: 0.9, Evidence: []relations.EvidenceInput{
			{
				Kind: relations.EvidenceCode, Repository: "repo", Commit: "abc", File: "Mapper.java", Symbol: "map", StartLine: 1, EndLine: 2,
			},
			{
				Kind: relations.EvidenceDatabaseConstraint, DataSourceID: testDataSourceID,
				ConstraintSchema: "app", ConstraintName: "fk_orders_customer", ScanRunID: testProjectID + 25,
			},
		},
		Actor: "agent", Origin: audit.OriginAgent, Reason: "Initial mapping", RequestID: "create-1",
		ExpectedRevisionNo: &expected, CreatedAt: testMCPTime,
	}
	proposed := *active
	proposed.ID++
	proposed.RevisionNo = 2
	proposed.Kind = relations.ProposalTombstone
	proposed.Origin = audit.OriginWeb
	return relations.Relation{
		ID: testRelationID, ProjectID: testProjectID, Type: relations.TypeConditionalValueCopy,
		LatestRevisionNo: 2, Status: relations.StatusApproved, Active: active, Proposed: &proposed,
		Effective: true, CreatedAt: testMCPTime,
	}
}

func testTraceResult() graph.TraceResult {
	relation := testRelation()
	return graph.TraceResult{
		Paths: []graph.Path{{
			Nodes: []int64{11, 12},
			Steps: []graph.Step{{
				Edge: graph.Edge{
					RelationID: relation.ID, VersionID: relation.Active.ID, ProjectID: relation.ProjectID,
					SourceNodeID: 11, TargetNodeID: 12, Type: relation.Type, Guard: relation.Active.Guard,
					Status:   relations.StatusApproved,
					Selector: relation.Active.Selector, Transform: relation.Active.Transform, Confidence: relation.Active.Confidence,
				},
				Evaluation: conditions.Evaluation{
					Truth:   conditions.TruthUnknown,
					Missing: []conditions.MissingReference{{NodeID: 11}, {Parameter: "tenant"}},
				},
			}},
		}},
		VisitedNodes: 2, CycleDetected: true, Truncated: false,
	}
}

func testInitSession(status reconcile.Status) reconcile.Session {
	completed := testMCPTime.Add(time.Minute)
	session := reconcile.Session{
		ID: testSessionID, ProjectID: testProjectID, RepositoryID: 41, Mode: reconcile.ModeFull,
		SourceCommit: "abc123", Scope: json.RawMessage(`{"module":"service"}`), Status: status,
		Principal: relations.Principal{Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent},
		RequestID: "init-1", CreatedAt: testMCPTime,
	}
	if status == reconcile.StatusCompleted {
		session.CompletedAt = &completed
	}
	return session
}

func testJob() jobs.Job {
	started := testMCPTime.Add(time.Second)
	completed := testMCPTime.Add(2 * time.Second)
	return jobs.Job{
		ID: testJobID, ProjectID: testProjectID, Type: jobs.TypeSchemaScan, Status: jobs.StatusSucceeded,
		Payload: json.RawMessage(`{"dataSourceId":"9007199254740997"}`),
		Result:  json.RawMessage(`{"scanRunId":"9007199254741010"}`), ErrorCode: "",
		CreatedAt: testMCPTime, StartedAt: &started, CompletedAt: &completed, RevisionNo: 3,
	}
}
