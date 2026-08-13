package graph

import (
	"strings"

	"github.com/benenen/dbgraph/internal/catalog"
)

// Cardinality is how many rows each end of a relation can hold for one row of
// the other. It is inferred from the indexes the last scan saw, not measured
// from the data, so it says what the schema permits rather than what the rows
// currently happen to contain.
type Cardinality string

const (
	// CardinalityUnknown is the honest answer when index metadata is missing.
	// A source imported before dbgraph read indexes knows nothing about
	// uniqueness, and guessing N:N there would read as a finding.
	CardinalityUnknown    Cardinality = "UNKNOWN"
	CardinalityOneToOne   Cardinality = "ONE_TO_ONE"
	CardinalityOneToMany  Cardinality = "ONE_TO_MANY"
	CardinalityManyToOne  Cardinality = "MANY_TO_ONE"
	CardinalityManyToMany Cardinality = "MANY_TO_MANY"
)

// EdgeCardinality reads both ends' uniqueness off their tables' indexes.
func EdgeCardinality(
	sourceIndexes []catalog.Index,
	sourceColumn string,
	targetIndexes []catalog.Index,
	targetColumn string,
) Cardinality {
	source := columnUniqueness(sourceIndexes, sourceColumn)
	target := columnUniqueness(targetIndexes, targetColumn)
	if source == uniquenessUnknown || target == uniquenessUnknown {
		return CardinalityUnknown
	}
	switch {
	case source == uniquenessOne && target == uniquenessOne:
		return CardinalityOneToOne
	case source == uniquenessOne:
		return CardinalityOneToMany
	case target == uniquenessOne:
		return CardinalityManyToOne
	default:
		return CardinalityManyToMany
	}
}

type uniqueness int

const (
	uniquenessUnknown uniqueness = iota
	uniquenessOne
	uniquenessMany
)

// columnUniqueness asks whether one column can repeat. Only an index over that
// column alone settles it: a unique index over (BusinessID, Type) constrains
// the pair, and leaves BusinessID free to repeat once per type.
//
// An empty index list means the scan recorded none, which is not the same as a
// table having none, so it answers unknown rather than "repeats".
func columnUniqueness(indexes []catalog.Index, column string) uniqueness {
	if len(indexes) == 0 {
		return uniquenessUnknown
	}
	for _, index := range indexes {
		if !index.Unique && !index.Primary {
			continue
		}
		if len(index.Columns) == 1 && strings.EqualFold(index.Columns[0], column) {
			return uniquenessOne
		}
	}
	return uniquenessMany
}
