package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	appstatus "github.com/benenen/dbgraph/internal/status"
)

type statusReader interface {
	Status(context.Context) (appstatus.Snapshot, error)
}

type handler struct {
	status statusReader
}

func NewHandler(
	status statusReader,
	mcpHandler http.Handler,
	webHandler http.Handler,
	consoleHandler http.Handler,
) http.Handler {
	h := &handler{status: status}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	if mcpHandler != nil {
		mux.Handle("/mcp", mcpHandler)
	}
	// The console owns its prefix; the existing panels keep everything else.
	if consoleHandler != nil {
		mux.Handle("/app/", consoleHandler)
		mux.Handle("/app", http.RedirectHandler("/app/", http.StatusMovedPermanently))
	}
	if webHandler != nil {
		mux.Handle("/", webHandler)
	}
	return mux
}

func (h *handler) health(response http.ResponseWriter, request *http.Request) {
	status, err := h.status.Status(request.Context())
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]any{
			"success": false,
			"data":    nil,
			"error":   "service unavailable",
		})
		return
	}

	writeJSON(response, http.StatusOK, map[string]any{
		"success": true,
		"data": map[string]any{
			"status":        "UP",
			"schemaVersion": status.SchemaVersion,
		},
		"error": nil,
	})
}

func writeJSON(response http.ResponseWriter, statusCode int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(statusCode)
	_ = json.NewEncoder(response).Encode(value)
}
