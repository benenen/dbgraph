package webapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/reconcile"
)

type traceContextDTO struct {
	Columns    map[string]json.RawMessage `json:"columns,omitempty"`
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
}

type traceDTO struct {
	StartNodeID  string          `json:"startNodeId"`
	TargetNodeID string          `json:"targetNodeId,omitempty"`
	Direction    string          `json:"direction"`
	Context      traceContextDTO `json:"context,omitempty"`
	MaxDepth     int             `json:"maxDepth,omitempty"`
	MaxNodes     int             `json:"maxNodes,omitempty"`
	MaxPaths     int             `json:"maxPaths,omitempty"`
}

// Page sizes are fixed server-side; clients no longer choose them.
const (
	defaultNodeSearchLimit = 20
	defaultUnresolvedLimit = 20
)

func (h *handler) searchNodes(response http.ResponseWriter, request *http.Request) {
	projectID, err := parseID(request.PathValue("projectID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid project ID", nil)
		return
	}
	limit := defaultNodeSearchLimit
	if h.services.Catalog == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid node search", nil)
		return
	}
	dataSourceID := int64(0)
	if raw := strings.TrimSpace(request.URL.Query().Get("dataSourceId")); raw != "" {
		dataSourceID, err = parseID(raw)
		if err != nil {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid data source ID", nil)
			return
		}
	}
	nodes, err := h.services.Catalog.SearchCurrentNodes(request.Context(), projectID, dataSourceID, request.URL.Query().Get("q"), limit)
	if err != nil {
		if errors.Is(err, catalog.ErrInvalidSnapshot) {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "node search was rejected", nil)
		} else {
			writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		}
		return
	}
	output := make([]map[string]any, len(nodes))
	for index, node := range nodes {
		output[index] = mapNode(node)
	}
	writeJSON(response, http.StatusOK, map[string]any{"nodes": output})
}

func (h *handler) getNode(response http.ResponseWriter, request *http.Request) {
	projectID, err := parseID(request.PathValue("projectID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid project ID", nil)
		return
	}
	nodeID, err := parseID(request.PathValue("nodeID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid node ID", nil)
		return
	}
	if h.services.Catalog == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	node, err := h.services.Catalog.GetCurrentNode(request.Context(), projectID, nodeID)
	if err != nil {
		if errors.Is(err, catalog.ErrNodeNotFound) {
			writeError(response, http.StatusNotFound, "NOT_FOUND", "catalog node not found", nil)
		} else if errors.Is(err, catalog.ErrInvalidSnapshot) {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "node lookup was rejected", nil)
		} else {
			writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		}
		return
	}
	writeJSON(response, http.StatusOK, mapNode(node))
}

func (h *handler) traceGraph(response http.ResponseWriter, request *http.Request) {
	projectID, err := parseID(request.PathValue("projectID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid graph trace", nil)
		return
	}
	if h.services.Graph == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	var input traceDTO
	if err := decodeJSON(response, request, maximumJSONRequestBytes, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid graph trace", nil)
		return
	}
	graphRequest, err := input.toRequest(projectID)
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid graph trace", nil)
		return
	}
	result, err := h.services.Graph.Trace(request.Context(), graphRequest)
	if err != nil {
		if errors.Is(err, graph.ErrInvalidTrace) {
			writeError(response, http.StatusUnprocessableEntity, "INVALID_TRACE", "graph trace was rejected", nil)
		} else {
			writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		}
		return
	}
	writeJSON(response, http.StatusOK, mapGraphResult(result))
}

func (input traceDTO) toRequest(projectID int64) (graph.TraceRequest, error) {
	startID, err := parseID(input.StartNodeID)
	if err != nil {
		return graph.TraceRequest{}, err
	}
	var targetID int64
	if input.TargetNodeID != "" {
		targetID, err = parseID(input.TargetNodeID)
		if err != nil {
			return graph.TraceRequest{}, err
		}
	}
	direction := graph.DirectionDownstream
	if strings.EqualFold(input.Direction, "UPSTREAM") {
		direction = graph.DirectionUpstream
	} else if input.Direction != "" && !strings.EqualFold(input.Direction, "DOWNSTREAM") {
		return graph.TraceRequest{}, errors.New("invalid direction")
	}
	conditionContext := conditions.Context{
		Columns:    make(map[int64]json.RawMessage, len(input.Context.Columns)),
		Parameters: make(map[string]json.RawMessage, len(input.Context.Parameters)),
	}
	for key, raw := range input.Context.Columns {
		id, err := parseID(key)
		if err != nil || !json.Valid(raw) {
			return graph.TraceRequest{}, errors.New("invalid column context")
		}
		conditionContext.Columns[id] = append(json.RawMessage(nil), raw...)
	}
	for key, raw := range input.Context.Parameters {
		if !json.Valid(raw) {
			return graph.TraceRequest{}, errors.New("invalid parameter context")
		}
		conditionContext.Parameters[key] = append(json.RawMessage(nil), raw...)
	}
	limits := graph.DefaultLimits()
	if input.MaxDepth != 0 {
		limits.MaxDepth = input.MaxDepth
	}
	if input.MaxNodes != 0 {
		limits.MaxNodes = input.MaxNodes
	}
	if input.MaxPaths != 0 {
		limits.MaxPaths = input.MaxPaths
	}
	return graph.TraceRequest{
		ProjectID: projectID, StartNodeID: startID, TargetNodeID: targetID,
		Direction: direction, Context: conditionContext, Limits: limits,
	}, nil
}

func (h *handler) listUnresolved(response http.ResponseWriter, request *http.Request) {
	projectID, err := parseID(request.PathValue("projectID"))
	limit := defaultUnresolvedLimit
	if h.services.Reconcile == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid unresolved query", nil)
		return
	}
	findings, err := h.services.Reconcile.ListUnresolved(request.Context(), projectID, limit)
	if err != nil {
		if errors.Is(err, reconcile.ErrInvalidInit) {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "unresolved query was rejected", nil)
		} else {
			writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		}
		return
	}
	output := make([]map[string]any, len(findings))
	for index, finding := range findings {
		output[index] = mapUnresolved(finding)
	}
	writeJSON(response, http.StatusOK, map[string]any{"findings": output})
}

func (h *handler) getJob(response http.ResponseWriter, request *http.Request) {
	projectID, jobID, ok := pathProjectSubjectIDs(response, request, "jobID")
	if !ok {
		return
	}
	if h.services.Jobs == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	job, err := h.services.Jobs.Get(request.Context(), jobID)
	if errors.Is(err, jobs.ErrJobNotFound) || (err == nil && job.ProjectID != projectID) {
		writeError(response, http.StatusNotFound, "NOT_FOUND", "job not found", nil)
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		return
	}
	writeJSON(response, http.StatusOK, mapJob(job))
}

func (h *handler) getInitSession(response http.ResponseWriter, request *http.Request) {
	projectID, sessionID, ok := pathProjectSubjectIDs(response, request, "sessionID")
	if !ok {
		return
	}
	if h.services.Reconcile == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	session, err := h.services.Reconcile.Get(request.Context(), sessionID)
	if errors.Is(err, reconcile.ErrInitNotFound) || (err == nil && session.ProjectID != projectID) {
		writeError(response, http.StatusNotFound, "NOT_FOUND", "relation init session not found", nil)
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		return
	}
	writeJSON(response, http.StatusOK, mapInitSession(session))
}

func (h *handler) listAuditEvents(response http.ResponseWriter, request *http.Request) {
	projectID, err := parseID(request.PathValue("projectID"))
	if h.services.Audit == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid audit query", nil)
		return
	}
	responseLimit := maximumAuditResponseCount
	events, err := h.services.Audit.ListProject(request.Context(), projectID, responseLimit+1)
	if err != nil {
		if errors.Is(err, audit.ErrInvalidEvent) {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "audit query was rejected", nil)
		} else {
			writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		}
		return
	}
	output, truncated, err := boundedAuditResponse(events, responseLimit)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"events": output, "truncated": truncated})
}

func (h *handler) listProjects(response http.ResponseWriter, request *http.Request) {
	if h.services.Projects == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	limit := catalog.DefaultListLimit
	projects, err := h.services.Projects.List(request.Context(), limit)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		return
	}
	output := make([]map[string]any, len(projects))
	for index, project := range projects {
		output[index] = mapProject(project)
	}
	writeJSON(response, http.StatusOK, output)
}

func (h *handler) listDataSources(response http.ResponseWriter, request *http.Request) {
	if h.services.Catalog == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	projectID, err := parseID(request.PathValue("projectID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid project ID", nil)
		return
	}
	limit := catalog.DefaultListLimit
	sources, err := h.services.Catalog.ListDataSources(request.Context(), projectID, limit)
	if err != nil {
		if errors.Is(err, catalog.ErrInvalidDataSource) {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid project ID", nil)
			return
		}
		writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		return
	}
	role := currentSession(request).session.Principal.Role
	output := make([]map[string]any, len(sources))
	for index, source := range sources {
		output[index] = mapDataSourceForRole(source, role)
	}
	writeJSON(response, http.StatusOK, output)
}

func (h *handler) listRepositories(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireAdmin(response, request); !ok {
		return
	}
	if h.services.CodeRepositories == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	projectID, err := parseID(request.PathValue("projectID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid project ID", nil)
		return
	}
	limit := catalog.DefaultListLimit
	repositories, err := h.services.CodeRepositories.List(request.Context(), projectID, limit)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		return
	}
	output := make([]map[string]any, len(repositories))
	for index, repository := range repositories {
		output[index] = mapCodeRepository(repository)
	}
	writeJSON(response, http.StatusOK, output)
}

func pathProjectSubjectIDs(response http.ResponseWriter, request *http.Request, subject string) (int64, int64, bool) {
	projectID, err := parseID(request.PathValue("projectID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid project ID", nil)
		return 0, 0, false
	}
	subjectID, err := parseID(request.PathValue(subject))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid resource ID", nil)
		return 0, 0, false
	}
	return projectID, subjectID, true
}

func mapNode(node catalog.Node) map[string]any {
	return map[string]any{
		"id": strconv.FormatInt(node.ID, 10), "versionId": strconv.FormatInt(node.VersionID, 10),
		"projectId": strconv.FormatInt(node.ProjectID, 10), "dataSourceId": strconv.FormatInt(node.DataSourceID, 10),
		"parentNodeId": optionalID(node.ParentNodeID), "kind": nodeKindName(node.Kind), "status": nodeStatusName(node.Status),
		"name": node.Name, "qualifiedName": node.QualifiedName, "dataType": node.DataType,
		"nullable": node.Nullable, "ordinal": node.Ordinal,
	}
}

func mapGraphResult(result graph.TraceResult) map[string]any {
	paths := make([]map[string]any, len(result.Paths))
	for pathIndex, path := range result.Paths {
		nodes := make([]string, len(path.Nodes))
		for index, id := range path.Nodes {
			nodes[index] = strconv.FormatInt(id, 10)
		}
		steps := make([]map[string]any, len(path.Steps))
		for index, step := range path.Steps {
			mappedStep := map[string]any{
				"relationId":   strconv.FormatInt(step.Edge.RelationID, 10),
				"sourceNodeId": strconv.FormatInt(step.Edge.SourceNodeID, 10),
				"targetNodeId": strconv.FormatInt(step.Edge.TargetNodeID, 10),
				"type":         relationTypeName(step.Edge.Type), "status": relationStatusName(step.Edge.Status),
				"confidence": step.Edge.Confidence,
				"truth":      truthName(step.Evaluation.Truth), "missing": mapMissing(step.Evaluation.Missing),
				"guard": mapBoolean(step.Edge.Guard), "selector": mapBoolean(step.Edge.Selector),
				"transform": mapValue(step.Edge.Transform),
			}
			if step.Edge.HasPendingProposal {
				mappedStep["proposalStatus"] = "PROPOSED"
			}
			steps[index] = mappedStep
		}
		paths[pathIndex] = map[string]any{"nodes": nodes, "steps": steps}
	}
	return map[string]any{
		"paths": paths, "visitedNodes": result.VisitedNodes,
		"cycleDetected": result.CycleDetected, "truncated": result.Truncated,
	}
}

func mapMissing(items []conditions.MissingReference) []map[string]string {
	result := make([]map[string]string, len(items))
	for index, item := range items {
		result[index] = map[string]string{"nodeId": optionalID(item.NodeID), "parameter": item.Parameter}
	}
	return result
}

func mapUnresolved(finding reconcile.Unresolved) map[string]any {
	return map[string]any{
		"id": strconv.FormatInt(finding.ID, 10), "projectId": strconv.FormatInt(finding.ProjectID, 10),
		"repositoryId": strconv.FormatInt(finding.RepositoryID, 10), "sessionId": strconv.FormatInt(finding.SessionID, 10),
		"type": finding.Type, "summary": finding.Summary, "evidence": finding.Evidence,
		"status": "OPEN", "actor": finding.Principal.Actor, "createdAt": finding.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapJob(job jobs.Job) map[string]any {
	return map[string]any{
		"id": strconv.FormatInt(job.ID, 10), "projectId": strconv.FormatInt(job.ProjectID, 10),
		"type": jobTypeName(job.Type), "status": jobStatusName(job.Status), "payload": job.Payload,
		"result": job.Result, "errorCode": job.ErrorCode, "revisionNo": job.RevisionNo,
		"createdAt": job.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapInitSession(session reconcile.Session) map[string]any {
	return map[string]any{
		"id": strconv.FormatInt(session.ID, 10), "projectId": strconv.FormatInt(session.ProjectID, 10),
		"repositoryId": strconv.FormatInt(session.RepositoryID, 10), "mode": initModeName(session.Mode),
		"sourceCommit": session.SourceCommit, "scope": session.Scope, "status": initStatusName(session.Status),
		"actor": session.Principal.Actor, "createdAt": session.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapAuditEvent(event audit.Event) map[string]any {
	return map[string]any{
		"id": strconv.FormatInt(event.ID, 10), "actor": event.Actor, "origin": originName(event.Origin),
		"action": event.Action, "subjectType": event.SubjectType, "subjectId": optionalID(event.SubjectID),
		"reason": event.Reason, "requestId": event.RequestID, "expectedRevisionNo": event.ExpectedRevision,
		"details": event.Details, "occurredAt": event.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
}

func optionalID(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func nodeKindName(kind catalog.NodeKind) string {
	switch kind {
	case catalog.NodeDatabase:
		return "DATABASE"
	case catalog.NodeSchema:
		return "SCHEMA"
	case catalog.NodeTable:
		return "TABLE"
	case catalog.NodeColumn:
		return "COLUMN"
	default:
		return "UNKNOWN"
	}
}

func nodeStatusName(status catalog.NodeStatus) string {
	switch status {
	case catalog.NodeActive:
		return "ACTIVE"
	case catalog.NodeStale:
		return "STALE"
	default:
		return "UNKNOWN"
	}
}

func truthName(truth conditions.Truth) string {
	if truth == conditions.TruthTrue {
		return "TRUE"
	}
	if truth == conditions.TruthFalse {
		return "FALSE"
	}
	return "UNKNOWN"
}

func jobStatusName(status jobs.Status) string {
	name := map[jobs.Status]string{
		jobs.StatusPending: "PENDING", jobs.StatusRunning: "RUNNING", jobs.StatusSucceeded: "SUCCEEDED",
		jobs.StatusFailed: "FAILED", jobs.StatusCancelled: "CANCELLED",
	}[status]
	if name == "" {
		return "UNKNOWN"
	}
	return name
}

func jobTypeName(jobType jobs.Type) string {
	if jobType == jobs.TypeSchemaScan {
		return "SCHEMA_SCAN"
	}
	return "UNKNOWN"
}

func initModeName(mode reconcile.Mode) string {
	switch mode {
	case reconcile.ModeFull:
		return "FULL"
	case reconcile.ModeIncremental:
		return "INCREMENTAL"
	default:
		return "UNKNOWN"
	}
}

func initStatusName(status reconcile.Status) string {
	name := map[reconcile.Status]string{
		reconcile.StatusOpen: "OPEN", reconcile.StatusCompleted: "COMPLETED",
		reconcile.StatusFailed: "FAILED", reconcile.StatusCancelled: "CANCELLED",
	}[status]
	if name == "" {
		return "UNKNOWN"
	}
	return name
}

func originName(origin audit.Origin) string {
	name := map[audit.Origin]string{audit.OriginAgent: "AGENT", audit.OriginWeb: "WEB", audit.OriginSystem: "SYSTEM"}[origin]
	if name == "" {
		return "UNKNOWN"
	}
	return name
}
