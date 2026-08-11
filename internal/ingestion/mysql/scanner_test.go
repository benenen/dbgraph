package mysql_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/ingestion/mysql"
)

func TestScannerReadsSchemaNodesAndDeclaredForeignKeys(t *testing.T) {
	t.Parallel()

	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create SQL mock: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})

	mock.ExpectBegin()
	mock.ExpectQuery("FROM information_schema[.]SCHEMATA").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{"SCHEMA_NAME"}).AddRow("learn"))
	mock.ExpectQuery("FROM information_schema[.]TABLES").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{"TABLE_SCHEMA", "TABLE_NAME"}).
			AddRow("learn", "classes").
			AddRow("learn", "students"))
	mock.ExpectQuery("FROM information_schema[.]COLUMNS").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{
			"TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME", "COLUMN_TYPE", "IS_NULLABLE", "ORDINAL_POSITION",
		}).
			AddRow("learn", "classes", "student_id", "bigint", "NO", 1).
			AddRow("learn", "students", "id", "bigint", "NO", 1).
			AddRow("learn", "students", "name", "varchar(200)", "YES", 2))
	mock.ExpectQuery("FROM information_schema[.]KEY_COLUMN_USAGE.*REFERENCED_TABLE_SCHEMA = [?]").
		WithArgs("learn", "learn").
		WillReturnRows(sqlmock.NewRows([]string{
			"CONSTRAINT_SCHEMA", "CONSTRAINT_NAME", "TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME",
			"REFERENCED_TABLE_SCHEMA", "REFERENCED_TABLE_NAME", "REFERENCED_COLUMN_NAME", "ORDINAL_POSITION",
		}).AddRow(
			"learn", "fk_classes_student", "learn", "classes", "student_id",
			"learn", "students", "id", 1,
		))
	mock.ExpectCommit()

	snapshot, err := mysql.NewScanner().Scan(context.Background(), database, "learn")
	if err != nil {
		t.Fatalf("scan MySQL schema: %v", err)
	}
	if len(snapshot.Nodes) != 7 {
		t.Fatalf("node count = %d, want 7", len(snapshot.Nodes))
	}
	if snapshot.Nodes[0].QualifiedName != "mysql://learn" {
		t.Fatalf("database qualified name = %q, want unique MySQL root", snapshot.Nodes[0].QualifiedName)
	}

	var studentName catalog.NodeInput
	for _, node := range snapshot.Nodes {
		if node.QualifiedName == "learn.students.name" {
			studentName = node
			break
		}
	}
	if studentName.Kind != catalog.NodeColumn || studentName.DataType != "varchar(200)" || !studentName.Nullable || studentName.Ordinal != 2 {
		t.Fatalf("student name column = %#v", studentName)
	}
	if len(snapshot.ForeignKeys) != 1 {
		t.Fatalf("foreign key count = %d, want 1", len(snapshot.ForeignKeys))
	}
	foreignKey := snapshot.ForeignKeys[0]
	if foreignKey.Name != "fk_classes_student" ||
		foreignKey.SourceColumn != "learn.classes.student_id" ||
		foreignKey.TargetColumn != "learn.students.id" {
		t.Fatalf("foreign key = %#v", foreignKey)
	}
	mock.ExpectClose()
	if err := database.Close(); err != nil {
		t.Fatalf("close SQL mock: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestIncrementalScannerQueriesOnlyScopedAndReferencedTables(t *testing.T) {
	t.Parallel()

	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	mock.ExpectBegin()
	mock.ExpectQuery("FROM information_schema[.]SCHEMATA").
		WithArgs("learn").
		WillReturnRows(sqlmock.NewRows([]string{"SCHEMA_NAME"}).AddRow("learn"))
	mock.ExpectQuery("FROM information_schema[.]KEY_COLUMN_USAGE.*REFERENCED_TABLE_SCHEMA = [?].*TABLE_NAME IN").
		WithArgs("learn", "learn", "classes").
		WillReturnRows(sqlmock.NewRows([]string{
			"CONSTRAINT_SCHEMA", "CONSTRAINT_NAME", "TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME",
			"REFERENCED_TABLE_SCHEMA", "REFERENCED_TABLE_NAME", "REFERENCED_COLUMN_NAME", "ORDINAL_POSITION",
		}).AddRow(
			"learn", "fk_classes_student", "learn", "classes", "student_id",
			"learn", "students", "id", 1,
		))
	mock.ExpectQuery("FROM information_schema[.]TABLES.*TABLE_NAME IN").
		WithArgs("learn", "classes", "students").
		WillReturnRows(sqlmock.NewRows([]string{"TABLE_SCHEMA", "TABLE_NAME"}).
			AddRow("learn", "classes").
			AddRow("learn", "students"))
	mock.ExpectQuery("FROM information_schema[.]COLUMNS.*TABLE_NAME IN").
		WithArgs("learn", "classes", "students").
		WillReturnRows(sqlmock.NewRows([]string{
			"TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME", "COLUMN_TYPE", "IS_NULLABLE", "ORDINAL_POSITION",
		}).
			AddRow("learn", "classes", "student_id", "bigint", "NO", 1).
			AddRow("learn", "students", "id", "bigint", "NO", 1))
	mock.ExpectCommit()

	snapshot, err := mysql.NewScanner().ScanTables(
		context.Background(), database, "learn", []string{"learn.classes"},
	)
	if err != nil {
		t.Fatalf("scan incremental table scope: %v", err)
	}
	if len(snapshot.Nodes) != 6 || len(snapshot.ForeignKeys) != 1 {
		t.Fatalf("incremental snapshot nodes=%d foreignKeys=%d", len(snapshot.Nodes), len(snapshot.ForeignKeys))
	}
	qualified := make(map[string]struct{}, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		qualified[node.QualifiedName] = struct{}{}
	}
	for _, expected := range []string{"learn.classes", "learn.classes.student_id", "learn.students", "learn.students.id"} {
		if _, found := qualified[expected]; !found {
			t.Fatalf("incremental snapshot omitted %q: %#v", expected, snapshot.Nodes)
		}
	}
	if _, found := qualified["learn.audit_log"]; found {
		t.Fatal("incremental snapshot included out-of-scope table")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}
