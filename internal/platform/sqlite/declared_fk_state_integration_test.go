package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestDeclaredForeignKeyCannotBeRestoredWhileAbsent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fixedTime := time.Date(2026, time.August, 11, 18, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(17, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	catalogService := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })
	dataSource, err := catalogService.CreateDataSource(ctx, catalog.CreateDataSource{
		Name: "primary", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "DECLARED_FK_STATE_MYSQL_DSN",
	})
	if err != nil {
		t.Fatal(err)
	}
	nodes := []catalog.NodeInput{
		{StableKey: "database:learn", Kind: catalog.NodeDatabase, Name: "learn", QualifiedName: "mysql://learn"},
		{StableKey: "schema:learn", ParentStableKey: "database:learn", Kind: catalog.NodeSchema, Name: "learn", QualifiedName: "learn"},
		{StableKey: "table:learn.classes", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "classes", QualifiedName: "learn.classes"},
		{StableKey: "column:learn.classes.student_id", ParentStableKey: "table:learn.classes", Kind: catalog.NodeColumn, Name: "student_id", QualifiedName: "learn.classes.student_id", DataType: "bigint", Ordinal: 1},
		{StableKey: "table:learn.students", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "students", QualifiedName: "learn.students"},
		{StableKey: "column:learn.students.id", ParentStableKey: "table:learn.students", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "learn.students.id", DataType: "bigint", Ordinal: 1},
	}
	foreignKey := catalog.DeclaredForeignKey{
		ConstraintSchema: "learn", Name: "fk_classes_student",
		SourceColumn: "learn.classes.student_id", TargetColumn: "learn.students.id", Ordinal: 1,
	}
	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		DataSourceID: dataSource.ID, Nodes: nodes,
		ForeignKeys: []catalog.DeclaredForeignKey{foreignKey},
	}); err != nil {
		t.Fatalf("publish declared foreign key: %v", err)
	}

	sourceNode, err := catalogService.FindCurrentNode(ctx, dataSource.ID, foreignKey.SourceColumn)
	if err != nil {
		t.Fatal(err)
	}
	targetNode, err := catalogService.FindCurrentNode(ctx, dataSource.ID, foreignKey.TargetColumn)
	if err != nil {
		t.Fatal(err)
	}
	graphService := graph.NewService(dbsqlite.NewGraphRepository(store))
	trace := func() graph.TraceResult {
		t.Helper()
		result, err := graphService.Trace(ctx, graph.TraceRequest{
			StartNodeID: sourceNode.ID, TargetNodeID: targetNode.ID,
			Direction: graph.DirectionDownstream, Context: conditions.Context{}, Limits: graph.DefaultLimits(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	initialTrace := trace()
	if len(initialTrace.Paths) != 1 {
		t.Fatalf("initial declared FK trace paths = %d, want 1", len(initialTrace.Paths))
	}
	relationID := initialTrace.Paths[0].Steps[0].Edge.RelationID
	relationCommands := relations.NewCommands(
		dbsqlite.NewRelationRepository(store), ids, func() time.Time { return fixedTime },
	)
	reviewer := relations.Principal{Actor: "reviewer", Role: relations.RoleReviewer, Origin: audit.OriginWeb}
	suppressed, err := relationCommands.Suppress(ctx, relations.ChangeState{
		RelationID: relationID, ExpectedRevisionNo: 1, Principal: reviewer,
		Reason: "Suppress the declared constraint while it is investigated.", RequestID: "suppress-declared-fk",
	})
	if err != nil {
		t.Fatalf("suppress declared FK: %v", err)
	}
	if suppressed.Status != relations.StatusSuppressed || suppressed.Effective || len(trace().Paths) != 0 {
		t.Fatalf("suppressed declared FK = %#v", suppressed)
	}

	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		DataSourceID: dataSource.ID, Nodes: nodes,
	}); err != nil {
		t.Fatalf("publish snapshot without declared FK: %v", err)
	}
	_, err = relationCommands.Restore(ctx, relations.ChangeState{
		RelationID: relationID, ExpectedRevisionNo: 1, Principal: reviewer,
		Reason: "Attempt to restore an absent constraint.", RequestID: "restore-absent-declared-fk",
	})
	if !errors.Is(err, relations.ErrInvalidTransition) {
		t.Fatalf("restore absent declared FK error = %v, want invalid transition", err)
	}
	absent, err := relationCommands.Get(ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if absent.Status != relations.StatusSuppressed || absent.Effective || len(trace().Paths) != 0 {
		t.Fatalf("absent declared FK = %#v", absent)
	}

	if _, err := catalogService.PublishSnapshot(ctx, catalog.PublishSnapshot{
		DataSourceID: dataSource.ID, Nodes: nodes,
		ForeignKeys: []catalog.DeclaredForeignKey{foreignKey},
	}); err != nil {
		t.Fatalf("publish reappeared declared FK: %v", err)
	}
	reappeared, err := relationCommands.Get(ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if reappeared.Status != relations.StatusSuppressed || reappeared.Effective || len(trace().Paths) != 0 {
		t.Fatalf("reappeared suppressed declared FK = %#v", reappeared)
	}

	restored, err := relationCommands.Restore(ctx, relations.ChangeState{
		RelationID: relationID, ExpectedRevisionNo: reappeared.LatestRevisionNo, Principal: reviewer,
		Reason: "Restore the constraint after it reappeared.", RequestID: "restore-present-declared-fk",
	})
	if err != nil {
		t.Fatalf("restore present declared FK: %v", err)
	}
	if restored.Status != relations.StatusApproved || !restored.Effective || len(trace().Paths) != 1 {
		t.Fatalf("restored declared FK = %#v", restored)
	}
}
