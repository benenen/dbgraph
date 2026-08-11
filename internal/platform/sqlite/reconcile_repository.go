package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
)

type ReconcileRepository struct {
	store *Store
}

func NewReconcileRepository(store *Store) *ReconcileRepository {
	return &ReconcileRepository{store: store}
}

func (r *ReconcileRepository) Begin(
	ctx context.Context,
	session reconcile.Session,
) (reconcile.Session, error) {
	var created reconcile.Session
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		existing, err := findInitSessionByRequest(
			ctx,
			tx,
			session.ProjectID,
			session.Principal.Actor,
			session.Principal.Origin,
			session.RequestID,
		)
		if err == nil {
			if existing.RepositoryID != session.RepositoryID || existing.Mode != session.Mode ||
				existing.SourceCommit != session.SourceCommit || string(existing.Scope) != string(session.Scope) {
				return reconcile.ErrIdempotencyConflict
			}
			created = existing
			return nil
		}
		if !errors.Is(err, reconcile.ErrInitNotFound) {
			return err
		}
		var repositoryExists int
		if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1 FROM repositories WHERE id = ? AND project_id = ?
)
`, session.RepositoryID, session.ProjectID).Scan(&repositoryExists); err != nil {
			return fmt.Errorf("verify relation init repository: %w", err)
		}
		if repositoryExists != 1 {
			return reconcile.ErrInvalidInit
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_init_sessions(
    id, project_id, repository_id, mode, source_commit, scope_json,
    status, actor, origin, request_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
			session.ID,
			session.ProjectID,
			session.RepositoryID,
			session.Mode,
			session.SourceCommit,
			string(session.Scope),
			session.Status,
			session.Principal.Actor,
			session.Principal.Origin,
			session.RequestID,
			formatTime(session.CreatedAt),
		); err != nil {
			return fmt.Errorf("insert relation init session: %w", err)
		}
		if err := insertInitAudit(
			ctx,
			tx,
			session.ID,
			session.ProjectID,
			session.ID,
			"RELATION_INIT_BEGUN",
			session.Principal,
			"Begin relation initialization.",
			session.RequestID,
			nil,
			session.CreatedAt,
		); err != nil {
			return err
		}
		created = session
		return nil
	})
	return created, err
}

func (r *ReconcileRepository) Get(ctx context.Context, sessionID int64) (reconcile.Session, error) {
	return getInitSession(ctx, r.store.db, sessionID)
}

func (r *ReconcileRepository) SubmitBatch(
	ctx context.Context,
	record reconcile.BatchRecord,
) (reconcile.BatchResult, error) {
	var result reconcile.BatchResult
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		session, err := getInitSession(ctx, tx, record.Session.ID)
		if err != nil {
			return err
		}
		if session.Status != reconcile.StatusOpen {
			return reconcile.ErrInitNotOpen
		}
		var existingDigest string
		var existingResult string
		err = tx.QueryRowContext(ctx, `
SELECT payload_digest, result_json
FROM relation_init_batches
WHERE session_id = ? AND idempotency_key = ?
`, session.ID, record.IdempotencyKey).Scan(&existingDigest, &existingResult)
		if err == nil {
			if existingDigest != record.PayloadDigest {
				return reconcile.ErrIdempotencyConflict
			}
			if err := json.Unmarshal([]byte(existingResult), &result); err != nil {
				return fmt.Errorf("decode relation init batch result: %w", err)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check relation init batch idempotency: %w", err)
		}

		var batchNumberExists int
		if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1 FROM relation_init_batches WHERE session_id = ? AND batch_no = ?
)
`, session.ID, record.BatchNo).Scan(&batchNumberExists); err != nil {
			return fmt.Errorf("check relation init batch number: %w", err)
		}
		if batchNumberExists == 1 {
			return reconcile.ErrBatchConflict
		}
		var nextBatchNo int
		if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(batch_no), 0) + 1
FROM relation_init_batches
WHERE session_id = ?
`, session.ID).Scan(&nextBatchNo); err != nil {
			return fmt.Errorf("read next relation init batch number: %w", err)
		}
		if record.BatchNo != nextBatchNo {
			return reconcile.ErrBatchConflict
		}

		result = reconcile.BatchResult{
			BatchID:       record.BatchID,
			SessionID:     session.ID,
			BatchNo:       record.BatchNo,
			Items:         make([]reconcile.ItemResult, 0, len(record.Proposals)),
			UnresolvedIDs: make([]int64, 0, len(record.Unresolved)),
			AcceptedAt:    record.AcceptedAt,
		}
		seenRelationIDs := make([]int64, 0, len(record.Proposals))
		deferredReproposals := make([]relations.ProposalRecord, 0)
		deferredRelationIDs := make(map[int64]struct{})
		for _, proposal := range record.Proposals {
			var relationID int64
			err := tx.QueryRowContext(ctx, `
SELECT id FROM relations WHERE project_id = ? AND create_fingerprint = ?
`, session.ProjectID, proposal.Fingerprint).Scan(&relationID)
			status := reconcile.ItemDeduplicated
			if errors.Is(err, sql.ErrNoRows) {
				created, err := insertCreateProposal(ctx, tx, proposal)
				if err != nil {
					return err
				}
				relationID = created.ID
				status = reconcile.ItemCreated
			} else if err != nil {
				return fmt.Errorf("resolve relation init proposal: %w", err)
			} else {
				current, err := getRelation(ctx, tx, relationID)
				if err != nil {
					return err
				}
				reproposal, ok := reconcile.PrepareReproposal(current, proposal)
				if ok {
					exists, err := initReproposalCandidateExists(ctx, tx, session.ID, relationID)
					if err != nil {
						return err
					}
					if _, scheduled := deferredRelationIDs[relationID]; !exists && !scheduled {
						deferredReproposals = append(deferredReproposals, reproposal)
						deferredRelationIDs[relationID] = struct{}{}
						status = reconcile.ItemReproposed
					}
				}
			}
			seenRelationIDs = append(seenRelationIDs, relationID)
			result.Items = append(result.Items, reconcile.ItemResult{RelationID: relationID, Status: status})
		}

		newUnresolved := make([]reconcile.Unresolved, 0, len(record.Unresolved))
		for _, finding := range record.Unresolved {
			var existingID int64
			err := tx.QueryRowContext(ctx, `
SELECT id
FROM unresolved_findings
WHERE repository_id = ? AND fingerprint = ? AND status = 1
`, finding.RepositoryID, finding.Fingerprint).Scan(&existingID)
			if err == nil {
				result.UnresolvedIDs = append(result.UnresolvedIDs, existingID)
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("resolve existing unresolved finding: %w", err)
			}
			result.UnresolvedIDs = append(result.UnresolvedIDs, finding.ID)
			newUnresolved = append(newUnresolved, finding)
		}
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode relation init batch result: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_init_batches(
    id, session_id, batch_no, idempotency_key, payload_digest,
    proposal_count, unresolved_count, result_json, accepted_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
			record.BatchID,
			session.ID,
			record.BatchNo,
			record.IdempotencyKey,
			record.PayloadDigest,
			len(record.Proposals),
			len(record.Unresolved),
			string(resultJSON),
			formatTime(record.AcceptedAt),
		); err != nil {
			return fmt.Errorf("insert relation init batch: %w", err)
		}
		for _, candidate := range deferredReproposals {
			if err := insertInitReproposalCandidate(ctx, tx, session.ID, record.BatchID, candidate); err != nil {
				return err
			}
		}
		for _, relationID := range seenRelationIDs {
			if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO relation_init_seen_relations(session_id, relation_id, batch_id)
VALUES (?, ?, ?)
`, session.ID, relationID, record.BatchID); err != nil {
				return fmt.Errorf("record relation seen in init session: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO relation_origins(
    relation_id, repository_id, first_session_id, created_at
) VALUES (?, ?, ?, ?)
`, relationID, session.RepositoryID, session.ID, formatTime(record.AcceptedAt)); err != nil {
				return fmt.Errorf("record relation repository origin: %w", err)
			}
		}
		for _, finding := range newUnresolved {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO unresolved_findings(
    id, project_id, repository_id, session_id, batch_id, fingerprint,
    finding_type, summary, evidence_json, status, actor, origin, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
				finding.ID,
				finding.ProjectID,
				finding.RepositoryID,
				finding.SessionID,
				record.BatchID,
				finding.Fingerprint,
				finding.Type,
				finding.Summary,
				string(finding.Evidence),
				finding.Status,
				finding.Principal.Actor,
				finding.Principal.Origin,
				formatTime(finding.CreatedAt),
			); err != nil {
				return fmt.Errorf("insert unresolved relation finding: %w", err)
			}
		}
		return insertInitAudit(
			ctx,
			tx,
			record.BatchID,
			session.ProjectID,
			session.ID,
			"RELATION_INIT_BATCH_ACCEPTED",
			session.Principal,
			"Accept a bounded relation initialization batch.",
			record.RequestID,
			nil,
			record.AcceptedAt,
		)
	})
	return result, err
}

func (r *ReconcileRepository) ListOmittedRelations(
	ctx context.Context,
	session reconcile.Session,
	budget reconcile.CompletionBudget,
) (plan reconcile.OmissionPlan, returnError error) {
	if budget.CandidateLimit < 0 || budget.CandidateLimit > reconcile.MaximumCompletionCandidates ||
		budget.RawByteLimit < 0 || budget.RawByteLimit > reconcile.MaximumCompletionCandidateRawBytes {
		return reconcile.OmissionPlan{}, reconcile.ErrCompletionBudgetExceeded
	}
	query := `
SELECT ro.relation_id,
       COALESCE(length(CAST(rv.guard_json AS BLOB)), 0) +
       COALESCE(length(CAST(rv.selector_json AS BLOB)), 0) +
       length(CAST(rv.transform_json AS BLOB)) AS ast_bytes
FROM relation_origins ro
JOIN relations r ON r.id = ro.relation_id
JOIN relation_current rc ON rc.relation_id = r.id
JOIN relation_versions rv ON rv.id = rc.active_version_id
WHERE ro.repository_id = ?
  AND r.project_id = ?
  AND rc.active_version_id IS NOT NULL
  AND rc.proposed_version_id IS NULL
  AND rc.status IN (?, ?)
  AND NOT EXISTS (
      SELECT 1
      FROM relation_init_seen_relations seen
      WHERE seen.session_id = ? AND seen.relation_id = ro.relation_id
  )
`
	arguments := []any{
		session.RepositoryID,
		session.ProjectID,
		relations.StatusApproved,
		relations.StatusSuppressed,
		session.ID,
	}
	if session.Mode == reconcile.ModeIncremental {
		relationIDs, err := reconcile.IncrementalRelationIDs(session.Scope)
		if err != nil {
			return reconcile.OmissionPlan{}, err
		}
		if len(relationIDs) == 0 {
			return reconcile.OmissionPlan{Relations: []relations.Relation{}}, nil
		}
		query += `  AND ro.relation_id IN (
      SELECT CAST(value AS INTEGER)
      FROM json_each(?, '$.relationIds')
  )
`
		arguments = append(arguments, string(session.Scope))
	}
	query += "ORDER BY ro.relation_id\nLIMIT ?"
	arguments = append(arguments, budget.CandidateLimit+1)
	rows, err := r.store.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return reconcile.OmissionPlan{}, fmt.Errorf("list omitted relations: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()
	relationIDs := make([]int64, 0, budget.CandidateLimit)
	var rawBytes int64
	for rows.Next() {
		var relationID int64
		var candidateRawBytes int64
		if err := rows.Scan(&relationID, &candidateRawBytes); err != nil {
			return reconcile.OmissionPlan{}, fmt.Errorf("scan omitted relation: %w", err)
		}
		if len(relationIDs) >= budget.CandidateLimit || candidateRawBytes < 0 ||
			rawBytes > budget.RawByteLimit-candidateRawBytes {
			return reconcile.OmissionPlan{}, reconcile.ErrCompletionBudgetExceeded
		}
		relationIDs = append(relationIDs, relationID)
		rawBytes += candidateRawBytes
	}
	if err := rows.Err(); err != nil {
		return reconcile.OmissionPlan{}, fmt.Errorf("iterate omitted relation rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return reconcile.OmissionPlan{}, fmt.Errorf("close omitted relation rows: %w", err)
	}
	omitted := make([]relations.Relation, 0, len(relationIDs))
	for _, relationID := range relationIDs {
		relation, err := getRelation(ctx, r.store.db, relationID)
		if err != nil {
			return reconcile.OmissionPlan{}, err
		}
		omitted = append(omitted, relation)
	}
	return reconcile.OmissionPlan{Relations: omitted, RawBytes: rawBytes}, nil
}

func (r *ReconcileRepository) CheckCompletion(
	ctx context.Context,
	session reconcile.Session,
	expectedBatchCount int,
) (reconcile.CompletionCheck, error) {
	current, err := getInitSession(ctx, r.store.db, session.ID)
	if err != nil {
		return reconcile.CompletionCheck{}, err
	}
	if current.Status != reconcile.StatusOpen {
		return reconcile.CompletionCheck{}, reconcile.ErrInitNotOpen
	}
	if err := verifyInitBatchCount(ctx, r.store.db, current.ID, expectedBatchCount); err != nil {
		return reconcile.CompletionCheck{}, err
	}
	check, err := initReproposalCandidateSummary(ctx, r.store.db, current.ID)
	if err != nil {
		return reconcile.CompletionCheck{}, err
	}
	if deferredCompletionBudgetExceeded(check) {
		return reconcile.CompletionCheck{}, reconcile.ErrCompletionBudgetExceeded
	}
	return check, nil
}

func (r *ReconcileRepository) Complete(
	ctx context.Context,
	record reconcile.CompletionRecord,
) (reconcile.Completion, error) {
	var completion reconcile.Completion
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		session, err := getInitSession(ctx, tx, record.Session.ID)
		if err != nil {
			return err
		}
		if session.Status != reconcile.StatusOpen {
			return reconcile.ErrInitNotOpen
		}
		if err := verifyInitBatchCount(ctx, tx, session.ID, record.ExpectedBatchCount); err != nil {
			return err
		}
		check, err := initReproposalCandidateSummary(ctx, tx, session.ID)
		if err != nil {
			return err
		}
		if completionBudgetExceeded(check, len(record.Candidates), 0) {
			return reconcile.ErrCompletionBudgetExceeded
		}
		omittedRawBytes, err := completionCandidateASTRawBytes(record.Candidates)
		if err != nil {
			return err
		}
		if completionBudgetExceeded(check, len(record.Candidates), omittedRawBytes) {
			return reconcile.ErrCompletionBudgetExceeded
		}
		deferred, err := loadInitReproposalCandidates(
			ctx,
			tx,
			session.ID,
			reconcile.MaximumCompletionCandidates-len(record.Candidates),
			int64(reconcile.MaximumCompletionCandidateRawBytes)-omittedRawBytes,
		)
		if err != nil {
			return err
		}
		candidates := make([]relations.ProposalRecord, 0, len(deferred)+len(record.Candidates))
		candidates = append(candidates, deferred...)
		candidates = append(candidates, record.Candidates...)
		candidateIDs := make([]int64, 0, len(candidates))
		for _, candidate := range candidates {
			relation, err := insertRevisionProposal(ctx, tx, candidate)
			if err != nil {
				return err
			}
			candidateIDs = append(candidateIDs, relation.ID)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE relation_init_sessions
SET status = ?, completed_at = ?
WHERE id = ? AND status = ?
`,
			reconcile.StatusCompleted,
			formatTime(record.CompletedAt),
			session.ID,
			reconcile.StatusOpen,
		); err != nil {
			return fmt.Errorf("complete relation init session: %w", err)
		}
		if err := insertInitAudit(
			ctx,
			tx,
			record.AuditID,
			session.ProjectID,
			session.ID,
			"RELATION_INIT_COMPLETED",
			record.Principal,
			record.Reason,
			record.RequestID,
			nil,
			record.CompletedAt,
		); err != nil {
			return err
		}
		completedAt := record.CompletedAt
		session.Status = reconcile.StatusCompleted
		session.CompletedAt = &completedAt
		completion = reconcile.Completion{Session: session, CandidateRelationIDs: candidateIDs}
		return nil
	})
	return completion, err
}

func (r *ReconcileRepository) ListUnresolved(
	ctx context.Context,
	projectID int64,
	limit int,
) (findings []reconcile.Unresolved, returnError error) {
	rows, err := r.store.db.QueryContext(ctx, `
SELECT
    id, project_id, repository_id, session_id, batch_id, fingerprint,
    finding_type, summary, evidence_json, status, actor, origin, created_at
FROM unresolved_findings
WHERE project_id = ? AND status = 1
ORDER BY created_at DESC, id DESC
LIMIT ?
`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list unresolved relation findings: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()
	findings = make([]reconcile.Unresolved, 0)
	for rows.Next() {
		var finding reconcile.Unresolved
		var evidence string
		var createdAt string
		if err := rows.Scan(
			&finding.ID,
			&finding.ProjectID,
			&finding.RepositoryID,
			&finding.SessionID,
			&finding.BatchID,
			&finding.Fingerprint,
			&finding.Type,
			&finding.Summary,
			&evidence,
			&finding.Status,
			&finding.Principal.Actor,
			&finding.Principal.Origin,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan unresolved relation finding: %w", err)
		}
		finding.Evidence = json.RawMessage(evidence)
		finding.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse unresolved finding time: %w", err)
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unresolved relation findings: %w", err)
	}
	return findings, nil
}

func findInitSessionByRequest(
	ctx context.Context,
	reader relationReader,
	projectID int64,
	actor string,
	origin any,
	requestID string,
) (reconcile.Session, error) {
	var sessionID int64
	err := reader.QueryRowContext(ctx, `
SELECT id
FROM relation_init_sessions
WHERE project_id = ? AND actor = ? AND origin = ? AND request_id = ?
`, projectID, actor, origin, requestID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return reconcile.Session{}, reconcile.ErrInitNotFound
	}
	if err != nil {
		return reconcile.Session{}, fmt.Errorf("find relation init request: %w", err)
	}
	return getInitSession(ctx, reader, sessionID)
}

func getInitSession(
	ctx context.Context,
	reader relationReader,
	sessionID int64,
) (reconcile.Session, error) {
	var session reconcile.Session
	var scope string
	var createdAt string
	var completedAt sql.NullString
	err := reader.QueryRowContext(ctx, `
SELECT
    id, project_id, repository_id, mode, source_commit, scope_json,
    status, actor, origin, request_id, created_at, completed_at
FROM relation_init_sessions
WHERE id = ?
`, sessionID).Scan(
		&session.ID,
		&session.ProjectID,
		&session.RepositoryID,
		&session.Mode,
		&session.SourceCommit,
		&scope,
		&session.Status,
		&session.Principal.Actor,
		&session.Principal.Origin,
		&session.RequestID,
		&createdAt,
		&completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return reconcile.Session{}, reconcile.ErrInitNotFound
	}
	if err != nil {
		return reconcile.Session{}, fmt.Errorf("select relation init session: %w", err)
	}
	session.Scope = json.RawMessage(scope)
	session.Principal.Role = relations.RoleAgent
	parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return reconcile.Session{}, fmt.Errorf("parse relation init creation time: %w", err)
	}
	session.CreatedAt = parsedCreatedAt
	if completedAt.Valid {
		parsedCompletedAt, err := time.Parse(time.RFC3339Nano, completedAt.String)
		if err != nil {
			return reconcile.Session{}, fmt.Errorf("parse relation init completion time: %w", err)
		}
		session.CompletedAt = &parsedCompletedAt
	}
	return session, nil
}

func insertInitAudit(
	ctx context.Context,
	tx *sql.Tx,
	auditID int64,
	projectID int64,
	sessionID int64,
	action string,
	principal relations.Principal,
	reason string,
	requestID string,
	expectedRevision any,
	occurredAt time.Time,
) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_events(
    id, project_id, actor, origin, action, subject_type, subject_id,
    reason, request_id, expected_revision_no, details_json, occurred_at
) VALUES (?, ?, ?, ?, ?, 'RELATION_INIT_SESSION', ?, ?, ?, ?, '{}', ?)
`,
		auditID,
		projectID,
		principal.Actor,
		principal.Origin,
		action,
		sessionID,
		reason,
		requestID,
		expectedRevision,
		formatTime(occurredAt),
	); err != nil {
		return fmt.Errorf("insert relation init audit event: %w", err)
	}
	return nil
}
