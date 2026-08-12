package sqlite

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/relations"
)

// LoadDataSourceGraph reads every approved relation whose two ends both live in
// one data source. Relations are declared between columns; the picture is drawn
// between the tables that own them, which is the shape a reader is looking for.
// A relation reaching outside the source is left out: a half-drawn edge would
// read as a missing table rather than as a relation that leaves.
func (r *GraphRepository) LoadDataSourceGraph(
	ctx context.Context,
	projectID int64,
	dataSourceID int64,
	maximumEdges int,
) (result graph.DataSourceGraph, returnError error) {
	if maximumEdges <= 0 {
		maximumEdges = graph.MaximumDataSourceGraphEdges
	}
	rows, err := r.store.db.QueryContext(ctx, `
WITH resolved AS (
    SELECT n.id AS node_id,
           n.data_source_id AS data_source_id,
           nv.name AS name,
           CASE WHEN n.kind = ? THEN n.id ELSE nv.parent_node_id END AS table_id
    FROM nodes n
    JOIN node_current nc ON nc.node_id = n.id
    JOIN node_versions nv ON nv.id = nc.version_id
    WHERE n.project_id = ?
)
SELECT e.relation_id, e.relation_type, e.confidence_bps,
       e.source_node_id, e.target_node_id,
       source.table_id, target.table_id,
       source.name, target.name,
       sourceTable.name, sourceTable.qualified_name,
       targetTable.name, targetTable.qualified_name,
       e.guard_json IS NOT NULL
FROM effective_edges e
JOIN resolved source ON source.node_id = e.source_node_id
JOIN resolved target ON target.node_id = e.target_node_id
JOIN node_current sourceCurrent ON sourceCurrent.node_id = source.table_id
JOIN node_versions sourceTable ON sourceTable.id = sourceCurrent.version_id
JOIN node_current targetCurrent ON targetCurrent.node_id = target.table_id
JOIN node_versions targetTable ON targetTable.id = targetCurrent.version_id
WHERE e.project_id = ?
  AND source.data_source_id = ?
  AND target.data_source_id = ?
  AND source.table_id IS NOT NULL
  AND target.table_id IS NOT NULL
ORDER BY sourceTable.qualified_name, targetTable.qualified_name, e.relation_id
LIMIT ?
`, catalog.NodeTable, projectID, projectID, dataSourceID, dataSourceID, maximumEdges+1)
	if err != nil {
		return graph.DataSourceGraph{}, fmt.Errorf("load data source graph: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	tables := map[int64]graph.Table{}
	edges := make([]graph.TableEdge, 0)
	for rows.Next() {
		if len(edges) == maximumEdges {
			result.Truncated = true
			break
		}
		var edge graph.TableEdge
		var relationType int
		var confidenceBasisPoints int
		var sourceName, sourceQualified string
		var targetName, targetQualified string
		if err := rows.Scan(
			&edge.RelationID, &relationType, &confidenceBasisPoints,
			&edge.SourceNodeID, &edge.TargetNodeID,
			&edge.SourceTableID, &edge.TargetTableID,
			&edge.SourceColumn, &edge.TargetColumn,
			&sourceName, &sourceQualified,
			&targetName, &targetQualified,
			&edge.Conditional,
		); err != nil {
			return graph.DataSourceGraph{}, fmt.Errorf("scan data source graph: %w", err)
		}
		edge.Type = relations.Type(relationType)
		edge.Confidence = float64(confidenceBasisPoints) / 10_000
		tables[edge.SourceTableID] = graph.Table{
			ID: edge.SourceTableID, Name: sourceName, QualifiedName: sourceQualified,
		}
		tables[edge.TargetTableID] = graph.Table{
			ID: edge.TargetTableID, Name: targetName, QualifiedName: targetQualified,
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return graph.DataSourceGraph{}, fmt.Errorf("iterate data source graph: %w", err)
	}
	result.Edges = edges
	result.Tables = make([]graph.Table, 0, len(tables))
	for _, table := range tables {
		result.Tables = append(result.Tables, table)
	}
	sort.Slice(result.Tables, func(first, second int) bool {
		return result.Tables[first].QualifiedName < result.Tables[second].QualifiedName
	})
	return result, nil
}

// ListTables reads the tables a data source imported, so the console can browse
// a catalog that holds no relations yet. The filter is a substring match rather
// than the full-text index, because this is browsing: an empty filter has to
// list everything.
func (r *CatalogRepository) ListTables(
	ctx context.Context,
	projectID int64,
	dataSourceID int64,
	filter string,
	limit int,
) (tables []catalog.TableSummary, returnError error) {
	pattern := "%" + escapeLikePattern(filter) + "%"
	rows, err := r.store.db.QueryContext(ctx, `
SELECT n.id, nv.name, nv.qualified_name
FROM nodes n
JOIN node_current nc ON nc.node_id = n.id
JOIN node_versions nv ON nv.id = nc.version_id
WHERE n.project_id = ?
  AND n.data_source_id = ?
  AND n.kind = ?
  AND nv.status = ?
  AND nv.qualified_name LIKE ? ESCAPE '\'
ORDER BY nv.qualified_name
LIMIT ?
`, projectID, dataSourceID, catalog.NodeTable, catalog.NodeActive, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	tables = make([]catalog.TableSummary, 0)
	for rows.Next() {
		var table catalog.TableSummary
		if err := rows.Scan(&table.ID, &table.Name, &table.QualifiedName); err != nil {
			return nil, fmt.Errorf("scan table: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tables: %w", err)
	}
	return tables, nil
}

// escapeLikePattern keeps a filter literal. Without it a name holding % or _
// would silently match far more than the operator typed.
func escapeLikePattern(value string) string {
	escaped := make([]rune, 0, len(value))
	for _, symbol := range value {
		if symbol == '%' || symbol == '_' || symbol == '\\' {
			escaped = append(escaped, '\\')
		}
		escaped = append(escaped, symbol)
	}
	return string(escaped)
}
