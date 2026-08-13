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
	raw, err := catalog.EncodeNodeMetadata(want, "orders placed by a tenant")
	if err != nil {
		t.Fatalf("EncodeNodeMetadata: %v", err)
	}
	got, comment := catalog.DecodeNodeMetadata(string(raw))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded indexes = %#v, want %#v", got, want)
	}
	if comment != "orders placed by a tenant" {
		t.Fatalf("decoded comment = %q", comment)
	}

	// A comment alone is worth storing: most tables have no index worth naming
	// and a description anyway.
	commentOnly, err := catalog.EncodeNodeMetadata(nil, "  tenant identifier  ")
	if err != nil {
		t.Fatalf("EncodeNodeMetadata comment only: %v", err)
	}
	if indexes, only := catalog.DecodeNodeMetadata(string(commentOnly)); indexes != nil || only != "tenant identifier" {
		t.Fatalf("comment-only metadata = %#v / %q", indexes, only)
	}

	// A comment longer than the cap is truncated rather than rejected: losing
	// the tail of a description is better than losing the whole scan.
	long, err := catalog.EncodeNodeMetadata(nil, strings.Repeat("x", catalog.MaximumCommentLength+50))
	if err != nil {
		t.Fatalf("EncodeNodeMetadata long comment: %v", err)
	}
	if _, stored := catalog.DecodeNodeMetadata(string(long)); len(stored) != catalog.MaximumCommentLength {
		t.Fatalf("stored comment length = %d, want %d", len(stored), catalog.MaximumCommentLength)
	}

	empty, err := catalog.EncodeNodeMetadata(nil, "")
	if err != nil || string(empty) != "{}" {
		t.Fatalf("empty metadata = %q, err=%v", empty, err)
	}
	for _, raw := range []string{"", "{}", "not-json"} {
		if indexes, comment := catalog.DecodeNodeMetadata(raw); indexes != nil || comment != "" {
			t.Fatalf("DecodeNodeMetadata(%q) = %#v / %q, want nil and empty", raw, indexes, comment)
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
			if _, err := catalog.EncodeNodeMetadata(test.indexes, ""); !errors.Is(err, catalog.ErrInvalidSnapshot) {
				t.Fatalf("EncodeNodeMetadata error = %v, want ErrInvalidSnapshot", err)
			}
		})
	}
}
