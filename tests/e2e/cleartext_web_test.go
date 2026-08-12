package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCleartextWebSignInWorksOverPlainHTTP drives the opt-in cleartext Web mode
// through a cookie jar, which enforces the Secure attribute the same way a
// browser does: a Secure cookie issued over http:// is never stored or replayed.
func TestCleartextWebSignInWorksOverPlainHTTP(t *testing.T) {
	const adminToken = "1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"

	testDirectory := t.TempDir()
	binaryPath := filepath.Join(testDirectory, "dbgraph")
	databasePath := filepath.Join(testDirectory, "dbgraph.sqlite")
	listenAddress := reserveLoopbackAddress(t)

	build := exec.Command("go", "build", "-o", binaryPath, "../../cmd/dbgraph")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dbgraph: %v\n%s", err, output)
	}

	var processOutput bytes.Buffer
	command := exec.Command(
		binaryPath, "serve",
		"--database", databasePath,
		"--listen", listenAddress,
		"--insecure-cleartext-web",
	)
	command.Stdout = &processOutput
	command.Stderr = &processOutput
	command.Env = append(controlledEnvironment(), "DBGRAPH_WEB_ADMIN_TOKEN="+adminToken)
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
		t.Errorf("close health response: %v", err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	client := &http.Client{
		Jar:     jar,
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	base := "http://" + listenAddress

	navigation, err := http.NewRequest(http.MethodGet, base+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	navigation.Header.Set("Accept", "text/html")
	redirect, err := client.Do(navigation)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	if err := redirect.Body.Close(); err != nil {
		t.Errorf("close redirect response: %v", err)
	}
	if redirect.StatusCode != http.StatusSeeOther || redirect.Header.Get("Location") != "/login" {
		t.Fatalf("GET / status=%d location=%q", redirect.StatusCode, redirect.Header.Get("Location"))
	}

	login, err := client.Post(base+"/login", "application/json",
		strings.NewReader(`{"token":"`+adminToken+`"}`))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	var envelope struct {
		Data struct {
			Actor string `json:"actor"`
			Role  string `json:"role"`
		} `json:"data"`
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(login.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if err := login.Body.Close(); err != nil {
		t.Errorf("close login response: %v", err)
	}
	if login.StatusCode != http.StatusOK || !envelope.Success || envelope.Data.Role != "ADMIN" {
		t.Fatalf("login status=%d envelope=%#v\n%s", login.StatusCode, envelope, processOutput.String())
	}

	parsedBase, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	stored := jar.Cookies(parsedBase)
	if len(stored) != 1 || stored[0].Name != "dbgraph-session" {
		t.Fatalf("cookie jar over plain HTTP holds %#v, want a single dbgraph-session cookie", stored)
	}

	authenticated, err := http.NewRequest(http.MethodGet, base+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticated.Header.Set("Accept", "text/html")
	page, err := client.Do(authenticated)
	if err != nil {
		t.Fatalf("authenticated GET /: %v", err)
	}
	body := new(bytes.Buffer)
	if _, err := body.ReadFrom(page.Body); err != nil {
		t.Fatalf("read page: %v", err)
	}
	if err := page.Body.Close(); err != nil {
		t.Errorf("close page response: %v", err)
	}
	if page.StatusCode != http.StatusOK {
		t.Fatalf("authenticated GET / status=%d body=%q", page.StatusCode, body.String())
	}
	if !strings.Contains(body.String(), "dbgraph") {
		t.Fatalf("authenticated page body=%q", body.String())
	}

	// Read the captured output only after the process exits: exec writes into
	// the buffer from its own goroutine until then.
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal dbgraph serve: %v", err)
	}
	select {
	case err := <-processDone:
		if err != nil {
			t.Fatalf("dbgraph serve exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dbgraph serve did not exit after SIGTERM")
	}
	if !strings.Contains(processOutput.String(), "insecure-cleartext-web is enabled") {
		t.Fatalf("startup output must warn about cleartext mode, got %q", processOutput.String())
	}
}

// TestServeRejectsWebCredentialsWithoutTLSOrOptIn keeps the default closed.
func TestServeRejectsWebCredentialsWithoutTLSOrOptIn(t *testing.T) {
	const adminToken = "1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"

	testDirectory := t.TempDir()
	binaryPath := filepath.Join(testDirectory, "dbgraph")
	build := exec.Command("go", "build", "-o", binaryPath, "../../cmd/dbgraph")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dbgraph: %v\n%s", err, output)
	}

	command := exec.Command(
		binaryPath, "serve",
		"--database", filepath.Join(testDirectory, "dbgraph.sqlite"),
		"--listen", reserveLoopbackAddress(t),
	)
	command.Env = append(controlledEnvironment(), "DBGRAPH_WEB_ADMIN_TOKEN="+adminToken)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("serve started with Web credentials over cleartext: %s", output)
	}
	if !strings.Contains(string(output), "web credentials require TLS") {
		t.Fatalf("serve output = %s", output)
	}
}
