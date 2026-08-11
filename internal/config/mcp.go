package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
)

var ErrInvalidMCPConfig = errors.New("invalid MCP configuration")

type MCPConfig struct {
	ServerURL string
	Token     string
}

func LoadMCP(arguments []string, lookupEnvironment EnvironmentLookup) (MCPConfig, error) {
	serverDefault := environmentValue(lookupEnvironment, "DBGRAPH_MCP_SERVER_URL", "http://127.0.0.1:8080")
	tokenDefault := environmentValue(lookupEnvironment, "DBGRAPH_MCP_TOKEN", "")
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	serverURL := flags.String("server-url", serverDefault, "dbgraph server URL")
	if err := flags.Parse(arguments); err != nil {
		return MCPConfig{}, fmt.Errorf("%w: %v", ErrInvalidMCPConfig, err)
	}
	if flags.NArg() != 0 {
		return MCPConfig{}, fmt.Errorf("%w: unexpected positional arguments", ErrInvalidMCPConfig)
	}
	parsed, err := url.Parse(strings.TrimSpace(*serverURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return MCPConfig{}, fmt.Errorf("%w: server URL must be HTTP(S) without credentials, query, or fragment", ErrInvalidMCPConfig)
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return MCPConfig{}, fmt.Errorf("%w: plain HTTP is allowed only for loopback servers", ErrInvalidMCPConfig)
	}
	return MCPConfig{ServerURL: parsed.String(), Token: strings.TrimSpace(tokenDefault)}, nil
}
