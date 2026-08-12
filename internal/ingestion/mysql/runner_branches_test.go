package mysql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/benenen/dbgraph/internal/catalog"
	mysqlingestion "github.com/benenen/dbgraph/internal/ingestion/mysql"
)

type runnerCatalog struct {
	dataSource catalog.DataSource
	getError   error
	beginError error
	scanRun    catalog.SchemaScanRun
	failedRun  catalog.SchemaScanRun
	failure    string
	failError  error
	published  catalog.PublishSnapshot
	result     catalog.PublishedSnapshot
	publishErr error
}

func (service *runnerCatalog) GetDataSource(context.Context, int64) (catalog.DataSource, error) {
	return service.dataSource, service.getError
}

func (service *runnerCatalog) GetProjectDataSource(ctx context.Context, _ int64, dataSourceID int64) (catalog.DataSource, error) {
	return service.GetDataSource(ctx, dataSourceID)
}

func (service *runnerCatalog) BeginSchemaScan(
	_ context.Context,
	projectID int64,
	dataSourceID int64,
) (catalog.SchemaScanRun, error) {
	if service.beginError != nil {
		return catalog.SchemaScanRun{}, service.beginError
	}
	if service.scanRun.ID == 0 {
		return catalog.SchemaScanRun{
			ID: 19, ProjectID: projectID, DataSourceID: dataSourceID,
			StartedAt: time.Date(2026, time.August, 11, 13, 0, 0, 0, time.UTC),
		}, nil
	}
	return service.scanRun, nil
}

func (service *runnerCatalog) FailSchemaScan(
	_ context.Context,
	run catalog.SchemaScanRun,
	errorCode string,
) error {
	service.failedRun = run
	service.failure = errorCode
	return service.failError
}

func (service *runnerCatalog) PublishStartedSnapshot(
	_ context.Context,
	_ catalog.SchemaScanRun,
	command catalog.PublishSnapshot,
) (catalog.PublishedSnapshot, error) {
	service.published = command
	return service.result, service.publishErr
}

type scannerFunc func(context.Context, *sql.DB, string) (catalog.ScannedSnapshot, error)

func (function scannerFunc) Scan(ctx context.Context, database *sql.DB, schema string) (catalog.ScannedSnapshot, error) {
	return function(ctx, database, schema)
}

func validRunnerSource() catalog.DataSource {
	return catalog.DataSource{
		ID: 11, Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "SOURCE_DSN",
	}
}

func TestRunnerRejectsSourceAndConfigurationBeforeOpeningDatabase(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("catalog unavailable")
	tests := []struct {
		name    string
		service *runnerCatalog
		lookup  mysqlingestion.EnvironmentLookup
		want    error
	}{
		{"catalog error", &runnerCatalog{getError: sentinel}, func(string) (string, bool) { return "", false }, sentinel},
		{"unsupported source", &runnerCatalog{dataSource: catalog.DataSource{Kind: catalog.DataSourceKind(99)}}, func(string) (string, bool) { return "", false }, mysqlingestion.ErrUnsupportedSource},
		{"missing DSN", &runnerCatalog{dataSource: validRunnerSource()}, func(string) (string, bool) { return "", false }, mysqlingestion.ErrMissingDSN},
		{"blank DSN", &runnerCatalog{dataSource: validRunnerSource()}, func(string) (string, bool) { return "  ", true }, mysqlingestion.ErrMissingDSN},
		{"invalid DSN", &runnerCatalog{dataSource: validRunnerSource()}, func(string) (string, bool) { return "%", true }, mysqlingestion.ErrInvalidDSN},
		{"missing database", &runnerCatalog{dataSource: validRunnerSource()}, func(string) (string, bool) { return "user:pass@tcp(localhost:3306)/", true }, mysqlingestion.ErrInvalidDSN},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			opened := false
			runner := mysqlingestion.NewRunner(test.service, scannerFunc(func(context.Context, *sql.DB, string) (catalog.ScannedSnapshot, error) {
				t.Fatal("scanner called")
				return catalog.ScannedSnapshot{}, nil
			}), func(context.Context, string) (*sql.DB, error) {
				opened = true
				return nil, errors.New("unexpected open")
			}, test.lookup)
			_, err := runner.Run(context.Background(), 7, 11)
			if !errors.Is(err, test.want) || opened {
				t.Fatalf("Run error = %v, want %v, opened = %v", err, test.want, opened)
			}
		})
	}
}

func TestRunnerPropagatesOpenScanCloseAndPublishFailures(t *testing.T) {
	t.Parallel()

	const dsn = "user:secret@tcp(localhost:3306)/source?tls=true"
	lookup := func(string) (string, bool) { return dsn, true }
	openFailure := errors.New("open failed")
	openRunner := mysqlingestion.NewRunner(&runnerCatalog{dataSource: validRunnerSource()}, nil,
		func(context.Context, string) (*sql.DB, error) { return nil, openFailure }, lookup)
	if _, err := openRunner.Run(context.Background(), 7, 11); !errors.Is(err, openFailure) {
		t.Fatalf("open error = %v", err)
	}

	for _, test := range []struct {
		name       string
		scanErr    error
		closeErr   error
		publishErr error
	}{
		{name: "scan", scanErr: errors.New("scan failed")},
		{name: "close", closeErr: errors.New("close failed")},
		{name: "publish", publishErr: errors.New("publish failed")},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			closeExpectation := mock.ExpectClose()
			if test.closeErr != nil {
				closeExpectation.WillReturnError(test.closeErr)
			}
			service := &runnerCatalog{
				dataSource: validRunnerSource(), result: catalog.PublishedSnapshot{NodeCount: 2}, publishErr: test.publishErr,
			}
			snapshot := catalog.ScannedSnapshot{Nodes: []catalog.NodeInput{{QualifiedName: "source"}}}
			runner := mysqlingestion.NewRunner(service, scannerFunc(func(_ context.Context, received *sql.DB, schema string) (catalog.ScannedSnapshot, error) {
				if received != database || schema != "source" {
					t.Fatalf("scanner input database=%p schema=%q", received, schema)
				}
				return snapshot, test.scanErr
			}), func(context.Context, string) (*sql.DB, error) { return database, nil }, lookup)

			result, runErr := runner.Run(context.Background(), 7, 11)
			want := test.scanErr
			if want == nil {
				want = test.closeErr
			}
			if want == nil {
				want = test.publishErr
			}
			if !errors.Is(runErr, want) {
				t.Fatalf("Run error = %v, want %v", runErr, want)
			}
			if want == nil && result.NodeCount != 2 {
				t.Fatalf("result = %#v", result)
			}
			if test.scanErr == nil && test.closeErr == nil {
				if service.published.ProjectID != 7 || service.published.DataSourceID != 11 || len(service.published.Nodes) != 1 {
					t.Fatalf("published = %#v", service.published)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("expectations: %v", err)
			}
		})
	}
}

type incrementalScanner struct {
	called bool
}

func (scanner *incrementalScanner) Scan(context.Context, *sql.DB, string) (catalog.ScannedSnapshot, error) {
	return catalog.ScannedSnapshot{}, errors.New("full scan was called")
}

func (scanner *incrementalScanner) ScanTables(
	_ context.Context,
	_ *sql.DB,
	databaseName string,
	tables []string,
) (catalog.ScannedSnapshot, error) {
	if databaseName != "source" || len(tables) != 1 || tables[0] != "source.orders" {
		return catalog.ScannedSnapshot{}, errors.New("unexpected incremental scope")
	}
	scanner.called = true
	return catalog.ScannedSnapshot{Nodes: []catalog.NodeInput{{QualifiedName: "source.orders"}}}, nil
}

func TestRunnerPublishesExplicitIncrementalTableScope(t *testing.T) {
	t.Parallel()

	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectClose()
	service := &runnerCatalog{dataSource: validRunnerSource()}
	scanner := &incrementalScanner{}
	runner := mysqlingestion.NewRunner(
		service,
		scanner,
		func(context.Context, string) (*sql.DB, error) { return database, nil },
		func(string) (string, bool) { return "user:secret@tcp(localhost:3306)/source?tls=true", true },
	)
	if _, err := runner.RunIncremental(context.Background(), 7, 11, []string{"source.orders"}); err != nil {
		t.Fatalf("run incremental schema scan: %v", err)
	}
	if !scanner.called || len(service.published.ScopeTables) != 1 || service.published.ScopeTables[0] != "source.orders" {
		t.Fatalf("incremental scanner called=%v publication=%#v", scanner.called, service.published)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFunctionsRejectInvalidOrInsecureDSNsWithoutDialing(t *testing.T) {
	t.Parallel()

	if _, err := mysqlingestion.Open(context.Background(), "%"); !errors.Is(err, mysqlingestion.ErrInvalidDSN) {
		t.Fatalf("Open invalid DSN = %v", err)
	}
	if _, err := mysqlingestion.OpenWithPolicy(context.Background(), "user:pass@tcp(example.test:3306)/db", mysqlingestion.ConnectionPolicy{}); !errors.Is(err, mysqlingestion.ErrTLSRequired) {
		t.Fatalf("OpenWithPolicy insecure DSN = %v", err)
	}
	if runner := mysqlingestion.NewRunner(nil, nil, nil, nil); runner == nil {
		t.Fatal("NewRunner returned nil")
	}
}
