package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

var ErrInvalidBackupConfig = errors.New("invalid backup configuration")

type BackupConfig struct {
	DatabasePath string
	OutputPath   string
}

func LoadBackup(arguments []string, lookupEnvironment EnvironmentLookup) (BackupConfig, error) {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databasePath := flags.String(
		"database",
		environmentValue(lookupEnvironment, "DBGRAPH_DATABASE_PATH", ""),
		"source SQLite database path",
	)
	outputPath := flags.String(
		"output",
		environmentValue(lookupEnvironment, "DBGRAPH_BACKUP_PATH", ""),
		"new backup path",
	)
	if err := flags.Parse(arguments); err != nil {
		return BackupConfig{}, fmt.Errorf("%w: %v", ErrInvalidBackupConfig, err)
	}
	if flags.NArg() != 0 {
		return BackupConfig{}, fmt.Errorf("%w: unexpected positional arguments", ErrInvalidBackupConfig)
	}
	config := BackupConfig{
		DatabasePath: strings.TrimSpace(*databasePath),
		OutputPath:   strings.TrimSpace(*outputPath),
	}
	if config.DatabasePath == "" || config.OutputPath == "" {
		return BackupConfig{}, fmt.Errorf("%w: database and output paths are required", ErrInvalidBackupConfig)
	}
	sourceAbsolute, sourceError := filepath.Abs(config.DatabasePath)
	outputAbsolute, outputError := filepath.Abs(config.OutputPath)
	if sourceError != nil || outputError != nil || filepath.Clean(sourceAbsolute) == filepath.Clean(outputAbsolute) {
		return BackupConfig{}, fmt.Errorf("%w: database and output paths must be different", ErrInvalidBackupConfig)
	}
	return config, nil
}
