package gitremote_test

import (
	"strings"
	"testing"

	"github.com/benenen/dbgraph/internal/gitremote"
)

func TestCanonicalizePreservesExactTransportIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{" https://GitHub.COM:443/acme/orders.git ", "https://github.com/acme/orders.git"},
		{"ssh://git@Git.Example.Test:22/platform/orders.git", "ssh://git@git.example.test/platform/orders.git"},
		{"git@Git.Example.Test:platform/orders.git", "scp://git@git.example.test/platform/orders.git"},
		{"git://Git.Example.Test/platform/orders.git", "git://git.example.test/platform/orders.git"},
		{"https://git.example.test/platform/orders", "https://git.example.test/platform/orders"},
	}
	for _, test := range tests {
		canonical, err := gitremote.Canonicalize(test.input)
		if err != nil {
			t.Errorf("Canonicalize(%q): %v", test.input, err)
			continue
		}
		if canonical != test.want {
			t.Errorf("Canonicalize(%q) = %q, want %q", test.input, canonical, test.want)
		}
	}
}

func TestCanonicalizeRejectsUnsafeOrAmbiguousRemotes(t *testing.T) {
	t.Parallel()

	for _, remote := range []string{
		"",
		"https://user@git.example.test/platform/orders.git",
		"https://git.example.test/platform/orders.git?token=secret",
		"https://git.example.test/platform/orders.git?",
		"https://git.example.test/platform/orders.git#fragment",
		"https://git.example.test/platform/orders.git#",
		"ssh://alice@git.example.test/platform/orders.git",
		"ssh://git:password@git.example.test/platform/orders.git",
		"ssh://git.example.test/platform/orders.git",
		"alice@git.example.test:platform/orders.git",
		"git.example.test:platform/orders.git",
		"git@git.example.test:/platform/orders.git",
		"file:///workspace/orders",
		"https://git.example.test/platform/%2E%2E/orders.git",
		"https://git.example.test/platform//orders.git",
		"https://git.example.test/platform/../orders.git",
		"https://git.example.test/platform/orders.git/",
		`https://git.example.test/platform\orders.git`,
		strings.Repeat("r", 2001),
	} {
		if canonical, err := gitremote.Canonicalize(remote); err == nil {
			t.Errorf("Canonicalize(%q) = %q, want error", remote, canonical)
		}
	}
}

func TestCanonicalizeDoesNotMergeProviderSpecificAliases(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"https://git.example.test/platform/orders",
		"https://git.example.test/platform/orders.git",
		"ssh://git@git.example.test/platform/orders.git",
		"git@git.example.test:platform/orders.git",
	}
	identities := make(map[string]string, len(inputs))
	for _, input := range inputs {
		canonical, err := gitremote.Canonicalize(input)
		if err != nil {
			t.Fatalf("Canonicalize(%q): %v", input, err)
		}
		if previous, exists := identities[canonical]; exists {
			t.Fatalf("%q and %q collapsed to %q", previous, input, canonical)
		}
		identities[canonical] = input
	}
}
