package webapi

import (
	"net/http"
	"testing"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/relations"
)

// A scanned catalog is worth reading before any relation exists, so the table
// list stands on its own and the filter reaches the service verbatim.
func TestTableListBrowsesOneDataSource(t *testing.T) {
	t.Parallel()

	service := &catalogHTTPStub{tables: []catalog.TableSummary{
		{ID: 91, Name: "orders", QualifiedName: "resource.orders"},
		{ID: 92, Name: "order_item", QualifiedName: "resource.order_item"},
	}}
	client := newWebTestClient(t, Services{Catalog: service}, relations.RoleViewer)

	response := client.request(http.MethodGet, "/api/v1/projects/10/data-sources/30/tables?q=order", "", false)
	assertWebStatus(t, response, http.StatusOK, "")
	if service.tablesFilter != "order" {
		t.Fatalf("filter = %q, want order", service.tablesFilter)
	}
	tables := decodeWebEnvelope(t, response)["data"].(map[string]any)["tables"].([]any)
	if len(tables) != 2 {
		t.Fatalf("tables = %#v, want two", tables)
	}
	first := tables[0].(map[string]any)
	// IDs cross the wire as strings: a Snowflake id exceeds a JavaScript number.
	if first["id"] != "91" || first["qualifiedName"] != "resource.orders" {
		t.Fatalf("table = %#v", first)
	}
}

// The graph is drawn between tables even though relations join columns, and it
// says which columns joined so a reader can tell two edges apart.
func TestDataSourceGraphReturnsTableEdges(t *testing.T) {
	t.Parallel()

	service := &graphHTTPStub{dataSourceGraph: graph.DataSourceGraph{
		Tables: []graph.Table{
			{ID: 91, Name: "orders", QualifiedName: "resource.orders"},
			{ID: 92, Name: "users", QualifiedName: "resource.users"},
		},
		Edges: []graph.TableEdge{{
			RelationID: 71, SourceTableID: 91, TargetTableID: 92,
			SourceColumn: "user_id", TargetColumn: "id",
			Conditional: true, Confidence: 0.95,
		}},
	}}
	client := newWebTestClient(t, Services{Graph: service}, relations.RoleViewer)

	response := client.request(http.MethodGet, "/api/v1/projects/10/data-sources/30/relation-graph", "", false)
	assertWebStatus(t, response, http.StatusOK, "")
	if service.dataSourceCalls != 1 || service.dataSourceSource != 30 {
		t.Fatalf("calls=%d dataSourceID=%d", service.dataSourceCalls, service.dataSourceSource)
	}
	data := decodeWebEnvelope(t, response)["data"].(map[string]any)
	edge := data["edges"].([]any)[0].(map[string]any)
	if edge["sourceTableId"] != "91" || edge["targetTableId"] != "92" {
		t.Fatalf("edge endpoints = %#v", edge)
	}
	if edge["sourceColumn"] != "user_id" || edge["targetColumn"] != "id" || edge["conditional"] != true {
		t.Fatalf("edge detail = %#v", edge)
	}
	if data["truncated"] != false {
		t.Fatalf("truncated = %#v, want false", data["truncated"])
	}
}

// A fully scanned source with no relations is the ordinary state, not an error.
func TestDataSourceGraphIsEmptyWithoutRelations(t *testing.T) {
	t.Parallel()

	client := newWebTestClient(t, Services{Graph: &graphHTTPStub{}}, relations.RoleViewer)
	response := client.request(http.MethodGet, "/api/v1/projects/10/data-sources/30/relation-graph", "", false)
	assertWebStatus(t, response, http.StatusOK, "")
	data := decodeWebEnvelope(t, response)["data"].(map[string]any)
	if len(data["edges"].([]any)) != 0 || len(data["tables"].([]any)) != 0 {
		t.Fatalf("graph = %#v, want empty", data)
	}
}
