package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	mysqldriver "github.com/go-sql-driver/mysql"
)

var (
	ErrMissingDSN          = errors.New("MySQL DSN environment variable is missing")
	ErrUnsupportedSource   = errors.New("unsupported data source")
	ErrInvalidDSN          = errors.New("invalid MySQL DSN")
	ErrIncrementalScan     = errors.New("invalid or unsupported incremental schema scan")
	ErrTLSRequired         = errors.New("TLS is required for MySQL TCP connections")
	ErrVerifiedTLSRequired = errors.New("verified TLS is required for MySQL TCP connections")
)

type ConnectionPolicy struct {
	AllowInsecureTLS bool
}

type catalogService interface {
	GetDataSource(context.Context, int64) (catalog.DataSource, error)
	BeginSchemaScan(context.Context, int64, int64) (catalog.SchemaScanRun, error)
	FailSchemaScan(context.Context, catalog.SchemaScanRun, string) error
	PublishStartedSnapshot(
		context.Context,
		catalog.SchemaScanRun,
		catalog.PublishSnapshot,
	) (catalog.PublishedSnapshot, error)
}

type schemaScanner interface {
	Scan(context.Context, *sql.DB, string) (catalog.ScannedSnapshot, error)
}

type incrementalSchemaScanner interface {
	ScanTables(context.Context, *sql.DB, string, []string) (catalog.ScannedSnapshot, error)
}

type OpenDatabase func(context.Context, string) (*sql.DB, error)

type EnvironmentLookup func(string) (string, bool)

type Runner struct {
	catalog           catalogService
	scanner           schemaScanner
	openDatabase      OpenDatabase
	lookupEnvironment EnvironmentLookup
}

func NewRunner(
	catalog catalogService,
	scanner schemaScanner,
	openDatabase OpenDatabase,
	lookupEnvironment EnvironmentLookup,
) *Runner {
	if openDatabase == nil {
		openDatabase = Open
	}
	return &Runner{
		catalog:           catalog,
		scanner:           scanner,
		openDatabase:      openDatabase,
		lookupEnvironment: lookupEnvironment,
	}
}

func (r *Runner) Run(ctx context.Context, dataSourceID int64) (catalog.PublishedSnapshot, error) {
	return r.run(ctx, dataSourceID, nil)
}

func (r *Runner) RunIncremental(
	ctx context.Context,
	dataSourceID int64,
	scopeTables []string,
) (catalog.PublishedSnapshot, error) {
	if len(scopeTables) == 0 || len(scopeTables) > catalog.MaximumIncrementalTables {
		return catalog.PublishedSnapshot{}, ErrIncrementalScan
	}
	if _, supported := r.scanner.(incrementalSchemaScanner); !supported {
		return catalog.PublishedSnapshot{}, ErrIncrementalScan
	}
	return r.run(ctx, dataSourceID, append([]string(nil), scopeTables...))
}

func (r *Runner) run(
	ctx context.Context,
	dataSourceID int64,
	scopeTables []string,
) (catalog.PublishedSnapshot, error) {
	dataSource, err := r.catalog.GetDataSource(ctx, dataSourceID)
	if err != nil {
		return catalog.PublishedSnapshot{}, err
	}
	if dataSource.Kind != catalog.DataSourceMySQL {
		return catalog.PublishedSnapshot{}, ErrUnsupportedSource
	}
	dsn, ok := r.lookupEnvironment(dataSource.DSNEnvironment)
	if !ok || strings.TrimSpace(dsn) == "" {
		return catalog.PublishedSnapshot{}, ErrMissingDSN
	}
	dsnConfig, err := mysqldriver.ParseDSN(dsn)
	if err != nil || strings.TrimSpace(dsnConfig.DBName) == "" {
		return catalog.PublishedSnapshot{}, ErrInvalidDSN
	}
	scanRun, err := r.catalog.BeginSchemaScan(ctx, dataSource.ProjectID, dataSource.ID)
	if err != nil {
		return catalog.PublishedSnapshot{}, err
	}

	database, err := r.openDatabase(ctx, dsn)
	if err != nil {
		return catalog.PublishedSnapshot{}, r.failSchemaScan(ctx, scanRun, "SOURCE_CONNECTION_FAILED", err)
	}
	var snapshot catalog.ScannedSnapshot
	var scanErr error
	if len(scopeTables) == 0 {
		snapshot, scanErr = r.scanner.Scan(ctx, database, dsnConfig.DBName)
	} else {
		snapshot, scanErr = r.scanner.(incrementalSchemaScanner).ScanTables(
			ctx, database, dsnConfig.DBName, scopeTables,
		)
	}
	closeErr := database.Close()
	if scanErr != nil {
		return catalog.PublishedSnapshot{}, r.failSchemaScan(ctx, scanRun, "SOURCE_QUERY_FAILED", scanErr)
	}
	if closeErr != nil {
		return catalog.PublishedSnapshot{}, r.failSchemaScan(
			ctx, scanRun, "SOURCE_CLOSE_FAILED", fmt.Errorf("close MySQL source: %w", closeErr),
		)
	}

	published, err := r.catalog.PublishStartedSnapshot(ctx, scanRun, catalog.PublishSnapshot{
		ProjectID:    dataSource.ProjectID,
		DataSourceID: dataSource.ID,
		Nodes:        snapshot.Nodes,
		ForeignKeys:  snapshot.ForeignKeys,
		ScopeTables:  append([]string(nil), scopeTables...),
	})
	if err != nil {
		return catalog.PublishedSnapshot{}, r.failSchemaScan(ctx, scanRun, "SNAPSHOT_PUBLICATION_FAILED", err)
	}
	return published, nil
}

func (r *Runner) failSchemaScan(
	ctx context.Context,
	run catalog.SchemaScanRun,
	errorCode string,
	cause error,
) error {
	recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := r.catalog.FailSchemaScan(recordContext, run, errorCode); err != nil {
		return errors.Join(cause, fmt.Errorf("record failed schema scan: %w", err))
	}
	return cause
}

func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	return OpenWithPolicy(ctx, dsn, ConnectionPolicy{})
}

func OpenWithPolicy(
	ctx context.Context,
	dsn string,
	policy ConnectionPolicy,
) (*sql.DB, error) {
	config, err := validatedConfiguration(dsn, policy)
	if err != nil {
		return nil, err
	}
	config.ParseTime = true
	config.Timeout = 5 * time.Second
	config.ReadTimeout = 30 * time.Second
	config.WriteTimeout = 5 * time.Second

	connector, err := mysqldriver.NewConnector(config)
	if err != nil {
		return nil, errors.New("invalid MySQL connection configuration")
	}
	database := sql.OpenDB(connector)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(5 * time.Minute)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to MySQL source: %w", err)
	}
	return database, nil
}

func ValidateDSN(dsn string, policy ConnectionPolicy) error {
	_, err := validatedConfiguration(dsn, policy)
	return err
}

func validatedConfiguration(
	dsn string,
	policy ConnectionPolicy,
) (*mysqldriver.Config, error) {
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, ErrInvalidDSN
	}
	if policy.AllowInsecureTLS || !strings.HasPrefix(config.Net, "tcp") {
		return config, nil
	}
	switch strings.ToLower(config.TLSConfig) {
	case "", "false":
		return nil, ErrTLSRequired
	case "preferred", "skip-verify":
		return nil, ErrVerifiedTLSRequired
	default:
		return config, nil
	}
}
