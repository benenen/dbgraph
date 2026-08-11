package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Config struct {
	ServerURL string
	Token     string
}

func Run(ctx context.Context, config Config, localTransport mcp.Transport) (returnError error) {
	endpoint, err := endpointURL(config.ServerURL)
	if err != nil {
		return err
	}
	if localTransport == nil {
		return errors.New("local MCP transport is required")
	}
	httpClient := &http.Client{Timeout: 30 * time.Second, CheckRedirect: rejectRedirect}
	if strings.TrimSpace(config.Token) != "" {
		httpClient.Transport = bearerTransport{base: http.DefaultTransport, token: strings.TrimSpace(config.Token)}
	}
	remoteClient := mcp.NewClient(&mcp.Implementation{Name: "dbgraph-stdio-proxy", Version: "0.1.0"}, nil)
	connectContext, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	defer cancelConnect()
	remoteSession, err := remoteClient.Connect(connectContext, &mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		return fmt.Errorf("connect to dbgraph MCP server: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, remoteSession.Close()) }()

	proxyServer := mcp.NewServer(&mcp.Implementation{Name: "dbgraph-stdio-proxy", Version: "0.1.0"}, nil)
	if err := mirrorTools(ctx, proxyServer, remoteSession); err != nil {
		return err
	}
	if err := proxyServer.Run(ctx, localTransport); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run local MCP proxy: %w", err)
	}
	return nil
}

func mirrorTools(ctx context.Context, proxyServer *mcp.Server, remote *mcp.ClientSession) error {
	cursor := ""
	seenCursors := make(map[string]struct{})
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		page, err := remote.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("list remote MCP tools: %w", err)
		}
		for _, remoteTool := range page.Tools {
			if remoteTool == nil {
				continue
			}
			tool := *remoteTool
			toolName := tool.Name
			proxyServer.AddTool(&tool, func(callContext context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				arguments := append(json.RawMessage(nil), request.Params.Arguments...)
				return remote.CallTool(callContext, &mcp.CallToolParams{
					Name: toolName, Arguments: arguments, InputResponses: request.Params.InputResponses,
					RequestState: request.Params.RequestState,
				})
			})
		}
		if page.NextCursor == "" {
			return nil
		}
		if _, repeated := seenCursors[page.NextCursor]; repeated {
			return errors.New("remote MCP tools pagination repeated a cursor")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	return errors.New("remote MCP tools pagination exceeded 100 pages")
}

func endpointURL(serverURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("server URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if parsed.Scheme == "http" && !isLoopback(parsed.Hostname()) {
		return "", errors.New("plain HTTP is allowed only for a loopback dbgraph server")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/mcp"
	}
	return parsed.String(), nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func rejectRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copy)
}
