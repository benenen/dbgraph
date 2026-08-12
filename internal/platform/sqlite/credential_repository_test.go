package sqlite_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	appauth "github.com/benenen/dbgraph/internal/auth"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestCredentialsSeedOnceThenLoadWithoutTheEnvironment(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "dbgraph.sqlite")
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const adminToken = "1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"
	adminDigest := sha256.Sum256([]byte(adminToken))
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	repository := dbsqlite.NewCredentialRepository(store, func() time.Time { return now })

	seeded := []appauth.StoredCredential{
		{Actor: "web-admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb, Digest: adminDigest[:]},
	}
	if err := repository.SyncCredentials(ctx, seeded); err != nil {
		t.Fatalf("SyncCredentials: %v", err)
	}

	loaded, err := repository.ListCredentials(ctx)
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Actor != "web-admin" || !bytes.Equal(loaded[0].Digest, adminDigest[:]) {
		t.Fatalf("loaded = %#v", loaded)
	}

	authenticator, err := appauth.NewTokenAuthenticatorFromStored(loaded)
	if err != nil {
		t.Fatalf("build authenticator: %v", err)
	}
	if principal, ok := authenticator.AuthenticateToken(adminToken); !ok || principal.Role != relations.RoleAdmin {
		t.Fatalf("stored credential did not authenticate: principal=%#v ok=%t", principal, ok)
	}

	// Seeding is idempotent, and re-seeding the same actor rotates its digest
	// rather than adding a row.
	const rotatedToken = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	rotatedDigest := sha256.Sum256([]byte(rotatedToken))
	if err := repository.SyncCredentials(ctx, seeded); err != nil {
		t.Fatal(err)
	}
	if err := repository.SyncCredentials(ctx, []appauth.StoredCredential{
		{Actor: "web-admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb, Digest: rotatedDigest[:]},
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.ListCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || !bytes.Equal(loaded[0].Digest, rotatedDigest[:]) {
		t.Fatalf("after rotation loaded = %#v, want one row with the new digest", loaded)
	}

	// An empty seed leaves the stored credentials alone, which is what a server
	// started without any token in its environment must do.
	if err := repository.SyncCredentials(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if loaded, err = repository.ListCredentials(ctx); err != nil || len(loaded) != 1 {
		t.Fatalf("after an empty seed loaded = %#v err = %v", loaded, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(adminToken)) || bytes.Contains(raw, []byte(rotatedToken)) {
		t.Fatal("the database file contains a token in plaintext")
	}
}

func TestSyncCredentialsRefusesToMoveADigestBetweenActors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	digest := sha256.Sum256([]byte("1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"))
	repository := dbsqlite.NewCredentialRepository(store, nil)
	if err := repository.SyncCredentials(ctx, []appauth.StoredCredential{
		{Actor: "web-viewer", Role: relations.RoleViewer, Origin: audit.OriginWeb, Digest: digest[:]},
	}); err != nil {
		t.Fatal(err)
	}
	// Handing the viewer's token to the admin actor would silently escalate
	// whoever holds it. The unique index must refuse.
	if err := repository.SyncCredentials(ctx, []appauth.StoredCredential{
		{Actor: "web-admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb, Digest: digest[:]},
	}); err == nil {
		t.Fatal("re-pointing one actor's digest at another actor was accepted")
	}
	loaded, err := repository.ListCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Actor != "web-viewer" || loaded[0].Role != relations.RoleViewer {
		t.Fatalf("loaded = %#v, want the original viewer credential untouched", loaded)
	}
}
