package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecursiveGraphQueryKeepsASTPayloadsOutOfRecursiveQueue(t *testing.T) {
	t.Parallel()

	query := recursiveGraphQuery("source_node_id", "target_node_id")
	separator := "\n)\nSELECT\n"
	projectionOffset := strings.Index(query, separator)
	if projectionOffset < 0 {
		t.Fatalf("recursive graph query does not have a distinct final projection:\n%s", query)
	}
	recursiveQueue := query[:projectionOffset]
	if !strings.HasSuffix(recursiveQueue, "\n    LIMIT ?") {
		t.Fatalf("recursive queue is not row-bounded before final AST projection:\n%s", query)
	}
	for _, payloadColumn := range []string{"guard_json", "selector_json", "transform_json"} {
		if strings.Contains(recursiveQueue, payloadColumn) {
			t.Fatalf("recursive queue carries %s:\n%s", payloadColumn, recursiveQueue)
		}
		if !strings.Contains(query[projectionOffset+len(separator):], payloadColumn) {
			t.Fatalf("final projection does not load %s:\n%s", payloadColumn, query)
		}
	}
}

func TestRecursiveGraphQueryUsesIndexedFinalProjectionJoins(t *testing.T) {
	t.Parallel()

	store, err := Open(context.Background(), Config{Path: filepath.Join(t.TempDir(), "graph-plan.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rows, err := store.db.QueryContext(
		context.Background(),
		"EXPLAIN QUERY PLAN "+recursiveGraphQuery("source_node_id", "target_node_id"),
		1, 1, 1, 1, 8, 0, 0, 101,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var id int
		var parent int
		var unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	details := plan.String()
	for _, indexedLookup := range []string{
		"SEARCH ee USING INTEGER PRIMARY KEY",
		"SEARCH rc USING INTEGER PRIMARY KEY",
	} {
		if !strings.Contains(details, indexedLookup) {
			t.Fatalf("final recursive projection lacks %q lookup:\n%s", indexedLookup, details)
		}
	}
	if strings.Contains(details, "SCAN rc") {
		t.Fatalf("final recursive projection scans relation_current as a Cartesian input:\n%s", details)
	}
}

func TestGraphEdgeLoadBudgetBoundsRawASTBytes(t *testing.T) {
	t.Parallel()

	maximum := 16 << 20
	budget := graphEdgeLoadBudget{maximum: maximum}
	chunk := strings.Repeat("x", 1<<20)
	accepted := 0
	for budget.accept(sql.NullString{String: chunk, Valid: true}, sql.NullString{}, chunk) {
		accepted++
	}
	if accepted == 0 || budget.bytes > maximum {
		t.Fatalf("accepted=%d bytes=%d maximum=%d", accepted, budget.bytes, maximum)
	}
	if budget.accept(sql.NullString{String: chunk, Valid: true}, sql.NullString{}, chunk) {
		t.Fatal("exhausted graph AST budget accepted another row")
	}
}
