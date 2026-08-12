package auth

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/relations"
)

// StoredCredential is an access credential as it rests in SQLite: the actor it
// authenticates, what it may do, and a digest of the token.
//
// Only the digest is stored. The serving process never needs to reproduce a
// token, only to verify one presented to it, so the database holds nothing a
// thief could present as a credential and needs no encryption key.
type StoredCredential struct {
	Actor  string
	Role   relations.Role
	Origin audit.Origin
	Digest []byte
}

// EnvironmentCredentials collects every Web and MCP token present in the
// environment, as digests ready to persist. Absent variables are skipped, so a
// deployment that has already seeded its credentials can run with none set.
func EnvironmentCredentials(lookup EnvironmentLookup) ([]StoredCredential, error) {
	if lookup == nil {
		return nil, nil
	}
	// One credential per surface. Both carry Admin because a single token has to
	// cover everything that surface can do; split them again by adding entries
	// here if you want role separation back.
	definitions := []struct {
		key    string
		actor  string
		role   relations.Role
		origin audit.Origin
	}{
		{"DBGRAPH_WEB_TOKEN", "web", relations.RoleAdmin, audit.OriginWeb},
		{"DBGRAPH_MCP_TOKEN", "mcp", relations.RoleAdmin, audit.OriginAgent},
	}

	credentials := make([]StoredCredential, 0, len(definitions))
	seen := make(map[[sha256.Size]byte]string, len(definitions))
	for _, definition := range definitions {
		token, ok := lookup(definition.key)
		token = strings.TrimSpace(token)
		if !ok || token == "" {
			continue
		}
		if !validCredentialToken(token) {
			return nil, ErrInvalidCredential
		}
		digest := sha256.Sum256([]byte(token))
		// Two variables sharing a token would otherwise resolve to whichever
		// actor is applied last, silently granting that actor's role to the
		// other's holder. Refuse to start instead.
		if previous, duplicate := seen[digest]; duplicate {
			return nil, fmt.Errorf("%w: %s and %s hold the same token",
				ErrInvalidCredential, previous, definition.key)
		}
		seen[digest] = definition.key
		credentials = append(credentials, StoredCredential{
			Actor: definition.actor, Role: definition.role,
			Origin: definition.origin, Digest: digest[:],
		})
	}
	return credentials, nil
}

// KnownActors names every actor the current scheme issues. Anything else in
// storage is a leftover from an earlier scheme and must not keep authenticating.
func KnownActors() []string {
	return []string{"web", "mcp"}
}

// NewTokenAuthenticatorFromStored builds an authenticator from persisted
// digests, so a running server needs no token in its environment.
func NewTokenAuthenticatorFromStored(credentials []StoredCredential) (*TokenAuthenticator, error) {
	records := make([]tokenRecord, 0, len(credentials))
	seen := make(map[[sha256.Size]byte]struct{}, len(credentials))
	for _, credential := range credentials {
		actor := strings.TrimSpace(credential.Actor)
		if actor == "" || len(actor) > 200 ||
			credential.Role < relations.RoleViewer || credential.Role > relations.RoleAdmin ||
			credential.Origin < audit.OriginAgent || credential.Origin > audit.OriginSystem ||
			len(credential.Digest) != sha256.Size {
			return nil, ErrInvalidCredential
		}
		var digest [sha256.Size]byte
		copy(digest[:], credential.Digest)
		if _, exists := seen[digest]; exists {
			return nil, ErrInvalidCredential
		}
		seen[digest] = struct{}{}
		records = append(records, tokenRecord{
			digest: digest,
			principal: relations.Principal{
				Actor: actor, Role: credential.Role, Origin: credential.Origin,
			},
		})
	}
	return &TokenAuthenticator{records: records}, nil
}

// Filter returns the credentials issued for one surface, so the Web and MCP
// adapters keep separate authenticators as they always have.
func Filter(credentials []StoredCredential, origin audit.Origin) []StoredCredential {
	filtered := make([]StoredCredential, 0, len(credentials))
	for _, credential := range credentials {
		if credential.Origin == origin {
			filtered = append(filtered, credential)
		}
	}
	return filtered
}
