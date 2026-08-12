package e2e_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

type browserFixtureIDs struct{ next int64 }

func (g *browserFixtureIDs) Next(context.Context) (int64, error) {
	g.next++
	return g.next, nil
}

// TestBrowserConsoleBootstrapsAProject drives the console the way an operator
// does. The relation lifecycle it used to cover moved to the API surface when
// the vanilla panels were retired.
func TestBrowserConsoleBootstrapsAProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the browser process lifecycle assertion requires Unix signals")
	}
	if output, err := exec.Command("python3", "-c", "from playwright.sync_api import sync_playwright").CombinedOutput(); err != nil {
		t.Fatalf("Python Playwright is required for the browser E2E suite: %v: %s", err, output)
	}

	testDirectory := t.TempDir()
	adminToken := createBrowserToken(t)
	// The flow stores a connection string, which requires a sealing key.
	secretKey := createBrowserToken(t)
	databasePath := filepath.Join(testDirectory, "dbgraph.sqlite")
	projectID, _, _ := seedBrowserDatabase(t, databasePath)
	certificatePath, keyPath := createLoopbackCertificate(t, testDirectory)
	binaryPath := filepath.Join(testDirectory, "dbgraph")
	build := exec.Command("go", "build", "-o", binaryPath, "../../cmd/dbgraph")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dbgraph: %v\n%s", err, output)
	}

	listenAddress := reserveLoopbackAddress(t)
	var processOutput bytes.Buffer
	command := exec.Command(
		binaryPath, "serve", "--database", databasePath, "--listen", listenAddress,
		"--tls-cert", certificatePath, "--tls-key", keyPath,
	)
	command.Stdout = &processOutput
	command.Stderr = &processOutput
	command.Env = append(controlledEnvironment(),
		"DBGRAPH_WEB_ADMIN_TOKEN="+adminToken, "DBGRAPH_SECRET_KEY="+secretKey)
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

	baseURL := "https://" + listenAddress
	waitForTLSHealth(t, baseURL, processDone, &processOutput)
	browser := exec.Command("python3", "browser_flow.py")
	browser.Env = append(os.Environ(),
		"DBGRAPH_BROWSER_BASE_URL="+baseURL,
		"DBGRAPH_BROWSER_TOKEN="+adminToken,
		"DBGRAPH_BROWSER_PROJECT_ID="+strconv.FormatInt(projectID, 10),
		"DBGRAPH_BROWSER_ARTIFACTS="+testDirectory,
	)
	if output, err := browser.CombinedOutput(); err != nil {
		t.Fatalf("browser flow: %v\n%s\nserver output:\n%s", err, output, processOutput.String())
	}

	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal dbgraph serve: %v", err)
	}
	select {
	case err := <-processDone:
		if err != nil {
			t.Fatalf("dbgraph serve exit: %v\n%s", err, processOutput.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("dbgraph serve did not exit after interrupt\n%s", processOutput.String())
	}
}

func createBrowserToken(t *testing.T) string {
	t.Helper()
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(random)
}

func seedBrowserDatabase(t *testing.T, databasePath string) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	ids := &browserFixtureIDs{next: 100}
	now := time.Date(2026, time.August, 11, 19, 0, 0, 0, time.UTC)
	project, err := catalog.NewProjectService(dbsqlite.NewProjectRepository(store), ids, func() time.Time { return now }).Create(
		ctx, catalog.CreateProject{Name: "Browser E2E"},
	)
	if err != nil {
		t.Fatal(err)
	}
	catalogService := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return now })
	dataSource, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{
		ProjectID: project.ID,
		Name:      "browser-fixture", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "BROWSER_E2E_MYSQL_DSN",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project.ID, DataSourceID: dataSource.ID,
		Nodes: []catalog.NodeInput{
			{StableKey: "database:e2e", Kind: catalog.NodeDatabase, Name: "e2e", QualifiedName: "mysql://e2e"},
			{StableKey: "schema:e2e", ParentStableKey: "database:e2e", Kind: catalog.NodeSchema, Name: "e2e", QualifiedName: "e2e"},
			{StableKey: "table:e2e.mapping", ParentStableKey: "schema:e2e", Kind: catalog.NodeTable, Name: "mapping", QualifiedName: "e2e.mapping"},
			{StableKey: "column:e2e.mapping.source_value", ParentStableKey: "table:e2e.mapping", Kind: catalog.NodeColumn, Name: "source_value", QualifiedName: "e2e.mapping.source_value", DataType: "varchar", Ordinal: 1},
			{StableKey: "column:e2e.mapping.target_value", ParentStableKey: "table:e2e.mapping", Kind: catalog.NodeColumn, Name: "target_value", QualifiedName: "e2e.mapping.target_value", DataType: "varchar", Ordinal: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := catalogService.FindCurrentNode(ctx, project.ID, dataSource.ID, "e2e.mapping.source_value")
	if err != nil {
		t.Fatal(err)
	}
	target, err := catalogService.FindCurrentNode(ctx, project.ID, dataSource.ID, "e2e.mapping.target_value")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return project.ID, source.ID, target.ID
}

func createLoopbackCertificate(t *testing.T, directory string) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "dbgraph browser e2e"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePath := filepath.Join(directory, "server.crt")
	keyPath := filepath.Join(directory, "server.key")
	if err := os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certificatePath, keyPath
}

func waitForTLSHealth(t *testing.T, baseURL string, processDone <-chan error, processOutput *bytes.Buffer) {
	t.Helper()
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, // #nosec G402 -- generated loopback-only E2E certificate.
	}
	client := &http.Client{Transport: transport, Timeout: 250 * time.Millisecond}
	defer transport.CloseIdleConnections()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-processDone:
			t.Fatalf("dbgraph serve exited before browser test: %v\n%s", err, processOutput.String())
		case <-deadline.C:
			t.Fatalf("HTTPS health endpoint did not become ready\n%s", processOutput.String())
		case <-ticker.C:
			response, err := client.Get(fmt.Sprintf("%s/healthz", baseURL))
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return
				}
			}
		}
	}
}
