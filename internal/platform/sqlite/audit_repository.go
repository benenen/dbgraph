package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
)

type AuditRepository struct {
	store *Store
}

func NewAuditRepository(store *Store) *AuditRepository {
	return &AuditRepository{store: store}
}

func (r *AuditRepository) AppendAuditEvent(ctx context.Context, event audit.Event) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		return insertAuditEvent(ctx, tx, event)
	})
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

func insertAuditEvent(ctx context.Context, tx *sql.Tx, event audit.Event) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO audit_events(
    id, actor, origin, action, subject_type, subject_id,
    reason, request_id, expected_revision_no, details_json, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		event.ID,
		// A data source is shared, so an event about one belongs to no single
		// project. The column is nullable for exactly that case; writing 0 would
		// point at a project that cannot exist.
		optionalProjectID(event.ProjectID),
		event.Actor,
		event.Origin,
		event.Action,
		event.SubjectType,
		event.SubjectID,
		event.Reason,
		event.RequestID,
		event.ExpectedRevision,
		string(event.Details),
		event.OccurredAt.Format(time.RFC3339Nano),
	)
	return err
}

func (r *AuditRepository) ListAuditEvents(
	ctx context.Context,
	limit int,
) (events []audit.Event, returnError error) {
	rows, err := r.store.db.QueryContext(ctx, `
SELECT
    id, actor, origin, action, subject_type, subject_id,
    reason, request_id, expected_revision_no, details_json, occurred_at
FROM audit_events
WHERE 1=1
ORDER BY occurred_at DESC, id DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	events = make([]audit.Event, 0)
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return events, nil
}

type auditScanner interface {
	Scan(...any) error
}

func scanAuditEvent(scanner auditScanner) (audit.Event, error) {
	var event audit.Event
	var expectedRevision sql.NullInt64
	var details string
	var occurredAt string
	if err := scanner.Scan(
		&event.ID,
		&event.Actor,
		&event.Origin,
		&event.Action,
		&event.SubjectType,
		&event.SubjectID,
		&event.Reason,
		&event.RequestID,
		&expectedRevision,
		&details,
		&occurredAt,
	); err != nil {
		return audit.Event{}, fmt.Errorf("scan audit event: %w", err)
	}
	if expectedRevision.Valid {
		value := int(expectedRevision.Int64)
		event.ExpectedRevision = &value
	}
	event.Details = json.RawMessage(details)
	parsedTime, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return audit.Event{}, fmt.Errorf("parse audit event time: %w", err)
	}
	event.OccurredAt = parsedTime
	return event, nil
}

func optionalProjectID(projectID int64) any {
	if projectID <= 0 {
		return nil
	}
	return projectID
}
