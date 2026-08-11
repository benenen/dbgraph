package jsoncheck_test

import (
	"bytes"
	"testing"

	"github.com/benenen/dbgraph/internal/jsoncheck"
)

func TestValidateObjectEnforcesBytesDepthAndObjectRoot(t *testing.T) {
	t.Parallel()

	limits := jsoncheck.Limits{MaxBytes: 32, MaxDepth: 3}
	for _, input := range []string{`{}`, `{"a":[{"b":1}]}`, `{"n":9007199254740993}`} {
		if err := jsoncheck.ValidateObject([]byte(input), limits); err != nil {
			t.Fatalf("valid object %s rejected: %v", input, err)
		}
	}
	for _, input := range []string{
		`null`, `[]`, `"text"`, `{"a":{"b":{"c":{}}}}`, `{} {}`, string(bytes.Repeat([]byte{'x'}, 33)),
	} {
		if err := jsoncheck.ValidateObject([]byte(input), limits); err == nil {
			t.Fatalf("invalid input accepted: %q", input)
		}
	}
}
