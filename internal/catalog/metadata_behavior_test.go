package catalog_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/benenen/dbgraph/internal/catalog"
)

func TestNodeMetadataRoundTripsTableIndexes(t *testing.T) {
	t.Parallel()

	want := []catalog.Index{
		{Name: "PRIMARY", Unique: true, Primary: true, Columns: []string{"id"}},
		{Name: "idx_tenant_created", Columns: []string{"tenant_id", "created_at"}},
	}
	raw, err := catalog.EncodeNodeMetadata(want)
	if err != nil {
		t.Fatalf("EncodeNodeMetadata: %v", err)
	}
	if got := catalog.DecodeNodeMetadata(string(raw)); !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded indexes = %#v, want %#v", got, want)
	}

	empty, err := catalog.EncodeNodeMetadata(nil)
	if err != nil || string(empty) != "{}" {
		t.Fatalf("empty metadata = %q, err=%v", empty, err)
	}
	for _, raw := range []string{"", "{}", "not-json"} {
		if got := catalog.DecodeNodeMetadata(raw); got != nil {
			t.Fatalf("DecodeNodeMetadata(%q) = %#v, want nil", raw, got)
		}
	}
}

func TestNodeMetadataRejectsInvalidIndexes(t *testing.T) {
	t.Parallel()

	tooManyIndexes := make([]catalog.Index, catalog.MaximumIndexesPerTable+1)
	tooManyColumns := make([]string, catalog.MaximumIndexColumns+1)
	tests := []struct {
		name    string
		indexes []catalog.Index
	}{
		{name: "too many indexes", indexes: tooManyIndexes},
		{name: "blank index name", indexes: []catalog.Index{{Name: " ", Columns: []string{"id"}}}},
		{name: "long index name", indexes: []catalog.Index{{Name: strings.Repeat("x", 501), Columns: []string{"id"}}}},
		{name: "no columns", indexes: []catalog.Index{{Name: "idx"}}},
		{name: "too many columns", indexes: []catalog.Index{{Name: "idx", Columns: tooManyColumns}}},
		{name: "blank column", indexes: []catalog.Index{{Name: "idx", Columns: []string{" "}}}},
		{name: "long column", indexes: []catalog.Index{{Name: "idx", Columns: []string{strings.Repeat("x", 501)}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := catalog.EncodeNodeMetadata(test.indexes); !errors.Is(err, catalog.ErrInvalidSnapshot) {
				t.Fatalf("EncodeNodeMetadata error = %v, want ErrInvalidSnapshot", err)
			}
		})
	}
}
