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

func TestLoadServeAcceptsCleartextWebOnLoopbackOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		arguments   []string
		environment map[string]string
		wantAllowed bool
		wantError   bool
	}{
		{
			name:        "flag on loopback",
			arguments:   []string{"--database", "/tmp/dbgraph.sqlite", "--insecure-cleartext-web"},
			wantAllowed: true,
		},
		{
			name:      "environment variable on loopback",
			arguments: []string{"--database", "/tmp/dbgraph.sqlite"},
			environment: map[string]string{
				"DBGRAPH_INSECURE_CLEARTEXT_WEB": "true",
			},
			wantAllowed: true,
		},
		{
			name:      "disabled by default",
			arguments: []string{"--database", "/tmp/dbgraph.sqlite"},
		},
		{
			name: "rejected on a non-loopback listener",
			arguments: []string{
				"--database", "/tmp/dbgraph.sqlite", "--listen", "0.0.0.0:8080",
				"--tls-cert", "/tmp/cert.pem", "--tls-key", "/tmp/key.pem",
				"--insecure-cleartext-web",
			},
			wantError: true,
		},
		{
			name: "rejected together with TLS",
			arguments: []string{
				"--database", "/tmp/dbgraph.sqlite",
				"--tls-cert", "/tmp/cert.pem", "--tls-key", "/tmp/key.pem",
				"--insecure-cleartext-web",
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lookupEnvironment := func(key string) (string, bool) {
				value, ok := test.environment[key]
				return value, ok
			}
			serveConfig, err := config.LoadServe(test.arguments, lookupEnvironment)
			if test.wantError {
				if !errors.Is(err, config.ErrInvalidServeConfig) {
					t.Fatalf("LoadServe error = %v, want ErrInvalidServeConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadServe: %v", err)
			}
			if serveConfig.AllowCleartextWeb != test.wantAllowed {
				t.Fatalf("AllowCleartextWeb = %t, want %t", serveConfig.AllowCleartextWeb, test.wantAllowed)
			}
		})
	}
}

func TestLoadServeAcceptsInsecureMySQLTLSOnLoopbackOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		arguments   []string
		environment map[string]string
		wantAllowed bool
		wantError   bool
	}{
		{
			name:        "flag on loopback",
			arguments:   []string{"--database", "/tmp/dbgraph.sqlite", "--insecure-mysql-tls"},
			wantAllowed: true,
		},
		{
			name:        "environment variable on loopback",
			arguments:   []string{"--database", "/tmp/dbgraph.sqlite"},
			environment: map[string]string{"DBGRAPH_INSECURE_MYSQL_TLS": "1"},
			wantAllowed: true,
		},
		{
			name:      "disabled by default",
			arguments: []string{"--database", "/tmp/dbgraph.sqlite"},
		},
		{
			name: "rejected on a non-loopback listener",
			arguments: []string{
				"--database", "/tmp/dbgraph.sqlite", "--listen", "0.0.0.0:8080",
				"--tls-cert", "/tmp/cert.pem", "--tls-key", "/tmp/key.pem",
				"--insecure-mysql-tls",
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lookupEnvironment := func(key string) (string, bool) {
				value, ok := test.environment[key]
				return value, ok
			}
			serveConfig, err := config.LoadServe(test.arguments, lookupEnvironment)
			if test.wantError {
				if !errors.Is(err, config.ErrInvalidServeConfig) {
					t.Fatalf("LoadServe error = %v, want ErrInvalidServeConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadServe: %v", err)
			}
			if serveConfig.AllowInsecureMySQLTLS != test.wantAllowed {
				t.Fatalf("AllowInsecureMySQLTLS = %t, want %t", serveConfig.AllowInsecureMySQLTLS, test.wantAllowed)
			}
		})
	}
}
