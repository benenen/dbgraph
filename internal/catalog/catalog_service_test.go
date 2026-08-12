package catalog_test

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

func TestAdminCreatesAuditedDataSource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixedTime := time.Date(2026, time.August, 11, 12, 30, 0, 0, time.UTC)
	ids, err := id.NewGenerator(5, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	project, err := catalog.NewProjectService(dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime }).Create(
		ctx, catalog.CreateProject{Name: "Admin Test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	service := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })
	command := catalog.AdminCreateDataSource{
		ProjectID: project.ID, Name: "primary", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "PRIMARY_MYSQL_DSN", Reason: "Configure production metadata source", RequestID: "web-1",
		Principal: relations.Principal{Actor: "viewer", Role: relations.RoleViewer, Origin: audit.OriginWeb},
	}
	if _, err := service.CreateDataSourceAsAdmin(ctx, command); !errors.Is(err, catalog.ErrForbidden) {
		t.Fatalf("viewer error = %v, want forbidden", err)
	}
	command.Principal = relations.Principal{Actor: "admin", Role: relations.RoleAdmin, Origin: audit.OriginWeb}
	created, err := service.CreateDataSourceAsAdmin(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	events, err := dbsqlite.NewAuditRepository(store).ListAuditEvents(ctx, project.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "DATA_SOURCE_CREATED" || events[0].SubjectID != created.ID ||
		events[0].Actor != "admin" || events[0].RequestID != "web-1" {
		t.Fatalf("audit events = %#v", events)
	}
}

func TestPublishSnapshotPublishesAndReconcilesDeclaredForeignKey(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixedTime := time.Date(2026, time.August, 11, 16, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(9, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	project, err := catalog.NewProjectService(dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime }).Create(
		ctx, catalog.CreateProject{Name: "Declared FK"},
	)
	if err != nil {
		t.Fatal(err)
	}
	service := catalog.NewService(dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime })
	source, err := service.CreateDataSource(ctx, catalog.CreateDataSource{
		ProjectID: project.ID, Name: "primary", Kind: catalog.DataSourceMySQL, DSNEnvironment: "FK_MYSQL_DSN",
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
	fk := catalog.DeclaredForeignKey{
		ConstraintSchema: "learn", Name: "fk_classes_student",
		SourceColumn: "learn.classes.student_id", TargetColumn: "learn.students.id", Ordinal: 1,
	}
	first, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project.ID, DataSourceID: source.ID, Nodes: nodes, ForeignKeys: []catalog.DeclaredForeignKey{fk},
	})
	if err != nil {
		t.Fatalf("publish snapshot with FK: %v", err)
	}
	from, err := service.FindCurrentNode(ctx, project.ID, source.ID, fk.SourceColumn)
	if err != nil {
		t.Fatal(err)
	}
	to, err := service.FindCurrentNode(ctx, project.ID, source.ID, fk.TargetColumn)
	if err != nil {
		t.Fatal(err)
	}
	graphService := graph.NewService(dbsqlite.NewGraphRepository(store))
	trace := func() graph.TraceResult {
		result, err := graphService.Trace(ctx, graph.TraceRequest{
			ProjectID: project.ID, StartNodeID: from.ID, TargetNodeID: to.ID,
			Direction: graph.DirectionDownstream, Context: conditions.Context{}, Limits: graph.DefaultLimits(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := trace()
	if len(result.Paths) != 1 || len(result.Paths[0].Steps) != 1 || result.Paths[0].Steps[0].Edge.Type != relations.TypeDeclaredForeignKey {
		t.Fatalf("declared FK trace = %#v", result)
	}
	relationID := result.Paths[0].Steps[0].Edge.RelationID
	relationRepository := dbsqlite.NewRelationRepository(store)
	relation, err := relationRepository.Get(ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if relation.Status != relations.StatusApproved || !relation.Effective || relation.Active == nil ||
		relation.Active.Origin != audit.OriginSystem || len(relation.Active.Evidence) != 1 ||
		relation.Active.Evidence[0].Kind != relations.EvidenceDatabaseConstraint ||
		relation.Active.Evidence[0].DataSourceID != source.ID || relation.Active.Evidence[0].ScanRunID != first.ScanRunID {
		t.Fatalf("declared FK relation = %#v", relation)
	}

	if _, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project.ID, DataSourceID: source.ID, Nodes: nodes, ForeignKeys: []catalog.DeclaredForeignKey{fk},
	}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := relationRepository.Get(ctx, relationID)
	if err != nil || unchanged.LatestRevisionNo != 1 || len(trace().Paths) != 1 {
		t.Fatalf("unchanged FK relation = %#v, err=%v", unchanged, err)
	}
	if _, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project.ID, DataSourceID: source.ID, Nodes: nodes,
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := relationRepository.Get(ctx, relationID)
	if err != nil || removed.Status != relations.StatusTombstoned || removed.Effective || removed.LatestRevisionNo != 2 || len(trace().Paths) != 0 {
		t.Fatalf("removed FK relation = %#v, err=%v", removed, err)
	}
	if _, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project.ID, DataSourceID: source.ID, Nodes: nodes, ForeignKeys: []catalog.DeclaredForeignKey{fk},
	}); err != nil {
		t.Fatal(err)
	}
	reappeared, err := relationRepository.Get(ctx, relationID)
	if err != nil || reappeared.Status != relations.StatusApproved || !reappeared.Effective || reappeared.LatestRevisionNo != 3 || len(trace().Paths) != 1 {
		t.Fatalf("reappeared FK relation = %#v, err=%v", reappeared, err)
	}
}

func TestServicePublishesCurrentSchemaSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{
		Path: filepath.Join(t.TempDir(), "dbgraph.sqlite"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	fixedTime := time.Date(2026, time.August, 11, 13, 0, 0, 0, time.UTC)
	idGenerator, err := id.NewGenerator(6, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("create ID generator: %v", err)
	}
	projectService := catalog.NewProjectService(
		dbsqlite.NewProjectRepository(store),
		idGenerator,
		func() time.Time { return fixedTime },
	)
	project, err := projectService.Create(ctx, catalog.CreateProject{Name: "Learning Platform"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	service := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, idGenerator),
		idGenerator,
		func() time.Time { return fixedTime },
	)
	dataSource, err := service.CreateDataSource(ctx, catalog.CreateDataSource{
		ProjectID:      project.ID,
		Name:           "Primary MySQL",
		Kind:           catalog.DataSourceMySQL,
		DSNEnvironment: "DBGRAPH_PRIMARY_MYSQL_DSN",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}
	retrievedSource, err := service.GetDataSource(ctx, dataSource.ID)
	if err != nil {
		t.Fatalf("get data source: %v", err)
	}
	if retrievedSource != dataSource {
		t.Fatalf("retrieved data source = %#v, want %#v", retrievedSource, dataSource)
	}

	published, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID:    project.ID,
		DataSourceID: dataSource.ID,
		Nodes: []catalog.NodeInput{
			{StableKey: "database:primary", Kind: catalog.NodeDatabase, Name: "primary", QualifiedName: "primary"},
			{StableKey: "schema:learn", ParentStableKey: "database:primary", Kind: catalog.NodeSchema, Name: "learn", QualifiedName: "learn"},
			{StableKey: "table:learn.students", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "students", QualifiedName: "learn.students"},
			{StableKey: "column:learn.students.id", ParentStableKey: "table:learn.students", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "learn.students.id", DataType: "bigint", Nullable: false, Ordinal: 1},
		},
	})
	if err != nil {
		t.Fatalf("publish snapshot: %v", err)
	}
	if published.ScanRunID <= 0 || published.NodeCount != 4 {
		t.Fatalf("published snapshot = %#v", published)
	}

	node, err := service.FindCurrentNode(ctx, project.ID, dataSource.ID, "learn.students.id")
	if err != nil {
		t.Fatalf("find current node: %v", err)
	}
	if node.Kind != catalog.NodeColumn || node.DataType != "bigint" || node.Status != catalog.NodeActive {
		t.Fatalf("current node = %#v", node)
	}
	if node.VersionID <= 0 || node.ScanRunID != published.ScanRunID {
		t.Fatalf("current node version = %#v, snapshot = %#v", node, published)
	}

	secondarySource, err := service.CreateDataSource(ctx, catalog.CreateDataSource{
		ProjectID:      project.ID,
		Name:           "Analytics MySQL",
		Kind:           catalog.DataSourceMySQL,
		DSNEnvironment: "DBGRAPH_ANALYTICS_MYSQL_DSN",
	})
	if err != nil {
		t.Fatalf("create secondary data source: %v", err)
	}
	if _, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID:    project.ID,
		DataSourceID: secondarySource.ID,
		Nodes: []catalog.NodeInput{
			{StableKey: "database:analytics", Kind: catalog.NodeDatabase, Name: "analytics", QualifiedName: "analytics"},
			{StableKey: "schema:learn", ParentStableKey: "database:analytics", Kind: catalog.NodeSchema, Name: "learn", QualifiedName: "learn"},
			{StableKey: "table:learn.students", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "students", QualifiedName: "learn.students"},
			{StableKey: "column:learn.students.id", ParentStableKey: "table:learn.students", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "learn.students.id", DataType: "varchar(64)", Ordinal: 1},
		},
	}); err != nil {
		t.Fatalf("publish secondary snapshot: %v", err)
	}
	secondaryNode, err := service.FindCurrentNode(ctx, project.ID, secondarySource.ID, "learn.students.id")
	if err != nil {
		t.Fatalf("find secondary current node: %v", err)
	}
	if secondaryNode.DataSourceID != secondarySource.ID || secondaryNode.DataType != "varchar(64)" {
		t.Fatalf("secondary current node = %#v", secondaryNode)
	}
	primaryNode, err := service.FindCurrentNode(ctx, project.ID, dataSource.ID, "learn.students.id")
	if err != nil {
		t.Fatalf("find primary current node after secondary publication: %v", err)
	}
	if primaryNode.ID != node.ID || primaryNode.DataType != "bigint" {
		t.Fatalf("primary current node changed across data sources: %#v", primaryNode)
	}

	matches, err := service.SearchCurrentNodes(ctx, project.ID, 0, "student", 10)
	if err != nil {
		t.Fatalf("search current nodes: %v", err)
	}
	if len(matches) != 4 {
		t.Fatalf("search match count = %d, want table and column from both data sources", len(matches))
	}
	scopedMatches, err := service.SearchCurrentNodes(ctx, project.ID, secondarySource.ID, "student", 10)
	if err != nil {
		t.Fatalf("search current nodes scoped to a data source: %v", err)
	}
	if len(scopedMatches) != 2 {
		t.Fatalf("scoped search match count = %d, want table and column from the secondary data source only", len(scopedMatches))
	}
	for _, match := range scopedMatches {
		if match.DataSourceID != secondarySource.ID {
			t.Fatalf("scoped search returned a node from another data source: %#v", match)
		}
	}
	matchCounts := map[int64]int{}
	for _, match := range matches {
		matchCounts[match.DataSourceID]++
	}
	if matchCounts[dataSource.ID] != 2 || matchCounts[secondarySource.ID] != 2 {
		t.Fatalf("search matches by data source = %#v, matches = %#v", matchCounts, matches)
	}

	secondSnapshot, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID:    project.ID,
		DataSourceID: dataSource.ID,
		Nodes: []catalog.NodeInput{
			{StableKey: "database:primary", Kind: catalog.NodeDatabase, Name: "primary", QualifiedName: "primary"},
			{StableKey: "schema:learn", ParentStableKey: "database:primary", Kind: catalog.NodeSchema, Name: "learn", QualifiedName: "learn"},
			{StableKey: "table:learn.students", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "students", QualifiedName: "learn.students"},
		},
	})
	if err != nil {
		t.Fatalf("publish second snapshot: %v", err)
	}
	if secondSnapshot.StaleCount != 1 {
		t.Fatalf("second snapshot stale count = %d, want 1", secondSnapshot.StaleCount)
	}
	staleNode, err := service.FindCurrentNode(ctx, project.ID, dataSource.ID, "learn.students.id")
	if err != nil {
		t.Fatalf("find stale node: %v", err)
	}
	if staleNode.ID != node.ID || staleNode.VersionID == node.VersionID || staleNode.Status != catalog.NodeStale {
		t.Fatalf("stale node = %#v, previous = %#v", staleNode, node)
	}
	matches, err = service.SearchCurrentNodes(ctx, project.ID, 0, "student", 10)
	if err != nil {
		t.Fatalf("search after stale publication: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("search matches after stale publication = %#v", matches)
	}
	matchCounts = map[int64]int{}
	for _, match := range matches {
		matchCounts[match.DataSourceID]++
	}
	if matchCounts[dataSource.ID] != 1 || matchCounts[secondarySource.ID] != 2 {
		t.Fatalf("search matches by data source after stale publication = %#v", matchCounts)
	}
}

func TestIncrementalSnapshotMarksOnlyNodesInsideExplicitTableScopeStale(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixedTime := time.Date(2026, time.August, 11, 16, 30, 0, 0, time.UTC)
	ids, err := id.NewGenerator(15, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	project, err := catalog.NewProjectService(
		dbsqlite.NewProjectRepository(store), ids, func() time.Time { return fixedTime },
	).Create(ctx, catalog.CreateProject{Name: "Incremental catalog"})
	if err != nil {
		t.Fatal(err)
	}
	service := catalog.NewService(
		dbsqlite.NewCatalogRepository(store, ids), ids, func() time.Time { return fixedTime },
	)
	source, err := service.CreateDataSource(ctx, catalog.CreateDataSource{
		ProjectID: project.ID, Name: "primary", Kind: catalog.DataSourceMySQL,
		DSNEnvironment: "INCREMENTAL_MYSQL_DSN",
	})
	if err != nil {
		t.Fatal(err)
	}
	ancestors := []catalog.NodeInput{
		{StableKey: "database:learn", Kind: catalog.NodeDatabase, Name: "learn", QualifiedName: "mysql://learn"},
		{StableKey: "schema:learn", ParentStableKey: "database:learn", Kind: catalog.NodeSchema, Name: "learn", QualifiedName: "learn"},
	}
	fullNodes := append(append([]catalog.NodeInput(nil), ancestors...),
		catalog.NodeInput{StableKey: "table:learn.a", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "a", QualifiedName: "learn.a"},
		catalog.NodeInput{StableKey: "column:learn.a.id", ParentStableKey: "table:learn.a", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "learn.a.id", DataType: "bigint", Ordinal: 1},
		catalog.NodeInput{StableKey: "table:learn.b", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "b", QualifiedName: "learn.b"},
		catalog.NodeInput{StableKey: "column:learn.b.id", ParentStableKey: "table:learn.b", Kind: catalog.NodeColumn, Name: "id", QualifiedName: "learn.b.id", DataType: "bigint", Ordinal: 1},
	)
	if _, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project.ID, DataSourceID: source.ID, Nodes: fullNodes,
	}); err != nil {
		t.Fatal(err)
	}
	bBefore, err := service.FindCurrentNode(ctx, project.ID, source.ID, "learn.b.id")
	if err != nil {
		t.Fatal(err)
	}
	incrementalNodes := append(append([]catalog.NodeInput(nil), ancestors...),
		catalog.NodeInput{StableKey: "table:learn.a", ParentStableKey: "schema:learn", Kind: catalog.NodeTable, Name: "a", QualifiedName: "learn.a"},
	)
	published, err := service.PublishSnapshot(ctx, catalog.PublishSnapshot{
		ProjectID: project.ID, DataSourceID: source.ID, Nodes: incrementalNodes,
		ScopeTables: []string{"learn.a"},
	})
	if err != nil {
		t.Fatalf("publish incremental snapshot: %v", err)
	}
	if published.StaleCount != 1 {
		t.Fatalf("incremental stale count = %d, want one scoped column", published.StaleCount)
	}
	aAfter, err := service.FindCurrentNode(ctx, project.ID, source.ID, "learn.a.id")
	if err != nil || aAfter.Status != catalog.NodeStale {
		t.Fatalf("scoped missing column = %#v, err=%v", aAfter, err)
	}
	bAfter, err := service.FindCurrentNode(ctx, project.ID, source.ID, "learn.b.id")
	if err != nil {
		t.Fatal(err)
	}
	if bAfter.Status != catalog.NodeActive || bAfter.VersionID != bBefore.VersionID {
		t.Fatalf("out-of-scope node changed: before=%#v after=%#v", bBefore, bAfter)
	}
}
