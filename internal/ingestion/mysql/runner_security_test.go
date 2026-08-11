package mysql_test

import (
	"errors"
	"testing"

	"github.com/benenen/dbgraph/internal/ingestion/mysql"
)

func TestValidateDSNRequiresVerifiedTLSForTCP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		dsn           string
		allowInsecure bool
		wantError     error
	}{
		{
			name:      "TCP without TLS",
			dsn:       "readonly:secret@tcp(mysql.example.test:3306)/catalog",
			wantError: mysql.ErrTLSRequired,
		},
		{
			name: "TCP with verified TLS",
			dsn:  "readonly:secret@tcp(mysql.example.test:3306)/catalog?tls=true",
		},
		{
			name:      "TCP preferred TLS can downgrade",
			dsn:       "readonly:secret@tcp(mysql.example.test:3306)/catalog?tls=preferred",
			wantError: mysql.ErrVerifiedTLSRequired,
		},
		{
			name:      "TCP skip verify is rejected",
			dsn:       "readonly:secret@tcp(mysql.example.test:3306)/catalog?tls=skip-verify",
			wantError: mysql.ErrVerifiedTLSRequired,
		},
		{
			name: "Unix socket does not require TLS",
			dsn:  "readonly:secret@unix(/var/run/mysqld/mysqld.sock)/catalog",
		},
		{
			name:          "explicit development policy permits insecure TCP",
			dsn:           "readonly:secret@tcp(127.0.0.1:3306)/catalog?tls=skip-verify",
			allowInsecure: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := mysql.ValidateDSN(test.dsn, mysql.ConnectionPolicy{
				AllowInsecureTLS: test.allowInsecure,
			})
			if test.wantError == nil && err != nil {
				t.Fatalf("ValidateDSN: %v", err)
			}
			if test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("ValidateDSN error = %v, want %v", err, test.wantError)
			}
		})
	}
}
