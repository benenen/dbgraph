package auth

import (
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/benenen/dbgraph/internal/relations"
)

var testCredentialToken = testHexToken(0)

func testHexToken(seed byte) string {
	random := make([]byte, 32)
	for index := range random {
		random[index] = seed + byte(index)
	}
	return hex.EncodeToString(random)
}

func TestTokenAuthenticatorRejectsLowEntropyTokens(t *testing.T) {
	for _, token := range []string{"short-token", "not-a-64-character-hex-token-with-thirty-two-random-bytes!!", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		_, err := NewTokenAuthenticator([]Credential{{
			Token: token, Actor: "analysis-agent", Role: relations.RoleAgent,
		}})
		if !errors.Is(err, ErrInvalidCredential) {
			t.Fatalf("low-entropy token %q error = %v, want %v", token, err, ErrInvalidCredential)
		}
	}
}

func TestTokenAuthenticatorUsesExactBearerTokensAndDoesNotRetainPlaintext(t *testing.T) {
	authenticator, err := NewTokenAuthenticator([]Credential{{Token: testCredentialToken, Actor: "analysis-agent", Role: relations.RoleAgent}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "http://localhost/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+testCredentialToken)
	principal, ok := authenticator.Authenticate(request)
	if !ok || principal.Actor != "analysis-agent" || principal.Role != relations.RoleAgent {
		t.Fatalf("principal = %#v, ok = %v", principal, ok)
	}

	request.Header.Set("Authorization", "Bearer "+testCredentialToken+"-extra")
	if _, ok := authenticator.Authenticate(request); ok {
		t.Fatal("prefix token was accepted")
	}
	if len(authenticator.records) != 1 || string(authenticator.records[0].digest[:]) == testCredentialToken {
		t.Fatal("authenticator retained an invalid token representation")
	}
}
