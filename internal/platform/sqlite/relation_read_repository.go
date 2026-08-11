package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/relations"
)

func (r *RelationRepository) Get(ctx context.Context, relationID int64) (relations.Relation, error) {
	return getRelation(ctx, r.store.db, relationID)
}

func (r *RelationRepository) ListProposals(
	ctx context.Context,
	projectID int64,
	limit int,
) ([]relations.Relation, error) {
	rows, err := r.store.db.QueryContext(ctx, `
SELECT r.id
FROM relations r
JOIN relation_current rc ON rc.relation_id = r.id
WHERE r.project_id = ? AND rc.proposed_version_id IS NOT NULL
ORDER BY rc.updated_at, r.id
LIMIT ?
`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list relation proposals: %w", err)
	}
	ids := make([]int64, 0)
	for rows.Next() {
		var relationID int64
		if err := rows.Scan(&relationID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan relation proposal: %w", err)
		}
		ids = append(ids, relationID)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close relation proposal rows: %w", err)
	}
	proposals := make([]relations.Relation, 0, len(ids))
	for _, relationID := range ids {
		relation, err := r.Get(ctx, relationID)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, relation)
	}
	return proposals, nil
}

type relationReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func getRelation(ctx context.Context, reader relationReader, relationID int64) (relations.Relation, error) {
	var relation relations.Relation
	var activeVersionID sql.NullInt64
	var proposedVersionID sql.NullInt64
	var createdAt string
	var effective int
	err := reader.QueryRowContext(ctx, `
SELECT
    r.id, r.project_id, r.relation_type, rc.latest_revision_no,
    rc.status, rc.active_version_id, rc.proposed_version_id,
    r.created_at,
    EXISTS(SELECT 1 FROM effective_edges ee WHERE ee.relation_id = r.id)
FROM relations r
JOIN relation_current rc ON rc.relation_id = r.id
WHERE r.id = ?
`, relationID).Scan(
		&relation.ID,
		&relation.ProjectID,
		&relation.Type,
		&relation.LatestRevisionNo,
		&relation.Status,
		&activeVersionID,
		&proposedVersionID,
		&createdAt,
		&effective,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return relations.Relation{}, relations.ErrRelationNotFound
	}
	if err != nil {
		return relations.Relation{}, fmt.Errorf("select relation: %w", err)
	}
	parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return relations.Relation{}, fmt.Errorf("parse relation creation time: %w", err)
	}
	relation.CreatedAt = parsedCreatedAt
	relation.Effective = effective == 1
	if activeVersionID.Valid {
		active, err := loadRelationRevision(ctx, reader, activeVersionID.Int64)
		if err != nil {
			return relations.Relation{}, err
		}
		relation.Active = &active
	}
	if proposedVersionID.Valid {
		proposed, err := loadRelationRevision(ctx, reader, proposedVersionID.Int64)
		if err != nil {
			return relations.Relation{}, err
		}
		relation.Proposed = &proposed
	}
	return relation, nil
}

func loadRelationRevision(
	ctx context.Context,
	reader relationReader,
	versionID int64,
) (relations.Revision, error) {
	var revision relations.Revision
	var guardJSON sql.NullString
	var selectorJSON sql.NullString
	var transformJSON string
	var confidenceBPS int
	var expectedRevision sql.NullInt64
	var createdAt string
	var stale bool
	err := reader.QueryRowContext(ctx, `
SELECT
    rv.id, rv.relation_id, rv.revision_no, rv.proposal_kind,
    source.node_id, target.node_id,
    rv.guard_json, rv.selector_json, rv.transform_json, rv.confidence_bps,
    rv.actor, rv.origin, rv.reason, rv.request_id,
    rv.expected_revision_no, rv.created_at,
    EXISTS(SELECT 1 FROM relation_stale_versions stale WHERE stale.version_id = rv.id)
FROM relation_versions rv
JOIN relation_version_endpoints source
  ON source.version_id = rv.id AND source.endpoint_kind = ?
JOIN relation_version_endpoints target
  ON target.version_id = rv.id AND target.endpoint_kind = ?
WHERE rv.id = ?
`, endpointSource, endpointTarget, versionID).Scan(
		&revision.ID,
		&revision.RelationID,
		&revision.RevisionNo,
		&revision.Kind,
		&revision.SourceNodeID,
		&revision.TargetNodeID,
		&guardJSON,
		&selectorJSON,
		&transformJSON,
		&confidenceBPS,
		&revision.Actor,
		&revision.Origin,
		&revision.Reason,
		&revision.RequestID,
		&expectedRevision,
		&createdAt,
		&stale,
	)
	if err != nil {
		return relations.Revision{}, fmt.Errorf("select relation revision: %w", err)
	}
	if stale {
		revision.Kind = relations.ProposalStale
	}
	if guardJSON.Valid {
		var guard conditions.Boolean
		if err := json.Unmarshal([]byte(guardJSON.String), &guard); err != nil {
			return relations.Revision{}, fmt.Errorf("decode relation guard: %w", err)
		}
		revision.Guard = &guard
	}
	if selectorJSON.Valid {
		var selector conditions.Boolean
		if err := json.Unmarshal([]byte(selectorJSON.String), &selector); err != nil {
			return relations.Revision{}, fmt.Errorf("decode relation selector: %w", err)
		}
		revision.Selector = &selector
	}
	if err := json.Unmarshal([]byte(transformJSON), &revision.Transform); err != nil {
		return relations.Revision{}, fmt.Errorf("decode relation transform: %w", err)
	}
	revision.Confidence = float64(confidenceBPS) / 10_000
	if expectedRevision.Valid {
		expected := int(expectedRevision.Int64)
		revision.ExpectedRevisionNo = &expected
	}
	parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return relations.Revision{}, fmt.Errorf("parse relation revision time: %w", err)
	}
	revision.CreatedAt = parsedCreatedAt

	rows, err := reader.QueryContext(ctx, `
SELECT
    evidence_kind, repository_name, commit_hash, file_path, symbol, start_line, end_line,
    COALESCE(data_source_id, 0), constraint_schema, constraint_name, COALESCE(scan_run_id, 0)
FROM relation_evidence
WHERE version_id = ?
ORDER BY ordinal
`, versionID)
	if err != nil {
		return relations.Revision{}, fmt.Errorf("list relation evidence: %w", err)
	}
	for rows.Next() {
		var evidence relations.EvidenceInput
		if err := rows.Scan(
			&evidence.Kind,
			&evidence.Repository,
			&evidence.Commit,
			&evidence.File,
			&evidence.Symbol,
			&evidence.StartLine,
			&evidence.EndLine,
			&evidence.DataSourceID,
			&evidence.ConstraintSchema,
			&evidence.ConstraintName,
			&evidence.ScanRunID,
		); err != nil {
			_ = rows.Close()
			return relations.Revision{}, fmt.Errorf("scan relation evidence: %w", err)
		}
		revision.Evidence = append(revision.Evidence, evidence)
	}
	if err := rows.Close(); err != nil {
		return relations.Revision{}, fmt.Errorf("close relation evidence rows: %w", err)
	}
	return revision, nil
}
