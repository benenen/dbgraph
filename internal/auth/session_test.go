package auth

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestSessionManagerStoresHashesAndRequiresExactCSRFToken(t *testing.T) {
	authenticator, err := NewTokenAuthenticator([]Credential{{
		Token: testCredentialToken, Actor: "web-editor", Role: relations.RoleEditor, Origin: audit.OriginWeb,
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	random := bytes.NewReader(bytes.Repeat([]byte{7}, 64))
	manager := NewSessionManager(authenticator, func() time.Time { return now }, random)
	sessionToken, created, err := manager.Create(testCredentialToken)
	if err != nil {
		t.Fatal(err)
	}
	if created.Principal.Role != relations.RoleEditor || created.CSRFToken == "" || sessionToken == "" {
		t.Fatalf("created session = %#v", created)
	}
	if !manager.ValidateCSRF(sessionToken, created.CSRFToken) || manager.ValidateCSRF(sessionToken, created.CSRFToken+"x") {
		t.Fatal("CSRF validation did not require the exact token")
	}
	loaded, ok := manager.Get(sessionToken)
	if !ok || loaded.Principal.Actor != "web-editor" || loaded.CSRFToken != created.CSRFToken {
		t.Fatalf("loaded session = %#v, ok=%v", loaded, ok)
	}
	now = now.Add(sessionTTL)
	if _, ok := manager.Get(sessionToken); ok {
		t.Fatal("expired session was accepted")
	}
}

func TestSessionManagerBoundsActiveSessionsPerActor(t *testing.T) {
	authenticator, err := NewTokenAuthenticator([]Credential{{
		Token: testCredentialToken, Actor: "web-editor", Role: relations.RoleEditor, Origin: audit.OriginWeb,
	}})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewSessionManager(authenticator, time.Now, nil)
	for index := 0; index < maximumSessionsPerActor; index++ {
		if _, _, err := manager.Create(testCredentialToken); err != nil {
			t.Fatalf("create session %d: %v", index, err)
		}
	}
	if _, _, err := manager.Create(testCredentialToken); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("session over limit error = %v", err)
	}
}
