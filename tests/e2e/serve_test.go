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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	if envelope.Data.SchemaVersion != 2 {
		t.Fatalf("health schema version = %d, want 2", envelope.Data.SchemaVersion)
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

func TestServeReplacesAndResolvesSourceBindingThroughAuthenticatedMCP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the process signal assertion requires Unix signals")
	}
	fixture := startSourceBindingServe(t)
	assertSourceBindingReplace(t, fixture)
	assertSourceBindingResolve(t, fixture)
	fixture.stop(t)
}

const (
	e2eRepositoryID int64 = 101
	e2eDataSourceID int64 = 201
	e2eMCPToken           = "606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f"
)

type sourceBindingServeFixture struct {
	command     *exec.Cmd
	processDone <-chan error
	output      *bytes.Buffer
	session     *mcp.ClientSession
}

func startSourceBindingServe(t *testing.T) sourceBindingServeFixture {
	t.Helper()
	testDirectory := t.TempDir()
	binaryPath := filepath.Join(testDirectory, "dbgraph")
	databasePath := filepath.Join(testDirectory, "dbgraph.sqlite")
	listenAddress := reserveLoopbackAddress(t)
	build := exec.Command("go", "build", "-o", binaryPath, "../../cmd/dbgraph")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dbgraph: %v\n%s", err, output)
	}
	seedSourceBindingCatalog(t, databasePath, e2eRepositoryID, e2eDataSourceID)
	var processOutput bytes.Buffer
	command := exec.Command(binaryPath, "serve", "--database", databasePath, "--listen", listenAddress)
	command.Stdout = &processOutput
	command.Stderr = &processOutput
	command.Env = append(controlledEnvironment(), "DBGRAPH_MCP_TOKEN="+e2eMCPToken)
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
	health := waitForHealth(t, listenAddress, processDone, &processOutput)
	if err := health.Body.Close(); err != nil {
		t.Fatalf("close health response: %v", err)
	}
	return sourceBindingServeFixture{
		command: command, processDone: processDone, output: &processOutput,
		session: connectAuthenticatedMCP(t, "http://"+listenAddress+"/mcp", e2eMCPToken),
	}
}

func assertSourceBindingReplace(t *testing.T, fixture sourceBindingServeFixture) {
	t.Helper()
	replaceArguments := json.RawMessage(fmt.Sprintf(`{
		"repositoryId":%q,"context":"production","dataSourceIds":[%q],
		"expectedRevisionNo":0,"reason":"Bind production source.","requestId":"e2e-binding-1"
	}`, strconv.FormatInt(e2eRepositoryID, 10), strconv.FormatInt(e2eDataSourceID, 10)))
	replaced, err := fixture.session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "dbgraph_replace_source_binding", Arguments: replaceArguments,
	})
	if err != nil || replaced.IsError {
		t.Fatalf("replace source binding result=%#v error=%v\n%s", replaced, err, fixture.output.String())
	}
	replacedOutput, ok := replaced.StructuredContent.(map[string]any)
	if !ok || replacedOutput["repositoryId"] != "101" || replacedOutput["revisionNo"] != float64(1) {
		t.Fatalf("replace source binding output = %#v", replaced.StructuredContent)
	}
}

func assertSourceBindingResolve(t *testing.T, fixture sourceBindingServeFixture) {
	t.Helper()
	resolved, err := fixture.session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "dbgraph_resolve_workspace_data_sources",
		Arguments: json.RawMessage(`{
			"remotes":["https://github.com/acme/orders-service.git"],"context":"production"
		}`),
	})
	if err != nil || resolved.IsError {
		t.Fatalf("resolve source binding result=%#v error=%v\n%s", resolved, err, fixture.output.String())
	}
	resolvedOutput, ok := resolved.StructuredContent.(map[string]any)
	if !ok || resolvedOutput["status"] != "RESOLVED" || resolvedOutput["repositoryId"] != "101" ||
		resolvedOutput["bindingRevisionNo"] != float64(1) {
		t.Fatalf("resolve source binding output = %#v", resolved.StructuredContent)
	}
	sources, ok := resolvedOutput["dataSources"].([]any)
	if !ok || len(sources) != 1 {
		t.Fatalf("resolved data sources = %#v", resolvedOutput["dataSources"])
	}
	source, ok := sources[0].(map[string]any)
	if !ok || source["id"] != "201" || source["name"] != "orders-primary" || source["kind"] != "MYSQL" {
		t.Fatalf("resolved data source = %#v", sources[0])
	}
}

func (fixture sourceBindingServeFixture) stop(t *testing.T) {
	t.Helper()
	if err := fixture.session.Close(); err != nil {
		t.Fatalf("close MCP session: %v", err)
	}
	if err := fixture.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal dbgraph serve: %v", err)
	}
	select {
	case err := <-fixture.processDone:
		if err != nil {
			t.Fatalf("dbgraph serve exit: %v\n%s", err, fixture.output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("dbgraph serve did not exit after SIGTERM\n%s", fixture.output.String())
	}
}

func seedSourceBindingCatalog(t *testing.T, databasePath string, repositoryID int64, dataSourceID int64) {
	t.Helper()
	store, err := dbsqlite.Open(t.Context(), dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("open source binding seed store: %v", err)
	}
	createdAt := time.Date(2026, 8, 14, 7, 30, 0, 0, time.UTC)
	if err := dbsqlite.NewCodeRepository(store).CreateCodeRepository(t.Context(), catalog.CodeRepository{
		ID: repositoryID, Name: "orders-service", RemoteURL: "https://github.com/acme/orders-service.git",
		DefaultBranch: "main", CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		_ = store.Close()
		t.Fatalf("seed code repository: %v", err)
	}
	if err := dbsqlite.NewCatalogRepository(store, nil).CreateDataSource(t.Context(), catalog.DataSource{
		ID: dataSourceID, Name: "orders-primary", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "ORDERS_DSN", CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		_ = store.Close()
		t.Fatalf("seed data source: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close source binding seed store: %v", err)
	}
}

type e2eBearerTransport struct {
	token string
}

func (transport e2eBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+transport.token)
	return http.DefaultTransport.RoundTrip(copy)
}

func connectAuthenticatedMCP(t *testing.T, endpoint string, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "dbgraph-e2e", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: e2eBearerTransport{token: token}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect authenticated MCP client: %v", err)
	}
	return session
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
