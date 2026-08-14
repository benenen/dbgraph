package mcpapi

import (
	"context"

	"github.com/benenen/dbgraph/internal/relations"
	"github.com/benenen/dbgraph/internal/sourcebinding"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type resolveWorkspaceDataSourcesInput struct {
	Remotes *[]string `json:"remotes"`
	Context string    `json:"context,omitempty"`
}

type replaceSourceBindingInput struct {
	RepositoryID       string    `json:"repositoryId"`
	Context            string    `json:"context"`
	DataSourceIDs      *[]string `json:"dataSourceIds"`
	ExpectedRevisionNo *int      `json:"expectedRevisionNo"`
	Reason             string    `json:"reason"`
	RequestID          string    `json:"requestId"`
}

type sourceBindingDataSourceOutput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type sourceBindingResolutionOutput struct {
	Status            string                          `json:"status"`
	RepositoryID      string                          `json:"repositoryId,omitempty"`
	RepositoryName    string                          `json:"repositoryName,omitempty"`
	Context           string                          `json:"context,omitempty"`
	BindingRevisionID string                          `json:"bindingRevisionId,omitempty"`
	BindingRevisionNo int                             `json:"bindingRevisionNo,omitempty"`
	DataSources       []sourceBindingDataSourceOutput `json:"dataSources"`
}

type sourceBindingRevisionOutput struct {
	ID             string                          `json:"id"`
	RepositoryID   string                          `json:"repositoryId"`
	RepositoryName string                          `json:"repositoryName"`
	Context        string                          `json:"context"`
	RevisionNo     int                             `json:"revisionNo"`
	DataSources    []sourceBindingDataSourceOutput `json:"dataSources"`
	CreatedAt      string                          `json:"createdAt"`
}

func registerSourceBindingReadTools(server *mcp.Server, services Services, principal relations.Principal) {
	registerTool(server, objectTool(
		"dbgraph_resolve_workspace_data_sources",
		"Resolve exact Git workspace evidence and context to an audited data-source binding.",
	), func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		input resolveWorkspaceDataSourcesInput,
	) (*mcp.CallToolResult, sourceBindingResolutionOutput, error) {
		if principal.Role != relations.RoleAgent && principal.Role != relations.RoleAdmin {
			return nil, sourceBindingResolutionOutput{}, errForbidden
		}
		if services.SourceBindings == nil {
			return nil, sourceBindingResolutionOutput{}, errServiceUnavailable
		}
		if input.Remotes == nil {
			return nil, sourceBindingResolutionOutput{}, errInvalidToolInput
		}
		resolution, err := services.SourceBindings.ResolveWorkspace(ctx, sourcebinding.WorkspaceEvidence{
			Remotes: append([]string(nil), (*input.Remotes)...), Context: input.Context,
		})
		if err != nil {
			return nil, sourceBindingResolutionOutput{}, err
		}
		return nil, mapSourceBindingResolution(resolution), nil
	})
}

func registerSourceBindingWriteTools(server *mcp.Server, services Services, principal relations.Principal) {
	registerTool(server, objectTool(
		"dbgraph_replace_source_binding",
		"Atomically replace an audited repository/context data-source binding. Requires Admin.",
	), func(
		ctx context.Context,
		_ *mcp.CallToolRequest,
		input replaceSourceBindingInput,
	) (*mcp.CallToolResult, sourceBindingRevisionOutput, error) {
		if principal.Role != relations.RoleAdmin {
			return nil, sourceBindingRevisionOutput{}, errForbidden
		}
		if services.SourceBindings == nil {
			return nil, sourceBindingRevisionOutput{}, errServiceUnavailable
		}
		repositoryID, err := parseID(input.RepositoryID)
		if err != nil {
			return nil, sourceBindingRevisionOutput{}, err
		}
		if input.DataSourceIDs == nil || input.ExpectedRevisionNo == nil ||
			*input.ExpectedRevisionNo < 0 || *input.ExpectedRevisionNo > sourcebinding.MaximumExpectedRevisionNo {
			return nil, sourceBindingRevisionOutput{}, errInvalidToolInput
		}
		dataSourceIDs, err := parseUniqueSourceBindingIDs(*input.DataSourceIDs)
		if err != nil {
			return nil, sourceBindingRevisionOutput{}, err
		}
		revision, err := services.SourceBindings.ReplaceBindingSet(ctx, sourcebinding.ReplaceBindingSet{
			RepositoryID: repositoryID, Context: input.Context, DataSourceIDs: dataSourceIDs,
			ExpectedRevisionNo: *input.ExpectedRevisionNo, Principal: principal,
			Reason: input.Reason, RequestID: input.RequestID,
		})
		if err != nil {
			return nil, sourceBindingRevisionOutput{}, err
		}
		return nil, mapSourceBindingRevision(revision), nil
	})
}

func parseUniqueSourceBindingIDs(values []string) ([]int64, error) {
	if len(values) > 50 {
		return nil, errInvalidToolInput
	}
	result := make([]int64, len(values))
	seen := make(map[int64]struct{}, len(values))
	for index, value := range values {
		id, err := parseID(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[id]; exists {
			return nil, errInvalidToolInput
		}
		seen[id] = struct{}{}
		result[index] = id
	}
	return result, nil
}

func mapSourceBindingResolution(resolution sourcebinding.Resolution) sourceBindingResolutionOutput {
	return sourceBindingResolutionOutput{
		Status: string(resolution.Status), RepositoryID: formatID(resolution.RepositoryID),
		RepositoryName: resolution.RepositoryName, Context: resolution.Context,
		BindingRevisionID: formatID(resolution.BindingRevisionID), BindingRevisionNo: resolution.BindingRevisionNo,
		DataSources: mapSourceBindingDataSources(resolution.DataSources),
	}
}

func mapSourceBindingRevision(revision sourcebinding.BindingRevision) sourceBindingRevisionOutput {
	return sourceBindingRevisionOutput{
		ID: formatID(revision.ID), RepositoryID: formatID(revision.RepositoryID), RepositoryName: revision.RepositoryName,
		Context: revision.Context, RevisionNo: revision.RevisionNo,
		DataSources: mapSourceBindingDataSources(revision.DataSources), CreatedAt: revision.CreatedAt.UTC().Format(timeFormat),
	}
}

func mapSourceBindingDataSources(sources []sourcebinding.DataSource) []sourceBindingDataSourceOutput {
	result := make([]sourceBindingDataSourceOutput, len(sources))
	for index, source := range sources {
		result[index] = sourceBindingDataSourceOutput{ID: formatID(source.ID), Name: source.Name, Kind: source.Kind}
	}
	return result
}
