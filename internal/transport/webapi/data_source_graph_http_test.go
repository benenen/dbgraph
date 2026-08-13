package webapi

import (
	"net/http"
	"strings"
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

	response := client.request(http.MethodGet, "/api/v1/data-sources/30/tables?q=order", "", false)
	assertWebStatus(t, response, http.StatusOK, "")
	if service.tablesFilter != "order" {
		t.Fatalf("filter = %q, want order", service.tablesFilter)
	}
	if service.tablesSourceID != 30 || service.tablesLimit != maximumTableListSize {
		t.Fatalf("list tables source=%d limit=%d", service.tablesSourceID, service.tablesLimit)
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

func TestTableDetailReturnsColumnsAndIndexes(t *testing.T) {
	t.Parallel()

	service := &catalogHTTPStub{tableDetail: catalog.TableDetail{
		Table: catalog.TableSummary{ID: 91, Name: "orders", QualifiedName: "resource.orders"},
		Columns: []catalog.Column{
			{ID: 101, Name: "id", DataType: "BIGINT", Nullable: false, Ordinal: 1},
			{ID: 102, Name: "external_id", DataType: "VARCHAR(64)", Nullable: true, Ordinal: 2},
		},
		Indexes: []catalog.Index{{Name: "PRIMARY", Unique: true, Primary: true, Columns: []string{"id"}}},
	}}
	client := newWebTestClient(t, Services{Catalog: service}, relations.RoleViewer)

	response := client.request(http.MethodGet, "/api/v1/tables/91", "", false)
	assertWebStatus(t, response, http.StatusOK, "")
	if service.tableDetailID != 91 {
		t.Fatalf("table detail id=%d, want 91", service.tableDetailID)
	}
	data := decodeWebEnvelope(t, response)["data"].(map[string]any)
	columns := data["columns"].([]any)
	if len(columns) != 2 || columns[1].(map[string]any)["id"] != "102" ||
		columns[1].(map[string]any)["nullable"] != true {
		t.Fatalf("columns=%#v", columns)
	}
	indexes := data["indexes"].([]any)
	if len(indexes) != 1 || indexes[0].(map[string]any)["name"] != "PRIMARY" ||
		indexes[0].(map[string]any)["primary"] != true {
		t.Fatalf("indexes=%#v", indexes)
	}
}

func TestCatalogBrowseRoutesValidateInputsAndMapServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		services   Services
		path       string
		wantStatus int
		wantCode   string
	}{
		{name: "invalid data source id", services: Services{Catalog: &catalogHTTPStub{}}, path: "/api/v1/data-sources/not-an-id/tables", wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "missing table list service", services: Services{}, path: "/api/v1/data-sources/30/tables", wantStatus: http.StatusServiceUnavailable, wantCode: "UNAVAILABLE"},
		{name: "long table filter", services: Services{Catalog: &catalogHTTPStub{}}, path: "/api/v1/data-sources/30/tables?q=" + strings.Repeat("x", 201), wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "table list source missing", services: Services{Catalog: &catalogHTTPStub{tablesErr: catalog.ErrDataSourceNotFound}}, path: "/api/v1/data-sources/30/tables", wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "table list unexpected", services: Services{Catalog: &catalogHTTPStub{tablesErr: errUnexpectedWebFailure}}, path: "/api/v1/data-sources/30/tables", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "invalid table id", services: Services{Catalog: &catalogHTTPStub{}}, path: "/api/v1/tables/0", wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "missing table detail service", services: Services{}, path: "/api/v1/tables/91", wantStatus: http.StatusServiceUnavailable, wantCode: "UNAVAILABLE"},
		{name: "table missing", services: Services{Catalog: &catalogHTTPStub{tableDetailErr: catalog.ErrNodeNotFound}}, path: "/api/v1/tables/91", wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "table detail unexpected", services: Services{Catalog: &catalogHTTPStub{tableDetailErr: errUnexpectedWebFailure}}, path: "/api/v1/tables/91", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newWebTestClient(t, test.services, relations.RoleViewer)
			response := client.request(http.MethodGet, test.path, "", false)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func TestTableListReportsItsHardCap(t *testing.T) {
	tables := make([]catalog.TableSummary, maximumTableListSize)
	service := &catalogHTTPStub{tables: tables}
	client := newWebTestClient(t, Services{Catalog: service}, relations.RoleViewer)

	response := client.request(http.MethodGet, "/api/v1/data-sources/30/tables", "", false)
	assertWebStatus(t, response, http.StatusOK, "")
	if decodeWebEnvelope(t, response)["data"].(map[string]any)["truncated"] != true {
		t.Fatalf("table list should report truncation at %d rows", maximumTableListSize)
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

	response := client.request(http.MethodGet, "/api/v1/data-sources/30/relation-graph", "", false)
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
	response := client.request(http.MethodGet, "/api/v1/data-sources/30/relation-graph", "", false)
	assertWebStatus(t, response, http.StatusOK, "")
	data := decodeWebEnvelope(t, response)["data"].(map[string]any)
	if len(data["edges"].([]any)) != 0 || len(data["tables"].([]any)) != 0 {
		t.Fatalf("graph = %#v, want empty", data)
	}
}

func TestDataSourceGraphValidatesInputsAndMapsFailures(t *testing.T) {
	tests := []struct {
		name       string
		services   Services
		path       string
		wantStatus int
		wantCode   string
	}{
		{name: "invalid id", services: Services{Graph: &graphHTTPStub{}}, path: "/api/v1/data-sources/nope/relation-graph", wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST"},
		{name: "missing service", services: Services{}, path: "/api/v1/data-sources/30/relation-graph", wantStatus: http.StatusServiceUnavailable, wantCode: "UNAVAILABLE"},
		{name: "service failure", services: Services{Graph: &graphHTTPStub{dataSourceErr: errUnexpectedWebFailure}}, path: "/api/v1/data-sources/30/relation-graph", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newWebTestClient(t, test.services, relations.RoleViewer)
			response := client.request(http.MethodGet, test.path, "", false)
			assertWebStatus(t, response, test.wantStatus, test.wantCode)
		})
	}
}
