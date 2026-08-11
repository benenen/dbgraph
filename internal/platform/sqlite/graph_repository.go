package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/graph"
)

type GraphRepository struct {
	store *Store
}

type graphEdgeLoadBudget struct {
	bytes   int
	maximum int
}

func (b *graphEdgeLoadBudget) accept(guard sql.NullString, selector sql.NullString, transform string) bool {
	rowBytes := 128 + len(transform)
	if guard.Valid {
		rowBytes += len(guard.String)
	}
	if selector.Valid {
		rowBytes += len(selector.String)
	}
	if b.bytes+rowBytes > b.maximum {
		return false
	}
	b.bytes += rowBytes
	return true
}

func NewGraphRepository(store *Store) *GraphRepository {
	return &GraphRepository{store: store}
}

func (r *GraphRepository) LoadRecursiveEdges(
	ctx context.Context,
	request graph.RecursiveTraceRequest,
	visit func(graph.RecursiveEdgeState) error,
) (resultTruncated bool, loadedBytes int, returnError error) {
	if request.ProjectID <= 0 || request.StartNodeID <= 0 || request.TargetNodeID < 0 ||
		(request.Direction != graph.DirectionDownstream && request.Direction != graph.DirectionUpstream) ||
		request.MaxDepth < 1 || request.MaxDepth > 64 ||
		request.MaxEdgeExpansions < 1 || request.MaxEdgeExpansions > 100_000 ||
		request.MaxLoadedBytes < 1 || request.MaxLoadedBytes > 16<<20 || visit == nil {
		return false, 0, errors.New("invalid recursive graph request")
	}
	currentColumn := "source_node_id"
	nextColumn := "target_node_id"
	if request.Direction == graph.DirectionUpstream {
		currentColumn = "target_node_id"
		nextColumn = "source_node_id"
	}
	query := recursiveGraphQuery(currentColumn, nextColumn)
	rows, err := r.store.db.QueryContext(
		ctx,
		query,
		request.StartNodeID,
		request.StartNodeID,
		request.ProjectID,
		request.StartNodeID,
		request.MaxDepth,
		request.TargetNodeID,
		request.TargetNodeID,
		request.MaxEdgeExpansions+1,
	)
	if err != nil {
		return false, 0, fmt.Errorf("load recursive effective graph edges: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	loadBudget := graphEdgeLoadBudget{maximum: request.MaxLoadedBytes}
	expansions := 0
	truncated := false
	for rows.Next() {
		expansions++
		if expansions > request.MaxEdgeExpansions {
			truncated = true
			break
		}
		var state graph.RecursiveEdgeState
		var cycle int
		var guardJSON sql.NullString
		var selectorJSON sql.NullString
		var transformJSON string
		var confidenceBPS int
		if err := rows.Scan(
			&state.StateKey,
			&state.ParentStateKey,
			&state.Depth,
			&state.NextNodeID,
			&cycle,
			&state.Edge.RelationID,
			&state.Edge.VersionID,
			&state.Edge.ProjectID,
			&state.Edge.SourceNodeID,
			&state.Edge.TargetNodeID,
			&state.Edge.Type,
			&state.Edge.Status,
			&state.Edge.HasPendingProposal,
			&guardJSON,
			&selectorJSON,
			&transformJSON,
			&confidenceBPS,
		); err != nil {
			return false, loadBudget.bytes, fmt.Errorf("scan recursive effective graph edge: %w", err)
		}
		if !loadBudget.accept(guardJSON, selectorJSON, transformJSON) {
			truncated = true
			break
		}
		state.Cycle = cycle == 1
		if guardJSON.Valid {
			var guard conditions.Boolean
			if err := json.Unmarshal([]byte(guardJSON.String), &guard); err != nil {
				return false, loadBudget.bytes, fmt.Errorf("decode recursive effective edge guard: %w", err)
			}
			state.Edge.Guard = &guard
		}
		if selectorJSON.Valid {
			var selector conditions.Boolean
			if err := json.Unmarshal([]byte(selectorJSON.String), &selector); err != nil {
				return false, loadBudget.bytes, fmt.Errorf("decode recursive effective edge selector: %w", err)
			}
			state.Edge.Selector = &selector
		}
		if err := json.Unmarshal([]byte(transformJSON), &state.Edge.Transform); err != nil {
			return false, loadBudget.bytes, fmt.Errorf("decode recursive effective edge transform: %w", err)
		}
		state.Edge.Confidence = float64(confidenceBPS) / 10_000
		if err := visit(state); err != nil {
			return false, loadBudget.bytes, err
		}
	}
	if err := rows.Err(); err != nil {
		return false, loadBudget.bytes, fmt.Errorf("iterate recursive effective graph edges: %w", err)
	}
	return truncated, loadBudget.bytes, nil
}

func recursiveGraphQuery(currentColumn string, nextColumn string) string {
	return `
WITH RECURSIVE walk(
    state_key, parent_state_key, depth, current_node_id, node_path, cycle,
    relation_id, version_id, project_id, source_node_id, target_node_id
) AS (
    SELECT
        CAST(ee.relation_id AS TEXT), '', 1, ee.` + nextColumn + `,
        printf(',%lld,%lld,', ?, ee.` + nextColumn + `),
        ee.` + nextColumn + ` = ?,
        ee.relation_id, ee.version_id, ee.project_id,
        ee.source_node_id, ee.target_node_id
    FROM effective_edges ee
    WHERE ee.project_id = ? AND ee.` + currentColumn + ` = ?

    UNION ALL

    SELECT
        walk.state_key || ',' || ee.relation_id, walk.state_key,
        walk.depth + 1, ee.` + nextColumn + `,
        walk.node_path || printf('%lld,', ee.` + nextColumn + `),
        instr(walk.node_path, printf(',%lld,', ee.` + nextColumn + `)) > 0,
        ee.relation_id, ee.version_id, ee.project_id,
        ee.source_node_id, ee.target_node_id
    FROM walk
    JOIN effective_edges ee
      ON ee.project_id = walk.project_id
     AND ee.` + currentColumn + ` = walk.current_node_id
    WHERE walk.cycle = 0
      AND walk.depth < ?
      AND (? = 0 OR walk.current_node_id <> ?)
    ORDER BY 3 ASC, 7 ASC
    LIMIT ?
)
SELECT
    walk.state_key, walk.parent_state_key, walk.depth, walk.current_node_id, walk.cycle,
    walk.relation_id, walk.version_id, walk.project_id, walk.source_node_id, walk.target_node_id,
    ee.relation_type, rc.status, rc.proposed_version_id IS NOT NULL,
    ee.guard_json, ee.selector_json, ee.transform_json, ee.confidence_bps
FROM walk
JOIN effective_edges ee
  ON ee.relation_id = walk.relation_id
 AND ee.version_id = walk.version_id
 AND ee.project_id = walk.project_id
JOIN relation_current rc ON rc.relation_id = walk.relation_id
`
}

func (r *GraphRepository) LoadEdges(
	ctx context.Context,
	projectID int64,
	nodeIDs []int64,
	direction graph.Direction,
	limit int,
	byteLimit int,
) (resultEdges []graph.Edge, resultTruncated bool, loadedBytes int, returnError error) {
	if len(nodeIDs) == 0 {
		return []graph.Edge{}, false, 0, nil
	}
	if limit <= 0 || byteLimit <= 0 {
		return []graph.Edge{}, true, 0, nil
	}
	nodeIDsJSON, err := json.Marshal(nodeIDs)
	if err != nil {
		return nil, false, 0, fmt.Errorf("encode effective graph node IDs: %w", err)
	}
	query := `
SELECT
    ee.relation_id, ee.version_id, ee.project_id, ee.source_node_id, ee.target_node_id,
    ee.relation_type, rc.status, rc.proposed_version_id IS NOT NULL,
    ee.guard_json, ee.selector_json, ee.transform_json, ee.confidence_bps
FROM effective_edges ee
JOIN relation_current rc ON rc.relation_id = ee.relation_id
WHERE ee.project_id = ? AND ee.source_node_id IN (SELECT CAST(value AS INTEGER) FROM json_each(?))
ORDER BY ee.source_node_id, ee.target_node_id, ee.relation_id
LIMIT ?
`
	if direction == graph.DirectionUpstream {
		query = `
SELECT
    ee.relation_id, ee.version_id, ee.project_id, ee.source_node_id, ee.target_node_id,
    ee.relation_type, rc.status, rc.proposed_version_id IS NOT NULL,
    ee.guard_json, ee.selector_json, ee.transform_json, ee.confidence_bps
FROM effective_edges ee
JOIN relation_current rc ON rc.relation_id = ee.relation_id
WHERE ee.project_id = ? AND ee.target_node_id IN (SELECT CAST(value AS INTEGER) FROM json_each(?))
ORDER BY ee.target_node_id, ee.target_node_id, ee.source_node_id, ee.relation_id
LIMIT ?
`
	}
	rows, err := r.store.db.QueryContext(ctx, query, projectID, string(nodeIDsJSON), limit+1)
	if err != nil {
		return nil, false, 0, fmt.Errorf("load effective graph edges: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	edges := make([]graph.Edge, 0)
	truncated := false
	loadBudget := graphEdgeLoadBudget{maximum: byteLimit}
	for rows.Next() {
		var edge graph.Edge
		var guardJSON sql.NullString
		var selectorJSON sql.NullString
		var transformJSON string
		var confidenceBPS int
		var hasProposal bool
		if err := rows.Scan(
			&edge.RelationID,
			&edge.VersionID,
			&edge.ProjectID,
			&edge.SourceNodeID,
			&edge.TargetNodeID,
			&edge.Type,
			&edge.Status,
			&hasProposal,
			&guardJSON,
			&selectorJSON,
			&transformJSON,
			&confidenceBPS,
		); err != nil {
			return nil, false, 0, fmt.Errorf("scan effective graph edge: %w", err)
		}
		edge.HasPendingProposal = hasProposal
		if len(edges) >= limit {
			truncated = true
			break
		}
		if !loadBudget.accept(guardJSON, selectorJSON, transformJSON) {
			truncated = true
			break
		}
		if guardJSON.Valid {
			var guard conditions.Boolean
			if err := json.Unmarshal([]byte(guardJSON.String), &guard); err != nil {
				return nil, false, 0, fmt.Errorf("decode effective edge guard: %w", err)
			}
			edge.Guard = &guard
		}
		if selectorJSON.Valid {
			var selector conditions.Boolean
			if err := json.Unmarshal([]byte(selectorJSON.String), &selector); err != nil {
				return nil, false, 0, fmt.Errorf("decode effective edge selector: %w", err)
			}
			edge.Selector = &selector
		}
		if err := json.Unmarshal([]byte(transformJSON), &edge.Transform); err != nil {
			return nil, false, 0, fmt.Errorf("decode effective edge transform: %w", err)
		}
		edge.Confidence = float64(confidenceBPS) / 10_000
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, fmt.Errorf("iterate effective graph edges: %w", err)
	}
	return edges, truncated, loadBudget.bytes, nil
}
