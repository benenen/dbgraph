package mcpapi

import (
	"context"
	"slices"
	"testing"

	"github.com/benenen/dbgraph/internal/catalog"
	appstatus "github.com/benenen/dbgraph/internal/status"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type statusStub struct{}

func (statusStub) Status(context.Context) (appstatus.Snapshot, error) {
	return appstatus.Snapshot{SchemaVersion: 3, SQLiteVersion: "3.51.4", JournalMode: "wal", ForeignKeysEnabled: true}, nil
}

type catalogStub struct {
}

func (s *catalogStub) FindCurrentNode(context.Context, int64, string) (catalog.Node, error) {
	return catalog.Node{}, nil
}

func (s *catalogStub) SearchCurrentNodes(_ context.Context, _ int64, _ string, _ int) ([]catalog.Node, error) {
	return []catalog.Node{{ID: 9_007_199_254_740_993, Kind: catalog.NodeColumn, QualifiedName: "app.orders.id"}}, nil
}

func TestServerPublishesTheCompleteMCPToolSurfaceAndStringIDs(t *testing.T) {
	catalogService := &catalogStub{}
	server := NewServer(Services{Status: statusStub{}, Catalog: catalogService}, ViewerPrincipal())
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"dbgraph_begin_relation_init", "dbgraph_complete_relation_init", "dbgraph_explain_relation",
		"dbgraph_get_job", "dbgraph_get_node", "dbgraph_get_relation", "dbgraph_get_relation_init",
		"dbgraph_impact", "dbgraph_list_proposals", "dbgraph_list_unresolved", "dbgraph_propose_relation",
		"dbgraph_propose_relation_revision", "dbgraph_propose_relation_tombstone", "dbgraph_propose_relations",
		"dbgraph_replace_source_binding", "dbgraph_resolve_workspace_data_sources", "dbgraph_restore_relation",
		"dbgraph_review_relation", "dbgraph_search_nodes", "dbgraph_start_schema_scan",
		"dbgraph_status", "dbgraph_suppress_relation", "dbgraph_trace",
	}
	got := make([]string, 0, len(tools.Tools))
	schemas := make(map[string]map[string]any, len(tools.Tools))
	var proposalSchema map[string]any
	var initSchema map[string]any
	var scanSchema map[string]any
	for _, tool := range tools.Tools {
		got = append(got, tool.Name)
		schemas[tool.Name], _ = tool.InputSchema.(map[string]any)
		if tool.Name == "dbgraph_propose_relation" {
			proposalSchema, _ = tool.InputSchema.(map[string]any)
		}
		if tool.Name == "dbgraph_start_schema_scan" {
			scanSchema, _ = tool.InputSchema.(map[string]any)
		}
		if tool.Name == "dbgraph_begin_relation_init" {
			initSchema, _ = tool.InputSchema.(map[string]any)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	assertSchemaProperties(t, schemas["dbgraph_search_nodes"], "query", "limit")
	assertSchemaProperties(t, schemas["dbgraph_get_node"], "dataSourceId", "qualifiedName")
	assertSchemaProperties(t, schemas["dbgraph_list_proposals"], "limit")
	assertSchemaProperties(t, schemas["dbgraph_list_unresolved"], "limit")
	assertSchemaProperties(t, schemas["dbgraph_start_schema_scan"], "dataSourceId")
	assertSchemaProperties(t, schemas["dbgraph_begin_relation_init"], "repositoryId", "mode")
	assertSchemaProperties(t, schemas["dbgraph_resolve_workspace_data_sources"], "remotes", "context")
	assertSchemaProperties(t, schemas["dbgraph_replace_source_binding"],
		"repositoryId", "context", "dataSourceIds", "expectedRevisionNo", "reason", "requestId")
	assertSchemaProperties(t, schemas["dbgraph_trace"], "startNodeId", "targetNodeId")
	assertSchemaProperties(t, schemas["dbgraph_impact"], "startNodeId", "targetNodeId")
	properties, _ := proposalSchema["properties"].(map[string]any)
	if proposalSchema["additionalProperties"] != false || properties["role"] != nil {
		t.Fatalf("proposal schema = %#v", proposalSchema)
	}
	scanProperties, _ := scanSchema["properties"].(map[string]any)
	if scanSchema["additionalProperties"] != false || scanProperties["reason"] == nil || scanProperties["requestId"] == nil ||
		!schemaRequires(scanSchema, "reason") || !schemaRequires(scanSchema, "requestId") {
		t.Fatalf("schema scan schema = %#v", scanSchema)
	}
	assertIncrementalInitScopeSchema(t, initSchema)
	bindingProperties, _ := schemas["dbgraph_replace_source_binding"]["properties"].(map[string]any)
	if schemas["dbgraph_replace_source_binding"]["additionalProperties"] != false || bindingProperties["role"] != nil ||
		!schemaRequires(schemas["dbgraph_replace_source_binding"], "dataSourceIds") {
		t.Fatalf("source binding schema = %#v", schemas["dbgraph_replace_source_binding"])
	}

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "dbgraph_search_nodes",
		Arguments: map[string]any{
			"query": "orders",
			"limit": 10,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("search returned a tool error: %v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content type = %T", result.StructuredContent)
	}
	nodes, ok := structured["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("nodes = %#v", structured["nodes"])
	}
	node, ok := nodes[0].(map[string]any)
	if !ok || node["id"] != "9007199254740993" {
		t.Fatalf("node = %#v", nodes[0])
	}
}

func assertSchemaProperties(t *testing.T, schema map[string]any, names ...string) {
	t.Helper()
	properties, _ := schema["properties"].(map[string]any)
	for _, name := range names {
		if properties[name] == nil {
			t.Fatalf("schema property %q is missing: %#v", name, schema)
		}
	}
}

func assertIncrementalInitScopeSchema(t *testing.T, schema map[string]any) {
	t.Helper()
	allOf, ok := schema["allOf"].([]any)
	if !ok || len(allOf) != 1 {
		t.Fatalf("relation init conditional schema = %#v", schema)
	}
	conditional, _ := allOf[0].(map[string]any)
	thenSchema, _ := conditional["then"].(map[string]any)
	thenProperties, _ := thenSchema["properties"].(map[string]any)
	scopeSchema, _ := thenProperties["scope"].(map[string]any)
	scopeProperties, _ := scopeSchema["properties"].(map[string]any)
	relationIDs, _ := scopeProperties["relationIds"].(map[string]any)
	items, _ := relationIDs["items"].(map[string]any)
	if !schemaRequires(thenSchema, "scope") || !schemaRequires(scopeSchema, "relationIds") ||
		relationIDs["maxItems"] != float64(1000) || items["pattern"] != "^[1-9][0-9]{0,18}$" {
		t.Fatalf("incremental relation init scope schema = %#v", conditional)
	}
}

func schemaRequires(schema map[string]any, field string) bool {
	required, _ := schema["required"].([]any)
	for _, value := range required {
		if value == field {
			return true
		}
	}
	return false
}
