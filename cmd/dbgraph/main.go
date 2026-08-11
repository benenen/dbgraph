package main

import (
	"context"
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
	catalogService := catalog.NewService(dbsqlite.NewCatalogRepository(store, allocator), allocator, time.Now)
	projectService := catalog.NewProjectService(dbsqlite.NewProjectRepository(store), allocator, time.Now)
	codeRepositoryService := catalog.NewCodeRepositoryService(dbsqlite.NewCodeRepository(store), allocator, time.Now)
	relationCommands := relations.NewCommands(dbsqlite.NewRelationRepository(store), allocator, time.Now)
	graphService := graph.NewService(dbsqlite.NewGraphRepository(store))
	reconcileService := reconcile.NewService(
		dbsqlite.NewReconcileRepository(store), relationCommands, allocator, time.Now,
	)
	jobRepository := dbsqlite.NewJobRepository(store)
	schemaRunner := mysqlingestion.NewRunner(catalogService, mysqlingestion.NewScanner(), nil, os.LookupEnv)
	schemaScans := jobs.NewSchemaScanCoordinator(jobRepository, catalogService, schemaRunner, allocator, time.Now)
	auditService := audit.NewService(dbsqlite.NewAuditRepository(store), allocator, time.Now)
	authenticator, err := appauth.LoadMCPAuthenticator(os.LookupEnv)
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
	webAuthenticator, err := appauth.LoadWebAuthenticator(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("configure Web authentication: %w", err)
	}
	if webAuthenticator.HasCredentials() && serveConfig.TLSCertFile == "" {
		return errors.New("web credentials require TLS (--tls-cert and --tls-key)")
	}
	webHandler := webapi.NewHandler(webapi.Services{
		Projects: projectService, CodeRepositories: codeRepositoryService,
		Catalog: catalogService, Relations: relationCommands, Graph: graphService,
		Reconcile: reconcileService, Jobs: schemaScans, Audit: auditService,
	}, appauth.NewSessionManager(webAuthenticator, time.Now, nil))

	listener, err := net.Listen("tcp", serveConfig.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on HTTP address: %w", err)
	}

	server := &http.Server{
		Handler:           httpapi.NewHandler(store, mcpHandler, webHandler),
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
