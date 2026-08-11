package id_test

import (
	"context"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/id"
)

func TestGeneratorProducesOrderedUniqueIDsWithinMillisecond(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, time.August, 11, 10, 0, 0, 123_000_000, time.UTC)
	generator, err := id.NewGenerator(7, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}

	first, err := generator.Next(context.Background())
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	second, err := generator.Next(context.Background())
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	third, err := generator.Next(context.Background())
	if err != nil {
		t.Fatalf("third Next: %v", err)
	}

	if first <= 0 {
		t.Fatalf("first ID = %d, want positive", first)
	}
	if first >= second || second >= third {
		t.Fatalf("IDs are not strictly ordered: %d, %d, %d", first, second, third)
	}
}
