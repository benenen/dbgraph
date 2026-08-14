package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/benenen/dbgraph/internal/gitremote"
)

type repositoryIdentityCandidate struct {
	id     int64
	remote string
}

const repositoryIdentityBackfillBatchSize = 100

func backfillRepositoryIdentities(ctx context.Context, database *sql.DB) error {
	for {
		complete, err := backfillRepositoryIdentityBatch(ctx, database)
		if err != nil || complete {
			return err
		}
	}
}

func backfillRepositoryIdentityBatch(ctx context.Context, database *sql.DB) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin repository identity backfill: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	lastRepositoryID, complete, err := readRepositoryIdentityBackfillState(ctx, tx)
	if err != nil {
		return false, err
	}
	if complete {
		return true, nil
	}
	candidates, err := listRepositoryIdentityCandidates(ctx, tx, lastRepositoryID, repositoryIdentityBackfillBatchSize)
	if err != nil {
		return false, err
	}
	for _, candidate := range candidates {
		if err := insertRepositoryIdentity(ctx, tx, candidate.id, candidate.remote); err != nil {
			return false, err
		}
	}
	complete = len(candidates) < repositoryIdentityBackfillBatchSize
	if len(candidates) > 0 {
		lastRepositoryID = candidates[len(candidates)-1].id
	}
	if err := writeRepositoryIdentityBackfillState(ctx, tx, lastRepositoryID, complete); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit repository identity backfill: %w", err)
	}
	return complete, nil
}

func readRepositoryIdentityBackfillState(ctx context.Context, tx *sql.Tx) (int64, bool, error) {
	var lastRepositoryID int64
	var complete bool
	if err := tx.QueryRowContext(ctx, `
SELECT last_repository_id, completed_at IS NOT NULL
FROM repository_identity_backfill_state
WHERE singleton = 1
`).Scan(&lastRepositoryID, &complete); err != nil {
		return 0, false, fmt.Errorf("read repository identity backfill state: %w", err)
	}
	return lastRepositoryID, complete, nil
}

func listRepositoryIdentityCandidates(
	ctx context.Context,
	tx *sql.Tx,
	lastRepositoryID int64,
	limit int,
) ([]repositoryIdentityCandidate, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, remote_url
FROM repositories
WHERE id > ?
ORDER BY id
LIMIT ?
`, lastRepositoryID, limit)
	if err != nil {
		return nil, fmt.Errorf("list repository identities for backfill: %w", err)
	}
	var candidates []repositoryIdentityCandidate
	for rows.Next() {
		var candidate repositoryIdentityCandidate
		if err := rows.Scan(&candidate.id, &candidate.remote); err != nil {
			return nil, errors.Join(fmt.Errorf("scan repository identity candidate: %w", err), rows.Close())
		}
		candidates = append(candidates, candidate)
	}
	if err := errors.Join(
		wrapOptionalError("iterate repository identity candidates", rows.Err()),
		wrapOptionalError("close repository identity candidates", rows.Close()),
	); err != nil {
		return nil, err
	}
	return candidates, nil
}

func writeRepositoryIdentityBackfillState(
	ctx context.Context,
	tx *sql.Tx,
	lastRepositoryID int64,
	complete bool,
) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE repository_identity_backfill_state
SET last_repository_id = ?,
    completed_at = CASE WHEN ? THEN strftime('%Y-%m-%dT%H:%M:%fZ', 'now') ELSE NULL END
WHERE singleton = 1
`, lastRepositoryID, complete); err != nil {
		return fmt.Errorf("update repository identity backfill state: %w", err)
	}
	return nil
}

func insertRepositoryIdentity(ctx context.Context, tx *sql.Tx, repositoryID int64, remote string) error {
	canonical, err := gitremote.Canonicalize(remote)
	if err != nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO repository_identities(repository_id, identity_kind, normalized_value)
VALUES (?, 'GIT_REMOTE', ?)
ON CONFLICT(repository_id, identity_kind, normalized_value) DO NOTHING
`, repositoryID, canonical); err != nil {
		return fmt.Errorf("insert repository identity: %w", err)
	}
	return nil
}
