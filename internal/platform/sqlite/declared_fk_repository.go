package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/relations"
)

const declaredForeignKeyActor = "dbgraph-schema-scanner"

type declaredForeignKeyMapping struct {
	StableKey           string
	RelationID          int64
	SourceQualifiedName string
	Present             bool
}

func (r *CatalogRepository) reconcileDeclaredForeignKeys(
	ctx context.Context,
	tx *sql.Tx,
	publication catalog.SnapshotPublication,
	nodeIDs map[string]int64,
) error {
	mappings, err := loadDeclaredForeignKeyMappings(ctx, tx, publication.DataSourceID)
	if err != nil {
		return err
	}
	qualifiedNodeIDs := make(map[string]int64, len(publication.Nodes))
	for _, node := range publication.Nodes {
		qualifiedNodeIDs[node.QualifiedName] = nodeIDs[node.StableKey]
	}
	seen := make(map[string]struct{}, len(publication.ForeignKeys))
	for _, foreignKey := range publication.ForeignKeys {
		stableKey := declaredForeignKeyStableKey(publication.DataSourceID, foreignKey)
		seen[stableKey] = struct{}{}
		sourceNodeID := qualifiedNodeIDs[foreignKey.SourceColumn]
		targetNodeID := qualifiedNodeIDs[foreignKey.TargetColumn]
		mapping, exists := mappings[stableKey]
		if !exists {
			relationID, err := r.insertDeclaredForeignKey(
				ctx, tx, publication, stableKey, sourceNodeID, targetNodeID, foreignKey,
			)
			if err != nil {
				return err
			}
			mapping = declaredForeignKeyMapping{StableKey: stableKey, RelationID: relationID, Present: true}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO declared_foreign_key_relations(
    data_source_id, stable_key, relation_id, source_node_id, target_node_id,
    constraint_schema, constraint_name, ordinal, last_seen_scan_run_id,
    is_present, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
`,
				publication.DataSourceID, stableKey, relationID, sourceNodeID, targetNodeID,
				foreignKey.ConstraintSchema, foreignKey.Name, foreignKey.Ordinal,
				publication.ScanRunID, formatTime(publication.StartedAt), formatTime(publication.StartedAt),
			); err != nil {
				return fmt.Errorf("insert declared foreign key mapping: %w", err)
			}
		} else {
			if !mapping.Present {
				if err := r.reactivateDeclaredForeignKey(
					ctx, tx, publication, mapping.RelationID, stableKey, sourceNodeID, targetNodeID, foreignKey,
				); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `
UPDATE declared_foreign_key_relations
SET last_seen_scan_run_id = ?, is_present = 1, updated_at = ?
WHERE data_source_id = ? AND stable_key = ?
`, publication.ScanRunID, formatTime(publication.StartedAt), publication.DataSourceID, stableKey); err != nil {
				return fmt.Errorf("refresh declared foreign key mapping: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_scan_foreign_keys(
    scan_run_id, data_source_id, stable_key, relation_id, source_node_id,
    target_node_id, constraint_schema, constraint_name, ordinal, recorded_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
			publication.ScanRunID, publication.DataSourceID, stableKey, mapping.RelationID,
			sourceNodeID, targetNodeID, foreignKey.ConstraintSchema, foreignKey.Name,
			foreignKey.Ordinal, formatTime(publication.StartedAt),
		); err != nil {
			return fmt.Errorf("insert schema scan foreign key fact: %w", err)
		}
	}
	for stableKey, mapping := range mappings {
		if !mapping.Present {
			continue
		}
		if _, exists := seen[stableKey]; exists {
			continue
		}
		if !nodeWithinTableScope(catalog.NodeColumn, mapping.SourceQualifiedName, publication.ScopeTables) {
			continue
		}
		if err := r.removeDeclaredForeignKey(ctx, tx, publication, mapping.RelationID, stableKey); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE declared_foreign_key_relations
SET is_present = 0, updated_at = ?
WHERE data_source_id = ? AND stable_key = ?
`, formatTime(publication.StartedAt), publication.DataSourceID, stableKey); err != nil {
			return fmt.Errorf("mark declared foreign key absent: %w", err)
		}
	}
	return nil
}

func loadDeclaredForeignKeyMappings(
	ctx context.Context,
	tx *sql.Tx,
	dataSourceID int64,
) (mappings map[string]declaredForeignKeyMapping, returnError error) {
	rows, err := tx.QueryContext(ctx, `
SELECT mapping.stable_key, mapping.relation_id, version.qualified_name, mapping.is_present
FROM declared_foreign_key_relations mapping
JOIN node_current current ON current.node_id = mapping.source_node_id
JOIN node_versions version ON version.id = current.version_id
WHERE mapping.data_source_id = ?
`, dataSourceID)
	if err != nil {
		return nil, fmt.Errorf("list declared foreign key mappings: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()
	mappings = make(map[string]declaredForeignKeyMapping)
	for rows.Next() {
		var mapping declaredForeignKeyMapping
		var present int
		if err := rows.Scan(
			&mapping.StableKey,
			&mapping.RelationID,
			&mapping.SourceQualifiedName,
			&present,
		); err != nil {
			return nil, fmt.Errorf("scan declared foreign key mapping: %w", err)
		}
		mapping.Present = present == 1
		mappings[mapping.StableKey] = mapping
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate declared foreign key mappings: %w", err)
	}
	return mappings, nil
}

func declaredForeignKeyIsPresent(ctx context.Context, tx *sql.Tx, relationID int64) (bool, error) {
	var present int
	err := tx.QueryRowContext(ctx, `
SELECT is_present
FROM declared_foreign_key_relations
WHERE relation_id = ?
`, relationID).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read declared foreign key presence: %w", err)
	}
	return present == 1, nil
}

func (r *CatalogRepository) insertDeclaredForeignKey(
	ctx context.Context,
	tx *sql.Tx,
	publication catalog.SnapshotPublication,
	stableKey string,
	sourceNodeID int64,
	targetNodeID int64,
	foreignKey catalog.DeclaredForeignKey,
) (int64, error) {
	relationID, err := r.ids.Next(ctx)
	if err != nil {
		return 0, err
	}
	versionID, err := r.ids.Next(ctx)
	if err != nil {
		return 0, err
	}
	eventID, err := r.ids.Next(ctx)
	if err != nil {
		return 0, err
	}
	auditID, err := r.ids.Next(ctx)
	if err != nil {
		return 0, err
	}
	revision := declaredForeignKeyRevision(
		versionID, relationID, 1, publication, stableKey, sourceNodeID, targetNodeID, foreignKey,
	)
	record := declaredForeignKeyRecord(stableKey, relationID, versionID, revision)
	if err := verifyRelationNodes(ctx, tx, revision, record.References); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO relations(id, relation_type, create_fingerprint, created_at)
VALUES (?, ?, ?, ?, ?)
`, relationID, relations.TypeDeclaredForeignKey, stableKey, formatTime(publication.StartedAt)); err != nil {
		return 0, fmt.Errorf("insert declared foreign key relation: %w", err)
	}
	if err := insertRelationVersion(ctx, tx, record); err != nil {
		return 0, err
	}
	requestID := declaredForeignKeyRequestID(publication.ScanRunID, stableKey, "create")
	if err := insertRelationEvent(
		ctx, tx, eventID, relationID, versionID,
		relations.EventApproved, declaredForeignKeyActor, audit.OriginSystem,
		"Declared foreign key observed in schema snapshot.", requestID, nil, publication.StartedAt,
	); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_current(
    relation_id, latest_revision_no, active_version_id,
    proposed_version_id, status, updated_at
) VALUES (?, 1, ?, NULL, ?, ?)
`, relationID, versionID, relations.StatusApproved, formatTime(publication.StartedAt)); err != nil {
		return 0, fmt.Errorf("publish declared foreign key current relation: %w", err)
	}
	relation := relations.Relation{ID: relationID, ProjectID: Type: relations.TypeDeclaredForeignKey}
	if err := insertEffectiveEdge(ctx, tx, relation, revision, publication.StartedAt); err != nil {
		return 0, err
	}
	if err := insertRelationAudit(
		ctx, tx, auditID, relationID, "DECLARED_FOREIGN_KEY_PUBLISHED",
		declaredForeignKeyActor, audit.OriginSystem, "Declared foreign key observed in schema snapshot.",
		requestID, nil, 1, publication.StartedAt,
	); err != nil {
		return 0, err
	}
	return relationID, nil
}

func (r *CatalogRepository) reactivateDeclaredForeignKey(
	ctx context.Context,
	tx *sql.Tx,
	publication catalog.SnapshotPublication,
	relationID int64,
	stableKey string,
	sourceNodeID int64,
	targetNodeID int64,
	foreignKey catalog.DeclaredForeignKey,
) error {
	current, err := getRelation(ctx, tx, relationID)
	if err != nil {
		return err
	}
	if current.Status == relations.StatusSuppressed {
		return nil
	}
	if current.Status != relations.StatusTombstoned || current.Active == nil || current.Proposed != nil {
		return relations.ErrInvalidTransition
	}
	versionID, approvedEventID, supersededEventID, auditID, err := r.nextFourIDs(ctx)
	if err != nil {
		return err
	}
	revision := declaredForeignKeyRevision(
		versionID, relationID, current.LatestRevisionNo+1, publication,
		stableKey, sourceNodeID, targetNodeID, foreignKey,
	)
	expected := current.LatestRevisionNo
	revision.ExpectedRevisionNo = &expected
	record := declaredForeignKeyRecord(stableKey, relationID, versionID, revision)
	if err := insertRelationVersion(ctx, tx, record); err != nil {
		return err
	}
	requestID := declaredForeignKeyRequestID(publication.ScanRunID, stableKey, "restore")
	if err := insertRelationEvent(ctx, tx, approvedEventID, relationID, versionID,
		relations.EventApproved, declaredForeignKeyActor, audit.OriginSystem,
		"Declared foreign key reappeared in schema snapshot.", requestID, expected, publication.StartedAt); err != nil {
		return err
	}
	if err := insertRelationEvent(ctx, tx, supersededEventID, relationID, current.Active.ID,
		relations.EventSuperseded, declaredForeignKeyActor, audit.OriginSystem,
		"Declared foreign key reappeared in schema snapshot.", requestID, expected, publication.StartedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE relation_current
SET latest_revision_no = ?, active_version_id = ?, proposed_version_id = NULL,
    status = ?, updated_at = ?
WHERE relation_id = ?
`, revision.RevisionNo, versionID, relations.StatusApproved, formatTime(publication.StartedAt), relationID); err != nil {
		return fmt.Errorf("reactivate declared foreign key relation: %w", err)
	}
	relation := relations.Relation{ID: relationID, ProjectID: Type: relations.TypeDeclaredForeignKey}
	if err := insertEffectiveEdge(ctx, tx, relation, revision, publication.StartedAt); err != nil {
		return err
	}
	return insertRelationAudit(
		ctx, tx, auditID, relationID, "DECLARED_FOREIGN_KEY_RESTORED",
		declaredForeignKeyActor, audit.OriginSystem, "Declared foreign key reappeared in schema snapshot.",
		requestID, expected, revision.RevisionNo, publication.StartedAt,
	)
}

func (r *CatalogRepository) removeDeclaredForeignKey(
	ctx context.Context,
	tx *sql.Tx,
	publication catalog.SnapshotPublication,
	relationID int64,
	stableKey string,
) error {
	current, err := getRelation(ctx, tx, relationID)
	if err != nil {
		return err
	}
	if current.Status == relations.StatusSuppressed || current.Status == relations.StatusTombstoned {
		return nil
	}
	if current.Status != relations.StatusApproved || current.Active == nil || current.Proposed != nil {
		return relations.ErrInvalidTransition
	}
	versionID, tombstoneEventID, supersededEventID, auditID, err := r.nextFourIDs(ctx)
	if err != nil {
		return err
	}
	expected := current.LatestRevisionNo
	revision := *current.Active
	revision.ID = versionID
	revision.RevisionNo++
	revision.Kind = relations.ProposalTombstone
	revision.Actor = declaredForeignKeyActor
	revision.Origin = audit.OriginSystem
	revision.Reason = "Declared foreign key was absent from the latest schema snapshot."
	revision.RequestID = declaredForeignKeyRequestID(publication.ScanRunID, stableKey, "remove")
	revision.ExpectedRevisionNo = &expected
	revision.CreatedAt = publication.StartedAt
	record := declaredForeignKeyRecord(stableKey, relationID, versionID, revision)
	if err := insertRelationVersion(ctx, tx, record); err != nil {
		return err
	}
	if err := insertRelationEvent(ctx, tx, tombstoneEventID, relationID, versionID,
		relations.EventTombstoned, declaredForeignKeyActor, audit.OriginSystem,
		revision.Reason, revision.RequestID, expected, publication.StartedAt); err != nil {
		return err
	}
	if err := insertRelationEvent(ctx, tx, supersededEventID, relationID, current.Active.ID,
		relations.EventSuperseded, declaredForeignKeyActor, audit.OriginSystem,
		revision.Reason, revision.RequestID, expected, publication.StartedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE relation_current
SET latest_revision_no = ?, active_version_id = ?, proposed_version_id = NULL,
    status = ?, updated_at = ?
WHERE relation_id = ?
`, revision.RevisionNo, versionID, relations.StatusTombstoned, formatTime(publication.StartedAt), relationID); err != nil {
		return fmt.Errorf("tombstone declared foreign key relation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM effective_edges WHERE relation_id = ?", relationID); err != nil {
		return fmt.Errorf("remove declared foreign key effective edge: %w", err)
	}
	return insertRelationAudit(
		ctx, tx, auditID, relationID, "DECLARED_FOREIGN_KEY_TOMBSTONED",
		declaredForeignKeyActor, audit.OriginSystem, revision.Reason, revision.RequestID,
		expected, revision.RevisionNo, publication.StartedAt,
	)
}

func declaredForeignKeyRevision(
	versionID int64,
	relationID int64,
	revisionNo int,
	publication catalog.SnapshotPublication,
	stableKey string,
	sourceNodeID int64,
	targetNodeID int64,
	foreignKey catalog.DeclaredForeignKey,
) relations.Revision {
	return relations.Revision{
		ID: versionID, RelationID: relationID, RevisionNo: revisionNo,
		Kind: relations.ProposalContent, SourceNodeID: sourceNodeID, TargetNodeID: targetNodeID,
		Transform:  conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: sourceNodeID},
		Confidence: 1,
		Evidence: []relations.EvidenceInput{{
			Kind:         relations.EvidenceDatabaseConstraint,
			DataSourceID: publication.DataSourceID, ConstraintSchema: foreignKey.ConstraintSchema,
			ConstraintName: foreignKey.Name, ScanRunID: publication.ScanRunID,
		}},
		Actor: declaredForeignKeyActor, Origin: audit.OriginSystem,
		Reason:    "Declared foreign key observed in schema snapshot.",
		RequestID: declaredForeignKeyRequestID(publication.ScanRunID, stableKey, "content"),
		CreatedAt: publication.StartedAt,
	}
}

func declaredForeignKeyRecord(
	stableKey string,
	relationID int64,
	versionID int64,
	revision relations.Revision,
) relations.ProposalRecord {
	return relations.ProposalRecord{
		RelationID: relationID, VersionID: versionID, ProjectID: 
		Type: relations.TypeDeclaredForeignKey, Fingerprint: declaredForeignKeyContentFingerprint(revision),
		Revision:   revision,
		References: []relations.Reference{{NodeID: revision.SourceNodeID, Role: relations.ReferenceTransform}},
	}
}

func declaredForeignKeyStableKey(dataSourceID int64, foreignKey catalog.DeclaredForeignKey) string {
	return hashText(
		strconv.FormatInt(dataSourceID, 10), foreignKey.ConstraintSchema, foreignKey.Name,
		strconv.Itoa(foreignKey.Ordinal), foreignKey.SourceColumn, foreignKey.TargetColumn,
	)
}

func declaredForeignKeyContentFingerprint(revision relations.Revision) string {
	return hashText(
		strconv.Itoa(int(relations.TypeDeclaredForeignKey)),
		strconv.FormatInt(revision.SourceNodeID, 10), strconv.FormatInt(revision.TargetNodeID, 10),
		strconv.Itoa(int(revision.Kind)),
	)
}

func declaredForeignKeyRequestID(scanRunID int64, stableKey string, action string) string {
	return "scan:" + strconv.FormatInt(scanRunID, 10) + ":" + action + ":" + stableKey[:16]
}

func hashText(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (r *CatalogRepository) nextFourIDs(ctx context.Context) (int64, int64, int64, int64, error) {
	values := [4]int64{}
	for index := range values {
		value, err := r.ids.Next(ctx)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		values[index] = value
	}
	return values[0], values[1], values[2], values[3], nil
}
