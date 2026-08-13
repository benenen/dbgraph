package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/jobs"
)

type JobRepository struct {
	store *Store
}

const (
	recoveredSchemaScanErrorCode    = "PROCESS_INTERRUPTED"
	recoveredSchemaScanErrorMessage = "schema scan did not complete"
)

func NewJobRepository(store *Store) *JobRepository {
	return &JobRepository{store: store}
}

func (r *JobRepository) CreateJob(ctx context.Context, job jobs.Job) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		return insertJob(ctx, tx, job)
	})
	if err != nil {
		return fmt.Errorf("insert job: %w", mapJobStoreError(err))
	}
	return nil
}

func (r *JobRepository) CreateSchemaScanJob(
	ctx context.Context,
	job jobs.Job,
	event audit.Event,
	maximumPending int,
) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		var queued int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM jobs
WHERE job_type = ? AND status IN (?, ?)
`, jobs.TypeSchemaScan, jobs.StatusPending, jobs.StatusRunning).Scan(&queued); err != nil {
			return fmt.Errorf("count queued schema scans: %w", err)
		}
		if maximumPending <= 0 || queued >= maximumPending {
			return jobs.ErrQueueFull
		}
		if err := insertJob(ctx, tx, job); err != nil {
			return err
		}
		if err := insertAuditEvent(ctx, tx, event); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("queue schema scan: %w", mapJobStoreError(err))
	}
	return nil
}

func insertJob(ctx context.Context, tx *sql.Tx, job jobs.Job) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO jobs(
    id, job_type, status, payload_json, result_json,
    error_code, error_message, created_at, started_at, completed_at, revision_no
) VALUES (?, ?, ?, ?, NULL, NULL, NULL, ?, NULL, NULL, ?)
`,
		job.ID,
		job.Type,
		job.Status,
		string(job.Payload),
		job.CreatedAt.Format(time.RFC3339Nano),
		job.RevisionNo,
	)
	return err
}

func (r *JobRepository) ClaimNextSchemaScan(ctx context.Context, startedAt time.Time) (jobs.Job, error) {
	var claimed jobs.Job
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
UPDATE jobs
SET status = ?, started_at = ?, revision_no = revision_no + 1
WHERE id = (
    SELECT id FROM jobs
    WHERE job_type = ? AND status = ?
    ORDER BY created_at, id
    LIMIT 1
)
AND status = ?
RETURNING
    id, job_type, status, payload_json, result_json,
    error_code, error_message, created_at, started_at, completed_at, revision_no
`,
			jobs.StatusRunning,
			startedAt.Format(time.RFC3339Nano),
			jobs.TypeSchemaScan,
			jobs.StatusPending,
			jobs.StatusPending,
		)
		var err error
		claimed, err = scanJob(row)
		if errors.Is(err, sql.ErrNoRows) {
			return jobs.ErrNoPendingJob
		}
		return err
	})
	if err != nil {
		return jobs.Job{}, mapJobStoreError(err)
	}
	return claimed, nil
}

func (r *JobRepository) RecoverRunningSchemaScans(
	ctx context.Context,
	recoveredAt time.Time,
) (int, error) {
	var recovered int64
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
UPDATE schema_scan_runs
SET status = ?, error_code = ?, error_message = ?, completed_at = ?
WHERE status = ?
`,
			scanStatusFailed,
			recoveredSchemaScanErrorCode,
			recoveredSchemaScanErrorMessage,
			recoveredAt.UTC().Format(time.RFC3339Nano),
			scanStatusRunning,
		); err != nil {
			return fmt.Errorf("terminate interrupted schema scan runs: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, started_at = NULL, revision_no = revision_no + 1
WHERE job_type = ? AND status = ?
`, jobs.StatusPending, jobs.TypeSchemaScan, jobs.StatusRunning)
		if err != nil {
			return err
		}
		recovered, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return 0, mapJobStoreError(err)
	}
	return int(recovered), nil
}

func (r *JobRepository) FinishSchemaScan(
	ctx context.Context,
	completion jobs.SchemaScanCompletion,
) (jobs.Job, error) {
	var finished jobs.Job
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		var result any
		if len(completion.Result) > 0 {
			result = string(completion.Result)
		}
		row := tx.QueryRowContext(ctx, `
UPDATE jobs
SET
    status = ?, result_json = ?, error_code = NULLIF(?, ''),
    error_message = NULLIF(?, ''), completed_at = ?, revision_no = revision_no + 1
WHERE id = ? AND status = ? AND revision_no = ?
RETURNING
    id, job_type, status, payload_json, result_json,
    error_code, error_message, created_at, started_at, completed_at, revision_no
`,
			completion.Status,
			result,
			completion.ErrorCode,
			completion.ErrorMessage,
			completion.CompletedAt.Format(time.RFC3339Nano),
			completion.JobID,
			jobs.StatusRunning,
			completion.ExpectedRevisionNo,
		)
		var err error
		finished, err = scanJob(row)
		if errors.Is(err, sql.ErrNoRows) {
			return jobs.ErrJobConflict
		}
		return err
	})
	if err != nil {
		return jobs.Job{}, mapJobStoreError(err)
	}
	return finished, nil
}

func mapJobStoreError(err error) error {
	if errors.Is(err, ErrWriteQueueFull) {
		return fmt.Errorf("%w: %w", jobs.ErrStoreBusy, err)
	}
	return err
}

func (r *JobRepository) GetJob(ctx context.Context, jobID int64) (jobs.Job, error) {
	job, err := scanJob(r.store.db.QueryRowContext(ctx, `
SELECT
    id, job_type, status, payload_json, result_json,
    error_code, error_message, created_at, started_at, completed_at, revision_no
FROM jobs
WHERE id = ?
`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return jobs.Job{}, jobs.ErrJobNotFound
	}
	return job, err
}

type jobScanner interface {
	Scan(...any) error
}

func scanJob(scanner jobScanner) (jobs.Job, error) {
	var job jobs.Job
	var payload string
	var result sql.NullString
	var errorCode sql.NullString
	var errorMessage sql.NullString
	var createdAt string
	var startedAt sql.NullString
	var completedAt sql.NullString
	err := scanner.Scan(
		&job.ID,
		&job.Type,
		&job.Status,
		&payload,
		&result,
		&errorCode,
		&errorMessage,
		&createdAt,
		&startedAt,
		&completedAt,
		&job.RevisionNo,
	)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("select job: %w", err)
	}

	job.Payload = json.RawMessage(payload)
	if result.Valid {
		job.Result = json.RawMessage(result.String)
	}
	job.ErrorCode = errorCode.String
	job.ErrorMessage = errorMessage.String
	job.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("parse job creation time: %w", err)
	}
	job.StartedAt, err = parseOptionalTime(startedAt)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("parse job start time: %w", err)
	}
	job.CompletedAt, err = parseOptionalTime(completedAt)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("parse job completion time: %w", err)
	}
	return job, nil
}

func parseOptionalTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
