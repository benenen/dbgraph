package webapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/graph"
)

// listTables browses one data source's imported tables. It exists because a
// scanned catalog is worth reading before any relation has been proposed, which
// is the state every source is in after its first scan.
func (h *handler) listTables(response http.ResponseWriter, request *http.Request) {
	projectID, dataSourceID, ok := pathProjectSubjectIDs(response, request, "dataSourceID")
	if !ok {
		return
	}
	if h.services.Catalog == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	filter := request.URL.Query().Get("q")
	if len(filter) > 200 {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid table filter", nil)
		return
	}
	tables, err := h.services.Catalog.ListTables(
		request.Context(), projectID, dataSourceID, filter, maximumTableListSize,
	)
	if err != nil {
		writeAdminError(response, err)
		return
	}
	output := make([]map[string]any, len(tables))
	for index, table := range tables {
		output[index] = map[string]any{
			"id":            strconv.FormatInt(table.ID, 10),
			"name":          table.Name,
			"qualifiedName": table.QualifiedName,
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"tables":    output,
		"truncated": len(tables) == maximumTableListSize,
	})
}

// maximumTableListSize bounds one browse. The list is capped rather than paged
// because the filter is how a reader narrows it, and scanning names works only
// with all of them present. Reaching the cap is reported, never silent.
const maximumTableListSize = catalog.MaximumTableListLimit

// dataSourceGraph returns the approved relations inside one data source, drawn
// between the tables that own the joined columns.
func (h *handler) dataSourceGraph(response http.ResponseWriter, request *http.Request) {
	projectID, dataSourceID, ok := pathProjectSubjectIDs(response, request, "dataSourceID")
	if !ok {
		return
	}
	if h.services.Graph == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	result, err := h.services.Graph.DataSourceGraph(request.Context(), projectID, dataSourceID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"tables":    mapGraphTables(result.Tables),
		"edges":     mapGraphEdges(result.Edges),
		"truncated": result.Truncated,
	})
}

// tableDetail reads one table's columns and indexes. It is a separate call from
// the table list because a database here holds 459 tables: carrying every
// column of every one of them would be most of the catalog, fetched to show one.
func (h *handler) tableDetail(response http.ResponseWriter, request *http.Request) {
	projectID, tableID, ok := pathProjectSubjectIDs(response, request, "tableID")
	if !ok {
		return
	}
	if h.services.Catalog == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	detail, err := h.services.Catalog.TableDetail(request.Context(), projectID, tableID)
	if err != nil {
		if errors.Is(err, catalog.ErrNodeNotFound) {
			writeError(response, http.StatusNotFound, "NOT_FOUND", "table not found", nil)
			return
		}
		writeAdminError(response, err)
		return
	}
	columns := make([]map[string]any, len(detail.Columns))
	for index, column := range detail.Columns {
		columns[index] = map[string]any{
			"id":       strconv.FormatInt(column.ID, 10),
			"name":     column.Name,
			"dataType": column.DataType,
			"nullable": column.Nullable,
			"ordinal":  column.Ordinal,
		}
	}
	indexes := make([]map[string]any, len(detail.Indexes))
	for position, index := range detail.Indexes {
		indexes[position] = map[string]any{
			"name":    index.Name,
			"unique":  index.Unique,
			"primary": index.Primary,
			"columns": index.Columns,
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"id":            strconv.FormatInt(detail.Table.ID, 10),
		"name":          detail.Table.Name,
		"qualifiedName": detail.Table.QualifiedName,
		"columns":       columns,
		"indexes":       indexes,
	})
}

func mapGraphTables(tables []graph.Table) []map[string]any {
	output := make([]map[string]any, len(tables))
	for index, table := range tables {
		output[index] = map[string]any{
			"id":            strconv.FormatInt(table.ID, 10),
			"name":          table.Name,
			"qualifiedName": table.QualifiedName,
		}
	}
	return output
}

func mapGraphEdges(edges []graph.TableEdge) []map[string]any {
	output := make([]map[string]any, len(edges))
	for index, edge := range edges {
		output[index] = map[string]any{
			"relationId":    strconv.FormatInt(edge.RelationID, 10),
			"sourceTableId": strconv.FormatInt(edge.SourceTableID, 10),
			"targetTableId": strconv.FormatInt(edge.TargetTableID, 10),
			"sourceColumn":  edge.SourceColumn,
			"targetColumn":  edge.TargetColumn,
			"conditional":   edge.Conditional,
			"confidence":    edge.Confidence,
		}
		if len(edge.Guard) > 0 {
			output[index]["guard"] = edge.Guard
		}
	}
	return output
}
