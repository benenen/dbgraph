package mysql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/benenen/dbgraph/internal/catalog"
	mysqlingestion "github.com/benenen/dbgraph/internal/ingestion/mysql"
)

type resolveCatalog struct {
	source catalog.DataSource
}

func (c *resolveCatalog) GetDataSource(context.Context, int64) (catalog.DataSource, error) {
	return c.source, nil
}

func (c *resolveCatalog) GetProjectDataSource(ctx context.Context, dataSourceID int64) (catalog.DataSource, error) {
	return c.GetDataSource(ctx, dataSourceID)
}

func (c *resolveCatalog) BeginSchemaScan(_ context.Context, dataSourceID int64) (catalog.SchemaScanRun, error) {
	return catalog.SchemaScanRun{ID: 40, DataSourceID: dataSourceID}, nil
}

func (c *resolveCatalog) FailSchemaScan(context.Context, catalog.SchemaScanRun, string) error {
	return nil
}

func (c *resolveCatalog) PublishStartedSnapshot(
	context.Context,
	catalog.SchemaScanRun,
	catalog.PublishSnapshot,
) (catalog.PublishedSnapshot, error) {
	return catalog.PublishedSnapshot{}, nil
}

// TestRunnerPrefersTheSealedDSNOverTheEnvironment proves the stored credential
// is the one dialled, and that the environment is not consulted for it.
func TestRunnerPrefersTheSealedDSNOverTheEnvironment(t *testing.T) {
	t.Parallel()

	const sealedDSN = "sealed:pw@tcp(127.0.0.1:3306)/sealed_db"
	const environmentDSN = "env:pw@tcp(127.0.0.1:3306)/env_db"

	var dialled string
	environmentReads := 0
	runner := mysqlingestion.NewRunner(
		&resolveCatalog{source: catalog.DataSource{
			ID: 30, Kind: catalog.DataSourceMySQL,
			DSNEnvironment: "ORDERS_DSN", DSNKeyID: "abcd1234",
			DSNCiphertext: []byte("ciphertext"),
		}},
		nil,
		func(_ context.Context, dsn string) (*sql.DB, error) {
			dialled = dsn
			return nil, errors.New("stop before dialling")
		},
		func(string) (string, bool) {
			environmentReads++
			return environmentDSN, true
		},
		mysqlingestion.WithSecretOpener(func(keyID string, ciphertext []byte) (string, error) {
			if keyID != "abcd1234" || string(ciphertext) != "ciphertext" {
				t.Fatalf("opener received keyID=%q ciphertext=%q", keyID, ciphertext)
			}
			return sealedDSN, nil
		}),
	)

	_, err := runner.Run(context.Background(), 30)
	if err == nil {
		t.Fatal("expected the run to stop at the dial")
	}
	if dialled != sealedDSN {
		t.Fatalf("dialled %q, want the sealed DSN %q", dialled, sealedDSN)
	}
	if environmentReads != 0 {
		t.Fatalf("environment was read %d times for a source with a stored DSN", environmentReads)
	}
}

// TestRunnerFallsBackToTheEnvironmentWithoutCiphertext keeps every pre-existing
// data source working.
func TestRunnerFallsBackToTheEnvironmentWithoutCiphertext(t *testing.T) {
	t.Parallel()

	const environmentDSN = "env:pw@tcp(127.0.0.1:3306)/env_db"

	var dialled string
	runner := mysqlingestion.NewRunner(
		&resolveCatalog{source: catalog.DataSource{
			ID: 31, Kind: catalog.DataSourceMySQL, DSNEnvironment: "ORDERS_DSN",
		}},
		nil,
		func(_ context.Context, dsn string) (*sql.DB, error) {
			dialled = dsn
			return nil, errors.New("stop before dialling")
		},
		func(name string) (string, bool) {
			if name != "ORDERS_DSN" {
				t.Fatalf("looked up %q", name)
			}
			return environmentDSN, true
		},
		mysqlingestion.WithSecretOpener(func(string, []byte) (string, error) {
			t.Fatal("the opener must not be called without ciphertext")
			return "", nil
		}),
	)

	if _, err := runner.Run(context.Background(), 31); err == nil {
		t.Fatal("expected the run to stop at the dial")
	}
	if dialled != environmentDSN {
		t.Fatalf("dialled %q, want the environment DSN", dialled)
	}
}

// TestRunnerReportsAnUnreadableSealedDSN surfaces a key mismatch instead of
// silently falling back to a stale environment variable.
func TestRunnerReportsAnUnreadableSealedDSN(t *testing.T) {
	t.Parallel()

	runner := mysqlingestion.NewRunner(
		&resolveCatalog{source: catalog.DataSource{
			ID: 32, Kind: catalog.DataSourceMySQL,
			DSNEnvironment: "ORDERS_DSN", DSNKeyID: "old-key", DSNCiphertext: []byte("ciphertext"),
		}},
		nil,
		func(context.Context, string) (*sql.DB, error) {
			t.Fatal("must not dial when the stored DSN cannot be opened")
			return nil, nil
		},
		func(string) (string, bool) { return "env:pw@tcp(127.0.0.1:3306)/env_db", true },
		mysqlingestion.WithSecretOpener(func(string, []byte) (string, error) {
			return "", errors.New("authentication failed")
		}),
	)

	if _, err := runner.Run(context.Background(), 32); err == nil {
		t.Fatal("expected an error for an unreadable stored DSN")
	}
}
