package catalog_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/relations"
)

type recordingDSNSealer struct {
	plaintext string
	err       error
}

func (s *recordingDSNSealer) Seal(plaintext string) (string, []byte, error) {
	s.plaintext = plaintext
	if s.err != nil {
		return "", nil, s.err
	}
	return "test-key", []byte("sealed-test-value"), nil
}

func TestDataSourceSealsAndValidatesAStoredDSN(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	sealer := &recordingDSNSealer{}
	validated := ""
	service := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime },
		catalog.WithDSNValidator(func(dsn string) error {
			validated = dsn
			return nil
		}),
		catalog.WithDSNSealer(sealer),
	)

	created, err := service.CreateDataSource(ctx, catalog.CreateDataSource{
		Name: "stored", Kind: catalog.DataSourceMySQL, DSN: "  mysql://example.invalid/test  ",
	})
	if err != nil {
		t.Fatalf("CreateDataSource: %v", err)
	}
	if validated != "mysql://example.invalid/test" || sealer.plaintext != validated {
		t.Fatalf("validated %q and sealed %q", validated, sealer.plaintext)
	}
	if created.DSNKeyID != "test-key" || string(created.DSNCiphertext) != "sealed-test-value" {
		t.Fatalf("sealed fields = key:%q ciphertext:%q", created.DSNKeyID, created.DSNCiphertext)
	}

	updated, err := service.UpdateDataSourceAsAdmin(ctx, catalog.AdminUpdateDataSource{
		DataSourceID: created.ID, Name: "stored-renamed", DSN: "mysql://example.invalid/rotated",
		Principal: relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb},
		Reason:    "Rotate test credential", RequestID: "rotate-1",
	})
	if err != nil {
		t.Fatalf("UpdateDataSourceAsAdmin: %v", err)
	}
	if updated.Name != "stored-renamed" || validated != "mysql://example.invalid/rotated" || sealer.plaintext != validated {
		t.Fatalf("updated source = %#v, validated=%q sealed=%q", updated, validated, sealer.plaintext)
	}
	events, err := dbsqlite.NewAuditRepository(store).ListAuditEvents(ctx, 10)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 || events[0].Action != "DATA_SOURCE_UPDATED" ||
		strings.Contains(string(events[0].Details), "mysql://") {
		t.Fatalf("audit event = %#v", events)
	}
}

func TestDataSourceRejectsAnUnusableOrUnsealableDSN(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, ids, fixedTime := openCatalogBoundaryStore(t, ctx)
	repository := dbsqlite.NewCatalogRepository(store, ids)
	command := catalog.CreateDataSource{Name: "stored", Kind: catalog.DataSourceMySQL, DSN: "mysql://example.invalid/test"}

	service := catalog.NewService(repository, ids, func() time.Time { return fixedTime })
	if _, err := service.CreateDataSource(ctx, command); !errors.Is(err, catalog.ErrSealerUnavailable) {
		t.Fatalf("missing sealer error = %v", err)
	}

	validatorError := errors.New("driver rejected the DSN")
	service = catalog.NewService(
		repository, ids, func() time.Time { return fixedTime },
		catalog.WithDSNValidator(func(string) error { return validatorError }),
		catalog.WithDSNSealer(&recordingDSNSealer{}),
	)
	if _, err := service.CreateDataSource(ctx, command); !errors.Is(err, catalog.ErrUnusableDSN) {
		t.Fatalf("validator error = %v", err)
	}

	service = catalog.NewService(repository, ids, func() time.Time { return fixedTime }, catalog.WithDSNSealer(&recordingDSNSealer{}))
	command.DSN = strings.Repeat("x", catalog.MaximumDSNLength+1)
	if _, err := service.CreateDataSource(ctx, command); !errors.Is(err, catalog.ErrInvalidDataSource) {
		t.Fatalf("long DSN error = %v", err)
	}

	sealError := errors.New("test sealer failed")
	service = catalog.NewService(repository, ids, func() time.Time { return fixedTime }, catalog.WithDSNSealer(&recordingDSNSealer{err: sealError}))
	command.DSN = "mysql://example.invalid/test"
	if _, err := service.CreateDataSource(ctx, command); !errors.Is(err, sealError) {
		t.Fatalf("sealer error = %v", err)
	}
}
