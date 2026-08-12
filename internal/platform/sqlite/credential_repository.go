package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	appauth "github.com/benenen/dbgraph/internal/auth"
)

// CredentialRepository persists access-token digests so a running server does
// not need the tokens in its environment.
type CredentialRepository struct {
	store *Store
	now   func() time.Time
}

func NewCredentialRepository(store *Store, now func() time.Time) *CredentialRepository {
	if now == nil {
		now = time.Now
	}
	return &CredentialRepository{store: store, now: now}
}

// SyncCredentials upserts the supplied credentials by actor. It is the seeding
// path: whatever the environment offers at startup becomes the stored truth for
// those actors, and actors it does not mention keep their stored digest.
func (r *CredentialRepository) SyncCredentials(ctx context.Context, credentials []appauth.StoredCredential) error {
	if len(credentials) == 0 {
		return nil
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		for _, credential := range credentials {
			// No DELETE here on purpose: a digest that already belongs to
			// another actor must collide against the unique index and fail the
			// startup, rather than quietly moving the credential and handing
			// one actor's role to the other's token holder.
			if _, err := tx.ExecContext(ctx, `
INSERT INTO access_credentials(actor, role, origin, token_digest, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(actor) DO UPDATE SET
    role = excluded.role,
    origin = excluded.origin,
    token_digest = excluded.token_digest,
    updated_at = excluded.updated_at
`, credential.Actor, credential.Role, credential.Origin, credential.Digest, now, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sync access credentials: %w", err)
	}
	return nil
}

// PruneUnknownActors deletes stored credentials for actors the current scheme
// no longer issues. Without it, tokens seeded under an earlier scheme would go
// on authenticating forever, because nothing else ever removes a credential.
func (r *CredentialRepository) PruneUnknownActors(ctx context.Context, known []string) (int64, error) {
	if len(known) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(known))
	arguments := make([]any, len(known))
	for index, actor := range known {
		placeholders[index] = "?"
		arguments[index] = actor
	}
	var removed int64
	err := r.store.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"DELETE FROM access_credentials WHERE actor NOT IN ("+strings.Join(placeholders, ",")+")",
			arguments...)
		if err != nil {
			return err
		}
		removed, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("prune access credentials: %w", err)
	}
	return removed, nil
}

// ListCredentials returns every stored credential.
func (r *CredentialRepository) ListCredentials(ctx context.Context) (credentials []appauth.StoredCredential, returnError error) {
	rows, err := r.store.db.QueryContext(ctx, `
SELECT actor, role, origin, token_digest
FROM access_credentials
ORDER BY actor
`)
	if err != nil {
		return nil, fmt.Errorf("select access credentials: %w", err)
	}
	defer func() { returnError = errors.Join(returnError, rows.Close()) }()

	for rows.Next() {
		var credential appauth.StoredCredential
		if err := rows.Scan(&credential.Actor, &credential.Role, &credential.Origin, &credential.Digest); err != nil {
			return nil, fmt.Errorf("scan access credential: %w", err)
		}
		credentials = append(credentials, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate access credentials: %w", err)
	}
	return credentials, nil
}
