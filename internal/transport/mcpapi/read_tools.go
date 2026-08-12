package mcpapi

import (
	"context"
	"fmt"
	"strconv"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type statusInput struct{}

type statusOutput struct {
	Status             string `json:"status"`
	SchemaVersion      int    `json:"schemaVersion"`
	SQLiteVersion      string `json:"sqliteVersion"`
	JournalMode        string `json:"journalMode"`
	ForeignKeysEnabled bool   `json:"foreignKeysEnabled"`
}

type searchNodesInput struct {
	ProjectID string `json:"projectId" jsonschema:"project Snowflake ID as a decimal string"`
	Query     string `json:"query" jsonschema:"catalog search text"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum number of results from 1 to 100; defaults to 20"`
}

type getNodeInput struct {
	ProjectID     string `json:"projectId" jsonschema:"project Snowflake ID as a decimal string"`
	DataSourceID  string `json:"dataSourceId" jsonschema:"data source Snowflake ID as a decimal string"`
	QualifiedName string `json:"qualifiedName" jsonschema:"source-qualified catalog node name"`
}

type nodeOutput struct {
	ID            string `json:"id"`
	VersionID     string `json:"versionId"`
	ProjectID     string `json:"projectId"`
	DataSourceID  string `json:"dataSourceId"`
	ScanRunID     string `json:"scanRunId"`
	ParentNodeID  string `json:"parentNodeId,omitempty"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	StableKey     string `json:"stableKey"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualifiedName"`
	DataType      string `json:"dataType,omitempty"`
	Nullable      bool   `json:"nullable"`
	Ordinal       int    `json:"ordinal"`
}

type searchNodesOutput struct {
	Nodes []nodeOutput `json:"nodes"`
}

func registerReadTools(server *mcp.Server, services Services) {
	registerTool(server, objectTool("dbgraph_status", "Report dbgraph storage and schema health."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ statusInput) (*mcp.CallToolResult, statusOutput, error) {
			if services.Status == nil {
				return nil, statusOutput{}, errServiceUnavailable
			}
			snapshot, err := services.Status.Status(ctx)
			if err != nil {
				return nil, statusOutput{}, safeToolError(err)
			}
			return nil, statusOutput{
				Status: "UP", SchemaVersion: snapshot.SchemaVersion, SQLiteVersion: snapshot.SQLiteVersion,
				JournalMode: snapshot.JournalMode, ForeignKeysEnabled: snapshot.ForeignKeysEnabled,
			}, nil
		})

	registerTool(server, objectTool("dbgraph_search_nodes", "Search current catalog table and column nodes."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input searchNodesInput) (*mcp.CallToolResult, searchNodesOutput, error) {
			if services.Catalog == nil {
				return nil, searchNodesOutput{}, errServiceUnavailable
			}
			projectID, err := parseID(input.ProjectID)
			if err != nil {
				return nil, searchNodesOutput{}, safeToolError(err)
			}
			if input.Limit == 0 {
				input.Limit = 20
			}
			nodes, err := services.Catalog.SearchCurrentNodes(ctx, projectID, 0, input.Query, input.Limit)
			if err != nil {
				return nil, searchNodesOutput{}, safeToolError(err)
			}
			result := make([]nodeOutput, len(nodes))
			for index, node := range nodes {
				result[index] = mapNode(node)
			}
			return nil, searchNodesOutput{Nodes: result}, nil
		})

	registerTool(server, objectTool("dbgraph_get_node", "Get a current catalog node by source-qualified name."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input getNodeInput) (*mcp.CallToolResult, nodeOutput, error) {
			if services.Catalog == nil {
				return nil, nodeOutput{}, errServiceUnavailable
			}
			projectID, err := parseID(input.ProjectID)
			if err != nil {
				return nil, nodeOutput{}, safeToolError(err)
			}
			dataSourceID, err := parseID(input.DataSourceID)
			if err != nil {
				return nil, nodeOutput{}, err
			}
			node, err := services.Catalog.FindCurrentNode(ctx, projectID, dataSourceID, input.QualifiedName)
			if err != nil {
				return nil, nodeOutput{}, safeToolError(err)
			}
			return nil, mapNode(node), nil
		})

	registerRelationReadTools(server, services)
	registerGraphReadTools(server, services)
	registerReconcileReadTools(server, services)
	registerJobReadTools(server, services)
}

func parseID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%w: invalid decimal ID", errInvalidToolInput)
	}
	return id, nil
}

func formatID(value int64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func mapNode(node catalog.Node) nodeOutput {
	return nodeOutput{
		ID: formatID(node.ID), VersionID: formatID(node.VersionID), ProjectID: formatID(node.ProjectID),
		DataSourceID: formatID(node.DataSourceID), ScanRunID: formatID(node.ScanRunID),
		ParentNodeID: formatID(node.ParentNodeID), Kind: nodeKindName(node.Kind), Status: nodeStatusName(node.Status),
		StableKey: node.StableKey, Name: node.Name, QualifiedName: node.QualifiedName,
		DataType: node.DataType, Nullable: node.Nullable, Ordinal: node.Ordinal,
	}
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
