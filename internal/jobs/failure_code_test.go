package jobs_test

import (
	"errors"
	"testing"

	mysqlingestion "github.com/benenen/dbgraph/internal/ingestion/mysql"
)

// A scan that stops for a nameable reason must report that reason. Flattening
// every failure to SCHEMA_SCAN_FAILED tells an operator only that something
// went wrong, which is the one thing they already know.
func TestSchemaScanFailureCarriesItsReason(t *testing.T) {
	t.Parallel()

	failure := mysqlingestion.ScanFailure{
		Code:  "SOURCE_TLS_REQUIRED",
		Cause: mysqlingestion.ErrTLSRequired,
	}
	if code := mysqlingestion.ScanFailureCode(failure); code != "SOURCE_TLS_REQUIRED" {
		t.Fatalf("ScanFailureCode = %q, want SOURCE_TLS_REQUIRED", code)
	}
	if !errors.Is(failure, mysqlingestion.ErrTLSRequired) {
		t.Fatal("the failure no longer unwraps to its cause")
	}
	if code := mysqlingestion.ScanFailureCode(errors.New("unrelated")); code != "" {
		t.Fatalf("ScanFailureCode of an unrelated error = %q, want empty", code)
	}
}
