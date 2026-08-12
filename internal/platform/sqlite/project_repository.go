package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
)

type ProjectRepository struct {
	store *Store
}

func NewProjectRepository(store *Store) *ProjectRepository {
	return &ProjectRepository{store: store}
}

func (r *ProjectRepository) CreateProject(ctx context.Context, project catalog.Project) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		return insertProject(ctx, tx, project)
	})
	if err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	return nil
}

func (r *ProjectRepository) CreateProjectWithAudit(
	ctx context.Context,
	project catalog.Project,
	event audit.Event,
) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		if err := insertProject(ctx, tx, project); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, event)
	})
	if err != nil {
		return fmt.Errorf("insert audited project: %w", err)
	}
	return nil
}

func insertProject(ctx context.Context, tx *sql.Tx, project catalog.Project) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO projects(id, name, description, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
`,
		project.ID,
		project.Name,
		project.Description,
		project.CreatedAt.Format(time.RFC3339Nano),
		project.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (r *ProjectRepository) GetProject(ctx context.Context, projectID int64) (catalog.Project, error) {
	var project catalog.Project
	var createdAt string
	var updatedAt string
	err := r.store.db.QueryRowContext(ctx, `
SELECT id, name, description, created_at, updated_at
FROM projects
WHERE id = ?
`, projectID).Scan(
		&project.ID,
		&project.Name,
		&project.Description,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.Project{}, catalog.ErrProjectNotFound
	}
	if err != nil {
		return catalog.Project{}, fmt.Errorf("select project: %w", err)
	}

	project.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return catalog.Project{}, fmt.Errorf("parse project creation time: %w", err)
	}
	project.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return catalog.Project{}, fmt.Errorf("parse project update time: %w", err)
	}
	return project, nil
}

// UpdateProjectWithAudit renames or re-describes a project. Identity and
// history are untouched; only the descriptive fields move.
func (r *ProjectRepository) UpdateProjectWithAudit(
	ctx context.Context,
	project catalog.Project,
	event audit.Event,
) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
UPDATE projects SET name = ?, description = ?, updated_at = ?
WHERE id = ? AND archived_at IS NULL
`, project.Name, project.Description, project.UpdatedAt.Format(time.RFC3339Nano), project.ID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return catalog.ErrProjectNotFound
		}
		return insertAuditEvent(ctx, tx, event)
	})
	if err != nil {
		if errors.Is(err, catalog.ErrProjectNotFound) {
			return err
		}
		return fmt.Errorf("update project: %w", err)
	}
	return nil
}

// ArchiveProject retires a project. It is not deleted: audit_events is
// append-only and every project has events from its own creation, so removing
// the row would either orphan that history or break the append-only rule.
// Archiving hides it from the console and drops its data source links, and its
// catalog, relations, and audit trail stay exactly where they are.
func (r *ProjectRepository) ArchiveProject(ctx context.Context, projectID int64, at time.Time) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
UPDATE projects SET archived_at = ?, updated_at = ?
WHERE id = ? AND archived_at IS NULL
`, at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), projectID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return catalog.ErrProjectNotFound
		}
		_, err = tx.ExecContext(ctx,
			"DELETE FROM project_data_sources WHERE project_id = ?", projectID)
		return err
	})
	if err != nil {
		if errors.Is(err, catalog.ErrProjectNotFound) {
			return err
		}
		return fmt.Errorf("archive project: %w", err)
	}
	return nil
}

func (r *ProjectRepository) ListProjects(ctx context.Context, limit int) (projects []catalog.Project, returnError error) {
	rows, err := r.store.db.QueryContext(ctx, `
SELECT id, name, description, created_at, updated_at
FROM projects
WHERE archived_at IS NULL
ORDER BY name, id
LIMIT ?
`, limit)
	if err != nil {
		return nil, fmt.Errorf("select projects: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	for rows.Next() {
		var project catalog.Project
		var createdAt string
		var updatedAt string
		if err := rows.Scan(&project.ID, &project.Name, &project.Description, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		project.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse project creation time: %w", err)
		}
		project.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse project update time: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}
