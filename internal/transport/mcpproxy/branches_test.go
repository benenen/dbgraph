package mcpproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestEndpointURLValidationAndDefaults(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"http://localhost:8080":      "http://localhost:8080/mcp",
		"http://127.0.0.1:8080/":     "http://127.0.0.1:8080/mcp",
		"http://[::1]:8080/custom":   "http://[::1]:8080/custom",
		"https://dbgraph.test":       "https://dbgraph.test/mcp",
		" https://dbgraph.test/mcp ": "https://dbgraph.test/mcp",
	}
	for input, want := range valid {
		got, err := endpointURL(input)
		if err != nil || got != want {
			t.Fatalf("endpointURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{
		"", "%", "ftp://localhost/path", "http:///path", "http://user:pass@localhost",
		"http://localhost/mcp?x=1", "http://localhost/mcp#fragment", "http://example.test/mcp",
	} {
		if _, err := endpointURL(input); err == nil {
			t.Fatalf("endpointURL(%q) returned nil", input)
		}
	}
}

func TestLoopbackRecognition(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"localhost", "LOCALHOST", "127.0.0.1", "::1"} {
		if !isLoopback(host) {
			t.Fatalf("%q was not recognized as loopback", host)
		}
	}
	for _, host := range []string{"", "example.test", "192.0.2.1"} {
		if isLoopback(host) {
			t.Fatalf("%q was recognized as loopback", host)
		}
	}
}

func TestBearerTransportClonesRequestAndOverridesAuthorization(t *testing.T) {
	t.Parallel()

	original := &http.Request{Header: http.Header{"Authorization": []string{"Bearer old"}, "X-Test": []string{"value"}}}
	transport := bearerTransport{
		token: "new-token",
		base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			if request == original || request.Header.Get("Authorization") != "Bearer new-token" || request.Header.Get("X-Test") != "value" {
				t.Fatalf("forwarded request = %#v", request)
			}
			request.Header.Set("X-Test", "changed")
			return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
		}),
	}
	response, err := transport.RoundTrip(original)
	if err != nil || response.StatusCode != http.StatusNoContent {
		t.Fatalf("RoundTrip response = %#v, error = %v", response, err)
	}
	if original.Header.Get("Authorization") != "Bearer old" || original.Header.Get("X-Test") != "value" {
		t.Fatalf("original request was mutated: %#v", original.Header)
	}

	sentinel := errors.New("transport failed")
	transport.base = roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, sentinel })
	if _, err := transport.RoundTrip(original); !errors.Is(err, sentinel) {
		t.Fatalf("RoundTrip error = %v", err)
	}
}

func TestRedirectsAndEarlyRunValidation(t *testing.T) {
	t.Parallel()

	if err := rejectRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("rejectRedirect = %v", err)
	}
	if err := Run(context.Background(), Config{ServerURL: "%"}, &mcp.StdioTransport{}); err == nil {
		t.Fatal("Run accepted invalid endpoint")
	}
	if err := Run(context.Background(), Config{ServerURL: "http://localhost"}, nil); err == nil {
		t.Fatal("Run accepted nil local transport")
	}
}
