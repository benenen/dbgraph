package sqlite_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

// TestStoredDSNCiphertextNeverLandsInTheDatabaseAsPlaintext is the whole point
// of sealing: a stolen database file must not be a credential.
func TestStoredDSNCiphertextNeverLandsInTheDatabaseAsPlaintext(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "dbgraph.sqlite")
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	createdAt := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	if err := dbsqlite.NewProjectRepository(store).CreateProject(ctx, catalog.Project{
		ID: 10, Name: "orders", CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}

	const password = "TotallySecretPassword123"
	ciphertext := []byte("sealed-bytes-standing-in-for-aes-gcm-output")
	repository := dbsqlite.NewCatalogRepository(store, nil)
	if err := repository.CreateDataSource(ctx, catalog.DataSource{
		ID: 30, Name: "orders-primary", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "ORDERS_DSN", DSNKeyID: "abcd1234", DSNCiphertext: ciphertext,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}, 10); err != nil {
		t.Fatalf("CreateDataSource: %v", err)
	}

	loaded, err := repository.GetDataSource(ctx, 30)
	if err != nil {
		t.Fatalf("GetDataSource: %v", err)
	}
	if loaded.DSNKeyID != "abcd1234" {
		t.Fatalf("DSNKeyID = %q, want abcd1234", loaded.DSNKeyID)
	}
	if !bytes.Equal(loaded.DSNCiphertext, ciphertext) {
		t.Fatalf("DSNCiphertext = %q, want the stored bytes", loaded.DSNCiphertext)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	raw, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(password)) {
		t.Fatal("the database file contains the plaintext password")
	}
}

// TestDataSourceWithoutCiphertextKeepsWorking protects every row created before
// this feature existed.
func TestDataSourceWithoutCiphertextKeepsWorking(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	createdAt := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	if err := dbsqlite.NewProjectRepository(store).CreateProject(ctx, catalog.Project{
		ID: 10, Name: "orders", CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	repository := dbsqlite.NewCatalogRepository(store, nil)
	if err := repository.CreateDataSource(ctx, catalog.DataSource{
		ID: 31, Name: "legacy", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "LEGACY_DSN", CreatedAt: createdAt, UpdatedAt: createdAt,
	}, 10); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.GetDataSource(ctx, 31)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DSNEnvironment != "LEGACY_DSN" || loaded.DSNKeyID != "" || loaded.DSNCiphertext != nil {
		t.Fatalf("legacy data source = %#v, want an environment-only row", loaded)
	}
}
