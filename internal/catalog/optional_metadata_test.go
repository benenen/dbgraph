package catalog_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/relations"
)

// A source that carries its own sealed connection string needs no environment
// variable, and an operator should not have to invent a reason for routine
// console work. Both fields are optional; the audit trail stays complete
// because a blank reason is recorded as a stated default.
func TestDataSourceAcceptsABlankEnvironmentAndReason(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fixedTime := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(6, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	admin := relations.Principal{Actor: "web", Role: relations.RoleAdmin, Origin: audit.OriginWeb}
	service := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })

	created, err := service.CreateDataSourceAsAdmin(ctx, catalog.AdminCreateDataSource{
		Name: "resource", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "", Reason: "", RequestID: "web-1", Principal: admin,
	})
	if err != nil {
		t.Fatalf("CreateDataSourceAsAdmin with blank environment and reason: %v", err)
	}
	if created.DSNEnvironment != "" {
		t.Fatalf("DSNEnvironment = %q, want it left blank", created.DSNEnvironment)
	}

	updated, err := service.UpdateDataSourceAsAdmin(ctx, catalog.AdminUpdateDataSource{
		DataSourceID: created.ID, Name: "resource-renamed",
		DSNEnvironment: "", Reason: "", RequestID: "web-2", Principal: admin,
	})
	if err != nil {
		t.Fatalf("UpdateDataSourceAsAdmin with blank environment and reason: %v", err)
	}
	if updated.Name != "resource-renamed" {
		t.Fatalf("Name = %q, want resource-renamed", updated.Name)
	}

	events, err := audit.NewService(
		dbsqlite.NewAuditRepository(store), ids, func() time.Time { return fixedTime },
	).ListEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	// Later events keep the data source itself as their audit subject.
	recorded := map[string]string{}
	for _, event := range events {
		recorded[event.Action] = event.Reason
	}
	if recorded["DATA_SOURCE_CREATED"] != "Registered from the console" {
		t.Fatalf("audit reasons = %#v, want the recorded default", recorded)
	}
}

// A non-blank environment name still has to look like one, so a typo is not
// silently stored as the variable a scan will fail to find.
func TestDataSourceStillRejectsAMalformedEnvironmentName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fixedTime := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(7, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	service := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })

	if _, err := service.CreateDataSourceAsAdmin(ctx, catalog.AdminCreateDataSource{
		Name: "resource", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "lower case", Reason: "", RequestID: "web-1",
		Principal: relations.Principal{Actor: "web", Role: relations.RoleAdmin, Origin: audit.OriginWeb},
	}); err == nil {
		t.Fatal("CreateDataSourceAsAdmin accepted a malformed environment name")
	}
}
