package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appstatus "github.com/benenen/dbgraph/internal/status"
)

func TestHealthReportsServiceStatus(t *testing.T) {
	handler := NewHandler(statusStub{
		snapshot: appstatus.Snapshot{SchemaVersion: 3},
	}, nil, nil)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Status        string `json:"status"`
			SchemaVersion int    `json:"schemaVersion"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if !body.Success || body.Data.Status != "UP" || body.Data.SchemaVersion != 3 || body.Error != nil {
		t.Fatalf("health response = %#v", body)
	}
}

func TestHealthReturnsSafeUnavailableResponse(t *testing.T) {
	handler := NewHandler(statusStub{err: errors.New("database password secret-value")}, nil, nil)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"success":false`) || !strings.Contains(body, `"error":"service unavailable"`) {
		t.Fatalf("unavailable response = %q", body)
	}
	if strings.Contains(body, "secret-value") || strings.Contains(body, "database password") {
		t.Fatalf("unavailable response leaked internal error: %q", body)
	}
}

func TestHandlerComposesHealthMCPAndWebRoutes(t *testing.T) {
	mcpHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Handler", "mcp")
		response.WriteHeader(http.StatusAccepted)
	})
	webHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Handler", "web")
		response.WriteHeader(http.StatusCreated)
	})
	handler := NewHandler(statusStub{snapshot: appstatus.Snapshot{SchemaVersion: 3}}, mcpHandler, webHandler)

	testCases := []struct {
		name            string
		method          string
		path            string
		expectedStatus  int
		expectedHandler string
	}{
		{name: "health takes precedence", method: http.MethodGet, path: "/healthz", expectedStatus: http.StatusOK},
		{name: "MCP mount", method: http.MethodPost, path: "/mcp", expectedStatus: http.StatusAccepted, expectedHandler: "mcp"},
		{name: "Web fallback", method: http.MethodGet, path: "/relations", expectedStatus: http.StatusCreated, expectedHandler: "web"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(testCase.method, testCase.path, nil)

			handler.ServeHTTP(response, request)

			if response.Code != testCase.expectedStatus {
				t.Fatalf("status code = %d, want %d", response.Code, testCase.expectedStatus)
			}
			if actual := response.Header().Get("X-Handler"); actual != testCase.expectedHandler {
				t.Fatalf("X-Handler = %q, want %q", actual, testCase.expectedHandler)
			}
		})
	}
}

func TestHandlerWithoutOptionalMountsReturnsNotFound(t *testing.T) {
	handler := NewHandler(statusStub{snapshot: appstatus.Snapshot{SchemaVersion: 3}}, nil, nil)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
}

type statusStub struct {
	snapshot appstatus.Snapshot
	err      error
}

func (s statusStub) Status(context.Context) (appstatus.Snapshot, error) {
	return s.snapshot, s.err
}
