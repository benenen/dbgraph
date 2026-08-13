package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
)

const (
	scanStatusRunning   = 1
	scanStatusSucceeded = 2
	scanStatusFailed    = 3
)

type catalogIDGenerator interface {
	Next(context.Context) (int64, error)
}

type reservingIDGenerator interface {
	Ensure(context.Context, int) error
}

type CatalogRepository struct {
	store *Store
	ids   catalogIDGenerator
}

func NewCatalogRepository(store *Store, ids catalogIDGenerator) *CatalogRepository {
	return &CatalogRepository{store: store, ids: ids}
}

func (r *CatalogRepository) CreateDataSource(
	ctx context.Context,
	source catalog.DataSource,
) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		return insertDataSource(ctx, tx, source)
	})
	if err != nil {
		return translateDataSourceWrite(err)
	}
	return nil
}

func (r *CatalogRepository) CreateDataSourceWithAudit(
	ctx context.Context,
	source catalog.DataSource,
	event audit.Event,
) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		if err := insertDataSource(ctx, tx, source); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, event)
	})
	if err != nil {
		return translateDataSourceWrite(err)
	}
	return nil
}

func insertDataSource(ctx context.Context, tx *sql.Tx, source catalog.DataSource) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO data_sources(
    id, name, source_kind, dsn_environment, dsn_key_id, dsn_ciphertext, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`,
		source.ID,
		source.Name,
		source.Kind,
		source.DSNEnvironment,
		optionalText(source.DSNKeyID),
		optionalBlob(source.DSNCiphertext),
		source.CreatedAt.Format(time.RFC3339Nano),
		source.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	return linkDataSource(ctx, tx, source.ID, source.CreatedAt)
}

// linkDataSource records that a project uses a data source. Linking twice is a
// no-op so a repeated request is harmless.
func linkDataSource(ctx context.Context, tx *sql.Tx, dataSourceID int64, at time.Time) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO project_data_sources(data_source_id, created_at)
VALUES (?, ?, ?)
ON CONFLICT(data_source_id) DO NOTHING
`, dataSourceID, at.Format(time.RFC3339Nano))
	return err
}

// ListAllDataSources returns the shared registry, so a project can adopt a
// source that already exists instead of registering it twice.
func (r *CatalogRepository) ListAllDataSources(
	ctx context.Context,
	limit int,
) (sources []catalog.DataSource, returnError error) {
	rows, err := r.store.db.QueryContext(ctx, `
SELECT id, name, source_kind, dsn_environment, dsn_key_id, dsn_ciphertext, created_at, updated_at
FROM data_sources
ORDER BY name, id
LIMIT ?
`, limit)
	if err != nil {
		return nil, fmt.Errorf("select data sources: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	for rows.Next() {
		source, err := scanDataSourceRow(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate data sources: %w", err)
	}
	return sources, nil
}

// LinkDataSource adopts an existing source into a project.
func (r *CatalogRepository) LinkDataSource(ctx context.Context, dataSourceID int64, at time.Time) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM data_sources WHERE id = ?)", dataSourceID).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return catalog.ErrDataSourceNotFound
		}
		return linkDataSource(ctx, tx, dataSourceID, at)
	})
	if err != nil {
		if errors.Is(err, catalog.ErrDataSourceNotFound) {
			return err
		}
		return fmt.Errorf("link data source: %w", err)
	}
	return nil
}

// UnlinkDataSource drops the adoption. The source and any catalog the project
// already scanned from it stay put; only the association goes.
func (r *CatalogRepository) UnlinkDataSource(ctx context.Context, dataSourceID int64) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM project_data_sources WHERE data_source_id = ?",
			dataSourceID)
		return err
	})
	if err != nil {
		return fmt.Errorf("unlink data source: %w", err)
	}
	return nil
}

// UpdateDataSourceWithAudit renames a source and optionally replaces its
// sealed DSN. A nil ciphertext leaves the stored credential exactly as it is,
// because the connection string is write-only and the caller cannot echo back
// what it does not know.
func (r *CatalogRepository) UpdateDataSourceWithAudit(
	ctx context.Context,
	source catalog.DataSource,
	replaceSecret bool,
	event audit.Event,
) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		var result sql.Result
		var err error
		if replaceSecret {
			result, err = tx.ExecContext(ctx, `
UPDATE data_sources
SET name = ?, dsn_environment = ?, dsn_key_id = ?, dsn_ciphertext = ?, updated_at = ?
WHERE id = ?
`, source.Name, source.DSNEnvironment, optionalText(source.DSNKeyID),
				optionalBlob(source.DSNCiphertext), source.UpdatedAt.Format(time.RFC3339Nano), source.ID)
		} else {
			result, err = tx.ExecContext(ctx, `
UPDATE data_sources SET name = ?, dsn_environment = ?, updated_at = ?
WHERE id = ?
`, source.Name, source.DSNEnvironment, source.UpdatedAt.Format(time.RFC3339Nano), source.ID)
		}
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return catalog.ErrDataSourceNotFound
		}
		return insertAuditEvent(ctx, tx, event)
	})
	if err != nil {
		if errors.Is(err, catalog.ErrDataSourceNotFound) {
			return err
		}
		return translateDataSourceWrite(err)
	}
	return nil
}

// DeleteDataSource removes a source, every project's link to it, and the scan
// runs that recorded the attempts to read it. It is refused only while catalog
// nodes exist, because those are the imported data itself and deleting the
// source would orphan them. A source whose scans all failed imported nothing,
// and that is exactly the misconfigured source an operator needs to remove.
func (r *CatalogRepository) DeleteDataSource(ctx context.Context, dataSourceID int64) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		var importedNodes int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM nodes WHERE data_source_id = ?", dataSourceID).Scan(&importedNodes); err != nil {
			return err
		}
		if importedNodes > 0 {
			return catalog.ErrDataSourceInUse
		}
		var referencedContent int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM relation_evidence WHERE data_source_id = ?",
			dataSourceID).Scan(&referencedContent); err != nil {
			return err
		}
		if referencedContent > 0 {
			return catalog.ErrDataSourceInUse
		}
		// Scan artifacts describe attempts to read a source that is going away.
		for _, table := range []string{
			"schema_scan_foreign_keys",
			"declared_foreign_key_relations",
			"schema_scan_runs",
		} {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM "+table+" WHERE data_source_id = ?", dataSourceID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM project_data_sources WHERE data_source_id = ?", dataSourceID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM data_sources WHERE id = ?", dataSourceID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return catalog.ErrDataSourceNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, catalog.ErrDataSourceInUse) || errors.Is(err, catalog.ErrDataSourceNotFound) {
			return err
		}
		return fmt.Errorf("delete data source: %w", err)
	}
	return nil
}

// GetProjectDataSource returns a data source only when the project links it,
// so a scan cannot run against a source the project never adopted.
func (r *CatalogRepository) GetProjectDataSource(
	ctx context.Context,
	dataSourceID int64,
) (catalog.DataSource, error) {
	var linked int
	if err := r.store.db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM project_data_sources WHERE data_source_id = ?)
`, dataSourceID).Scan(&linked); err != nil {
		return catalog.DataSource{}, fmt.Errorf("verify data source link: %w", err)
	}
	if linked != 1 {
		return catalog.DataSource{}, catalog.ErrDataSourceNotFound
	}
	return r.GetDataSource(ctx, dataSourceID)
}

func (r *CatalogRepository) GetDataSource(ctx context.Context, dataSourceID int64) (catalog.DataSource, error) {
	var source catalog.DataSource
	var createdAt string
	var updatedAt string
	var keyID sql.NullString
	var ciphertext []byte
	err := r.store.db.QueryRowContext(ctx, `
SELECT id, name, source_kind, dsn_environment, dsn_key_id, dsn_ciphertext, created_at, updated_at
FROM data_sources
WHERE id = ?
`, dataSourceID).Scan(
		&source.ID,
		&source.Name,
		&source.Kind,
		&source.DSNEnvironment,
		&keyID,
		&ciphertext,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.DataSource{}, catalog.ErrDataSourceNotFound
	}
	if err != nil {
		return catalog.DataSource{}, fmt.Errorf("select data source: %w", err)
	}
	source.DSNKeyID = keyID.String
	source.DSNCiphertext = ciphertext
	source.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return catalog.DataSource{}, fmt.Errorf("parse data source creation time: %w", err)
	}
	source.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return catalog.DataSource{}, fmt.Errorf("parse data source update time: %w", err)
	}
	return source, nil
}

func (r *CatalogRepository) BeginSchemaScan(ctx context.Context, run catalog.SchemaScanRun) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		if err := verifyDataSource(ctx, tx, run.DataSourceID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO schema_scan_runs(
    id, data_source_id, status, started_at
) VALUES (?, ?, ?, ?, ?)
`, run.ID, run.DataSourceID, scanStatusRunning, run.StartedAt.Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		return fmt.Errorf("begin schema scan run: %w", err)
	}
	return nil
}

func (r *CatalogRepository) FailSchemaScan(ctx context.Context, failure catalog.SchemaScanFailure) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
UPDATE schema_scan_runs
SET status = ?, error_code = ?, error_message = ?, completed_at = ?
WHERE id = ? AND data_source_id = ? AND status = ?
`,
			scanStatusFailed,
			failure.ErrorCode,
			failure.ErrorMessage,
			failure.CompletedAt.Format(time.RFC3339Nano),
			failure.Run.ID,
			failure.Run.DataSourceID,
			scanStatusRunning,
		)
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if updated != 1 {
			return catalog.ErrInvalidSnapshot
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("fail schema scan run: %w", err)
	}
	return nil
}

func (r *CatalogRepository) PublishSnapshot(
	ctx context.Context,
	publication catalog.SnapshotPublication,
) (catalog.PublishedSnapshot, error) {
	if reservingIDs, ok := r.ids.(reservingIDGenerator); ok {
		maximumIDs := (2 * len(publication.Nodes)) + catalog.MaximumSnapshotNodes +
			(4 * len(publication.ForeignKeys)) + (4 * catalog.MaximumSnapshotForeignKeys)
		if err := reservingIDs.Ensure(ctx, maximumIDs); err != nil {
			return catalog.PublishedSnapshot{}, fmt.Errorf("reserve schema snapshot IDs: %w", err)
		}
	}
	var published catalog.PublishedSnapshot
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		var err error
		published, err = r.publishSnapshot(ctx, tx, publication)
		return err
	})
	return published, err
}

func (r *CatalogRepository) publishSnapshot(
	ctx context.Context,
	tx *sql.Tx,
	publication catalog.SnapshotPublication,
) (catalog.PublishedSnapshot, error) {

	if err := verifyDataSource(ctx, tx, publication.DataSourceID); err != nil {
		return catalog.PublishedSnapshot{}, err
	}
	startedAt := publication.StartedAt.Format(time.RFC3339Nano)
	if publication.Prestarted {
		if err := verifyRunningSchemaScan(ctx, tx, publication); err != nil {
			return catalog.PublishedSnapshot{}, err
		}
	} else if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_scan_runs(
    id, data_source_id, status, started_at
) VALUES (?, ?, ?, ?, ?)
`,
		publication.ScanRunID,
		publication.DataSourceID,
		scanStatusRunning,
		startedAt,
	); err != nil {
		return catalog.PublishedSnapshot{}, fmt.Errorf("insert schema scan run: %w", err)
	}

	nodeIDs, err := r.resolveNodeIDs(ctx, tx, publication)
	if err != nil {
		return catalog.PublishedSnapshot{}, err
	}
	versionIDs := make(map[int64]int64, len(publication.Nodes))
	searchRecords := make(map[int64]currentNodeRecord, len(publication.Nodes))
	for _, input := range publication.Nodes {
		versionID, err := r.ids.Next(ctx)
		if err != nil {
			return catalog.PublishedSnapshot{}, fmt.Errorf("generate node version ID: %w", err)
		}
		parentNodeID := nullableNodeID(nodeIDs, input.ParentStableKey)
		metadata, err := catalog.EncodeNodeMetadata(input.Indexes)
		if err != nil {
			return catalog.PublishedSnapshot{}, fmt.Errorf("encode node metadata: %w", err)
		}
		if err := insertNodeVersion(
			ctx,
			tx,
			versionID,
			nodeIDs[input.StableKey],
			publication.ScanRunID,
			parentNodeID,
			catalog.NodeActive,
			input.Name,
			input.QualifiedName,
			input.DataType,
			input.Nullable,
			input.Ordinal,
			string(metadata),
			startedAt,
		); err != nil {
			return catalog.PublishedSnapshot{}, err
		}
		nodeID := nodeIDs[input.StableKey]
		versionIDs[nodeID] = versionID
		searchRecords[nodeID] = currentNodeRecord{
			ID:            nodeID,
			Name:          input.Name,
			QualifiedName: input.QualifiedName,
			DataType:      input.DataType,
		}
	}

	presentStableKeys := make(map[string]struct{}, len(publication.Nodes))
	for _, input := range publication.Nodes {
		presentStableKeys[input.StableKey] = struct{}{}
	}
	staleNodes, err := loadMissingCurrentNodes(
		ctx, tx, publication.DataSourceID, presentStableKeys, publication.ScopeTables,
	)
	if err != nil {
		return catalog.PublishedSnapshot{}, err
	}
	for _, staleNode := range staleNodes {
		versionID, err := r.ids.Next(ctx)
		if err != nil {
			return catalog.PublishedSnapshot{}, fmt.Errorf("generate stale node version ID: %w", err)
		}
		if err := insertNodeVersion(
			ctx,
			tx,
			versionID,
			staleNode.ID,
			publication.ScanRunID,
			staleNode.ParentNodeID,
			catalog.NodeStale,
			staleNode.Name,
			staleNode.QualifiedName,
			staleNode.DataType,
			staleNode.Nullable,
			staleNode.Ordinal,
			staleNodeMetadata(staleNode.Metadata),
			startedAt,
		); err != nil {
			return catalog.PublishedSnapshot{}, err
		}
		versionIDs[staleNode.ID] = versionID
	}

	for nodeID, versionID := range versionIDs {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO node_current(node_id, version_id, published_at)
VALUES (?, ?, ?)
ON CONFLICT(node_id) DO UPDATE SET
    version_id = excluded.version_id,
    published_at = excluded.published_at
`, nodeID, versionID, startedAt); err != nil {
			return catalog.PublishedSnapshot{}, fmt.Errorf("publish current node: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM node_search WHERE node_id = ?", nodeID); err != nil {
			return catalog.PublishedSnapshot{}, fmt.Errorf("delete current node search record: %w", err)
		}
		if record, active := searchRecords[nodeID]; active {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO node_search(node_id, name, qualified_name, data_type)
VALUES (?, ?, ?, ?, ?)
`, nodeID, record.Name, record.QualifiedName, record.DataType); err != nil {
				return catalog.PublishedSnapshot{}, fmt.Errorf("insert current node search record: %w", err)
			}
		}
	}
	if err := r.reconcileDeclaredForeignKeys(ctx, tx, publication, nodeIDs); err != nil {
		return catalog.PublishedSnapshot{}, err
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE schema_scan_runs
SET status = ?, node_count = ?, stale_count = ?, completed_at = ?
WHERE id = ?
`,
		scanStatusSucceeded,
		len(publication.Nodes),
		len(staleNodes),
		startedAt,
		publication.ScanRunID,
	); err != nil {
		return catalog.PublishedSnapshot{}, fmt.Errorf("complete schema scan run: %w", err)
	}

	return catalog.PublishedSnapshot{
		ScanRunID:   publication.ScanRunID,
		NodeCount:   len(publication.Nodes),
		StaleCount:  len(staleNodes),
		PublishedAt: publication.StartedAt,
	}, nil
}

func verifyRunningSchemaScan(
	ctx context.Context,
	tx *sql.Tx,
	publication catalog.SnapshotPublication,
) error {
	var found int
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1
    FROM schema_scan_runs
    WHERE id = ? AND data_source_id = ? AND status = ?
)
`, publication.ScanRunID, publication.DataSourceID, scanStatusRunning).Scan(&found); err != nil {
		return fmt.Errorf("verify running schema scan: %w", err)
	}
	if found != 1 {
		return catalog.ErrInvalidSnapshot
	}
	return nil
}

func (r *CatalogRepository) FindCurrentNode(
	ctx context.Context,
	dataSourceID int64,
	qualifiedName string,
) (catalog.Node, error) {
	var node catalog.Node
	var parentNodeID sql.NullInt64
	var nullable int
	var versionCreated string
	err := r.store.db.QueryRowContext(ctx, `
SELECT
    n.id, nv.id, n.n.data_source_id, nv.scan_run_id,
    nv.parent_node_id, n.kind, nv.status, n.stable_key, nv.name,
    nv.qualified_name, nv.data_type, nv.nullable, nv.ordinal_position,
    nv.created_at
FROM nodes n
JOIN node_current nc ON nc.node_id = n.id
JOIN node_versions nv ON nv.id = nc.version_id
WHERE n.data_source_id = ? AND nv.qualified_name = ?
ORDER BY n.id
LIMIT 1
`, dataSourceID, qualifiedName).Scan(
		&node.ID,
		&node.VersionID,
		&node.DataSourceID,
		&node.ScanRunID,
		&parentNodeID,
		&node.Kind,
		&node.Status,
		&node.StableKey,
		&node.Name,
		&node.QualifiedName,
		&node.DataType,
		&nullable,
		&node.Ordinal,
		&versionCreated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.Node{}, catalog.ErrNodeNotFound
	}
	if err != nil {
		return catalog.Node{}, fmt.Errorf("select current node: %w", err)
	}
	if parentNodeID.Valid {
		node.ParentNodeID = parentNodeID.Int64
	}
	node.Nullable = nullable == 1
	node.VersionCreated, err = time.Parse(time.RFC3339Nano, versionCreated)
	if err != nil {
		return catalog.Node{}, fmt.Errorf("parse node version time: %w", err)
	}
	return node, nil
}

func (r *CatalogRepository) SearchCurrentNodes(
	ctx context.Context,
	dataSourceID int64,
	query string,
	limit int,
) (nodes []catalog.Node, returnError error) {
	searchQuery := ftsPrefixQuery(query)
	sourceClause := ""
	arguments := []any{searchQuery, searchQuery, catalog.NodeActive}
	if dataSourceID > 0 {
		sourceClause = "\n  AND n.data_source_id = ?"
		arguments = append(arguments, dataSourceID)
	}
	arguments = append(arguments, limit)
	rows, err := r.store.db.QueryContext(ctx, `
WITH matched_nodes(node_id) AS (
    SELECT node_id
    FROM node_search
    WHERE node_search MATCH ?
    UNION
    SELECT node_id
    FROM relation_evidence_search
    WHERE relation_evidence_search MATCH ?
)
SELECT
    n.id, nv.id, n.n.data_source_id, nv.scan_run_id,
    nv.parent_node_id, n.kind, nv.status, n.stable_key, nv.name,
    nv.qualified_name, nv.data_type, nv.nullable, nv.ordinal_position,
    nv.created_at
FROM matched_nodes
JOIN nodes n ON n.id = matched_nodes.node_id
JOIN node_current nc ON nc.node_id = n.id
JOIN node_versions nv ON nv.id = nc.version_id
WHERE nv.status = ?`+sourceClause+`
ORDER BY length(nv.qualified_name), nv.qualified_name
LIMIT ?
`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("search current nodes: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	nodes = make([]catalog.Node, 0)
	for rows.Next() {
		node, err := scanCurrentNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current node search: %w", err)
	}
	return nodes, nil
}

func (r *CatalogRepository) GetCurrentNode(
	ctx context.Context,
	nodeID int64,
) (catalog.Node, error) {
	node, err := scanCurrentNode(r.store.db.QueryRowContext(ctx, `
SELECT
    n.id, nv.id, n.n.data_source_id, nv.scan_run_id,
    nv.parent_node_id, n.kind, nv.status, n.stable_key, nv.name,
    nv.qualified_name, nv.data_type, nv.nullable, nv.ordinal_position,
    nv.created_at
FROM nodes n
JOIN node_current nc ON nc.node_id = n.id
JOIN node_versions nv ON nv.id = nc.version_id
WHERE n.id = ?
`, nodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.Node{}, catalog.ErrNodeNotFound
	}
	if err != nil {
		return catalog.Node{}, fmt.Errorf("select current node by ID: %w", err)
	}
	return node, nil
}

func ftsPrefixQuery(query string) string {
	terms := strings.Fields(query)
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ReplaceAll(term, `"`, `""`)
		quoted = append(quoted, `"`+term+`"*`)
	}
	return strings.Join(quoted, " AND ")
}

func scanCurrentNode(scanner auditScanner) (catalog.Node, error) {
	var node catalog.Node
	var parentNodeID sql.NullInt64
	var nullable int
	var versionCreated string
	if err := scanner.Scan(
		&node.ID,
		&node.VersionID,
		&node.DataSourceID,
		&node.ScanRunID,
		&parentNodeID,
		&node.Kind,
		&node.Status,
		&node.StableKey,
		&node.Name,
		&node.QualifiedName,
		&node.DataType,
		&nullable,
		&node.Ordinal,
		&versionCreated,
	); err != nil {
		return catalog.Node{}, fmt.Errorf("scan current node: %w", err)
	}
	if parentNodeID.Valid {
		node.ParentNodeID = parentNodeID.Int64
	}
	node.Nullable = nullable == 1
	parsedTime, err := time.Parse(time.RFC3339Nano, versionCreated)
	if err != nil {
		return catalog.Node{}, fmt.Errorf("parse node version time: %w", err)
	}
	node.VersionCreated = parsedTime
	return node, nil
}

func verifyDataSource(ctx context.Context, tx *sql.Tx, dataSourceID int64) error {
	var found int
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM data_sources WHERE id = ?)
`, dataSourceID).Scan(&found); err != nil {
		return fmt.Errorf("verify data source: %w", err)
	}
	if found != 1 {
		return catalog.ErrInvalidSnapshot
	}
	return nil
}

func (r *CatalogRepository) resolveNodeIDs(
	ctx context.Context,
	tx *sql.Tx,
	publication catalog.SnapshotPublication,
) (map[string]int64, error) {
	nodeIDs := make(map[string]int64, len(publication.Nodes))
	rows, err := tx.QueryContext(ctx, `
SELECT stable_key, id
FROM nodes
WHERE data_source_id = ?
`, publication.DataSourceID)
	if err != nil {
		return nil, fmt.Errorf("list existing nodes: %w", err)
	}
	for rows.Next() {
		var stableKey string
		var nodeID int64
		if err := rows.Scan(&stableKey, &nodeID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan existing node: %w", err)
		}
		nodeIDs[stableKey] = nodeID
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close existing nodes: %w", err)
	}

	for _, input := range publication.Nodes {
		if _, exists := nodeIDs[input.StableKey]; exists {
			continue
		}
		nodeID, err := r.ids.Next(ctx)
		if err != nil {
			return nil, fmt.Errorf("generate node ID: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO nodes(id, data_source_id, stable_key, kind, created_at)
VALUES (?, ?, ?, ?, ?, ?)
`,
			nodeID,
			publication.DataSourceID,
			input.StableKey,
			input.Kind,
			publication.StartedAt.Format(time.RFC3339Nano),
		); err != nil {
			return nil, fmt.Errorf("insert catalog node: %w", err)
		}
		nodeIDs[input.StableKey] = nodeID
	}
	return nodeIDs, nil
}

func nullableNodeID(nodeIDs map[string]int64, stableKey string) any {
	if stableKey == "" {
		return nil
	}
	return nodeIDs[stableKey]
}

func insertNodeVersion(
	ctx context.Context,
	tx *sql.Tx,
	versionID int64,
	nodeID int64,
	scanRunID int64,
	parentNodeID any,
	status catalog.NodeStatus,
	name string,
	qualifiedName string,
	dataType string,
	nullable bool,
	ordinal int,
	metadata string,
	createdAt string,
) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO node_versions(
    id, node_id, scan_run_id, parent_node_id, status, name,
    qualified_name, data_type, nullable, ordinal_position, metadata_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		versionID,
		nodeID,
		scanRunID,
		parentNodeID,
		status,
		name,
		qualifiedName,
		dataType,
		nullable,
		ordinal,
		metadata,
		createdAt,
	)
	if err != nil {
		return fmt.Errorf("insert node version: %w", err)
	}
	return nil
}

type currentNodeRecord struct {
	ID            int64
	ParentNodeID  any
	Kind          catalog.NodeKind
	Name          string
	QualifiedName string
	DataType      string
	Nullable      bool
	Ordinal       int
	// Metadata carries forward on a stale version. Marking a node gone is not a
	// reason to forget what the last scan saw on it.
	Metadata string
}

// staleNodeMetadata keeps a stale version storable: the column is NOT NULL and
// must hold valid JSON, and a row written before metadata existed may be empty.
func staleNodeMetadata(stored string) string {
	if stored == "" {
		return "{}"
	}
	return stored
}

func loadMissingCurrentNodes(
	ctx context.Context,
	tx *sql.Tx,
	dataSourceID int64,
	presentStableKeys map[string]struct{},
	scopeTables []string,
) (missing []currentNodeRecord, returnError error) {
	rows, err := tx.QueryContext(ctx, `
SELECT
    n.id, n.stable_key, n.kind, nv.parent_node_id, nv.name,
    nv.qualified_name, nv.data_type, nv.nullable, nv.ordinal_position, nv.metadata_json
FROM nodes n
JOIN node_current nc ON nc.node_id = n.id
JOIN node_versions nv ON nv.id = nc.version_id
WHERE n.data_source_id = ? AND nv.status = ?
`, dataSourceID, catalog.NodeActive)
	if err != nil {
		return nil, fmt.Errorf("list current nodes: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	missing = make([]currentNodeRecord, 0)
	for rows.Next() {
		var record currentNodeRecord
		var stableKey string
		var parentNodeID sql.NullInt64
		var nullable int
		if err := rows.Scan(
			&record.ID,
			&stableKey,
			&record.Kind,
			&parentNodeID,
			&record.Name,
			&record.QualifiedName,
			&record.DataType,
			&nullable,
			&record.Ordinal,
			&record.Metadata,
		); err != nil {
			return nil, fmt.Errorf("scan current node: %w", err)
		}
		if _, present := presentStableKeys[stableKey]; present {
			continue
		}
		if !nodeWithinTableScope(record.Kind, record.QualifiedName, scopeTables) {
			continue
		}
		if parentNodeID.Valid {
			record.ParentNodeID = parentNodeID.Int64
		}
		record.Nullable = nullable == 1
		missing = append(missing, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current nodes: %w", err)
	}
	return missing, nil
}

func nodeWithinTableScope(kind catalog.NodeKind, qualifiedName string, scopeTables []string) bool {
	if len(scopeTables) == 0 {
		return true
	}
	for _, table := range scopeTables {
		if kind == catalog.NodeTable && qualifiedName == table {
			return true
		}
		if kind == catalog.NodeColumn && strings.HasPrefix(qualifiedName, table+".") {
			return true
		}
	}
	return false
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalBlob(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// translateDataSourceWrite turns the name collision into a client-facing error.
// Data source names are unique service-wide now that projects share them, so
// this is a routine mistake rather than an internal fault.
func translateDataSourceWrite(err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed: data_sources.name") {
		return catalog.ErrDataSourceNameTaken
	}
	return fmt.Errorf("insert data source: %w", err)
}

type rowScanner interface {
	Scan(destination ...any) error
}

func scanDataSourceRow(row rowScanner) (catalog.DataSource, error) {
	var source catalog.DataSource
	var createdAt string
	var updatedAt string
	var keyID sql.NullString
	var ciphertext []byte
	if err := row.Scan(
		&source.ID, &source.Name, &source.Kind,
		&source.DSNEnvironment, &keyID, &ciphertext, &createdAt, &updatedAt,
	); err != nil {
		return catalog.DataSource{}, fmt.Errorf("scan data source: %w", err)
	}
	source.DSNKeyID = keyID.String
	source.DSNCiphertext = ciphertext
	var err error
	if source.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return catalog.DataSource{}, fmt.Errorf("parse data source creation time: %w", err)
	}
	if source.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return catalog.DataSource{}, fmt.Errorf("parse data source update time: %w", err)
	}
	return source, nil
}
