package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
)

var (
	ErrBackupDestinationExists = errors.New("backup destination already exists")
	ErrInvalidBackupPath       = errors.New("invalid SQLite backup path")
)

const backupTemporaryDirectoryPrefix = ".dbgraph-backup-"

func Backup(ctx context.Context, databasePath string, outputPath string) (returnError error) {
	if err := requireFilesystemValidation(runtime.GOOS); err != nil {
		return err
	}
	sourcePath, err := canonicalDatabasePath(databasePath)
	if err != nil {
		return fmt.Errorf("resolve backup source: %w", err)
	}
	outputAbsolute, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("%w: resolve destination", ErrInvalidBackupPath)
	}
	if err := os.MkdirAll(filepath.Dir(outputAbsolute), 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	destinationDirectory, err := canonicalDatabasePath(filepath.Dir(outputAbsolute))
	if err != nil {
		return fmt.Errorf("resolve backup destination directory: %w", err)
	}
	destinationName := filepath.Base(outputAbsolute)
	if destinationName == "." || destinationName == string(filepath.Separator) {
		return ErrInvalidBackupPath
	}
	destinationPath := filepath.Join(destinationDirectory, destinationName)
	if sourcePath == destinationPath {
		return ErrInvalidBackupPath
	}
	for _, path := range []string{sourcePath, destinationPath} {
		if err := rejectNetworkFilesystem(path); err != nil {
			return err
		}
	}
	destinationRoot, err := os.OpenRoot(destinationDirectory)
	if err != nil {
		return fmt.Errorf("open backup destination directory: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, destinationRoot.Close()) }()
	if _, err := destinationRoot.Lstat(destinationName); err == nil {
		return ErrBackupDestinationExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect backup destination: %w", err)
	}
	stage, err := newBackupStage(destinationRoot)
	if err != nil {
		return err
	}
	defer func() { returnError = errors.Join(returnError, stage.Close()) }()

	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(sourcePath)}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(5000)")
	databaseURL.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return fmt.Errorf("open backup source: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer func() { returnError = errors.Join(returnError, database.Close()) }()
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to backup source: %w", err)
	}
	if _, err := database.ExecContext(ctx, "VACUUM INTO ?", stage.Path()); err != nil {
		return fmt.Errorf("create SQLite backup: %w", err)
	}
	if err := secureAndSyncBackup(stage.File()); err != nil {
		return err
	}
	if err := verifyBackup(ctx, stage.Path()); err != nil {
		return err
	}
	if err := stage.Publish(destinationName); err != nil {
		return err
	}
	if err := stage.SyncDestination(); err != nil {
		return err
	}
	return nil
}

type backupStage interface {
	Path() string
	File() *os.File
	Publish(string) error
	SyncDestination() error
	Close() error
}

func createBackupTemporaryDirectory(root *os.Root) (string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", fmt.Errorf("generate backup temporary directory: %w", err)
		}
		name := backupTemporaryDirectoryPrefix + hex.EncodeToString(random)
		if err := root.Mkdir(name, 0o700); err == nil {
			return name, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("create backup temporary directory: %w", err)
		}
	}
	return "", errors.New("create backup temporary directory: name attempts exhausted")
}

func secureAndSyncBackup(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect SQLite backup: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("SQLite backup is not a regular file")
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure SQLite backup: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("synchronize SQLite backup: %w", err)
	}
	return nil
}

func verifyBackup(ctx context.Context, path string) (returnError error) {
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	databaseURL.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return fmt.Errorf("open backup verification database: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, database.Close()) }()
	var integrity string
	if err := database.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("verify SQLite backup: %w", err)
	}
	if integrity != "ok" {
		return errors.New("SQLite backup integrity check failed")
	}
	return nil
}
