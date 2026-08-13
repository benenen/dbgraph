package mcpapi

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type getJobInput struct {
	JobID string `json:"jobId"`
}

type startSchemaScanInput struct {
	DataSourceID string   `json:"dataSourceId"`
	Mode         string   `json:"mode"`
	Tables       []string `json:"tables"`
	Reason       string   `json:"reason"`
	RequestID    string   `json:"requestId"`
}

type jobOutput struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Status       string `json:"status"`
	Payload      any    `json:"payload"`
	Result       any    `json:"result,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	CreatedAt    string `json:"createdAt"`
	StartedAt    string `json:"startedAt,omitempty"`
	CompletedAt  string `json:"completedAt,omitempty"`
	RevisionNo   int    `json:"revisionNo"`
}

func registerJobReadTools(server *mcp.Server, services Services) {
	registerTool(server, objectTool("dbgraph_get_job", "Get a background schema scan job."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input getJobInput) (*mcp.CallToolResult, jobOutput, error) {
			if services.Jobs == nil {
				return nil, jobOutput{}, errServiceUnavailable
			}
			jobID, err := parseID(input.JobID)
			if err != nil {
				return nil, jobOutput{}, err
			}
			job, err := services.Jobs.Get(ctx, jobID)
			return nil, mapJob(job), safeToolError(err)
		})
}

func registerJobWriteTools(server *mcp.Server, services Services, principal relations.Principal) {
	registerTool(server, objectTool("dbgraph_start_schema_scan", "Queue a schema scan job. Requires Admin; source credentials stay in environment variables."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input startSchemaScanInput) (*mcp.CallToolResult, jobOutput, error) {
			if principal.Role != relations.RoleAdmin {
				return nil, jobOutput{}, errForbidden
			}
			if services.Jobs == nil {
				return nil, jobOutput{}, errServiceUnavailable
			}
			dataSourceID, err := parseID(input.DataSourceID)
			if err != nil {
				return nil, jobOutput{}, err
			}
			mode, err := parseSchemaScanMode(input.Mode)
			if err != nil {
				return nil, jobOutput{}, err
			}
			job, err := services.Jobs.Start(ctx, jobs.StartSchemaScan{
				DataSourceID: dataSourceID,
				Mode:         mode, Tables: append([]string(nil), input.Tables...),
				Reason: input.Reason, RequestID: input.RequestID,
			})
			return nil, mapJob(job), safeToolError(err)
		})
}

func parseSchemaScanMode(value string) (jobs.SchemaScanMode, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "FULL":
		return jobs.SchemaScanFull, nil
	case "INCREMENTAL":
		return jobs.SchemaScanIncremental, nil
	default:
		return 0, errInvalidToolInput
	}
}

func mapJob(job jobs.Job) jobOutput {
	var payload any
	var result any
	_ = json.Unmarshal(job.Payload, &payload)
	if len(job.Result) > 0 {
		_ = json.Unmarshal(job.Result, &result)
	}
	output := jobOutput{
		ID: formatID(job.ID), Type: jobTypeName(job.Type),
		Status: jobStatusName(job.Status), Payload: payload, Result: result,
		ErrorCode: job.ErrorCode,
		CreatedAt: job.CreatedAt.UTC().Format(timeFormat), RevisionNo: job.RevisionNo,
	}
	if job.StartedAt != nil {
		output.StartedAt = job.StartedAt.UTC().Format(timeFormat)
	}
	if job.CompletedAt != nil {
		output.CompletedAt = job.CompletedAt.UTC().Format(timeFormat)
	}
	return output
}

func jobTypeName(value jobs.Type) string {
	if value == jobs.TypeSchemaScan {
		return "SCHEMA_SCAN"
	}
	return "UNKNOWN"
}

func jobStatusName(value jobs.Status) string {
	switch value {
	case jobs.StatusPending:
		return "PENDING"
	case jobs.StatusRunning:
		return "RUNNING"
	case jobs.StatusSucceeded:
		return "SUCCEEDED"
	case jobs.StatusFailed:
		return "FAILED"
	case jobs.StatusCancelled:
		return "CANCELLED"
	default:
		return "UNKNOWN"
	}
}
