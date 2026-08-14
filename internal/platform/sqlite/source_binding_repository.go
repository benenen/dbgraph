package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/sourcebinding"
)

type SourceBindingRepository struct {
	store *Store
}

func NewSourceBindingRepository(store *Store) *SourceBindingRepository {
	return &SourceBindingRepository{store: store}
}

func (r *SourceBindingRepository) GetRepository(
	ctx context.Context,
	repositoryID int64,
) (sourcebinding.RepositoryRecord, error) {
	var repository sourcebinding.RepositoryRecord
	err := r.store.db.QueryRowContext(ctx, `
SELECT id, name, remote_url
FROM repositories
WHERE id = ?
`, repositoryID).Scan(&repository.ID, &repository.Name, &repository.RemoteURL)
	if errors.Is(err, sql.ErrNoRows) {
		return sourcebinding.RepositoryRecord{}, sourcebinding.ErrRepositoryNotFound
	}
	if err != nil {
		return sourcebinding.RepositoryRecord{}, fmt.Errorf("select source binding repository: %w", err)
	}
	return repository, nil
}

func (r *SourceBindingRepository) FindRepositoriesByCanonicalRemotes(
	ctx context.Context,
	canonicalRemotes []string,
	limit int,
) ([]sourcebinding.RepositoryRecord, error) {
	if len(canonicalRemotes) == 0 || limit <= 0 {
		return nil, nil
	}
	return r.findStoredRepositoryIdentities(ctx, canonicalRemotes, limit)
}

func (r *SourceBindingRepository) findStoredRepositoryIdentities(
	ctx context.Context,
	canonicalRemotes []string,
	limit int,
) ([]sourcebinding.RepositoryRecord, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(canonicalRemotes)), ",")
	arguments := make([]any, 0, len(canonicalRemotes)+1)
	for _, remote := range canonicalRemotes {
		arguments = append(arguments, remote)
	}
	arguments = append(arguments, limit)
	rows, err := r.store.db.QueryContext(ctx, `
SELECT DISTINCT repositories.id, repositories.name, repositories.remote_url
FROM repository_identities
JOIN repositories ON repositories.id = repository_identities.repository_id
WHERE repository_identities.identity_kind = 'GIT_REMOTE'
  AND repository_identities.normalized_value IN (`+placeholders+`)
ORDER BY repositories.id
LIMIT ?
`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("find source binding repositories: %w", err)
	}
	return scanRepositoryRecords(rows, "source binding repositories")
}

func scanRepositoryRecords(rows *sql.Rows, description string) ([]sourcebinding.RepositoryRecord, error) {
	repositories := make([]sourcebinding.RepositoryRecord, 0)
	for rows.Next() {
		var repository sourcebinding.RepositoryRecord
		if err := rows.Scan(&repository.ID, &repository.Name, &repository.RemoteURL); err != nil {
			return nil, errors.Join(fmt.Errorf("scan %s: %w", description, err), rows.Close())
		}
		repositories = append(repositories, repository)
	}
	if err := errors.Join(
		wrapOptionalError("iterate "+description, rows.Err()),
		wrapOptionalError("close "+description, rows.Close()),
	); err != nil {
		return nil, err
	}
	return repositories, nil
}

func wrapOptionalError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func (r *SourceBindingRepository) GetCurrentBinding(
	ctx context.Context,
	repositoryID int64,
	contextName string,
) (sourcebinding.BindingRevision, error) {
	return readCurrentBinding(ctx, r.store.db, repositoryID, contextName)
}

func (r *SourceBindingRepository) ReplaceBinding(
	ctx context.Context,
	record sourcebinding.PersistBindingRevision,
	event audit.Event,
) (sourcebinding.BindingRevision, error) {
	var persisted sourcebinding.BindingRevision
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		var err error
		persisted, err = replaceBindingTransaction(ctx, tx, record, event)
		return err
	})
	if err != nil {
		return sourcebinding.BindingRevision{}, err
	}
	return persisted, nil
}

func replaceBindingTransaction(
	ctx context.Context,
	tx *sql.Tx,
	record sourcebinding.PersistBindingRevision,
	event audit.Event,
) (sourcebinding.BindingRevision, error) {
	existing, existingExpectedRevision, existingReason, found, err := findBindingByRequest(ctx, tx, record)
	if err != nil {
		return sourcebinding.BindingRevision{}, err
	}
	if found {
		if sameBindingRequest(existing, existingExpectedRevision, existingReason, record) {
			return existing, nil
		}
		return sourcebinding.BindingRevision{}, sourcebinding.ErrBindingConflict
	}
	bindingSetID, currentRevision, err := ensureBindingSet(ctx, tx, record)
	if err != nil {
		return sourcebinding.BindingRevision{}, err
	}
	if currentRevision != record.ExpectedRevisionNo {
		return sourcebinding.BindingRevision{}, &sourcebinding.RevisionConflictError{CurrentRevisionNo: currentRevision}
	}
	dataSources, err := loadBindingDataSources(ctx, tx, record.DataSourceIDs)
	if err != nil {
		return sourcebinding.BindingRevision{}, err
	}
	revision := record.Revision
	revision.RevisionNo = currentRevision + 1
	revision.DataSources = dataSources
	if err := insertSourceBindingRevision(ctx, tx, bindingSetID, revision, record); err != nil {
		return sourcebinding.BindingRevision{}, err
	}
	if err := insertSourceBindingMembers(ctx, tx, revision.ID, dataSources); err != nil {
		return sourcebinding.BindingRevision{}, err
	}
	if err := updateCurrentSourceBinding(ctx, tx, bindingSetID, revision.ID); err != nil {
		return sourcebinding.BindingRevision{}, err
	}
	if err := insertAuditEvent(ctx, tx, event); err != nil {
		return sourcebinding.BindingRevision{}, err
	}
	return revision, nil
}

func insertSourceBindingRevision(
	ctx context.Context,
	tx *sql.Tx,
	bindingSetID int64,
	revision sourcebinding.BindingRevision,
	record sourcebinding.PersistBindingRevision,
) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO source_binding_revisions(
    id, binding_set_id, revision_no, expected_revision_no,
    actor, origin, reason, request_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, revision.ID, bindingSetID, revision.RevisionNo, record.ExpectedRevisionNo,
		record.Actor, record.Origin, record.Reason, record.RequestID, formatTime(revision.CreatedAt)); err != nil {
		return translateSourceBindingWrite(err)
	}
	return nil
}

func insertSourceBindingMembers(
	ctx context.Context,
	tx *sql.Tx,
	revisionID int64,
	dataSources []sourcebinding.DataSource,
) error {
	for _, source := range dataSources {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO source_binding_members(binding_revision_id, data_source_id)
VALUES (?, ?)
`, revisionID, source.ID); err != nil {
			return translateSourceBindingWrite(err)
		}
	}
	return nil
}

func updateCurrentSourceBinding(ctx context.Context, tx *sql.Tx, bindingSetID int64, revisionID int64) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO source_binding_current(binding_set_id, binding_revision_id)
VALUES (?, ?)
ON CONFLICT(binding_set_id) DO UPDATE SET binding_revision_id = excluded.binding_revision_id
`, bindingSetID, revisionID); err != nil {
		return fmt.Errorf("update current source binding: %w", err)
	}
	return nil
}

func findBindingByRequest(
	ctx context.Context,
	tx *sql.Tx,
	record sourcebinding.PersistBindingRevision,
) (sourcebinding.BindingRevision, int, string, bool, error) {
	var revision sourcebinding.BindingRevision
	var expectedRevision int
	var reason string
	var createdAt string
	err := tx.QueryRowContext(ctx, `
SELECT source_binding_revisions.id, source_binding_sets.repository_id, repositories.name,
       source_binding_sets.context_name, source_binding_revisions.revision_no,
       source_binding_revisions.expected_revision_no, source_binding_revisions.reason,
       source_binding_revisions.created_at
FROM source_binding_revisions
JOIN source_binding_sets ON source_binding_sets.id = source_binding_revisions.binding_set_id
JOIN repositories ON repositories.id = source_binding_sets.repository_id
WHERE source_binding_revisions.actor = ?
  AND source_binding_revisions.origin = ?
  AND source_binding_revisions.request_id = ?
`, record.Actor, record.Origin, record.RequestID).Scan(
		&revision.ID, &revision.RepositoryID, &revision.RepositoryName,
		&revision.Context, &revision.RevisionNo, &expectedRevision, &reason, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return sourcebinding.BindingRevision{}, 0, "", false, nil
	}
	if err != nil {
		return sourcebinding.BindingRevision{}, 0, "", false, fmt.Errorf("read source binding request: %w", err)
	}
	revision.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return sourcebinding.BindingRevision{}, 0, "", false, fmt.Errorf("parse source binding request time: %w", err)
	}
	revision.DataSources, err = loadRevisionDataSources(ctx, tx, revision.ID)
	if err != nil {
		return sourcebinding.BindingRevision{}, 0, "", false, err
	}
	return revision, expectedRevision, reason, true, nil
}

func sameBindingRequest(
	existing sourcebinding.BindingRevision,
	existingExpectedRevision int,
	existingReason string,
	record sourcebinding.PersistBindingRevision,
) bool {
	if existing.RepositoryID != record.Revision.RepositoryID || existing.Context != record.Revision.Context ||
		existingExpectedRevision != record.ExpectedRevisionNo || existingReason != record.Reason ||
		len(existing.DataSources) != len(record.DataSourceIDs) {
		return false
	}
	existingIDs := make(map[int64]struct{}, len(existing.DataSources))
	for _, source := range existing.DataSources {
		existingIDs[source.ID] = struct{}{}
	}
	for _, id := range record.DataSourceIDs {
		if _, exists := existingIDs[id]; !exists {
			return false
		}
	}
	return true
}

func ensureBindingSet(
	ctx context.Context,
	tx *sql.Tx,
	record sourcebinding.PersistBindingRevision,
) (int64, int, error) {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO repository_identities(repository_id, identity_kind, normalized_value)
VALUES (?, 'GIT_REMOTE', ?)
ON CONFLICT(repository_id, identity_kind, normalized_value) DO NOTHING
`, record.Revision.RepositoryID, record.CanonicalRemote); err != nil {
		return 0, 0, translateSourceBindingWrite(err)
	}
	var identityCount int
	var identityRepositoryID int64
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(MIN(repository_id), 0)
FROM repository_identities
WHERE identity_kind = 'GIT_REMOTE' AND normalized_value = ?
`, record.CanonicalRemote).Scan(&identityCount, &identityRepositoryID); err != nil {
		return 0, 0, fmt.Errorf("verify source binding repository identity: %w", err)
	}
	if identityCount != 1 || identityRepositoryID != record.Revision.RepositoryID {
		return 0, 0, sourcebinding.ErrBindingConflict
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO source_binding_sets(id, repository_id, context_name, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(repository_id, context_name) DO NOTHING
`, record.CandidateBindingSetID, record.Revision.RepositoryID, record.Revision.Context,
		formatTime(record.Revision.CreatedAt)); err != nil {
		return 0, 0, translateSourceBindingWrite(err)
	}
	var bindingSetID int64
	var currentRevision int
	err := tx.QueryRowContext(ctx, `
SELECT source_binding_sets.id, COALESCE(source_binding_revisions.revision_no, 0)
FROM source_binding_sets
LEFT JOIN source_binding_current ON source_binding_current.binding_set_id = source_binding_sets.id
LEFT JOIN source_binding_revisions ON source_binding_revisions.id = source_binding_current.binding_revision_id
WHERE source_binding_sets.repository_id = ? AND source_binding_sets.context_name = ?
`, record.Revision.RepositoryID, record.Revision.Context).Scan(&bindingSetID, &currentRevision)
	if err != nil {
		return 0, 0, fmt.Errorf("read current source binding revision: %w", err)
	}
	return bindingSetID, currentRevision, nil
}

func loadBindingDataSources(
	ctx context.Context,
	queryer interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	},
	dataSourceIDs []int64,
) (sources []sourcebinding.DataSource, returnError error) {
	if len(dataSourceIDs) == 0 {
		return []sourcebinding.DataSource{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(dataSourceIDs)), ",")
	arguments := make([]any, len(dataSourceIDs))
	for index, id := range dataSourceIDs {
		arguments[index] = id
	}
	rows, err := queryer.QueryContext(ctx, `
SELECT id, name, source_kind
FROM data_sources
WHERE id IN (`+placeholders+`)
ORDER BY name, id
`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("load source binding data sources: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()
	for rows.Next() {
		var source sourcebinding.DataSource
		var kind int
		if err := rows.Scan(&source.ID, &source.Name, &kind); err != nil {
			return nil, fmt.Errorf("scan source binding data source: %w", err)
		}
		source.Kind = sourceKindName(kind)
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source binding data sources: %w", err)
	}
	if len(sources) != len(dataSourceIDs) {
		return nil, sourcebinding.ErrInvalidBinding
	}
	return sources, nil
}

func readCurrentBinding(
	ctx context.Context,
	queryer interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	},
	repositoryID int64,
	contextName string,
) (sourcebinding.BindingRevision, error) {
	var revision sourcebinding.BindingRevision
	var createdAt string
	err := queryer.QueryRowContext(ctx, `
SELECT source_binding_revisions.id, source_binding_sets.repository_id, repositories.name,
       source_binding_sets.context_name, source_binding_revisions.revision_no,
       source_binding_revisions.created_at
FROM source_binding_sets
JOIN repositories ON repositories.id = source_binding_sets.repository_id
JOIN source_binding_current ON source_binding_current.binding_set_id = source_binding_sets.id
JOIN source_binding_revisions ON source_binding_revisions.id = source_binding_current.binding_revision_id
WHERE source_binding_sets.repository_id = ? AND source_binding_sets.context_name = ?
`, repositoryID, contextName).Scan(
		&revision.ID, &revision.RepositoryID, &revision.RepositoryName,
		&revision.Context, &revision.RevisionNo, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return sourcebinding.BindingRevision{}, sourcebinding.ErrBindingNotFound
	}
	if err != nil {
		return sourcebinding.BindingRevision{}, fmt.Errorf("read current source binding: %w", err)
	}
	revision.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return sourcebinding.BindingRevision{}, fmt.Errorf("parse source binding creation time: %w", err)
	}
	revision.DataSources, err = loadRevisionDataSources(ctx, queryer, revision.ID)
	if err != nil {
		return sourcebinding.BindingRevision{}, err
	}
	return revision, nil
}

func loadRevisionDataSources(
	ctx context.Context,
	queryer interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	},
	revisionID int64,
) (sources []sourcebinding.DataSource, returnError error) {
	rows, err := queryer.QueryContext(ctx, `
SELECT data_sources.id, data_sources.name, data_sources.source_kind
FROM source_binding_members
JOIN data_sources ON data_sources.id = source_binding_members.data_source_id
WHERE source_binding_members.binding_revision_id = ?
ORDER BY data_sources.name, data_sources.id
`, revisionID)
	if err != nil {
		return nil, fmt.Errorf("read source binding members: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()
	for rows.Next() {
		var source sourcebinding.DataSource
		var kind int
		if err := rows.Scan(&source.ID, &source.Name, &kind); err != nil {
			return nil, fmt.Errorf("scan source binding member: %w", err)
		}
		source.Kind = sourceKindName(kind)
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source binding members: %w", err)
	}
	if sources == nil {
		sources = []sourcebinding.DataSource{}
	}
	return sources, nil
}

func sourceKindName(kind int) string {
	if kind == 1 {
		return "MYSQL"
	}
	return "UNKNOWN"
}

func translateSourceBindingWrite(err error) error {
	message := err.Error()
	if strings.Contains(message,
		"source_binding_revisions.actor, source_binding_revisions.origin, source_binding_revisions.request_id") {
		return sourcebinding.ErrBindingConflict
	}
	if strings.Contains(message, "FOREIGN KEY constraint failed") || strings.Contains(message, "CHECK constraint failed") {
		return sourcebinding.ErrInvalidBinding
	}
	return fmt.Errorf("write source binding: %w", err)
}
