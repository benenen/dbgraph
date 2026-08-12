package auth_test

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"testing"

	"github.com/benenen/dbgraph/internal/audit"
	appauth "github.com/benenen/dbgraph/internal/auth"
	"github.com/benenen/dbgraph/internal/relations"
)

const (
	storedWebToken = "1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"
	storedMCPToken = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
)

func TestStoredCredentialsAuthenticateWithoutTheEnvironment(t *testing.T) {
	t.Parallel()

	webDigest := sha256.Sum256([]byte(storedWebToken))
	mcpDigest := sha256.Sum256([]byte(storedMCPToken))
	authenticator, err := appauth.NewTokenAuthenticatorFromStored([]appauth.StoredCredential{
		{Actor: "web-admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb, Digest: webDigest[:]},
		{Actor: "mcp-agent", Role: relations.RoleAgent, Origin: audit.OriginAgent, Digest: mcpDigest[:]},
	})
	if err != nil {
		t.Fatalf("NewTokenAuthenticatorFromStored: %v", err)
	}

	principal, ok := authenticator.AuthenticateToken(storedWebToken)
	if !ok || principal.Actor != "web-admin" || principal.Role != relations.RoleAdmin || principal.Origin != audit.OriginWeb {
		t.Fatalf("web principal = %#v ok = %t", principal, ok)
	}
	principal, ok = authenticator.AuthenticateToken(storedMCPToken)
	if !ok || principal.Actor != "mcp-agent" || principal.Role != relations.RoleAgent {
		t.Fatalf("mcp principal = %#v ok = %t", principal, ok)
	}
	if _, ok := authenticator.AuthenticateToken("0000000000000000000000000000000000000000000000000000000000000000"); ok {
		t.Fatal("an unknown token authenticated")
	}

	request, err := http.NewRequest(http.MethodGet, "https://localhost/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+storedMCPToken)
	if principal, ok := authenticator.Authenticate(request); !ok || principal.Actor != "mcp-agent" {
		t.Fatalf("bearer principal = %#v ok = %t", principal, ok)
	}
}

func TestStoredCredentialsRejectMalformedRecords(t *testing.T) {
	t.Parallel()

	digest := sha256.Sum256([]byte(storedWebToken))
	tests := []struct {
		name   string
		record appauth.StoredCredential
	}{
		{name: "empty actor", record: appauth.StoredCredential{Role: relations.RoleAdmin, Origin: audit.OriginWeb, Digest: digest[:]}},
		{name: "role out of range", record: appauth.StoredCredential{Actor: "a", Role: 99, Origin: audit.OriginWeb, Digest: digest[:]}},
		{name: "origin out of range", record: appauth.StoredCredential{Actor: "a", Role: relations.RoleAdmin, Origin: 99, Digest: digest[:]}},
		{name: "short digest", record: appauth.StoredCredential{Actor: "a", Role: relations.RoleAdmin, Origin: audit.OriginWeb, Digest: []byte{1, 2, 3}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := appauth.NewTokenAuthenticatorFromStored([]appauth.StoredCredential{test.record}); !errors.Is(err, appauth.ErrInvalidCredential) {
				t.Fatalf("error = %v, want ErrInvalidCredential", err)
			}
		})
	}
}

func TestEnvironmentCredentialsCollectsBothSurfaces(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		"DBGRAPH_WEB_TOKEN": storedWebToken,
		"DBGRAPH_MCP_TOKEN": storedMCPToken,
	}
	stored, err := appauth.EnvironmentCredentials(func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("EnvironmentCredentials: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("collected %d credentials, want 2", len(stored))
	}

	byActor := map[string]appauth.StoredCredential{}
	for _, credential := range stored {
		byActor[credential.Actor] = credential
	}
	webDigest := sha256.Sum256([]byte(storedWebToken))
	web, ok := byActor["web"]
	if !ok || string(web.Digest) != string(webDigest[:]) || web.Role != relations.RoleAdmin || web.Origin != audit.OriginWeb {
		t.Fatalf("web credential = %#v", web)
	}
	agent, ok := byActor["mcp"]
	if !ok || agent.Role != relations.RoleAdmin || agent.Origin != audit.OriginAgent {
		t.Fatalf("mcp credential = %#v", agent)
	}

	empty, err := appauth.EnvironmentCredentials(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("EnvironmentCredentials with no variables: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("collected %d credentials from an empty environment", len(empty))
	}
}

// TestEnvironmentCredentialsRefusesADuplicateToken keeps the pre-existing
// fail-closed behaviour: two variables holding one token would otherwise
// resolve to whichever actor is applied last, which is the more privileged.
func TestEnvironmentCredentialsRefusesADuplicateToken(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		"DBGRAPH_WEB_TOKEN": storedWebToken,
		"DBGRAPH_MCP_TOKEN": storedWebToken,
	}
	_, err := appauth.EnvironmentCredentials(func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	})
	if !errors.Is(err, appauth.ErrInvalidCredential) {
		t.Fatalf("error = %v, want ErrInvalidCredential for a shared token", err)
	}
}
