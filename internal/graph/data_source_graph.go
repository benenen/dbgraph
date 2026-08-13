package graph

import (
	"context"
	"encoding/json"

	"github.com/benenen/dbgraph/internal/relations"
)

// MaximumDataSourceGraphEdges bounds one whole-source read. A relation graph is
// drawn all at once rather than walked, so the bound is on the picture, not on
// a traversal depth.
const MaximumDataSourceGraphEdges = 2000

// Table is one table in a data source's relation graph. Relations are declared
// between columns; the graph is drawn between the tables that own them, because
// that is the shape a reader is looking for.
type Table struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualifiedName"`
}

// TableEdge is one approved relation, kept at column granularity so the drawing
// can say which columns join even while the nodes are tables.
type TableEdge struct {
	RelationID    int64          `json:"relationId"`
	Type          relations.Type `json:"type"`
	SourceTableID int64          `json:"sourceTableId"`
	TargetTableID int64          `json:"targetTableId"`
	SourceNodeID  int64          `json:"sourceNodeId"`
	TargetNodeID  int64          `json:"targetNodeId"`
	SourceColumn  string         `json:"sourceColumn"`
	TargetColumn  string         `json:"targetColumn"`
	Conditional   bool           `json:"conditional"`
	Confidence    float64        `json:"confidence"`
	// Guard is the stored condition AST. A reader clicking an edge wants to
	// know when the relation applies, and "conditional" alone does not say.
	Guard json.RawMessage `json:"guard,omitempty"`
}

// DataSourceGraph is every approved relation inside one data source, with the
// tables they connect.
type DataSourceGraph struct {
	Tables    []Table     `json:"tables"`
	Edges     []TableEdge `json:"edges"`
	Truncated bool        `json:"truncated"`
}

// DataSourceGraphRepository reads a whole source's relation graph. It is
// optional in the same way RecursiveRepository is: a Service without it simply
// cannot answer this question.
type DataSourceGraphRepository interface {
	LoadDataSourceGraph(
		ctx context.Context,
		dataSourceID int64,
		maximumEdges int,
	) (DataSourceGraph, error)
}

// DataSourceGraph returns every approved relation between tables in one data
// source. An empty graph is a real answer: a source can be fully scanned and
// still hold no relations until an agent or a reviewer proposes them.
func (s *Service) DataSourceGraph(
	ctx context.Context,
	dataSourceID int64,
) (DataSourceGraph, error) {
	if dataSourceID <= 0 {
		return DataSourceGraph{}, ErrInvalidTrace
	}
	repository, ok := s.repository.(DataSourceGraphRepository)
	if !ok {
		return DataSourceGraph{}, ErrInvalidTrace
	}
	return repository.LoadDataSourceGraph(ctx, dataSourceID, MaximumDataSourceGraphEdges)
}
