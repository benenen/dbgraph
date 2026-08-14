package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	appstatus "github.com/benenen/dbgraph/internal/status"
	"github.com/benenen/dbgraph/migrations"
	"github.com/gofrs/flock"
	_ "modernc.org/sqlite"
)

const migrationTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
`

var ErrWriterAlreadyActive = errors.New("SQLite writer is already active")

const (
	defaultWriteQueueCapacity = 64
	maximumWriteQueueCapacity = 4096
	readerConnectionPoolSize  = 8
)

type Config struct {
	Path               string
	WriteQueueCapacity int
}

type Status = appstatus.Snapshot

type Store struct {
	db         *sql.DB
	writerLock *flock.Flock
	writeMu    sync.Mutex
	writeQueue chan writeRequest
	writeSlots chan struct{}
	writeStop  chan struct{}
	writeDone  chan struct{}
	closed     bool
	closeOnce  sync.Once
	closeErr   error
}

func Open(ctx context.Context, config Config) (*Store, error) {
	if err := requireFilesystemValidation(runtime.GOOS); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Path) == "" {
		return nil, errors.New("SQLite path is required")
	}
	writeQueueCapacity := config.WriteQueueCapacity
	if writeQueueCapacity == 0 {
		writeQueueCapacity = defaultWriteQueueCapacity
	}
	if writeQueueCapacity < 1 || writeQueueCapacity > maximumWriteQueueCapacity {
		return nil, fmt.Errorf("SQLite write queue capacity must be between 1 and %d", maximumWriteQueueCapacity)
	}
	canonicalPath, err := canonicalDatabasePath(config.Path)
	if err != nil {
		return nil, err
	}
	if err := rejectNetworkFilesystem(canonicalPath); err != nil {
		return nil, err
	}
	writerLock := flock.New(canonicalPath + ".writer.lock")
	locked, err := writerLock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquire SQLite writer ownership: %w", err)
	}
	if !locked {
		return nil, ErrWriterAlreadyActive
	}
	if err := os.Chmod(canonicalPath+".writer.lock", 0o600); err != nil {
		_ = writerLock.Unlock()
		return nil, fmt.Errorf("secure SQLite writer lock: %w", err)
	}

	databaseURL := url.URL{
		Scheme: "file",
		Path:   filepath.ToSlash(canonicalPath),
	}
	query := databaseURL.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	databaseURL.RawQuery = query.Encode()

	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		_ = writerLock.Unlock()
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(readerConnectionPoolSize)
	db.SetMaxIdleConns(readerConnectionPoolSize)

	store := &Store{db: db, writerLock: writerLock}
	if err := db.PingContext(ctx); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("connect to SQLite database: %w", err)
	}
	if err := rejectDatabasePathReplacement(canonicalPath); err != nil {
		_ = store.Close()
		return nil, err
	}
	var runtimeVersion string
	if err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&runtimeVersion); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("read SQLite version: %w", err)
	}
	if err := CheckRuntimeVersion(runtimeVersion); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := backfillRepositoryIdentities(ctx, db); err != nil {
		_ = store.Close()
		return nil, err
	}
	store.startWriteWorker(writeQueueCapacity)
	if err := secureSQLiteArtifacts(canonicalPath); err != nil {
		_ = store.Close()
		return nil, err
	}

	return store, nil
}

func secureSQLiteArtifacts(databasePath string) error {
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm", databasePath + ".writer.lock"} {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect SQLite artifact: %w", err)
		}
		if !info.Mode().IsRegular() {
			return errors.New("SQLite artifact is not a regular file")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("secure SQLite artifact permissions: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.stopWriteWorker()
		s.closeErr = errors.Join(s.db.Close(), s.writerLock.Unlock())
	})
	return s.closeErr
}

func canonicalDatabasePath(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite path: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err == nil {
		return resolvedPath, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("resolve SQLite path: %w", err)
	}
	fileInfo, statErr := os.Lstat(absolutePath)
	if statErr == nil && fileInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("resolve SQLite path: broken symbolic link")
	}
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect SQLite path: %w", statErr)
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(absolutePath))
	if err != nil {
		return "", fmt.Errorf("resolve SQLite directory: %w", err)
	}
	return filepath.Join(resolvedParent, filepath.Base(absolutePath)), nil
}

func rejectDatabasePathReplacement(path string) error {
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect opened SQLite path: %w", err)
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("SQLite path changed to a symbolic link while opening")
	}
	return nil
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	var status Status
	var foreignKeys int

	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&status.SchemaVersion); err != nil {
		return Status{}, fmt.Errorf("read schema version: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&status.SQLiteVersion); err != nil {
		return Status{}, fmt.Errorf("read SQLite version: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&status.JournalMode); err != nil {
		return Status{}, fmt.Errorf("read journal mode: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return Status{}, fmt.Errorf("read foreign key mode: %w", err)
	}
	status.ForeignKeysEnabled = foreignKeys == 1

	return status, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, migrationTable); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if err := s.applyMigration(ctx, entry.Name()); err != nil {
			return err
		}
	}

	return nil
}

// noTransactionMarker opts a migration out of the surrounding transaction.
const noTransactionMarker = "-- dbgraph:no-transaction"

// applyMigrationOutsideTransaction runs SQLite's documented table-rebuild
// procedure: foreign keys off, the script's own transaction, then
// foreign_key_check before the version is recorded. A failed check leaves the
// version unrecorded so the migration is retried rather than silently skipped.
func (s *Store) applyMigrationOutsideTransaction(
	ctx context.Context,
	name string,
	version int,
	script []byte,
) (returnError error) {
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign keys for migration %q: %w", name, err)
	}
	defer func() {
		if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
			returnError = errors.Join(returnError, fmt.Errorf("restore foreign keys: %w", err))
		}
	}()

	if _, err := s.db.ExecContext(ctx, string(script)); err != nil {
		return fmt.Errorf("execute migration %q: %w", name, err)
	}

	violations, err := s.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("check foreign keys after migration %q: %w", name, err)
	}
	broken := violations.Next()
	if err := errors.Join(violations.Err(), violations.Close()); err != nil {
		return fmt.Errorf("check foreign keys after migration %q: %w", name, err)
	}
	if broken {
		return fmt.Errorf("migration %q left dangling foreign key references", name)
	}

	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO schema_migrations(version) VALUES (?)", version); err != nil {
		return fmt.Errorf("record migration %q: %w", name, err)
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, name string) error {
	versionText, _, ok := strings.Cut(name, "_")
	if !ok {
		return fmt.Errorf("invalid migration filename %q", name)
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version <= 0 {
		return fmt.Errorf("invalid migration version in %q", name)
	}

	var applied int
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)", version).Scan(&applied); err != nil {
		return fmt.Errorf("check migration %q: %w", name, err)
	}
	if applied == 1 {
		return nil
	}

	script, err := migrations.Files.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read migration %q: %w", name, err)
	}
	// SQLite ignores PRAGMA foreign_keys inside a transaction, so a migration
	// that rebuilds a referenced table has to run outside one. Such a migration
	// says so, and integrity is checked before the version is recorded.
	if bytes.Contains(script, []byte(noTransactionMarker)) {
		return s.applyMigrationOutsideTransaction(ctx, name, version, script)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %q: %w", name, err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, string(script)); err != nil {
		return fmt.Errorf("execute migration %q: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES (?)", version); err != nil {
		return fmt.Errorf("record migration %q: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %q: %w", name, err)
	}

	return nil
}
