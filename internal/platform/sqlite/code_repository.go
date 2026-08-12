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

type CodeRepository struct {
	store *Store
}

func NewCodeRepository(store *Store) *CodeRepository {
	return &CodeRepository{store: store}
}

func (r *CodeRepository) CreateCodeRepository(
	ctx context.Context,
	repository catalog.CodeRepository,
) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		return insertCodeRepository(ctx, tx, repository)
	})
	if err != nil {
		return fmt.Errorf("insert code repository: %w", err)
	}
	return nil
}

func (r *CodeRepository) CreateCodeRepositoryWithAudit(
	ctx context.Context,
	repository catalog.CodeRepository,
	event audit.Event,
) error {
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		if err := insertCodeRepository(ctx, tx, repository); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, event)
	})
	if err != nil {
		return fmt.Errorf("insert audited code repository: %w", err)
	}
	return nil
}

func insertCodeRepository(ctx context.Context, tx *sql.Tx, repository catalog.CodeRepository) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO repositories(
    id, project_id, name, remote_url, default_branch, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
`,
		repository.ID,
		repository.ProjectID,
		repository.Name,
		repository.RemoteURL,
		repository.DefaultBranch,
		formatTime(repository.CreatedAt),
		formatTime(repository.UpdatedAt),
	)
	return err
}

func (r *CodeRepository) GetCodeRepository(
	ctx context.Context,
	repositoryID int64,
) (catalog.CodeRepository, error) {
	var repository catalog.CodeRepository
	var createdAt string
	var updatedAt string
	err := r.store.db.QueryRowContext(ctx, `
SELECT id, project_id, name, remote_url, default_branch, created_at, updated_at
FROM repositories
WHERE id = ?
`, repositoryID).Scan(
		&repository.ID,
		&repository.ProjectID,
		&repository.Name,
		&repository.RemoteURL,
		&repository.DefaultBranch,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.CodeRepository{}, catalog.ErrRepositoryNotFound
	}
	if err != nil {
		return catalog.CodeRepository{}, fmt.Errorf("select code repository: %w", err)
	}
	repository.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return catalog.CodeRepository{}, fmt.Errorf("parse code repository creation time: %w", err)
	}
	repository.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return catalog.CodeRepository{}, fmt.Errorf("parse code repository update time: %w", err)
	}
	return repository, nil
}

func (r *CodeRepository) ListCodeRepositories(
	ctx context.Context,
	projectID int64,
	limit int,
) (repositories []catalog.CodeRepository, returnError error) {
	rows, err := r.store.db.QueryContext(ctx, `
SELECT id, project_id, name, remote_url, default_branch, created_at, updated_at
FROM repositories
WHERE project_id = ?
ORDER BY name, id
LIMIT ?
`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("select code repositories: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	for rows.Next() {
		var repository catalog.CodeRepository
		var createdAt string
		var updatedAt string
		if err := rows.Scan(
			&repository.ID, &repository.ProjectID, &repository.Name,
			&repository.RemoteURL, &repository.DefaultBranch, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan code repository: %w", err)
		}
		repository.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse code repository creation time: %w", err)
		}
		repository.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse code repository update time: %w", err)
		}
		repositories = append(repositories, repository)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate code repositories: %w", err)
	}
	return repositories, nil
}
