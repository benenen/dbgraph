package auth

import (
	"bytes"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestTokenAuthenticatorValidatesCredentialsAndBearerSyntax(t *testing.T) {
	t.Parallel()

	tooLong := strings.Repeat("x", maximumTokenLength+1)
	invalid := [][]Credential{
		{{Token: "", Actor: "actor", Role: relations.RoleViewer}},
		{{Token: tooLong, Actor: "actor", Role: relations.RoleViewer}},
		{{Token: testCredentialToken, Actor: "", Role: relations.RoleViewer}},
		{{Token: testCredentialToken, Actor: strings.Repeat("a", 201), Role: relations.RoleViewer}},
		{{Token: testCredentialToken, Actor: "actor", Role: 0}},
		{{Token: testCredentialToken, Actor: "actor", Role: relations.RoleAdmin + 1}},
		{{Token: testCredentialToken, Actor: "one", Role: relations.RoleViewer}, {Token: " " + testCredentialToken + " ", Actor: "two", Role: relations.RoleAdmin}},
		{{Token: testCredentialToken, Actor: "actor", Role: relations.RoleViewer, Origin: audit.OriginSystem + 1}},
	}
	for index, credentials := range invalid {
		if _, err := NewTokenAuthenticator(credentials); !errors.Is(err, ErrInvalidCredential) {
			t.Fatalf("invalid credential %d error = %v", index, err)
		}
	}

	authenticator, err := NewTokenAuthenticator([]Credential{{
		Token: testCredentialToken, Actor: "actor", Role: relations.RoleAdmin, Origin: audit.OriginWeb,
	}})
	if err != nil {
		t.Fatalf("NewTokenAuthenticator: %v", err)
	}
	if !authenticator.HasCredentials() || (*TokenAuthenticator)(nil).HasCredentials() {
		t.Fatal("HasCredentials returned an unexpected result")
	}

	request := httptest.NewRequest("GET", "https://dbgraph.test/", nil)
	for _, header := range []string{"", "Basic " + testCredentialToken, "Bearer", "Bearer " + testCredentialToken + " extra", "bearer " + testCredentialToken} {
		request.Header.Set("Authorization", header)
		if _, ok := authenticator.Authenticate(request); ok {
			t.Fatalf("accepted authorization header %q", header)
		}
	}
	request.Header.Set("Authorization", "Bearer "+testCredentialToken)
	principal, ok := authenticator.Authenticate(request)
	if !ok || principal.Actor != "actor" || principal.Origin != audit.OriginWeb {
		t.Fatalf("principal = %#v, ok = %v", principal, ok)
	}
	if _, ok := (*TokenAuthenticator)(nil).Authenticate(request); ok {
		t.Fatal("nil authenticator accepted request")
	}
	if _, ok := authenticator.AuthenticateToken(""); ok {
		t.Fatal("empty token accepted")
	}
	if _, ok := authenticator.AuthenticateToken(tooLong); ok {
		t.Fatal("oversized token accepted")
	}
}

func TestEnvironmentAuthenticatorLoadersMapRolesAndOrigins(t *testing.T) {
	t.Parallel()

	mcpAgentToken := testHexToken(1)
	mcpReviewerToken := testHexToken(2)
	mcpAdminToken := testHexToken(3)
	webViewerToken := testHexToken(4)
	webEditorToken := testHexToken(5)
	webReviewerToken := testHexToken(6)
	webAdminToken := testHexToken(7)
	values := map[string]string{
		"DBGRAPH_MCP_AGENT_TOKEN":    mcpAgentToken,
		"DBGRAPH_MCP_REVIEWER_TOKEN": mcpReviewerToken,
		"DBGRAPH_MCP_ADMIN_TOKEN":    mcpAdminToken,
		"DBGRAPH_WEB_VIEWER_TOKEN":   webViewerToken,
		"DBGRAPH_WEB_EDITOR_TOKEN":   webEditorToken,
		"DBGRAPH_WEB_REVIEWER_TOKEN": webReviewerToken,
		"DBGRAPH_WEB_ADMIN_TOKEN":    webAdminToken,
	}
	lookup := func(key string) (string, bool) {
		value, found := values[key]
		return value, found
	}
	mcpAuthenticator, err := LoadMCPAuthenticator(lookup)
	if err != nil {
		t.Fatalf("LoadMCPAuthenticator: %v", err)
	}
	for token, role := range map[string]relations.Role{
		mcpAgentToken:    relations.RoleAgent,
		mcpReviewerToken: relations.RoleReviewer,
		mcpAdminToken:    relations.RoleAdmin,
	} {
		principal, ok := mcpAuthenticator.AuthenticateToken(token)
		if !ok || principal.Role != role || principal.Origin != audit.OriginAgent {
			t.Fatalf("MCP token %q principal = %#v, ok = %v", token, principal, ok)
		}
	}
	webAuthenticator, err := LoadWebAuthenticator(lookup)
	if err != nil {
		t.Fatalf("LoadWebAuthenticator: %v", err)
	}
	for token, role := range map[string]relations.Role{
		webViewerToken:   relations.RoleViewer,
		webEditorToken:   relations.RoleEditor,
		webReviewerToken: relations.RoleReviewer,
		webAdminToken:    relations.RoleAdmin,
	} {
		principal, ok := webAuthenticator.AuthenticateToken(token)
		if !ok || principal.Role != role || principal.Origin != audit.OriginWeb {
			t.Fatalf("Web token %q principal = %#v, ok = %v", token, principal, ok)
		}
	}
	emptyMCP, err := LoadMCPAuthenticator(nil)
	if err != nil || emptyMCP.HasCredentials() {
		t.Fatalf("empty MCP authenticator = %#v, err = %v", emptyMCP, err)
	}
	emptyWeb, err := LoadWebAuthenticator(func(string) (string, bool) { return "  ", true })
	if err != nil || emptyWeb.HasCredentials() {
		t.Fatalf("empty Web authenticator = %#v, err = %v", emptyWeb, err)
	}
}

func TestSessionManagerRejectsInvalidRandomnessAndSupportsDelete(t *testing.T) {
	t.Parallel()

	authenticator, err := NewTokenAuthenticator([]Credential{{Token: testCredentialToken, Actor: "actor", Role: relations.RoleEditor}})
	if err != nil {
		t.Fatalf("NewTokenAuthenticator: %v", err)
	}
	manager := NewSessionManager(authenticator, nil, bytes.NewReader(bytes.Repeat([]byte{1}, 64)))
	if _, _, err := manager.Create("wrong"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("invalid credential error = %v", err)
	}
	sessionToken, session, err := manager.Create(testCredentialToken)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	manager.Delete(sessionToken)
	if _, ok := manager.Get(sessionToken); ok || manager.ValidateCSRF(sessionToken, session.CSRFToken) {
		t.Fatal("deleted session remained valid")
	}
	manager.Delete("")
	(*SessionManager)(nil).Delete("token")
	if _, ok := (*SessionManager)(nil).Get("token"); ok {
		t.Fatal("nil manager returned a session")
	}
	if (*SessionManager)(nil).ValidateCSRF("token", "csrf") {
		t.Fatal("nil manager validated CSRF")
	}
	if manager.ValidateCSRF("token", "") || manager.ValidateCSRF("token", strings.Repeat("x", maximumTokenLength+1)) {
		t.Fatal("invalid CSRF token was accepted")
	}

	readFailure := NewSessionManager(authenticator, time.Now, io.LimitReader(bytes.NewReader(make([]byte, 31)), 31))
	if _, _, err := readFailure.Create(testCredentialToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("session random failure = %v", err)
	}
	csrfFailure := NewSessionManager(authenticator, time.Now, io.LimitReader(bytes.NewReader(make([]byte, 63)), 63))
	if _, _, err := csrfFailure.Create(testCredentialToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("CSRF random failure = %v", err)
	}
}

func TestSessionManagerRemovesExpiredSessionsBeforeAdmission(t *testing.T) {
	t.Parallel()

	authenticator, err := NewTokenAuthenticator([]Credential{{Token: testCredentialToken, Actor: "actor", Role: relations.RoleEditor}})
	if err != nil {
		t.Fatalf("NewTokenAuthenticator: %v", err)
	}
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	manager := NewSessionManager(authenticator, func() time.Time { return now }, nil)
	for index := 0; index < maximumSessionsPerActor; index++ {
		if _, _, err := manager.Create(testCredentialToken); err != nil {
			t.Fatalf("Create %d: %v", index, err)
		}
	}
	now = now.Add(sessionTTL)
	if _, _, err := manager.Create(testCredentialToken); err != nil {
		t.Fatalf("Create after expiration: %v", err)
	}
	if len(manager.sessions) != 1 {
		t.Fatalf("active sessions = %d, want 1", len(manager.sessions))
	}
}
