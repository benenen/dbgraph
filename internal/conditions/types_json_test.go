package conditions_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/benenen/dbgraph/internal/conditions"
)

func TestLiteralValueUsesCanonicalIdeaWireShapeAndReadsLegacyShape(t *testing.T) {
	t.Parallel()

	want := conditions.Value{
		Kind: conditions.ValueLiteral,
		Literal: &conditions.Literal{
			Type: conditions.LiteralInteger, Value: json.RawMessage(`1`),
		},
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"kind":"literal","valueType":"integer","value":1}` {
		t.Fatalf("canonical literal JSON = %s", encoded)
	}

	for _, input := range []string{
		`{"kind":"literal","valueType":"integer","value":1}`,
		`{"kind":"literal","literal":{"type":"integer","value":1}}`,
	} {
		var decoded conditions.Value
		if err := json.Unmarshal([]byte(input), &decoded); err != nil {
			t.Fatalf("decode %s: %v", input, err)
		}
		if !reflect.DeepEqual(decoded, want) {
			t.Fatalf("decoded %s = %#v, want %#v", input, decoded, want)
		}
	}
}
