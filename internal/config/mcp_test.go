package config

import "testing"

func TestLoadMCPUsesServerURLFlagAndTokenEnvironment(t *testing.T) {
	lookup := func(key string) (string, bool) {
		values := map[string]string{
			"DBGRAPH_MCP_SERVER_URL": "http://environment.example",
			"DBGRAPH_MCP_TOKEN":      "environment-secret",
		}
		value, ok := values[key]
		return value, ok
	}
	config, err := LoadMCP([]string{"--server-url", "https://localhost:9443/custom-mcp"}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if config.ServerURL != "https://localhost:9443/custom-mcp" || config.Token != "environment-secret" {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadMCPRejectsCredentialsEmbeddedInURL(t *testing.T) {
	if _, err := LoadMCP([]string{"--server-url", "https://user:password@example.test"}, nil); err == nil {
		t.Fatal("credential-bearing URL was accepted")
	}
}
