package mcpproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Value string `json:"value"`
}

type echoOutput struct {
	Value string `json:"value"`
}

func TestProxyDiscoversAndForwardsRemoteTools(t *testing.T) {
	var receivedRaw json.RawMessage
	remote := mcp.NewServer(&mcp.Implementation{Name: "remote", Version: "1"}, nil)
	mcp.AddTool(remote, &mcp.Tool{Name: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, input echoInput) (*mcp.CallToolResult, echoOutput, error) {
			return nil, echoOutput(input), nil
		})
	remote.AddTool(&mcp.Tool{
		Name: "raw", InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		receivedRaw = append(json.RawMessage(nil), request.Params.Arguments...)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	remoteHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return remote
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(remoteHTTP.Close)

	clientTransport, proxyTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- Run(ctx, Config{ServerURL: remoteHTTP.URL}, proxyTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 2 || tools.Tools[0].Name != "echo" || tools.Tools[1].Name != "raw" {
		t.Fatalf("tools = %#v", tools.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"value": "forwarded"}})
	if err != nil || result.IsError {
		t.Fatalf("call result=%#v err=%v", result, err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["value"] != "forwarded" {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "raw", Arguments: json.RawMessage(`{"value":9007199254740993}`),
	})
	if err != nil || result.IsError || string(receivedRaw) != `{"value":9007199254740993}` {
		t.Fatalf("raw forwarding result=%#v err=%v received=%s", result, err, receivedRaw)
	}

	cancel()
	select {
	case <-proxyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not stop after cancellation")
	}
}
