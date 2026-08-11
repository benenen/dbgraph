package config_test

import (
	"errors"
	"testing"

	"github.com/benenen/dbgraph/internal/config"
)

func TestLoadServeFlagsOverrideEnvironment(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		"DBGRAPH_DATABASE_PATH":  "/environment/dbgraph.sqlite",
		"DBGRAPH_LISTEN_ADDRESS": "127.0.0.1:9090",
	}
	lookupEnvironment := func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	}

	serveConfig, err := config.LoadServe([]string{
		"--database", "/explicit/dbgraph.sqlite",
		"--listen", "127.0.0.1:8181",
	}, lookupEnvironment)
	if err != nil {
		t.Fatalf("LoadServe: %v", err)
	}

	if serveConfig.DatabasePath != "/explicit/dbgraph.sqlite" {
		t.Fatalf("database path = %q, want explicit path", serveConfig.DatabasePath)
	}
	if serveConfig.ListenAddress != "127.0.0.1:8181" {
		t.Fatalf("listen address = %q, want explicit address", serveConfig.ListenAddress)
	}
}

func TestLoadServeUsesEnvironmentWhenFlagsAreAbsent(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		"DBGRAPH_DATABASE_PATH":  "/environment/dbgraph.sqlite",
		"DBGRAPH_LISTEN_ADDRESS": "127.0.0.1:9090",
	}
	lookupEnvironment := func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	}

	serveConfig, err := config.LoadServe(nil, lookupEnvironment)
	if err != nil {
		t.Fatalf("LoadServe: %v", err)
	}
	if serveConfig.DatabasePath != "/environment/dbgraph.sqlite" {
		t.Fatalf("database path = %q, want environment path", serveConfig.DatabasePath)
	}
	if serveConfig.ListenAddress != "127.0.0.1:9090" {
		t.Fatalf("listen address = %q, want environment address", serveConfig.ListenAddress)
	}
}

func TestLoadServeRejectsMissingDatabasePath(t *testing.T) {
	t.Parallel()

	_, err := config.LoadServe(nil, func(string) (string, bool) { return "", false })
	if !errors.Is(err, config.ErrInvalidServeConfig) {
		t.Fatalf("LoadServe error = %v, want ErrInvalidServeConfig", err)
	}
}
