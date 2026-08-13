package mysql_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	"github.com/benenen/dbgraph/internal/ingestion/mysql"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestRunnerLoadsDSNFromEnvironmentAndPublishesSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{
		Path: filepath.Join(t.TempDir(), "dbgraph.sqlite"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	fixedTime := time.Date(2026, time.August, 11, 13, 30, 0, 0, time.UTC)
	idGenerator, err := id.NewGenerator(8, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	catalogService := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, idGenerator),
		idGenerator,
		func() time.Time { return fixedTime },
	)
	dataSource, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{

		Name:           "primary",
		Kind:           catalog.DataSourceMySQL,
		DSNEnvironment: "DBGRAPH_PRIMARY_MYSQL_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}

	sourceDatabase, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create source database mock: %v", err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery("FROM information_schema[.]SCHEMATA").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{"SCHEMA_NAME"}).AddRow("learn"))
	mock.ExpectQuery("FROM information_schema[.]TABLES").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{"TABLE_SCHEMA", "TABLE_NAME"}))
	mock.ExpectQuery("FROM information_schema[.]STATISTICS").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{
			"TABLE_SCHEMA", "TABLE_NAME", "INDEX_NAME", "NON_UNIQUE", "SEQ_IN_INDEX", "COLUMN_NAME",
		}).
			AddRow("learn", "students", "PRIMARY", 0, 1, "id").
			AddRow("learn", "classes", "idx_student", 1, 1, "student_id"))
	mock.ExpectQuery("FROM information_schema[.]COLUMNS").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{
			"TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME", "COLUMN_TYPE", "IS_NULLABLE", "ORDINAL_POSITION",
		}))
	mock.ExpectQuery("FROM information_schema[.]KEY_COLUMN_USAGE.*REFERENCED_TABLE_SCHEMA = [?]").
		WithArgs("learn", "learn").
		WillReturnRows(sqlmock.NewRows([]string{
			"CONSTRAINT_SCHEMA", "CONSTRAINT_NAME", "TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME",
			"REFERENCED_TABLE_SCHEMA", "REFERENCED_TABLE_NAME", "REFERENCED_COLUMN_NAME", "ORDINAL_POSITION",
		}))
	mock.ExpectCommit()
	mock.ExpectClose()

	const secretDSN = "readonly:secret@tcp(mysql.example.test:3306)/learn"
	openDatabase := func(_ context.Context, dsn string) (*sql.DB, error) {
		if dsn != secretDSN {
			t.Fatalf("opened DSN = %q, want environment value", dsn)
		}
		return sourceDatabase, nil
	}
	lookupEnvironment := func(key string) (string, bool) {
		if key == "DBGRAPH_PRIMARY_MYSQL_DSN" {
			return secretDSN, true
		}
		return "", false
	}
	runner := mysql.NewRunner(catalogService, mysql.NewScanner(), openDatabase, lookupEnvironment)

	published, err := runner.Run(ctx, dataSource.ID)
	if err != nil {
		t.Fatalf("run schema scan: %v", err)
	}
	if published.NodeCount != 2 {
		t.Fatalf("published node count = %d, want database and schema", published.NodeCount)
	}
	if _, err := catalogService.FindCurrentNode(ctx, dataSource.ID, "learn"); err != nil {
		t.Fatalf("find published schema: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("source SQL expectations: %v", err)
	}
}

func TestRunnerIgnoresCrossSchemaForeignKeysThatCatalogCannotRepresent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{
		Path: filepath.Join(t.TempDir(), "dbgraph.sqlite"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixedTime := time.Date(2026, time.August, 11, 13, 40, 0, 0, time.UTC)
	idGenerator, err := id.NewGenerator(9, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	catalogService := catalog.NewService(dbsqlite.NewCatalogRepository(store, idGenerator), idGenerator, func() time.Time { return fixedTime })
	dataSource, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{
		Name: "primary", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "CROSS_SCHEMA_MYSQL_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}

	sourceDatabase, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create source database mock: %v", err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery("FROM information_schema[.]SCHEMATA").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{"SCHEMA_NAME"}).AddRow("learn"))
	mock.ExpectQuery("FROM information_schema[.]TABLES").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{"TABLE_SCHEMA", "TABLE_NAME"}).
			AddRow("learn", "classes"))
	mock.ExpectQuery("FROM information_schema[.]STATISTICS").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{
			"TABLE_SCHEMA", "TABLE_NAME", "INDEX_NAME", "NON_UNIQUE", "SEQ_IN_INDEX", "COLUMN_NAME",
		}).
			AddRow("learn", "students", "PRIMARY", 0, 1, "id").
			AddRow("learn", "classes", "idx_student", 1, 1, "student_id"))
	mock.ExpectQuery("FROM information_schema[.]COLUMNS").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{
			"TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME", "COLUMN_TYPE", "IS_NULLABLE", "ORDINAL_POSITION",
		}).AddRow("learn", "classes", "external_id", "bigint", "NO", 1))
	mock.ExpectQuery("FROM information_schema[.]KEY_COLUMN_USAGE.*REFERENCED_TABLE_SCHEMA = [?]").
		WithArgs("learn", "learn").
		WillReturnRows(sqlmock.NewRows([]string{
			"CONSTRAINT_SCHEMA", "CONSTRAINT_NAME", "TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME",
			"REFERENCED_TABLE_SCHEMA", "REFERENCED_TABLE_NAME", "REFERENCED_COLUMN_NAME", "ORDINAL_POSITION",
		}).AddRow(
			"learn", "fk_classes_external", "learn", "classes", "external_id",
			"archive", "students", "id", 1,
		))
	mock.ExpectCommit()
	mock.ExpectClose()

	const sourceDSN = "readonly:secret@tcp(mysql.example.test:3306)/learn"
	runner := mysql.NewRunner(
		catalogService,
		mysql.NewScanner(),
		func(context.Context, string) (*sql.DB, error) { return sourceDatabase, nil },
		func(key string) (string, bool) {
			if key == "CROSS_SCHEMA_MYSQL_DSN" {
				return sourceDSN, true
			}
			return "", false
		},
	)

	published, err := runner.Run(ctx, dataSource.ID)
	if err != nil {
		t.Fatalf("publish schema containing external foreign key metadata: %v", err)
	}
	if published.NodeCount != 4 {
		t.Fatalf("published node count = %d, want 4 local nodes", published.NodeCount)
	}
	if _, err := catalogService.FindCurrentNode(
		ctx, dataSource.ID, "learn.classes.external_id",
	); err != nil {
		t.Fatalf("find local column after filtered publication: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("source SQL expectations: %v", err)
	}
}

func TestRunnerPersistsFailedScanRunWhenSourceCannotConnect(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "dbgraph.sqlite")
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	fixedTime := time.Date(2026, time.August, 11, 13, 45, 0, 0, time.UTC)
	idGenerator, err := id.NewGenerator(8, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	catalogService := catalog.NewService(dbsqlite.NewCatalogRepository(store, idGenerator), idGenerator, func() time.Time { return fixedTime })
	dataSource, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{

		Name: "unavailable", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "FAILED_SCAN_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}

	connectFailure := errors.New("source unavailable")
	runner := mysql.NewRunner(
		catalogService,
		mysql.NewScanner(),
		func(context.Context, string) (*sql.DB, error) { return nil, connectFailure },
		func(string) (string, bool) {
			return "readonly:secret@tcp(mysql.example.test:3306)/learn?tls=true", true
		},
	)
	if _, err := runner.Run(ctx, dataSource.ID); !errors.Is(err, connectFailure) {
		t.Fatalf("run scan error = %v, want source failure", err)
	}

	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	databaseURL.RawQuery = query.Encode()
	reader, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatalf("open read-only verification database: %v", err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close verification database: %v", err)
		}
	})
	var status int
	var errorCode string
	var errorMessage string
	var completedAt string
	if err := reader.QueryRowContext(ctx, `
SELECT status, error_code, error_message, completed_at
FROM schema_scan_runs
WHERE data_source_id = ?
`, dataSource.ID).Scan(&status, &errorCode, &errorMessage, &completedAt); err != nil {
		t.Fatalf("read failed schema scan run: %v", err)
	}
	if status != 3 || errorCode != "SOURCE_CONNECTION_FAILED" ||
		errorMessage != "schema scan did not complete" || completedAt == "" {
		t.Fatalf("failed run status=%d code=%q message=%q completedAt=%q", status, errorCode, errorMessage, completedAt)
	}
}
