package webapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/relations"
)

// TestCreateDataSourceAcceptsADSNAndNeverReturnsIt is the contract the whole
// feature rests on: the secret goes in and never comes back out.
func TestCreateDataSourceAcceptsADSNAndNeverReturnsIt(t *testing.T) {
	const dsn = "root:TotallySecretPassword123@tcp(127.0.0.1:3306)/orders?charset=utf8mb4"

	stub := &catalogHTTPStub{createResult: catalog.DataSource{
		ID: 30, Name: "orders-primary", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "ORDERS_DSN", DSNKeyID: "abcd1234", DSNCiphertext: []byte("sealed"),
	}}
	client := newWebTestClient(t, Services{Catalog: stub}, relations.RoleAdmin)

	body := `{"name":"orders-primary","kind":"MYSQL","dsnEnvironment":"ORDERS_DSN",` +
		`"dsn":"` + dsn + `","reason":"bootstrap the orders catalog"}`
	response := client.request(http.MethodPost, "/api/v1/projects/10/data-sources", body, true)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}

	if stub.createCommand.DSN != dsn {
		t.Fatalf("service received DSN %q, want the submitted one", stub.createCommand.DSN)
	}
	rendered := response.Body.String()
	for _, secret := range []string{dsn, "TotallySecretPassword123", "sealed", "abcd1234"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("create response leaked %q: %s", secret, rendered)
		}
	}

	listStub := &catalogHTTPStub{sources: []catalog.DataSource{{
		ID: 30, Name: "orders-primary", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "ORDERS_DSN", DSNKeyID: "abcd1234", DSNCiphertext: []byte("sealed"),
	}}}
	admin := newWebTestClient(t, Services{Catalog: listStub}, relations.RoleAdmin)
	listed := admin.request(http.MethodGet, "/api/v1/projects/10/data-sources", "", false)
	assertWebStatus(t, listed, http.StatusOK, "")
	for _, secret := range []string{"sealed", "abcd1234"} {
		if strings.Contains(listed.Body.String(), secret) {
			t.Fatalf("list response leaked %q: %s", secret, listed.Body.String())
		}
	}
}

// TestCreateDataSourceWithoutASecretKeyIsRejectedClearly keeps the failure
// legible instead of surfacing an opaque internal error.
func TestCreateDataSourceWithoutASecretKeyIsRejectedClearly(t *testing.T) {
	stub := &catalogHTTPStub{createErr: catalog.ErrSealerUnavailable}
	client := newWebTestClient(t, Services{Catalog: stub}, relations.RoleAdmin)

	body := `{"name":"orders-primary","kind":"MYSQL","dsnEnvironment":"ORDERS_DSN",` +
		`"dsn":"root:pw@tcp(127.0.0.1:3306)/orders","reason":"bootstrap"}`
	response := client.request(http.MethodPost, "/api/v1/projects/10/data-sources", body, true)
	assertWebStatus(t, response, http.StatusUnprocessableEntity, "SECRET_KEY_REQUIRED")
}
