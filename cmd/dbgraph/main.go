package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	appauth "github.com/benenen/dbgraph/internal/auth"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/config"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/id"
	mysqlingestion "github.com/benenen/dbgraph/internal/ingestion/mysql"
	"github.com/benenen/dbgraph/internal/jobs"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/benenen/dbgraph/internal/secret"
	"github.com/benenen/dbgraph/internal/transport/httpapi"
	"github.com/benenen/dbgraph/internal/transport/mcpapi"
	"github.com/benenen/dbgraph/internal/transport/mcpproxy"
	"github.com/benenen/dbgraph/internal/transport/webapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const shutdownTimeout = 5 * time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(arguments []string, errorOutput *os.File) int {
	if len(arguments) == 0 {
		writeUsage(errorOutput)
		return 2
	}
	switch arguments[0] {
	case "serve":
		return runServe(arguments[1:], errorOutput)
	case "mcp":
		return runMCP(arguments[1:], errorOutput)
	case "backup":
		return runBackup(arguments[1:], errorOutput)
	default:
		writeUsage(errorOutput)
		return 2
	}
}

func runBackup(arguments []string, errorOutput *os.File) int {
	backupConfig, err := config.LoadBackup(arguments, os.LookupEnv)
	if err != nil {
		writeDiagnostic(errorOutput, "%v\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := dbsqlite.Backup(ctx, backupConfig.DatabasePath, backupConfig.OutputPath); err != nil {
		writeDiagnostic(errorOutput, "dbgraph backup failed: %v\n", err)
		return 1
	}
	return 0
}

func runServe(arguments []string, errorOutput *os.File) int {
	serveConfig, err := config.LoadServe(arguments, os.LookupEnv)
	if err != nil {
		writeDiagnostic(errorOutput, "%v\n", err)
		return 2
	}

	if err := serve(serveConfig); err != nil {
		writeDiagnostic(errorOutput, "dbgraph serve failed: %v\n", err)
		return 1
	}
	return 0
}

func runMCP(arguments []string, errorOutput *os.File) int {
	mcpConfig, err := config.LoadMCP(arguments, os.LookupEnv)
	if err != nil {
		writeDiagnostic(errorOutput, "%v\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpproxy.Run(ctx, mcpproxy.Config{ServerURL: mcpConfig.ServerURL, Token: mcpConfig.Token}, &mcp.StdioTransport{}); err != nil {
		writeDiagnostic(errorOutput, "dbgraph mcp failed: %v\n", err)
		return 1
	}
	return 0
}

// mysqlOpener builds the source-database dialer for schema scans. The default
// policy requires verified TLS on TCP; allowInsecure relaxes that for local
// development against a MySQL that has no certificate.
func mysqlOpener(allowInsecure bool) mysqlingestion.OpenDatabase {
	return func(ctx context.Context, dsn string) (*sql.DB, error) {
		return mysqlingestion.OpenWithPolicy(ctx, dsn, mysqlingestion.ConnectionPolicy{
			AllowInsecureTLS: allowInsecure,
		})
	}
}

// loadSealer builds the DSN sealer from the environment. The key is a
// credential, so it is never read from a flag or stored in SQLite. Its absence
// is not an error: a deployment that only uses DSN environment variables needs
// no key.
func loadSealer(lookup func(string) (string, bool)) (*secret.Sealer, error) {
	key, ok := lookup("DBGRAPH_SECRET_KEY")
	if !ok || strings.TrimSpace(key) == "" {
		return nil, nil
	}
	sealer, err := secret.NewSealer(key)
	if err != nil {
		return nil, fmt.Errorf("configure the secret key: %w", err)
	}
	return sealer, nil
}

// dsnSealer adapts the secret package to the catalog service's seam.
type dsnSealer struct {
	sealer *secret.Sealer
}

func (d dsnSealer) Seal(plaintext string) (string, []byte, error) {
	sealed, err := d.sealer.Seal(plaintext)
	if err != nil {
		return "", nil, err
	}
	return sealed.KeyID, sealed.Ciphertext, nil
}

func writeUsage(output *os.File) {
	writeDiagnostic(output, "usage: dbgraph serve --database <path> [--listen <address>]\n")
	writeDiagnostic(output, "       dbgraph mcp [--server-url <url>]\n")
	writeDiagnostic(output, "       dbgraph backup --database <path> --output <path>\n")
}

func writeDiagnostic(output *os.File, format string, arguments ...any) {
	_, _ = fmt.Fprintf(output, format, arguments...)
}

func serve(serveConfig config.ServeConfig) (returnError error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: serveConfig.DatabasePath})
	if err != nil {
		return err
	}
	defer func() { returnError = errors.Join(returnError, store.Close()) }()
	allocator, err := id.NewPersistentAllocator(1, time.Now, store, 1024)
	if err != nil {
		return fmt.Errorf("configure persistent IDs: %w", err)
	}
	sealer, err := loadSealer(os.LookupEnv)
	if err != nil {
		return err
	}
	// Catch a connection string the scanner could never parse while the
	// operator is still looking at the field. The TLS policy stays with the
	// connection, so a DSN can be stored before the server that will use it
	// is started with the right flags.
	catalogOptions := []catalog.ServiceOption{
		catalog.WithDSNValidator(mysqlingestion.ValidateDSNShape),
	}
	runnerOptions := []mysqlingestion.RunnerOption{}
	if sealer != nil {
		catalogOptions = append(catalogOptions, catalog.WithDSNSealer(dsnSealer{sealer: sealer}))
		runnerOptions = append(runnerOptions, mysqlingestion.WithSecretOpener(
			func(keyID string, ciphertext []byte) (string, error) {
				return sealer.Open(secret.Sealed{KeyID: keyID, Ciphertext: ciphertext})
			},
		))
	}
	catalogService := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, allocator), allocator, time.Now, catalogOptions...,
	)
	codeRepositoryService := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), allocator, time.Now)
	relationCommands := relations.NewCommands(dbsqlite.NewRelationRepository(store), allocator, time.Now)
	graphService := graph.NewService(dbsqlite.NewGraphRepository(store))
	reconcileService := reconcile.NewService(
		dbsqlite.NewReconcileRepository(store), relationCommands, allocator, time.Now,
	)
	jobRepository := dbsqlite.NewJobRepository(store)
	if serveConfig.AllowInsecureMySQLTLS {
		writeDiagnostic(os.Stderr,
			"warning: --insecure-mysql-tls is enabled; schema scans may reach MySQL without verified TLS\n")
	}
	schemaRunner := mysqlingestion.NewRunner(
		catalogService, mysqlingestion.NewScanner(),
		mysqlOpener(serveConfig.AllowInsecureMySQLTLS), os.LookupEnv, runnerOptions...,
	)
	schemaScans := jobs.NewSchemaScanCoordinator(jobRepository, catalogService, schemaRunner, allocator, time.Now)
	auditService := audit.NewService(dbsqlite.NewAuditRepository(store), allocator, time.Now)
	// Access tokens are seeded from the environment once and then live in
	// SQLite as digests, so a running server needs no token in its environment.
	credentialRepository := dbsqlite.NewCredentialRepository(store, time.Now)
	seedCredentials, err := appauth.EnvironmentCredentials(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("read access tokens from the environment: %w", err)
	}
	if err := credentialRepository.SyncCredentials(ctx, seedCredentials); err != nil {
		return fmt.Errorf("store access tokens: %w", err)
	}
	removed, err := credentialRepository.PruneUnknownActors(ctx, appauth.KnownActors())
	if err != nil {
		return fmt.Errorf("prune access tokens: %w", err)
	}
	if removed > 0 {
		writeDiagnostic(os.Stderr,
			"removed %d access token(s) issued under an earlier scheme\n", removed)
	}
	storedCredentials, err := credentialRepository.ListCredentials(ctx)
	if err != nil {
		return fmt.Errorf("load access tokens: %w", err)
	}
	authenticator, err := appauth.NewTokenAuthenticatorFromStored(
		appauth.Filter(storedCredentials, audit.OriginAgent),
	)
	if err != nil {
		return fmt.Errorf("configure MCP authentication: %w", err)
	}
	if !loopbackListenAddress(serveConfig.ListenAddress) && !authenticator.HasCredentials() {
		return errors.New("non-loopback listeners require MCP credentials")
	}
	mcpHandler := mcpapi.NewHTTPHandler(mcpapi.Services{
		Status: store, Catalog: catalogService, Relations: relationCommands,
		Graph: graphService, Reconcile: reconcileService, Jobs: schemaScans,
	}, authenticator)
	webAuthenticator, err := appauth.NewTokenAuthenticatorFromStored(
		appauth.Filter(storedCredentials, audit.OriginWeb),
	)
	if err != nil {
		return fmt.Errorf("configure Web authentication: %w", err)
	}
	if webAuthenticator.HasCredentials() && serveConfig.TLSCertFile == "" && !serveConfig.AllowCleartextWeb {
		return errors.New("web credentials require TLS (--tls-cert and --tls-key) or --insecure-cleartext-web")
	}
	var webOptions []webapi.Option
	if serveConfig.AllowCleartextWeb {
		webOptions = append(webOptions, webapi.WithCleartextCookies())
		writeDiagnostic(os.Stderr,
			"warning: --insecure-cleartext-web is enabled; Web sessions and tokens are sent in the clear on %s\n",
			serveConfig.ListenAddress)
	}
	webHandler := webapi.NewHandler(webapi.Services{
		CodeRepositories: codeRepositoryService,
		Catalog:          catalogService, Relations: relationCommands, Graph: graphService,
		Reconcile: reconcileService, Jobs: schemaScans, Audit: auditService,
	}, appauth.NewSessionManager(webAuthenticator, time.Now, nil), webOptions...)

	consoleHandler, err := webapi.NewConsoleHandler()
	if err != nil {
		return fmt.Errorf("load the console: %w", err)
	}

	listener, err := net.Listen("tcp", serveConfig.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on HTTP address: %w", err)
	}

	server := &http.Server{
		Handler:           httpapi.NewHandler(store, mcpHandler, webHandler, consoleHandler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveHTTP := func() error {
		if serveConfig.TLSCertFile != "" {
			return server.ServeTLS(listener, serveConfig.TLSCertFile, serveConfig.TLSKeyFile)
		}
		return server.Serve(listener)
	}
	return runServeLifecycle(ctx, serveHTTP, server.Shutdown, server.Close, schemaScans.Run)
}

func loopbackListenAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

func runServeLifecycle(
	parent context.Context,
	serveHTTP func() error,
	shutdownHTTP func(context.Context) error,
	forceCloseHTTP func() error,
	runWorker func(context.Context) error,
) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	serveResult := make(chan error, 1)
	workerResult := make(chan error, 1)
	go func() { serveResult <- serveHTTP() }()
	go func() { workerResult <- runWorker(ctx) }()

	var lifecycleError error
	serveDone := false
	workerDone := false
	select {
	case err := <-serveResult:
		serveDone = true
		if parent.Err() == nil && err != nil && !errors.Is(err, http.ErrServerClosed) {
			lifecycleError = err
		}
	case err := <-workerResult:
		workerDone = true
		if parent.Err() == nil {
			if err == nil {
				lifecycleError = errors.New("schema scan worker stopped unexpectedly")
			} else {
				lifecycleError = fmt.Errorf("schema scan worker stopped: %w", err)
			}
		}
	case <-parent.Done():
	}

	cancel()
	shutdownContext, stopShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	shutdownError := shutdownHTTP(shutdownContext)
	stopShutdown()
	if shutdownError != nil {
		shutdownError = fmt.Errorf("shut down HTTP server: %w", shutdownError)
		if closeError := forceCloseHTTP(); closeError != nil {
			shutdownError = errors.Join(shutdownError, fmt.Errorf("force close HTTP server: %w", closeError))
		}
	}
	joinTimer := time.NewTimer(shutdownTimeout)
	defer joinTimer.Stop()
	if !serveDone {
		select {
		case err := <-serveResult:
			if lifecycleError == nil && parent.Err() == nil && err != nil && !errors.Is(err, http.ErrServerClosed) {
				lifecycleError = err
			}
		case <-joinTimer.C:
			closeError := forceCloseHTTP()
			return errors.Join(lifecycleError, shutdownError, errors.New("timed out waiting for HTTP server to stop"), closeError)
		}
	}
	if !workerDone {
		select {
		case err := <-workerResult:
			if lifecycleError == nil && parent.Err() == nil && err != nil && !errors.Is(err, context.Canceled) {
				lifecycleError = fmt.Errorf("stop schema scan worker: %w", err)
			}
		case <-joinTimer.C:
			return errors.Join(lifecycleError, shutdownError, errors.New("timed out waiting for schema scan worker to stop"))
		}
	}
	return errors.Join(lifecycleError, shutdownError)
}
