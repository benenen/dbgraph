package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/relations"
)

const (
	endpointSource = 1
	endpointTarget = 2
)

type RelationRepository struct {
	store *Store
}

func NewRelationRepository(store *Store) *RelationRepository {
	return &RelationRepository{store: store}
}

func (r *RelationRepository) ProposeCreate(
	ctx context.Context,
	record relations.ProposalRecord,
) (relations.Relation, error) {
	var created relations.Relation
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		var err error
		created, err = insertCreateProposal(ctx, tx, record)
		return err
	})
	return created, err
}

func insertCreateProposal(
	ctx context.Context,
	tx *sql.Tx,
	record relations.ProposalRecord,
) (relations.Relation, error) {
	var existingID int64
	err := tx.QueryRowContext(ctx, `
SELECT id FROM relations WHERE create_fingerprint = ?
`, record.ProjectID, record.Fingerprint).Scan(&existingID)
	if err == nil {
		return relations.Relation{}, relations.ErrDuplicateRelation
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return relations.Relation{}, fmt.Errorf("check duplicate relation: %w", err)
	}
	if err := verifyRelationNodes(ctx, tx, record.ProjectID, record.Revision, record.References); err != nil {
		return relations.Relation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO relations(id, relation_type, create_fingerprint, created_at)
VALUES (?, ?, ?, ?, ?)
`, record.RelationID, record.ProjectID, record.Type, record.Fingerprint, formatTime(record.Revision.CreatedAt)); err != nil {
		return relations.Relation{}, fmt.Errorf("insert relation: %w", err)
	}
	if err := insertRelationVersion(ctx, tx, record); err != nil {
		return relations.Relation{}, err
	}
	if err := insertRelationEvent(
		ctx,
		tx,
		record.EventID,
		record.RelationID,
		record.VersionID,
		relations.EventProposed,
		record.Revision.Actor,
		record.Revision.Origin,
		record.Revision.Reason,
		record.Revision.RequestID,
		nil,
		record.Revision.CreatedAt,
	); err != nil {
		return relations.Relation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_current(
    relation_id, latest_revision_no, active_version_id,
    proposed_version_id, status, updated_at
) VALUES (?, 1, NULL, ?, ?, ?)
`, record.RelationID, record.VersionID, relations.StatusPending, formatTime(record.Revision.CreatedAt)); err != nil {
		return relations.Relation{}, fmt.Errorf("insert current relation projection: %w", err)
	}
	if err := insertRelationAudit(
		ctx,
		tx,
		record.AuditID,
		record.RelationID,
		"RELATION_PROPOSED",
		record.Revision.Actor,
		record.Revision.Origin,
		record.Revision.Reason,
		record.Revision.RequestID,
		nil,
		record.Revision.RevisionNo,
		record.Revision.CreatedAt,
	); err != nil {
		return relations.Relation{}, err
	}
	return getRelation(ctx, tx, record.RelationID)
}

func (r *RelationRepository) ProposeRevision(
	ctx context.Context,
	record relations.ProposalRecord,
) (relations.Relation, error) {
	var revised relations.Relation
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		var err error
		revised, err = insertRevisionProposal(ctx, tx, record)
		return err
	})
	return revised, err
}

func insertRevisionProposal(
	ctx context.Context,
	tx *sql.Tx,
	record relations.ProposalRecord,
) (relations.Relation, error) {
	current, err := getRelation(ctx, tx, record.Revision.RelationID)
	if err != nil {
		return relations.Relation{}, err
	}
	expected := 0
	if record.Revision.ExpectedRevisionNo != nil {
		expected = *record.Revision.ExpectedRevisionNo
	}
	if expected != current.LatestRevisionNo {
		return relations.Relation{}, relationRevisionConflict(current)
	}
	if current.Proposed != nil {
		return relations.Relation{}, relations.ErrPendingProposal
	}
	if record.Revision.RevisionNo != current.LatestRevisionNo+1 || record.ProjectID != current.ProjectID {
		return relations.Relation{}, relations.ErrInvalidCommand
	}
	if err := verifyRelationNodes(ctx, tx, current.ProjectID, record.Revision, record.References); err != nil {
		return relations.Relation{}, err
	}
	if err := insertRelationVersion(ctx, tx, record); err != nil {
		return relations.Relation{}, err
	}
	if err := insertRelationEvent(
		ctx,
		tx,
		record.EventID,
		current.ID,
		record.VersionID,
		relations.EventProposed,
		record.Revision.Actor,
		record.Revision.Origin,
		record.Revision.Reason,
		record.Revision.RequestID,
		record.Revision.ExpectedRevisionNo,
		record.Revision.CreatedAt,
	); err != nil {
		return relations.Relation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE relation_current
SET latest_revision_no = ?, proposed_version_id = ?, updated_at = ?
WHERE relation_id = ?
`, record.Revision.RevisionNo, record.VersionID, formatTime(record.Revision.CreatedAt), current.ID); err != nil {
		return relations.Relation{}, fmt.Errorf("publish pending relation revision: %w", err)
	}
	if err := insertRelationAudit(
		ctx,
		tx,
		record.AuditID,
		current.ID,
		auditActionForProposal(record.Revision.Kind),
		record.Revision.Actor,
		record.Revision.Origin,
		record.Revision.Reason,
		record.Revision.RequestID,
		record.Revision.ExpectedRevisionNo,
		record.Revision.RevisionNo,
		record.Revision.CreatedAt,
	); err != nil {
		return relations.Relation{}, err
	}
	return getRelation(ctx, tx, current.ID)
}

func (r *RelationRepository) Review(
	ctx context.Context,
	record relations.ReviewRecord,
) (relations.Relation, error) {
	var reviewed relations.Relation
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		current, err := getRelation(ctx, tx, record.RelationID)
		if err != nil {
			return err
		}
		if record.ExpectedRevisionNo != current.LatestRevisionNo {
			return relationRevisionConflict(current)
		}
		if current.Proposed == nil || current.Proposed.RevisionNo != record.ExpectedRevisionNo {
			return relations.ErrInvalidTransition
		}

		eventType := relations.EventRejected
		action := "RELATION_REJECTED"
		if record.Decision == relations.DecisionApprove {
			switch current.Proposed.Kind {
			case relations.ProposalTombstone:
				eventType = relations.EventTombstoned
				action = "RELATION_TOMBSTONED"
			case relations.ProposalStale:
				eventType = relations.EventStale
				action = "RELATION_STALE"
			default:
				eventType = relations.EventApproved
				action = "RELATION_APPROVED"
			}
		}
		if err := insertRelationEvent(
			ctx,
			tx,
			record.EventID,
			current.ID,
			current.Proposed.ID,
			eventType,
			record.Principal.Actor,
			record.Principal.Origin,
			record.Reason,
			record.RequestID,
			record.ExpectedRevisionNo,
			record.OccurredAt,
		); err != nil {
			return err
		}

		if record.Decision == relations.DecisionReject {
			if _, err := tx.ExecContext(ctx, `
UPDATE relation_current
SET proposed_version_id = NULL, updated_at = ?
WHERE relation_id = ?
`, formatTime(record.OccurredAt), current.ID); err != nil {
				return fmt.Errorf("reject relation proposal: %w", err)
			}
		} else {
			if current.Active != nil {
				if err := insertRelationEvent(
					ctx,
					tx,
					record.SupersededEventID,
					current.ID,
					current.Active.ID,
					relations.EventSuperseded,
					record.Principal.Actor,
					record.Principal.Origin,
					record.Reason,
					record.RequestID,
					record.ExpectedRevisionNo,
					record.OccurredAt,
				); err != nil {
					return err
				}
			}
			status := relations.StatusApproved
			switch current.Proposed.Kind {
			case relations.ProposalTombstone:
				status = relations.StatusTombstoned
			case relations.ProposalStale:
				status = relations.StatusStale
			}
			if _, err := tx.ExecContext(ctx, `
UPDATE relation_current
SET active_version_id = proposed_version_id,
    proposed_version_id = NULL,
    status = ?,
    updated_at = ?
WHERE relation_id = ?
`, status, formatTime(record.OccurredAt), current.ID); err != nil {
				return fmt.Errorf("approve relation proposal: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM effective_edges WHERE relation_id = ?", current.ID); err != nil {
				return fmt.Errorf("remove previous effective relation edge: %w", err)
			}
			if status == relations.StatusApproved {
				if err := insertEffectiveEdge(ctx, tx, current, *current.Proposed, record.OccurredAt); err != nil {
					return err
				}
			}
		}
		if err := insertRelationAudit(
			ctx,
			tx,
			record.AuditID,
			current.ID,
			action,
			record.Principal.Actor,
			record.Principal.Origin,
			record.Reason,
			record.RequestID,
			record.ExpectedRevisionNo,
			record.ExpectedRevisionNo,
			record.OccurredAt,
		); err != nil {
			return err
		}
		reviewed, err = getRelation(ctx, tx, current.ID)
		return err
	})
	return reviewed, err
}

func (r *RelationRepository) Suppress(
	ctx context.Context,
	record relations.StateRecord,
) (relations.Relation, error) {
	return r.changeState(ctx, record, true)
}

func (r *RelationRepository) Restore(
	ctx context.Context,
	record relations.StateRecord,
) (relations.Relation, error) {
	return r.changeState(ctx, record, false)
}

func (r *RelationRepository) changeState(
	ctx context.Context,
	record relations.StateRecord,
	suppress bool,
) (relations.Relation, error) {
	var changed relations.Relation
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		current, err := getRelation(ctx, tx, record.RelationID)
		if err != nil {
			return err
		}
		if record.ExpectedRevisionNo != current.LatestRevisionNo {
			return relationRevisionConflict(current)
		}
		if current.Active == nil || current.Status == relations.StatusTombstoned || current.Proposed != nil {
			return relations.ErrInvalidTransition
		}
		if suppress && current.Status != relations.StatusApproved {
			return relations.ErrInvalidTransition
		}
		if !suppress && current.Status != relations.StatusSuppressed {
			return relations.ErrInvalidTransition
		}
		if !suppress && current.Type == relations.TypeDeclaredForeignKey {
			present, err := declaredForeignKeyIsPresent(ctx, tx, current.ID)
			if err != nil {
				return err
			}
			if !present {
				return relations.ErrInvalidTransition
			}
		}

		status := relations.StatusApproved
		eventType := relations.EventRestored
		action := "RELATION_RESTORED"
		if suppress {
			status = relations.StatusSuppressed
			eventType = relations.EventSuppressed
			action = "RELATION_SUPPRESSED"
			if _, err := tx.ExecContext(ctx, `
INSERT INTO suppression_rules(
    id, relation_id, approved_version_id, actor, origin,
    reason, request_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`,
				record.EventID,
				current.ID,
				current.Active.ID,
				record.Principal.Actor,
				record.Principal.Origin,
				record.Reason,
				record.RequestID,
				formatTime(record.OccurredAt),
			); err != nil {
				return fmt.Errorf("insert relation suppression rule: %w", err)
			}
		}
		if err := insertRelationEvent(
			ctx,
			tx,
			record.EventID,
			current.ID,
			current.Active.ID,
			eventType,
			record.Principal.Actor,
			record.Principal.Origin,
			record.Reason,
			record.RequestID,
			record.ExpectedRevisionNo,
			record.OccurredAt,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE relation_current SET status = ?, updated_at = ? WHERE relation_id = ?
`, status, formatTime(record.OccurredAt), current.ID); err != nil {
			return fmt.Errorf("change relation suppression state: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM effective_edges WHERE relation_id = ?", current.ID); err != nil {
			return fmt.Errorf("remove suppressed relation edge: %w", err)
		}
		if !suppress {
			if err := insertEffectiveEdge(ctx, tx, current, *current.Active, record.OccurredAt); err != nil {
				return err
			}
		}
		if err := insertRelationAudit(
			ctx,
			tx,
			record.AuditID,
			current.ID,
			action,
			record.Principal.Actor,
			record.Principal.Origin,
			record.Reason,
			record.RequestID,
			record.ExpectedRevisionNo,
			record.ExpectedRevisionNo,
			record.OccurredAt,
		); err != nil {
			return err
		}
		changed, err = getRelation(ctx, tx, current.ID)
		return err
	})
	return changed, err
}

func relationRevisionConflict(current relations.Relation) error {
	return &relations.RevisionConflictError{
		CurrentRevisionNo: current.LatestRevisionNo,
		Current:           &current,
	}
}

func insertRelationVersion(
	ctx context.Context,
	tx *sql.Tx,
	record relations.ProposalRecord,
) error {
	guardJSON, err := marshalOptionalBoolean(record.Revision.Guard)
	if err != nil {
		return err
	}
	selectorJSON, err := marshalOptionalBoolean(record.Revision.Selector)
	if err != nil {
		return err
	}
	transformJSON, err := json.Marshal(record.Revision.Transform)
	if err != nil {
		return fmt.Errorf("encode relation transform: %w", err)
	}
	confidenceBPS := int(math.Round(record.Revision.Confidence * 10_000))
	if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_versions(
    id, relation_id, revision_no, proposal_kind, confidence_bps,
    guard_json, selector_json, transform_json, content_fingerprint,
    actor, origin, reason, request_id, expected_revision_no, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		record.VersionID,
		record.Revision.RelationID,
		record.Revision.RevisionNo,
		storedProposalKind(record.Revision.Kind),
		confidenceBPS,
		guardJSON,
		selectorJSON,
		string(transformJSON),
		record.Fingerprint,
		record.Revision.Actor,
		record.Revision.Origin,
		record.Revision.Reason,
		record.Revision.RequestID,
		record.Revision.ExpectedRevisionNo,
		formatTime(record.Revision.CreatedAt),
	); err != nil {
		return fmt.Errorf("insert relation revision: %w", err)
	}
	if record.Revision.Kind == relations.ProposalStale {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_stale_versions(version_id) VALUES (?)
`, record.VersionID); err != nil {
			return fmt.Errorf("mark stale relation revision: %w", err)
		}
	}
	for endpointKind, nodeID := range map[int]int64{
		endpointSource: record.Revision.SourceNodeID,
		endpointTarget: record.Revision.TargetNodeID,
	} {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_version_endpoints(version_id, endpoint_kind, node_id)
VALUES (?, ?, ?)
`, record.VersionID, endpointKind, nodeID); err != nil {
			return fmt.Errorf("insert relation endpoint: %w", err)
		}
	}
	for _, reference := range record.References {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_references(version_id, reference_kind, node_id)
VALUES (?, ?, ?)
`, record.VersionID, reference.Role, reference.NodeID); err != nil {
			return fmt.Errorf("insert relation reference: %w", err)
		}
	}
	for index, evidence := range record.Revision.Evidence {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_evidence(
    version_id, ordinal, evidence_kind, repository_name, commit_hash,
    file_path, symbol, start_line, end_line, data_source_id,
    constraint_schema, constraint_name, scan_run_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
			record.VersionID,
			index+1,
			evidence.Kind,
			evidence.Repository,
			evidence.Commit,
			evidence.File,
			evidence.Symbol,
			evidence.StartLine,
			evidence.EndLine,
			nullablePositiveInt64(evidence.DataSourceID),
			evidence.ConstraintSchema,
			evidence.ConstraintName,
			nullablePositiveInt64(evidence.ScanRunID),
		); err != nil {
			return fmt.Errorf("insert relation evidence: %w", err)
		}
	}
	return nil
}

func nullablePositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func verifyRelationNodes(
	ctx context.Context,
	tx *sql.Tx,
	revision relations.Revision,
	references []relations.Reference,
) error {
	nodeSet := map[int64]struct{}{
		revision.SourceNodeID: {},
		revision.TargetNodeID: {},
	}
	for _, reference := range references {
		nodeSet[reference.NodeID] = struct{}{}
	}
	placeholders := make([]string, 0, len(nodeSet))
	arguments := make([]any, 0, len(nodeSet)+3)
	arguments = append(arguments, catalog.NodeColumn, catalog.NodeActive)
	for nodeID := range nodeSet {
		placeholders = append(placeholders, "?")
		arguments = append(arguments, nodeID)
	}
	var count int
	query := `
SELECT COUNT(*)
FROM nodes n
JOIN node_current nc ON nc.node_id = n.id
JOIN node_versions nv ON nv.id = nc.version_id
WHERE n.n.kind = ? AND nv.status = ?
  AND n.id IN (` + strings.Join(placeholders, ",") + `)
`
	if err := tx.QueryRowContext(ctx, query, arguments...).Scan(&count); err != nil {
		return fmt.Errorf("verify relation node references: %w", err)
	}
	if count != len(nodeSet) {
		return relations.ErrInvalidCommand
	}
	return nil
}

func insertEffectiveEdge(
	ctx context.Context,
	tx *sql.Tx,
	relation relations.Relation,
	revision relations.Revision,
	publishedAt time.Time,
) error {
	guardJSON, err := marshalOptionalBoolean(revision.Guard)
	if err != nil {
		return err
	}
	selectorJSON, err := marshalOptionalBoolean(revision.Selector)
	if err != nil {
		return err
	}
	transformJSON, err := json.Marshal(revision.Transform)
	if err != nil {
		return fmt.Errorf("encode effective relation transform: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO effective_edges(
    relation_id, version_id, source_node_id, target_node_id,
    relation_type, guard_json, selector_json, transform_json,
    confidence_bps, published_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		relation.ID,
		revision.ID,
		revision.SourceNodeID,
		revision.TargetNodeID,
		relation.Type,
		guardJSON,
		selectorJSON,
		string(transformJSON),
		int(math.Round(revision.Confidence*10_000)),
		formatTime(publishedAt),
	); err != nil {
		return fmt.Errorf("insert effective relation edge: %w", err)
	}
	return nil
}

func insertRelationEvent(
	ctx context.Context,
	tx *sql.Tx,
	eventID int64,
	relationID int64,
	versionID any,
	eventType relations.EventType,
	actor string,
	origin any,
	reason string,
	requestID string,
	expectedRevision any,
	occurredAt time.Time,
) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_events(
    id, relation_id, version_id, event_type, actor, origin,
    reason, request_id, expected_revision_no, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		eventID,

		relationID,
		versionID,
		eventType,
		actor,
		origin,
		reason,
		requestID,
		expectedRevision,
		formatTime(occurredAt),
	); err != nil {
		return fmt.Errorf("insert relation event: %w", err)
	}
	return nil
}

func insertRelationAudit(
	ctx context.Context,
	tx *sql.Tx,
	auditID int64,
	relationID int64,
	action string,
	actor string,
	origin any,
	reason string,
	requestID string,
	expectedRevision any,
	revisionNo int,
	occurredAt time.Time,
) error {
	details, err := json.Marshal(struct {
		RevisionNo int `json:"revisionNo"`
	}{RevisionNo: revisionNo})
	if err != nil {
		return fmt.Errorf("encode relation audit details: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_events(
    id, actor, origin, action, subject_type, subject_id,
    reason, request_id, expected_revision_no, details_json, occurred_at
) VALUES (?, ?, ?, ?, ?, 'RELATION', ?, ?, ?, ?, ?, ?)
`,
		auditID,

		actor,
		origin,
		action,
		relationID,
		reason,
		requestID,
		expectedRevision,
		string(details),
		formatTime(occurredAt),
	); err != nil {
		return fmt.Errorf("insert relation audit event: %w", err)
	}
	return nil
}

func marshalOptionalBoolean(value *conditions.Boolean) (any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode relation condition: %w", err)
	}
	return string(encoded), nil
}

func auditActionForProposal(kind relations.ProposalKind) string {
	switch kind {
	case relations.ProposalTombstone:
		return "RELATION_TOMBSTONE_PROPOSED"
	case relations.ProposalStale:
		return "RELATION_STALE_PROPOSED"
	default:
		return "RELATION_REVISION_PROPOSED"
	}
}

func storedProposalKind(kind relations.ProposalKind) relations.ProposalKind {
	if kind == relations.ProposalStale {
		return relations.ProposalTombstone
	}
	return kind
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
