package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
)

type initBatchCountReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func initReproposalCandidateSummary(
	ctx context.Context,
	reader initBatchCountReader,
	sessionID int64,
) (reconcile.CompletionCheck, error) {
	var check reconcile.CompletionCheck
	if err := reader.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(length(CAST(candidate_json AS BLOB))), 0)
FROM relation_init_reproposal_candidates
WHERE session_id = ?
`, sessionID).Scan(&check.DeferredCandidateCount, &check.DeferredCandidateRawBytes); err != nil {
		return reconcile.CompletionCheck{}, fmt.Errorf("summarize relation init reproposal candidates: %w", err)
	}
	return check, nil
}

func deferredCompletionBudgetExceeded(check reconcile.CompletionCheck) bool {
	return completionBudgetExceeded(check, 0, 0)
}

func completionBudgetExceeded(
	check reconcile.CompletionCheck,
	omittedCount int,
	omittedRawBytes int64,
) bool {
	if check.DeferredCandidateCount < 0 ||
		check.DeferredCandidateCount > reconcile.MaximumCompletionCandidates ||
		check.DeferredCandidateRawBytes < 0 ||
		check.DeferredCandidateRawBytes > reconcile.MaximumCompletionCandidateRawBytes ||
		omittedCount < 0 || omittedRawBytes < 0 {
		return true
	}
	return omittedCount > reconcile.MaximumCompletionCandidates-check.DeferredCandidateCount ||
		omittedRawBytes > int64(reconcile.MaximumCompletionCandidateRawBytes)-check.DeferredCandidateRawBytes
}

func completionCandidateASTRawBytes(candidates []relations.ProposalRecord) (int64, error) {
	var rawBytes int64
	for _, candidate := range candidates {
		values := make([]any, 0, 3)
		if candidate.Revision.Guard != nil {
			values = append(values, candidate.Revision.Guard)
		}
		if candidate.Revision.Selector != nil {
			values = append(values, candidate.Revision.Selector)
		}
		values = append(values, candidate.Revision.Transform)
		for _, value := range values {
			encoded, err := json.Marshal(value)
			if err != nil {
				return 0, fmt.Errorf("encode relation init completion candidate AST: %w", reconcile.ErrInvalidInit)
			}
			rawBytes += int64(len(encoded))
			if rawBytes > reconcile.MaximumCompletionCandidateRawBytes {
				return rawBytes, nil
			}
		}
	}
	return rawBytes, nil
}

func verifyInitBatchCount(
	ctx context.Context,
	reader initBatchCountReader,
	sessionID int64,
	expectedBatchCount int,
) error {
	var batchCount int
	var maximumBatchNo int
	if err := reader.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(MAX(batch_no), 0)
FROM relation_init_batches
WHERE session_id = ?
	`, sessionID).Scan(&batchCount, &maximumBatchNo); err != nil {
		return fmt.Errorf("verify relation init batches: %w", err)
	}
	if batchCount != expectedBatchCount || maximumBatchNo != expectedBatchCount {
		return reconcile.ErrIncompleteBatches
	}
	return nil
}

func initReproposalCandidateExists(
	ctx context.Context,
	tx *sql.Tx,
	sessionID int64,
	relationID int64,
) (bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1
    FROM relation_init_reproposal_candidates
    WHERE session_id = ? AND relation_id = ?
)
`, sessionID, relationID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check relation init reproposal candidate: %w", err)
	}
	return exists == 1, nil
}

func insertInitReproposalCandidate(
	ctx context.Context,
	tx *sql.Tx,
	sessionID int64,
	batchID int64,
	candidate relations.ProposalRecord,
) error {
	encoded, err := json.Marshal(candidate)
	if err != nil {
		return fmt.Errorf("encode relation init reproposal candidate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO relation_init_reproposal_candidates(
    session_id, batch_id, relation_id, candidate_json, created_at
) VALUES (?, ?, ?, ?, ?)
`, sessionID, batchID, candidate.RelationID, string(encoded), formatTime(candidate.Revision.CreatedAt)); err != nil {
		return fmt.Errorf("insert relation init reproposal candidate: %w", err)
	}
	return nil
}

func loadInitReproposalCandidates(
	ctx context.Context,
	tx *sql.Tx,
	sessionID int64,
	candidateLimit int,
	rawByteLimit int64,
) (candidates []relations.ProposalRecord, returnError error) {
	rows, err := tx.QueryContext(ctx, `
SELECT relation_id, candidate_json
FROM relation_init_reproposal_candidates
WHERE session_id = ?
ORDER BY batch_id, relation_id
`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list relation init reproposal candidates: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()
	candidates = make([]relations.ProposalRecord, 0)
	var rawBytes int64
	for rows.Next() {
		var relationID int64
		var encoded string
		if err := rows.Scan(&relationID, &encoded); err != nil {
			return nil, fmt.Errorf("scan relation init reproposal candidate: %w", err)
		}
		if len(candidates) >= candidateLimit || int64(len(encoded)) > rawByteLimit-rawBytes {
			return nil, reconcile.ErrCompletionBudgetExceeded
		}
		rawBytes += int64(len(encoded))
		var candidate relations.ProposalRecord
		if err := json.Unmarshal([]byte(encoded), &candidate); err != nil || candidate.RelationID != relationID {
			return nil, fmt.Errorf("decode relation init reproposal candidate: %w", reconcile.ErrInvalidInit)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate relation init reproposal candidates: %w", err)
	}
	return candidates, nil
}
