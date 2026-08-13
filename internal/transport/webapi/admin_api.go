package webapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/relations"
)

type createDataSourceDTO struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	DSNEnvironment string `json:"dsnEnvironment"`
	// DSN is write-only. It is sealed before storage and is never rendered back
	// to any client, so an Agent reading the catalog cannot recover it.
	DSN    string `json:"dsn"`
	Reason string `json:"reason"`
}

type updateDataSourceDTO struct {
	Name           string `json:"name"`
	DSNEnvironment string `json:"dsnEnvironment"`
	// DSN is write-only and optional: empty leaves the stored credential alone.
	DSN    string `json:"dsn"`
	Reason string `json:"reason"`
}

type createCodeRepositoryDTO struct {
	Name          string `json:"name"`
	RemoteURL     string `json:"remoteUrl"`
	DefaultBranch string `json:"defaultBranch"`
	Reason        string `json:"reason"`
}

type startSchemaScanDTO struct {
	Mode   string   `json:"mode"`
	Tables []string `json:"tables"`
	Reason string   `json:"reason"`
}

func (h *handler) createCodeRepository(response http.ResponseWriter, request *http.Request) {
	principal, ok := requireAdmin(response, request)
	if !ok {
		return
	}
	if h.services.CodeRepositories == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	var input createCodeRepositoryDTO
	if err := decodeJSON(response, request, maximumJSONRequestBytes, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid code repository", nil)
		return
	}
	repository, err := h.services.CodeRepositories.CreateAsAdmin(request.Context(), catalog.AdminCreateCodeRepository{
		Name: input.Name, RemoteURL: input.RemoteURL,
		DefaultBranch: input.DefaultBranch, Principal: principal,
		Reason: input.Reason, RequestID: currentRequestID(request),
	})
	if err != nil {
		writeAdminError(response, err)
		return
	}
	response.Header().Set("Location", "/api/v1/repositories/"+strconv.FormatInt(repository.ID, 10))
	writeJSON(response, http.StatusCreated, mapCodeRepository(repository))
}

func (h *handler) createDataSource(response http.ResponseWriter, request *http.Request) {
	principal, ok := requireAdmin(response, request)
	if !ok {
		return
	}
	if h.services.Catalog == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	var input createDataSourceDTO
	if err := decodeJSON(response, request, maximumJSONRequestBytes, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid data source", nil)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(input.Kind), "MYSQL") {
		writeError(response, http.StatusUnprocessableEntity, "INVALID_DATA_SOURCE", "data source was rejected", nil)
		return
	}
	source, err := h.services.Catalog.CreateDataSourceAsAdmin(request.Context(), catalog.AdminCreateDataSource{
		Name: input.Name, Kind: catalog.DataSourceMySQL,
		DSNEnvironment: input.DSNEnvironment, DSN: input.DSN, Principal: principal,
		Reason: input.Reason, RequestID: currentRequestID(request),
	})
	if err != nil {
		writeAdminError(response, err)
		return
	}
	response.Header().Set("Location", "/api/v1/data-sources/"+strconv.FormatInt(source.ID, 10))
	writeJSON(response, http.StatusCreated, mapDataSource(source))
}

func (h *handler) startSchemaScan(response http.ResponseWriter, request *http.Request) {
	principal, ok := requireAdmin(response, request)
	if !ok {
		return
	}
	if h.services.Jobs == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	dataSourceID, err := parseID(request.PathValue("dataSourceID"))
	if !ok {
		return
	}
	var input startSchemaScanDTO
	if err := decodeJSON(response, request, maximumJSONRequestBytes, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid schema scan request", nil)
		return
	}
	mode, ok := webSchemaScanMode(input.Mode)
	if !ok {
		writeError(response, http.StatusUnprocessableEntity, "INVALID_COMMAND", "admin command was rejected", nil)
		return
	}
	job, err := h.services.Jobs.Start(request.Context(), jobs.StartSchemaScan{
		DataSourceID: dataSourceID, Principal: principal,
		Mode: mode, Tables: append([]string(nil), input.Tables...),
		Reason: input.Reason, RequestID: currentRequestID(request),
	})
	if err != nil {
		writeAdminError(response, err)
		return
	}
	response.Header().Set("Location", "/api/v1/schema-scan-jobs/"+strconv.FormatInt(job.ID, 10))
	writeJSON(response, http.StatusCreated, mapJob(job))
}

func webSchemaScanMode(value string) (jobs.SchemaScanMode, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "FULL":
		return jobs.SchemaScanFull, true
	case "INCREMENTAL":
		return jobs.SchemaScanIncremental, true
	default:
		return 0, false
	}
}

func requireAdmin(response http.ResponseWriter, request *http.Request) (relations.Principal, bool) {
	principal := currentSession(request).session.Principal
	if principal.Role != relations.RoleAdmin {
		writeError(response, http.StatusForbidden, "FORBIDDEN", "permission denied", nil)
		return relations.Principal{}, false
	}
	return principal, true
}

func writeAdminError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrForbidden), errors.Is(err, jobs.ErrForbidden):
		writeError(response, http.StatusForbidden, "FORBIDDEN", "permission denied", nil)
	case errors.Is(err, catalog.ErrDataSourceNotFound):
		writeError(response, http.StatusNotFound, "NOT_FOUND", "data source not found", nil)
	case errors.Is(err, jobs.ErrQueueFull):
		response.Header().Set("Retry-After", "30")
		writeError(response, http.StatusServiceUnavailable, "QUEUE_FULL", "schema scan queue is full", nil)
	case errors.Is(err, catalog.ErrDataSourceInUse):
		writeError(response, http.StatusConflict, "IN_USE",
			"this data source has an imported catalog; unlink it from every project instead", nil)
	case errors.Is(err, catalog.ErrDataSourceNameTaken):
		writeError(response, http.StatusConflict, "NAME_TAKEN",
			"a data source with that name already exists; link it to this project instead", nil)
	case errors.Is(err, catalog.ErrUnusableDSN):
		// The text names parameters, never the connection string itself, so it
		// is safe to hand back and is the only way the operator learns what to
		// change.
		writeError(response, http.StatusUnprocessableEntity, "UNUSABLE_DSN", err.Error(), nil)
	case errors.Is(err, catalog.ErrSealerUnavailable):
		writeError(response, http.StatusUnprocessableEntity, "SECRET_KEY_REQUIRED",
			"storing a DSN requires DBGRAPH_SECRET_KEY on the server", nil)
	case errors.Is(err, catalog.ErrInvalidRepository),
		errors.Is(err, catalog.ErrInvalidDataSource), errors.Is(err, jobs.ErrInvalidJob):
		writeError(response, http.StatusUnprocessableEntity, "INVALID_COMMAND", "admin command was rejected", nil)
	default:
		writeError(response, http.StatusInternalServerError, "INTERNAL", "request could not be completed", nil)
	}
}

func mapCodeRepository(repository catalog.CodeRepository) map[string]any {
	return map[string]any{
		"id": strconv.FormatInt(repository.ID, 10), "name": repository.Name, "defaultBranch": repository.DefaultBranch,
		"createdAt": repository.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapDataSource(source catalog.DataSource) map[string]any {
	return map[string]any{
		"id":   strconv.FormatInt(source.ID, 10),
		"name": source.Name, "kind": "MYSQL", "dsnEnvironment": source.DSNEnvironment,
		"createdAt": source.CreatedAt.UTC().Format(time.RFC3339Nano), "updatedAt": source.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// mapDataSourceForRole withholds deployment detail from non-admins. The name
// and kind are needed by every role to label catalog nodes; the environment
// variable name describes the deployment, not the catalog.
func mapDataSourceForRole(source catalog.DataSource, role relations.Role) map[string]any {
	if role == relations.RoleAdmin {
		return mapDataSource(source)
	}
	return map[string]any{
		"id":   strconv.FormatInt(source.ID, 10),
		"name": source.Name, "kind": "MYSQL",
	}
}

// listAllDataSources serves the shared registry. Any signed-in role may see
// which sources exist so a project can adopt one; the DSN environment variable
// stays Admin-only, as it does everywhere else.
func (h *handler) listAllDataSources(response http.ResponseWriter, request *http.Request) {
	if h.services.Catalog == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	limit := catalog.DefaultListLimit
	sources, err := h.services.Catalog.ListAllDataSources(request.Context(), limit)
	if err != nil {
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

// deleteDataSource removes a source that has imported nothing. Admin only.
func (h *handler) deleteDataSource(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireAdmin(response, request); !ok {
		return
	}
	if h.services.Catalog == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	dataSourceID, err := parseID(request.PathValue("dataSourceID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid data source ID", nil)
		return
	}
	if err := h.services.Catalog.DeleteDataSource(request.Context(), dataSourceID); err != nil {
		writeAdminError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"deleted": strconv.FormatInt(dataSourceID, 10)})
}

func (h *handler) updateDataSource(response http.ResponseWriter, request *http.Request) {
	principal, ok := requireAdmin(response, request)
	if !ok {
		return
	}
	if h.services.Catalog == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "service unavailable", nil)
		return
	}
	dataSourceID, err := parseID(request.PathValue("dataSourceID"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid data source ID", nil)
		return
	}
	var input updateDataSourceDTO
	if err := decodeJSON(response, request, maximumJSONRequestBytes, &input); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "invalid data source", nil)
		return
	}
	source, err := h.services.Catalog.UpdateDataSourceAsAdmin(request.Context(), catalog.AdminUpdateDataSource{
		DataSourceID: dataSourceID, Name: input.Name, DSNEnvironment: input.DSNEnvironment,
		DSN: input.DSN, Principal: principal, Reason: input.Reason,
		RequestID: currentRequestID(request),
	})
	if err != nil {
		writeAdminError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapDataSourceForRole(source, principal.Role))
}
