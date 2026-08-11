package sqlite_test

import (
	"errors"
	"testing"

	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestCheckRuntimeVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		version   string
		supported bool
	}{
		{name: "previous patch", version: "3.51.2", supported: false},
		{name: "minimum", version: "3.51.3", supported: true},
		{name: "newer patch", version: "3.51.4", supported: true},
		{name: "newer minor", version: "3.52.0", supported: true},
		{name: "newer major", version: "4.0.0", supported: true},
		{name: "invalid", version: "development", supported: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := dbsqlite.CheckRuntimeVersion(test.version)
			if test.supported && err != nil {
				t.Fatalf("CheckRuntimeVersion(%q): %v", test.version, err)
			}
			if !test.supported && !errors.Is(err, dbsqlite.ErrUnsupportedRuntime) {
				t.Fatalf("CheckRuntimeVersion(%q) error = %v, want ErrUnsupportedRuntime", test.version, err)
			}
		})
	}
}
