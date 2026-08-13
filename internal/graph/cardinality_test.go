package graph_test

import (
	"testing"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/graph"
)

func TestEdgeCardinalityReadsUniquenessOffTheIndexes(t *testing.T) {
	t.Parallel()

	primary := func(column string) catalog.Index {
		return catalog.Index{Name: "PRIMARY", Unique: true, Primary: true, Columns: []string{column}}
	}
	plain := func(column string) catalog.Index {
		return catalog.Index{Name: "idx_" + column, Columns: []string{column}}
	}

	for name, testCase := range map[string]struct {
		sourceIndexes []catalog.Index
		sourceColumn  string
		targetIndexes []catalog.Index
		targetColumn  string
		want          graph.Cardinality
	}{
		// The ordinary foreign key: many rows on the near side point at one row
		// identified by its primary key.
		"many rows to one key": {
			sourceIndexes: []catalog.Index{plain("BusinessID"), primary("ObjectID")},
			sourceColumn:  "BusinessID",
			targetIndexes: []catalog.Index{primary("ObjectID")},
			targetColumn:  "ObjectID",
			want:          graph.CardinalityManyToOne,
		},
		"unique both sides": {
			sourceIndexes: []catalog.Index{{Name: "uq", Unique: true, Columns: []string{"ScheduleID"}}},
			sourceColumn:  "ScheduleID",
			targetIndexes: []catalog.Index{primary("ObjectID")},
			targetColumn:  "ObjectID",
			want:          graph.CardinalityOneToOne,
		},
		"unique source into a repeated target": {
			sourceIndexes: []catalog.Index{{Name: "uq", Unique: true, Columns: []string{"Code"}}},
			sourceColumn:  "Code",
			targetIndexes: []catalog.Index{plain("Code")},
			targetColumn:  "Code",
			want:          graph.CardinalityOneToMany,
		},
		"repeated on both sides": {
			sourceIndexes: []catalog.Index{plain("Tag")},
			sourceColumn:  "Tag",
			targetIndexes: []catalog.Index{plain("Tag")},
			targetColumn:  "Tag",
			want:          graph.CardinalityManyToMany,
		},
		// A unique index over two columns does not make either one unique on its
		// own: (BusinessID, Type) permits the same BusinessID under two types.
		"composite unique does not make a column unique": {
			sourceIndexes: []catalog.Index{{Name: "uq", Unique: true, Columns: []string{"BusinessID", "Type"}}},
			sourceColumn:  "BusinessID",
			targetIndexes: []catalog.Index{primary("ObjectID")},
			targetColumn:  "ObjectID",
			want:          graph.CardinalityManyToOne,
		},
		// No index metadata is not evidence of no index. A source scanned before
		// dbgraph read indexes must say it does not know, not invent N:N.
		"no metadata on the source": {
			sourceIndexes: nil,
			sourceColumn:  "BusinessID",
			targetIndexes: []catalog.Index{primary("ObjectID")},
			targetColumn:  "ObjectID",
			want:          graph.CardinalityUnknown,
		},
		"no metadata on the target": {
			sourceIndexes: []catalog.Index{plain("BusinessID")},
			sourceColumn:  "BusinessID",
			targetIndexes: nil,
			targetColumn:  "ObjectID",
			want:          graph.CardinalityUnknown,
		},
		// MySQL identifiers do not compare case-sensitively, and a mapper writes
		// whatever case the author typed.
		"column names compare without case": {
			sourceIndexes: []catalog.Index{plain("businessid"), primary("objectid")},
			sourceColumn:  "BusinessID",
			targetIndexes: []catalog.Index{primary("OBJECTID")},
			targetColumn:  "ObjectID",
			want:          graph.CardinalityManyToOne,
		},
	} {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := graph.EdgeCardinality(
				testCase.sourceIndexes, testCase.sourceColumn,
				testCase.targetIndexes, testCase.targetColumn,
			)
			if got != testCase.want {
				t.Fatalf("EdgeCardinality = %q, want %q", got, testCase.want)
			}
		})
	}
}
