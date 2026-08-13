package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
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
	GetProjectDataSource(context.Context, int64) (catalog.DataSource, error)
	BeginSchemaScan(context.Context, int64) (catalog.SchemaScanRun, error)
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

// SecretOpener decrypts a sealed DSN. The runner never holds the key itself.
type SecretOpener func(keyID string, ciphertext []byte) (string, error)

// RunnerOption adjusts an optional runner dependency.
type RunnerOption func(*Runner)

// WithSecretOpener lets the runner use a DSN stored with the data source.
// Without it, only the environment-variable path resolves.
func WithSecretOpener(open SecretOpener) RunnerOption {
	return func(r *Runner) { r.openSecret = open }
}

type Runner struct {
	catalog           catalogService
	scanner           schemaScanner
	openDatabase      OpenDatabase
	lookupEnvironment EnvironmentLookup
	openSecret        SecretOpener
}

func NewRunner(
	catalog catalogService,
	scanner schemaScanner,
	openDatabase OpenDatabase,
	lookupEnvironment EnvironmentLookup,
	options ...RunnerOption,
) *Runner {
	if openDatabase == nil {
		openDatabase = Open
	}
	runner := &Runner{
		catalog:           catalog,
		scanner:           scanner,
		openDatabase:      openDatabase,
		lookupEnvironment: lookupEnvironment,
	}
	for _, option := range options {
		option(runner)
	}
	return runner
}

// resolveDSN prefers a DSN stored with the data source and falls back to the
// environment variable it names. A stored DSN that cannot be opened is an
// error rather than a silent fallback: the environment may hold a stale
// credential, and quietly connecting with the wrong one hides a key problem.
func (r *Runner) resolveDSN(dataSource catalog.DataSource) (string, error) {
	if len(dataSource.DSNCiphertext) > 0 {
		if r.openSecret == nil {
			return "", ErrMissingDSN
		}
		dsn, err := r.openSecret(dataSource.DSNKeyID, dataSource.DSNCiphertext)
		if err != nil {
			return "", fmt.Errorf("%w: stored DSN could not be opened", ErrMissingDSN)
		}
		if strings.TrimSpace(dsn) == "" {
			return "", ErrMissingDSN
		}
		return dsn, nil
	}
	if r.lookupEnvironment == nil {
		return "", ErrMissingDSN
	}
	dsn, ok := r.lookupEnvironment(dataSource.DSNEnvironment)
	if !ok || strings.TrimSpace(dsn) == "" {
		return "", ErrMissingDSN
	}
	return dsn, nil
}

func (r *Runner) Run(
	ctx context.Context,
	dataSourceID int64,
) (catalog.PublishedSnapshot, error) {
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
	dataSource, err := r.catalog.GetProjectDataSource(ctx, dataSourceID)
	if err != nil {
		return catalog.PublishedSnapshot{}, err
	}
	if dataSource.Kind != catalog.DataSourceMySQL {
		return catalog.PublishedSnapshot{}, ErrUnsupportedSource
	}
	dsn, err := r.resolveDSN(dataSource)
	if err != nil {
		return catalog.PublishedSnapshot{}, err
	}
	dsnConfig, err := mysqldriver.ParseDSN(dsn)
	if err != nil || strings.TrimSpace(dsnConfig.DBName) == "" {
		return catalog.PublishedSnapshot{}, ErrInvalidDSN
	}
	scanRun, err := r.catalog.BeginSchemaScan(ctx, dataSource.ID)
	if err != nil {
		return catalog.PublishedSnapshot{}, err
	}

	database, err := r.openDatabase(ctx, dsn)
	if err != nil {
		// A refused connection and a refused policy need different fixes: one
		// is the database, the other is how this server was started.
		code := "SOURCE_CONNECTION_FAILED"
		if errors.Is(err, ErrTLSRequired) || errors.Is(err, ErrVerifiedTLSRequired) {
			code = "SOURCE_TLS_REQUIRED"
		}
		return catalog.PublishedSnapshot{}, r.failSchemaScan(ctx, scanRun, code, err)
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

// ScanFailure carries the reason a scan stopped out to the job that ran it,
// so the operator reads why it failed rather than that it failed.
type ScanFailure struct {
	Code  string
	Cause error
}

func (f ScanFailure) Error() string { return f.Code + ": " + f.Cause.Error() }

func (f ScanFailure) Unwrap() error { return f.Cause }

// FailureCode satisfies the contract the job coordinator reads, without the
// coordinator having to know this package exists.
func (f ScanFailure) FailureCode() string { return f.Code }

// ScanFailureCode reports the specific reason a scan stopped, or an empty
// string when the error did not come from a scan.
func ScanFailureCode(err error) string {
	var failure ScanFailure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
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
	return ScanFailure{Code: errorCode, Cause: cause}
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

// jdbcOnlyParameters are JDBC connection properties with no meaning to this
// driver. It forwards parameters it does not recognise to the server as SET
// statements, so a connection string pasted from a Spring configuration fails
// at connect with "Unknown system variable" rather than at save. None of these
// is a real MySQL system variable, so rejecting them cannot shadow a legitimate
// SET; the value is the equivalent this driver understands, empty where none is
// needed.
var jdbcOnlyParameters = map[string]string{
	"useSSL":                   "tls",
	"characterEncoding":        "charset",
	"allowMultiQueries":        "multiStatements",
	"serverTimezone":           "loc",
	"connectTimeout":           "timeout",
	"socketTimeout":            "readTimeout",
	"useUnicode":               "",
	"autoReconnect":            "",
	"zeroDateTimeBehavior":     "",
	"rewriteBatchedStatements": "",
	"useServerPrepStmts":       "",
	"allowPublicKeyRetrieval":  "",
}

// ErrJDBCParameters reports a JDBC connection string that was pasted unchanged.
var ErrJDBCParameters = errors.New("connection string uses JDBC parameters")

// UnsupportedParameters names the JDBC-only parameters in a DSN, with the
// equivalent this driver understands where one exists. It returns nothing for a
// DSN this driver can use.
func UnsupportedParameters(dsn string) []string {
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil
	}
	unsupported := make([]string, 0, len(config.Params))
	for name := range config.Params {
		equivalent, jdbc := jdbcOnlyParameters[name]
		if !jdbc {
			continue
		}
		if equivalent == "" {
			unsupported = append(unsupported, name+" (drop it)")
			continue
		}
		unsupported = append(unsupported, name+" (use "+equivalent+")")
	}
	sort.Strings(unsupported)
	return unsupported
}

func ValidateDSN(dsn string, policy ConnectionPolicy) error {
	if _, err := validatedConfiguration(dsn, policy); err != nil {
		return err
	}
	return ValidateDSNShape(dsn)
}

// ValidateDSNShape checks what is wrong with the connection string itself,
// leaving the TLS policy to the connection that will use it. A DSN is stored
// once and connected with under whatever policy the server runs; refusing to
// save one because this process happens to require TLS would conflate the two.
func ValidateDSNShape(dsn string) error {
	if _, err := mysqldriver.ParseDSN(dsn); err != nil {
		return ErrInvalidDSN
	}
	if unsupported := UnsupportedParameters(dsn); len(unsupported) > 0 {
		return fmt.Errorf("%w: %s", ErrJDBCParameters, strings.Join(unsupported, ", "))
	}
	return nil
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
