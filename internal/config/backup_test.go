package config_test

import (
	"errors"
	"testing"

	"github.com/benenen/dbgraph/internal/config"
)

func TestBackupConfigUsesFlagsBeforeEnvironment(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"DBGRAPH_DATABASE_PATH": "environment.sqlite",
		"DBGRAPH_BACKUP_PATH":   "environment-backup.sqlite",
	}
	loaded, err := config.LoadBackup([]string{
		"--database", "flag.sqlite", "--output", "flag-backup.sqlite",
	}, func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("load backup config: %v", err)
	}
	if loaded.DatabasePath != "flag.sqlite" || loaded.OutputPath != "flag-backup.sqlite" {
		t.Fatalf("backup config = %#v", loaded)
	}
}

func TestBackupConfigRequiresDistinctSourceAndOutput(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"--database", "source.sqlite"},
		{"--output", "backup.sqlite"},
		{"--database", "same.sqlite", "--output", "same.sqlite"},
		{"--database", "source.sqlite", "--output", "backup.sqlite", "extra"},
	} {
		if _, err := config.LoadBackup(arguments, nil); !errors.Is(err, config.ErrInvalidBackupConfig) {
			t.Fatalf("LoadBackup(%q) error = %v, want ErrInvalidBackupConfig", arguments, err)
		}
	}
}
