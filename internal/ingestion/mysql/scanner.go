package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/benenen/dbgraph/internal/catalog"
)

const schemataQuery = `
SELECT SCHEMA_NAME
FROM information_schema.SCHEMATA
WHERE SCHEMA_NAME = ?
ORDER BY SCHEMA_NAME
`

const tablesQuery = `
SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_COMMENT
FROM information_schema.TABLES
WHERE TABLE_TYPE = 'BASE TABLE'
  AND TABLE_SCHEMA = ?
ORDER BY TABLE_SCHEMA, TABLE_NAME
`

const columnsQuery = `
SELECT
    TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, ORDINAL_POSITION,
    COLUMN_COMMENT
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = ?
ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION
`

// indexesQuery reads every index a table declares, in column order. An index is
// recorded on the table it belongs to rather than as a node of its own: the
// graph is about relations, and an index takes part in none of them. NON_UNIQUE
// and SEQ_IN_INDEX come back as the source declared them, so a composite index
// keeps the order that decides which prefixes it can serve.
const indexesQuery = `
SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX, COLUMN_NAME
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = ?
ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX
`

const foreignKeysQuery = `
SELECT
    CONSTRAINT_SCHEMA, CONSTRAINT_NAME, TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME,
    REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME,
    ORDINAL_POSITION
FROM information_schema.KEY_COLUMN_USAGE
WHERE REFERENCED_TABLE_NAME IS NOT NULL
  AND TABLE_SCHEMA = ?
  AND REFERENCED_TABLE_SCHEMA = ?
ORDER BY CONSTRAINT_SCHEMA, CONSTRAINT_NAME, ORDINAL_POSITION
`

var (
	ErrInvalidDatabaseName = errors.New("invalid database name")
	ErrSchemaScanLimit     = errors.New("schema scan exceeded configured limits")
)

const (
	maximumScanNodes       = catalog.MaximumSnapshotNodes
	maximumScanForeignKeys = catalog.MaximumSnapshotForeignKeys
	maximumScanBytes       = 50 << 20
)

type scanBudget struct {
	nodes       int
	foreignKeys int
	bytes       int
}

func (b *scanBudget) addNode(values ...string) error {
	b.nodes++
	return b.addBytes(values...)
}

func (b *scanBudget) addForeignKey(values ...string) error {
	b.foreignKeys++
	return b.addBytes(values...)
}

func (b *scanBudget) addBytes(values ...string) error {
	for _, value := range values {
		b.bytes += len(value)
	}
	if b.nodes > maximumScanNodes || b.foreignKeys > maximumScanForeignKeys || b.bytes > maximumScanBytes {
		return ErrSchemaScanLimit
	}
	return nil
}

type Scanner struct{}

func NewScanner() *Scanner {
	return &Scanner{}
}

func (s *Scanner) Scan(
	ctx context.Context,
	database *sql.DB,
	databaseName string,
) (catalog.ScannedSnapshot, error) {
	databaseName = strings.TrimSpace(databaseName)
	if databaseName == "" || len(databaseName) > 500 {
		return catalog.ScannedSnapshot{}, ErrInvalidDatabaseName
	}

	tx, err := database.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return catalog.ScannedSnapshot{}, fmt.Errorf("begin read-only schema scan: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	budget := &scanBudget{}
	if err := budget.addNode(databaseName); err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	nodes := []catalog.NodeInput{{
		StableKey:     "database:" + databaseName,
		Kind:          catalog.NodeDatabase,
		Name:          databaseName,
		QualifiedName: "mysql://" + databaseName,
	}}
	schemaNodes, err := readSchemata(ctx, tx, databaseName, budget)
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	nodes = append(nodes, schemaNodes...)
	tableNodes, err := readTables(ctx, tx, databaseName, budget)
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	indexes, err := readIndexes(ctx, tx, indexesQuery, []any{databaseName})
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	attachIndexes(tableNodes, indexes)
	nodes = append(nodes, tableNodes...)
	columnNodes, err := readColumns(ctx, tx, databaseName, budget)
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	nodes = append(nodes, columnNodes...)
	foreignKeys, err := readForeignKeys(ctx, tx, databaseName, budget)
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}

	if err := tx.Commit(); err != nil {
		return catalog.ScannedSnapshot{}, fmt.Errorf("commit read-only schema scan: %w", err)
	}
	return catalog.ScannedSnapshot{Nodes: nodes, ForeignKeys: foreignKeys}, nil
}

func (s *Scanner) ScanTables(
	ctx context.Context,
	database *sql.DB,
	databaseName string,
	qualifiedTables []string,
) (catalog.ScannedSnapshot, error) {
	databaseName = strings.TrimSpace(databaseName)
	tableNames, err := incrementalTableNames(databaseName, qualifiedTables)
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return catalog.ScannedSnapshot{}, fmt.Errorf("begin read-only incremental schema scan: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	budget := &scanBudget{}
	if err := budget.addNode(databaseName); err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	nodes := []catalog.NodeInput{{
		StableKey: "database:" + databaseName, Kind: catalog.NodeDatabase,
		Name: databaseName, QualifiedName: "mysql://" + databaseName,
	}}
	schemaNodes, err := readSchemata(ctx, tx, databaseName, budget)
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	nodes = append(nodes, schemaNodes...)
	foreignKeys, err := readForeignKeysForTables(ctx, tx, databaseName, tableNames, budget)
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	expandedTables := append([]string(nil), tableNames...)
	seenTables := make(map[string]struct{}, len(expandedTables))
	for _, tableName := range expandedTables {
		seenTables[tableName] = struct{}{}
	}
	for _, foreignKey := range foreignKeys {
		parts := strings.Split(foreignKey.TargetColumn, ".")
		if len(parts) != 3 || parts[0] != databaseName {
			continue
		}
		if _, exists := seenTables[parts[1]]; exists {
			continue
		}
		seenTables[parts[1]] = struct{}{}
		expandedTables = append(expandedTables, parts[1])
	}
	sort.Strings(expandedTables)
	tableNodes, err := readTablesForNames(ctx, tx, databaseName, expandedTables, budget)
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	scopedIndexes, err := readIndexes(ctx, tx, strings.Replace(
		indexesQuery,
		"ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX",
		"AND TABLE_NAME IN ("+sqlPlaceholders(len(expandedTables))+
			") ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX",
		1,
	), schemaTableArguments(databaseName, expandedTables))
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	attachIndexes(tableNodes, scopedIndexes)
	nodes = append(nodes, tableNodes...)
	columnNodes, err := readColumnsForTables(ctx, tx, databaseName, expandedTables, budget)
	if err != nil {
		return catalog.ScannedSnapshot{}, err
	}
	nodes = append(nodes, columnNodes...)
	if err := tx.Commit(); err != nil {
		return catalog.ScannedSnapshot{}, fmt.Errorf("commit read-only incremental schema scan: %w", err)
	}
	return catalog.ScannedSnapshot{Nodes: nodes, ForeignKeys: foreignKeys}, nil
}

func incrementalTableNames(databaseName string, qualifiedTables []string) ([]string, error) {
	if databaseName == "" || len(databaseName) > 500 || len(qualifiedTables) == 0 ||
		len(qualifiedTables) > catalog.MaximumIncrementalTables {
		return nil, ErrInvalidDatabaseName
	}
	names := make([]string, 0, len(qualifiedTables))
	seen := make(map[string]struct{}, len(qualifiedTables))
	for _, qualifiedTable := range qualifiedTables {
		parts := strings.Split(strings.TrimSpace(qualifiedTable), ".")
		if len(parts) != 2 || parts[0] != databaseName || parts[1] == "" || len(parts[1]) > 500 {
			return nil, ErrInvalidDatabaseName
		}
		if _, exists := seen[parts[1]]; exists {
			return nil, ErrInvalidDatabaseName
		}
		seen[parts[1]] = struct{}{}
		names = append(names, parts[1])
	}
	sort.Strings(names)
	return names, nil
}

func readSchemata(
	ctx context.Context,
	tx *sql.Tx,
	databaseName string,
	budget *scanBudget,
) (nodes []catalog.NodeInput, returnError error) {
	rows, err := tx.QueryContext(ctx, schemataQuery, databaseName)
	if err != nil {
		return nil, fmt.Errorf("query MySQL schemata: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	nodes = make([]catalog.NodeInput, 0)
	for rows.Next() {
		var schemaName string
		if err := rows.Scan(&schemaName); err != nil {
			return nil, fmt.Errorf("scan MySQL schema: %w", err)
		}
		if err := budget.addNode(schemaName); err != nil {
			return nil, err
		}
		nodes = append(nodes, catalog.NodeInput{
			StableKey:       "schema:" + schemaName,
			ParentStableKey: "database:" + databaseName,
			Kind:            catalog.NodeSchema,
			Name:            schemaName,
			QualifiedName:   schemaName,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate MySQL schemata: %w", err)
	}
	return nodes, nil
}

func readTables(ctx context.Context, tx *sql.Tx, databaseName string, budget *scanBudget) ([]catalog.NodeInput, error) {
	return readTablesQuery(ctx, tx, tablesQuery, []any{databaseName}, budget)
}

func readTablesForNames(
	ctx context.Context,
	tx *sql.Tx,
	databaseName string,
	tableNames []string,
	budget *scanBudget,
) ([]catalog.NodeInput, error) {
	query := strings.Replace(
		tablesQuery,
		"ORDER BY TABLE_SCHEMA, TABLE_NAME",
		"AND TABLE_NAME IN ("+sqlPlaceholders(len(tableNames))+") ORDER BY TABLE_SCHEMA, TABLE_NAME",
		1,
	)
	return readTablesQuery(ctx, tx, query, schemaTableArguments(databaseName, tableNames), budget)
}

func readTablesQuery(
	ctx context.Context,
	tx *sql.Tx,
	query string,
	arguments []any,
	budget *scanBudget,
) (nodes []catalog.NodeInput, returnError error) {
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query MySQL tables: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	nodes = make([]catalog.NodeInput, 0)
	for rows.Next() {
		var schemaName string
		var tableName string
		var comment sql.NullString
		if err := rows.Scan(&schemaName, &tableName, &comment); err != nil {
			return nil, fmt.Errorf("scan MySQL table: %w", err)
		}
		if err := budget.addNode(schemaName, tableName); err != nil {
			return nil, err
		}
		qualifiedName := schemaName + "." + tableName
		nodes = append(nodes, catalog.NodeInput{
			StableKey:       "table:" + qualifiedName,
			ParentStableKey: "schema:" + schemaName,
			Kind:            catalog.NodeTable,
			Name:            tableName,
			QualifiedName:   qualifiedName,
			Comment:         comment.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate MySQL tables: %w", err)
	}
	return nodes, nil
}

// attachIndexes moves each table's indexes onto its node. Tables are matched by
// qualified name, the same key the rest of the snapshot is keyed on.
func attachIndexes(tables []catalog.NodeInput, indexes map[string][]catalog.Index) {
	for position := range tables {
		found, ok := indexes[tables[position].QualifiedName]
		if !ok {
			continue
		}
		if len(found) > catalog.MaximumIndexesPerTable {
			found = found[:catalog.MaximumIndexesPerTable]
		}
		tables[position].Indexes = found
	}
}

// readIndexes groups STATISTICS rows, which arrive one row per indexed column,
// into one entry per index with its columns in declared order.
func readIndexes(
	ctx context.Context,
	tx *sql.Tx,
	query string,
	arguments []any,
) (grouped map[string][]catalog.Index, returnError error) {
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query MySQL indexes: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	grouped = map[string][]catalog.Index{}
	// position remembers where an index already sits, so its later columns
	// append to it rather than starting a second entry with the same name.
	position := map[string]int{}
	for rows.Next() {
		var schemaName, tableName, indexName string
		var nonUnique int
		var sequence int
		var columnName sql.NullString
		if err := rows.Scan(
			&schemaName, &tableName, &indexName, &nonUnique, &sequence, &columnName,
		); err != nil {
			return nil, fmt.Errorf("scan MySQL index: %w", err)
		}
		// A functional index has an expression instead of a column. Recording
		// the index without its columns would misdescribe what it covers.
		if !columnName.Valid {
			continue
		}
		qualifiedName := schemaName + "." + tableName
		key := qualifiedName + "\x00" + indexName
		at, seen := position[key]
		if !seen {
			if len(grouped[qualifiedName]) >= catalog.MaximumIndexesPerTable {
				continue
			}
			grouped[qualifiedName] = append(grouped[qualifiedName], catalog.Index{
				Name:    indexName,
				Unique:  nonUnique == 0,
				Primary: indexName == "PRIMARY",
			})
			at = len(grouped[qualifiedName]) - 1
			position[key] = at
		}
		index := grouped[qualifiedName][at]
		if len(index.Columns) >= catalog.MaximumIndexColumns {
			continue
		}
		index.Columns = append(index.Columns, columnName.String)
		grouped[qualifiedName][at] = index
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate MySQL indexes: %w", err)
	}
	return grouped, nil
}

func readColumns(ctx context.Context, tx *sql.Tx, databaseName string, budget *scanBudget) ([]catalog.NodeInput, error) {
	return readColumnsQuery(ctx, tx, columnsQuery, []any{databaseName}, budget)
}

func readColumnsForTables(
	ctx context.Context,
	tx *sql.Tx,
	databaseName string,
	tableNames []string,
	budget *scanBudget,
) ([]catalog.NodeInput, error) {
	query := strings.Replace(
		columnsQuery,
		"ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION",
		"AND TABLE_NAME IN ("+sqlPlaceholders(len(tableNames))+") ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION",
		1,
	)
	return readColumnsQuery(ctx, tx, query, schemaTableArguments(databaseName, tableNames), budget)
}

func readColumnsQuery(
	ctx context.Context,
	tx *sql.Tx,
	query string,
	arguments []any,
	budget *scanBudget,
) (nodes []catalog.NodeInput, returnError error) {
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query MySQL columns: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	nodes = make([]catalog.NodeInput, 0)
	for rows.Next() {
		var schemaName string
		var tableName string
		var columnName string
		var dataType string
		var nullable string
		var ordinal int
		var comment sql.NullString
		if err := rows.Scan(
			&schemaName,
			&tableName,
			&columnName,
			&dataType,
			&nullable,
			&ordinal,
			&comment,
		); err != nil {
			return nil, fmt.Errorf("scan MySQL column: %w", err)
		}
		if err := budget.addNode(schemaName, tableName, columnName, dataType); err != nil {
			return nil, err
		}
		tableQualifiedName := schemaName + "." + tableName
		qualifiedName := tableQualifiedName + "." + columnName
		nodes = append(nodes, catalog.NodeInput{
			StableKey:       "column:" + qualifiedName,
			ParentStableKey: "table:" + tableQualifiedName,
			Kind:            catalog.NodeColumn,
			Name:            columnName,
			QualifiedName:   qualifiedName,
			DataType:        dataType,
			Nullable:        nullable == "YES",
			Ordinal:         ordinal,
			Comment:         comment.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate MySQL columns: %w", err)
	}
	return nodes, nil
}

func readForeignKeys(ctx context.Context, tx *sql.Tx, databaseName string, budget *scanBudget) ([]catalog.DeclaredForeignKey, error) {
	return readForeignKeysQuery(ctx, tx, foreignKeysQuery, []any{databaseName, databaseName}, databaseName, budget)
}

func readForeignKeysForTables(
	ctx context.Context,
	tx *sql.Tx,
	databaseName string,
	tableNames []string,
	budget *scanBudget,
) ([]catalog.DeclaredForeignKey, error) {
	query := strings.Replace(
		foreignKeysQuery,
		"ORDER BY CONSTRAINT_SCHEMA, CONSTRAINT_NAME, ORDINAL_POSITION",
		"AND TABLE_NAME IN ("+sqlPlaceholders(len(tableNames))+") ORDER BY CONSTRAINT_SCHEMA, CONSTRAINT_NAME, ORDINAL_POSITION",
		1,
	)
	return readForeignKeysQuery(ctx, tx, query, foreignKeyArguments(databaseName, tableNames), databaseName, budget)
}

func readForeignKeysQuery(
	ctx context.Context,
	tx *sql.Tx,
	query string,
	arguments []any,
	databaseName string,
	budget *scanBudget,
) (foreignKeys []catalog.DeclaredForeignKey, returnError error) {
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query MySQL foreign keys: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	foreignKeys = make([]catalog.DeclaredForeignKey, 0)
	for rows.Next() {
		var constraintSchema string
		var constraintName string
		var tableSchema string
		var tableName string
		var columnName string
		var referencedSchema string
		var referencedTable string
		var referencedColumn string
		var ordinal int
		if err := rows.Scan(
			&constraintSchema,
			&constraintName,
			&tableSchema,
			&tableName,
			&columnName,
			&referencedSchema,
			&referencedTable,
			&referencedColumn,
			&ordinal,
		); err != nil {
			return nil, fmt.Errorf("scan MySQL foreign key: %w", err)
		}
		if referencedSchema != databaseName {
			continue
		}
		if err := budget.addForeignKey(constraintSchema, constraintName, tableSchema, tableName, columnName, referencedSchema, referencedTable, referencedColumn); err != nil {
			return nil, err
		}
		foreignKeys = append(foreignKeys, catalog.DeclaredForeignKey{
			ConstraintSchema: constraintSchema,
			Name:             constraintName,
			SourceColumn:     tableSchema + "." + tableName + "." + columnName,
			TargetColumn:     referencedSchema + "." + referencedTable + "." + referencedColumn,
			Ordinal:          ordinal,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate MySQL foreign keys: %w", err)
	}
	return foreignKeys, nil
}

func sqlPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func schemaTableArguments(databaseName string, tableNames []string) []any {
	arguments := make([]any, 0, len(tableNames)+1)
	arguments = append(arguments, databaseName)
	for _, tableName := range tableNames {
		arguments = append(arguments, tableName)
	}
	return arguments
}

func foreignKeyArguments(databaseName string, tableNames []string) []any {
	arguments := make([]any, 0, len(tableNames)+2)
	arguments = append(arguments, databaseName, databaseName)
	for _, tableName := range tableNames {
		arguments = append(arguments, tableName)
	}
	return arguments
}
