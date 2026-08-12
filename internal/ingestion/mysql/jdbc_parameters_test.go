package mysql_test

import (
	"errors"
	"strings"
	"testing"

	mysqlingestion "github.com/benenen/dbgraph/internal/ingestion/mysql"
)

// A connection string pasted from a Spring configuration parses cleanly and
// then fails at connect, because the driver forwards what it does not
// recognise to the server as SET. Catching it at save turns a scan that fails
// minutes later into a message naming the parameter to change.
func TestValidateDSNNamesJDBCOnlyParameters(t *testing.T) {
	t.Parallel()

	const pasted = "root:secret@tcp(10.0.0.1:3306)/resource" +
		"?useUnicode=true&characterEncoding=utf-8&useSSL=false&allowMultiQueries=true"

	err := mysqlingestion.ValidateDSN(pasted, mysqlingestion.ConnectionPolicy{AllowInsecureTLS: true})
	if !errors.Is(err, mysqlingestion.ErrJDBCParameters) {
		t.Fatalf("ValidateDSN = %v, want ErrJDBCParameters", err)
	}
	for _, named := range []string{"useSSL (use tls)", "characterEncoding (use charset)",
		"allowMultiQueries (use multiStatements)", "useUnicode (drop it)"} {
		if !strings.Contains(err.Error(), named) {
			t.Fatalf("error %q does not name %q", err, named)
		}
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error %q leaks the credential", err)
	}
}

// The equivalent Go DSN passes, and so does a genuine MySQL system variable:
// forwarding those is the documented purpose of the parameter mechanism.
func TestValidateDSNAcceptsDriverParameters(t *testing.T) {
	t.Parallel()

	for _, dsn := range []string{
		"root:secret@tcp(10.0.0.1:3306)/resource?charset=utf8mb4&multiStatements=true",
		"root:secret@tcp(10.0.0.1:3306)/resource?charset=utf8mb4&sql_mode=%27ANSI_QUOTES%27",
	} {
		if err := mysqlingestion.ValidateDSN(dsn, mysqlingestion.ConnectionPolicy{AllowInsecureTLS: true}); err != nil {
			t.Fatalf("ValidateDSN(%q) = %v, want nil", dsn, err)
		}
	}
}
