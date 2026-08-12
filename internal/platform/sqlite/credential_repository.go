package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
			// A digest is unique across actors, so re-seeding one actor with a
			// token that already belongs to another must not silently move it.
			if _, err := tx.ExecContext(ctx, `
DELETE FROM access_credentials WHERE token_digest = ? AND actor <> ?
`, credential.Digest, credential.Actor); err != nil {
				return err
			}
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
