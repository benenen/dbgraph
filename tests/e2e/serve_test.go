package e2e_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServeBootstrapsFreshDatabaseAndBecomesHealthy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the process signal assertion requires Unix signals")
	}

	testDirectory := t.TempDir()
	binaryPath := filepath.Join(testDirectory, "dbgraph")
	databasePath := filepath.Join(testDirectory, "dbgraph.sqlite")
	listenAddress := reserveLoopbackAddress(t)

	build := exec.Command("go", "build", "-o", binaryPath, "../../cmd/dbgraph")
	buildOutput, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("build dbgraph: %v\n%s", err, buildOutput)
	}

	var processOutput bytes.Buffer
	command := exec.Command(
		binaryPath,
		"serve",
		"--database", databasePath,
		"--listen", listenAddress,
	)
	command.Stdout = &processOutput
	command.Stderr = &processOutput
	command.Env = controlledEnvironment()

	if err := command.Start(); err != nil {
		t.Fatalf("start dbgraph serve: %v", err)
	}
	processDone := make(chan error, 1)
	go func() {
		processDone <- command.Wait()
		close(processDone)
	}()
	t.Cleanup(func() {
		select {
		case <-processDone:
			return
		default:
			_ = command.Process.Kill()
			<-processDone
		}
	})

	response := waitForHealth(t, listenAddress, processDone, &processOutput)
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close health response: %v", err)
		}
	}()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("content type = %q, want application/json", contentType)
	}

	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Status        string `json:"status"`
			SchemaVersion int    `json:"schemaVersion"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if !envelope.Success {
		t.Fatal("health response success = false, want true")
	}
	if envelope.Data.Status != "UP" {
		t.Fatalf("health status value = %q, want UP", envelope.Data.Status)
	}
	if envelope.Data.SchemaVersion != 1 {
		t.Fatalf("health schema version = %d, want 1", envelope.Data.SchemaVersion)
	}
	if envelope.Error != nil {
		t.Fatalf("health error = %#v, want null", envelope.Error)
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal dbgraph serve: %v", err)
	}
	select {
	case err := <-processDone:
		if err != nil {
			t.Fatalf("dbgraph serve exit: %v\n%s", err, processOutput.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("dbgraph serve did not exit after SIGTERM\n%s", processOutput.String())
	}
}

func TestMCPProxyNeverCreatesOrOpensSQLite(t *testing.T) {
	testDirectory := t.TempDir()
	binaryPath := filepath.Join(testDirectory, "dbgraph")
	databasePath := filepath.Join(testDirectory, "must-not-exist.sqlite")
	build := exec.Command("go", "build", "-o", binaryPath, "../../cmd/dbgraph")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dbgraph: %v\n%s", err, output)
	}

	command := exec.Command(binaryPath, "mcp", "--server-url", "http://127.0.0.1:1")
	command.Env = append(controlledEnvironment(), "DBGRAPH_DATABASE_PATH="+databasePath)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("MCP proxy unexpectedly connected")
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("MCP proxy exit = %v, output=%s", err, output)
	}
	if !strings.Contains(string(output), "connect to dbgraph MCP server") {
		t.Fatalf("MCP proxy output = %s", output)
	}
	if _, statErr := os.Stat(databasePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("MCP proxy touched SQLite path: %v", statErr)
	}
}

func reserveLoopbackAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release listen address: %v", err)
	}
	return address
}

func controlledEnvironment() []string {
	allowed := []string{"PATH=", "TMPDIR=", "TZ="}
	var environment []string
	for _, item := range os.Environ() {
		for _, prefix := range allowed {
			if strings.HasPrefix(item, prefix) {
				environment = append(environment, item)
				break
			}
		}
	}
	return environment
}

func waitForHealth(
	t *testing.T,
	listenAddress string,
	processDone <-chan error,
	processOutput *bytes.Buffer,
) *http.Response {
	t.Helper()

	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-processDone:
			t.Fatalf("dbgraph serve exited before healthy: %v\n%s", err, processOutput.String())
		case <-deadline.C:
			t.Fatalf("health endpoint did not become ready\n%s", processOutput.String())
		case <-ticker.C:
			response, err := client.Get(fmt.Sprintf("http://%s/healthz", listenAddress))
			if err == nil {
				return response
			}
		}
	}
}
