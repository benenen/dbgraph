package webapi

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestConsoleServesTheShellWithAPerResponseCSPNonce(t *testing.T) {
	requireBuiltConsole(t)
	handler, err := NewConsoleHandler()
	if err != nil {
		t.Fatalf("create console handler: %v", err)
	}

	serve := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "https://localhost"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	root := serve("/app/")
	if root.Code != http.StatusOK {
		t.Fatalf("root status=%d body=%s", root.Code, root.Body.String())
	}
	if root.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		root.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("root headers=%v", root.Header())
	}
	assertConsoleSecurityHeaders(t, root)
	if strings.Contains(root.Body.String(), noncePlaceholder) {
		t.Fatalf("console shell retained the CSP nonce placeholder")
	}
	noncePattern := regexp.MustCompile(`<meta property="csp-nonce" content="([A-Za-z0-9+/]{22}==)"\s*/?>`)
	match := noncePattern.FindStringSubmatch(root.Body.String())
	if len(match) != 2 || !strings.Contains(root.Header().Get("Content-Security-Policy"), "'nonce-"+match[1]+"'") {
		t.Fatalf("shell nonce and CSP do not match: csp=%q", root.Header().Get("Content-Security-Policy"))
	}

	clientRoute := serve("/app/schema")
	if clientRoute.Code != http.StatusOK || !strings.Contains(clientRoute.Body.String(), "dbgraph console") {
		t.Fatalf("client route status=%d body=%s", clientRoute.Code, clientRoute.Body.String())
	}
	assertConsoleSecurityHeaders(t, clientRoute)
}

func TestConsoleDistinguishesMissingAssetsFromClientRoutes(t *testing.T) {
	requireBuiltConsole(t)
	handler, err := NewConsoleHandler()
	if err != nil {
		t.Fatalf("create console handler: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "https://localhost/app/assets/missing.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "dbgraph console") {
		t.Fatalf("missing asset status=%d body=%s", response.Code, response.Body.String())
	}
	assertConsoleSecurityHeaders(t, response)
}

func requireBuiltConsole(t *testing.T) {
	t.Helper()
	if _, err := fs.Stat(consoleFiles, "app/index.html"); err != nil {
		t.Skip("Vue console is not built; run make console")
	}
}

func assertConsoleSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	headers := response.Header()
	csp := headers.Get("Content-Security-Policy")
	for _, directive := range []string{
		"object-src 'none'",
		"'sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU='",
		"'sha256-0/4q5IwejFb2zgHlQwwtwmGHS8ZbXE1kmz/TkRFlZ7M='",
		"'sha256-yfc2FhpkFR0EAy3T+zDsaAFGXSP9B3ELNvaJKDzNhkk='",
		"'sha256-9xjtvxMT1ApHlgn9ohbh2FNfvK5Tqtzy94BjfXBeMSY='",
	} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("console CSP=%q missing %q", csp, directive)
		}
	}
	if strings.Contains(csp, "style-src 'self' 'unsafe-inline'") ||
		strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("console CSP=%q broadly permits inline code", csp)
	}
	if headers.Get("Referrer-Policy") != "no-referrer" ||
		headers.Get("X-Content-Type-Options") != "nosniff" ||
		headers.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("console security headers=%v", headers)
	}
}
