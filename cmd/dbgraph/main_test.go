package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benenen/dbgraph/internal/config"
	mysqlingestion "github.com/benenen/dbgraph/internal/ingestion/mysql"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestRunPrintsUsageForMissingOrUnknownCommand(t *testing.T) {
	testCases := []struct {
		name      string
		arguments []string
	}{
		{name: "missing command"},
		{name: "unknown command", arguments: []string{"unknown"}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			exitCode, stderr := runWithCapturedStderr(t, testCase.arguments)

			if exitCode != 2 {
				t.Fatalf("exit code = %d, want 2", exitCode)
			}
			for _, expected := range []string{
				"usage: dbgraph serve --database <path> [--listen <address>]",
				"dbgraph mcp [--server-url <url>]",
				"dbgraph backup --database <path> --output <path>",
			} {
				if !strings.Contains(stderr, expected) {
					t.Fatalf("stderr = %q, want %q", stderr, expected)
				}
			}
		})
	}
}

func TestRunBackupCreatesAStandaloneVerifiedSnapshot(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.sqlite")
	outputPath := filepath.Join(directory, "snapshot.sqlite")
	store, err := dbsqlite.Open(context.Background(), dbsqlite.Config{Path: sourcePath})
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	exitCode, stderr := runWithCapturedStderr(t, []string{
		"backup", "--database", sourcePath, "--output", outputPath,
	})
	if exitCode != 0 || stderr != "" {
		t.Fatalf("backup exit=%d stderr=%q", exitCode, stderr)
	}
	backup, err := dbsqlite.Open(context.Background(), dbsqlite.Config{Path: outputPath})
	if err != nil {
		t.Fatalf("open produced backup: %v", err)
	}
	if err := backup.Close(); err != nil {
		t.Fatalf("close produced backup: %v", err)
	}
}

func TestRunServeReportsConfigurationErrorsWithoutStarting(t *testing.T) {
	t.Setenv("DBGRAPH_DATABASE_PATH", "")
	t.Setenv("DBGRAPH_LISTEN_ADDRESS", "127.0.0.1:8080")

	exitCode, stderr := runWithCapturedStderr(t, []string{"serve"})

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr, "invalid serve configuration: database path is required") {
		t.Fatalf("stderr = %q, want safe missing database error", stderr)
	}
	if strings.Contains(stderr, "dbgraph serve failed") {
		t.Fatalf("configuration failure reached server startup: %q", stderr)
	}
}

func TestServeRejectsAnonymousMCPOnANonLoopbackListener(t *testing.T) {
	for _, key := range []string{"DBGRAPH_MCP_AGENT_TOKEN", "DBGRAPH_MCP_REVIEWER_TOKEN", "DBGRAPH_MCP_ADMIN_TOKEN"} {
		t.Setenv(key, "")
	}
	err := serve(config.ServeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "dbgraph.sqlite"), ListenAddress: "0.0.0.0:0",
		TLSCertFile: "unused.crt", TLSKeyFile: "unused.key",
	})
	if err == nil || !strings.Contains(err.Error(), "non-loopback listeners require MCP credentials") {
		t.Fatalf("serve error = %v, want MCP credential requirement", err)
	}
}

func TestLoopbackListenAddressDoesNotTreatWildcardOrMalformedHostsAsLocal(t *testing.T) {
	t.Parallel()

	testCases := map[string]bool{
		"127.0.0.1:8080": true,
		"[::1]:8080":     true,
		"localhost:8080": true,
		"0.0.0.0:8080":   false,
		"[::]:8080":      false,
		":8080":          false,
		"malformed":      false,
	}
	for address, expected := range testCases {
		if actual := loopbackListenAddress(address); actual != expected {
			t.Fatalf("loopbackListenAddress(%q) = %v, want %v", address, actual, expected)
		}
	}
}

func TestRunMCPRejectsInvalidArgumentsWithoutConnecting(t *testing.T) {
	t.Setenv("DBGRAPH_MCP_SERVER_URL", "http://127.0.0.1:8080")
	t.Setenv("DBGRAPH_MCP_TOKEN", "")

	testCases := []struct {
		name      string
		arguments []string
		expected  string
	}{
		{
			name:      "positional argument",
			arguments: []string{"mcp", "unexpected"},
			expected:  "invalid MCP configuration: unexpected positional arguments",
		},
		{
			name:      "unsupported scheme",
			arguments: []string{"mcp", "--server-url", "ftp://127.0.0.1:8080"},
			expected:  "invalid MCP configuration: server URL must be HTTP(S)",
		},
		{
			name:      "non-loopback plain HTTP",
			arguments: []string{"mcp", "--server-url", "http://dbgraph.example.test"},
			expected:  "invalid MCP configuration: plain HTTP is allowed only for loopback servers",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			exitCode, stderr := runWithCapturedStderr(t, testCase.arguments)

			if exitCode != 2 {
				t.Fatalf("exit code = %d, want 2", exitCode)
			}
			if !strings.Contains(stderr, testCase.expected) {
				t.Fatalf("stderr = %q, want %q", stderr, testCase.expected)
			}
			if strings.Contains(stderr, "dbgraph mcp failed") {
				t.Fatalf("invalid configuration reached MCP proxy: %q", stderr)
			}
		})
	}
}

func runWithCapturedStderr(t *testing.T, arguments []string) (int, string) {
	t.Helper()

	stderr, err := os.CreateTemp(t.TempDir(), "stderr-*.txt")
	if err != nil {
		t.Fatalf("create stderr capture: %v", err)
	}
	exitCode := run(arguments, stderr)
	if err := stderr.Close(); err != nil {
		t.Fatalf("close stderr capture: %v", err)
	}
	contents, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatalf("read stderr capture: %v", err)
	}
	return exitCode, string(contents)
}

func TestMySQLOpenerAppliesTheConfiguredTLSPolicy(t *testing.T) {
	t.Parallel()

	const cleartextDSN = "readonly:secret@tcp(127.0.0.1:1)/catalog"

	strict := mysqlOpener(false)
	if _, err := strict(context.Background(), cleartextDSN); !errors.Is(err, mysqlingestion.ErrTLSRequired) {
		t.Fatalf("strict opener error = %v, want ErrTLSRequired", err)
	}

	relaxed := mysqlOpener(true)
	_, err := relaxed(context.Background(), cleartextDSN)
	if err == nil {
		t.Fatal("relaxed opener unexpectedly connected to 127.0.0.1:1")
	}
	if errors.Is(err, mysqlingestion.ErrTLSRequired) || errors.Is(err, mysqlingestion.ErrVerifiedTLSRequired) {
		t.Fatalf("relaxed opener still enforces TLS: %v", err)
	}
}
