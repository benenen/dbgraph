package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/relations"
)

var ErrInvalidCredential = errors.New("invalid authentication credential")

const (
	credentialTokenHexLength  = 64
	maximumTokenLength        = 64
	minimumDistinctTokenBytes = 16
)

type Credential struct {
	Token  string
	Actor  string
	Role   relations.Role
	Origin audit.Origin
}

type tokenRecord struct {
	digest    [sha256.Size]byte
	principal relations.Principal
}

type TokenAuthenticator struct {
	records []tokenRecord
}

func (a *TokenAuthenticator) HasCredentials() bool {
	return a != nil && len(a.records) > 0
}

type EnvironmentLookup func(string) (string, bool)

func LoadMCPAuthenticator(lookup EnvironmentLookup) (*TokenAuthenticator, error) {
	definitions := []struct {
		key   string
		actor string
		role  relations.Role
	}{
		{"DBGRAPH_MCP_AGENT_TOKEN", "mcp-agent", relations.RoleAgent},
		{"DBGRAPH_MCP_REVIEWER_TOKEN", "mcp-reviewer", relations.RoleReviewer},
		{"DBGRAPH_MCP_ADMIN_TOKEN", "mcp-admin", relations.RoleAdmin},
	}
	credentials := make([]Credential, 0, len(definitions))
	for _, definition := range definitions {
		if lookup == nil {
			continue
		}
		token, ok := lookup(definition.key)
		if !ok || strings.TrimSpace(token) == "" {
			continue
		}
		credentials = append(credentials, Credential{Token: token, Actor: definition.actor, Role: definition.role})
	}
	return NewTokenAuthenticator(credentials)
}

func NewTokenAuthenticator(credentials []Credential) (*TokenAuthenticator, error) {
	records := make([]tokenRecord, 0, len(credentials))
	seen := make(map[[sha256.Size]byte]struct{}, len(credentials))
	for _, credential := range credentials {
		token := strings.TrimSpace(credential.Token)
		actor := strings.TrimSpace(credential.Actor)
		if !validCredentialToken(token) || actor == "" || len(actor) > 200 ||
			credential.Role < relations.RoleViewer || credential.Role > relations.RoleAdmin {
			return nil, ErrInvalidCredential
		}
		digest := sha256.Sum256([]byte(token))
		if _, exists := seen[digest]; exists {
			return nil, ErrInvalidCredential
		}
		seen[digest] = struct{}{}
		origin := credential.Origin
		if origin == 0 {
			origin = audit.OriginAgent
		}
		if origin < audit.OriginAgent || origin > audit.OriginSystem {
			return nil, ErrInvalidCredential
		}
		records = append(records, tokenRecord{
			digest: digest,
			principal: relations.Principal{
				Actor: actor, Role: credential.Role, Origin: origin,
			},
		})
	}
	return &TokenAuthenticator{records: records}, nil
}

func (a *TokenAuthenticator) Authenticate(request *http.Request) (relations.Principal, bool) {
	if a == nil {
		return relations.Principal{}, false
	}
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || strings.Count(header, " ") != 1 {
		return relations.Principal{}, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return a.AuthenticateToken(token)
}

func (a *TokenAuthenticator) AuthenticateToken(token string) (relations.Principal, bool) {
	if a == nil {
		return relations.Principal{}, false
	}
	if !validCredentialToken(token) {
		return relations.Principal{}, false
	}
	digest := sha256.Sum256([]byte(token))
	for _, record := range a.records {
		if subtle.ConstantTimeCompare(digest[:], record.digest[:]) == 1 {
			return record.principal, true
		}
	}
	return relations.Principal{}, false
}

func validCredentialToken(token string) bool {
	if len(token) != credentialTokenHexLength {
		return false
	}
	decoded := make([]byte, hex.DecodedLen(len(token)))
	if _, err := hex.Decode(decoded, []byte(token)); err != nil || len(decoded) != 32 {
		return false
	}
	var observed [256]bool
	distinct := 0
	for _, value := range decoded {
		if observed[value] {
			continue
		}
		observed[value] = true
		distinct++
	}
	return distinct >= minimumDistinctTokenBytes
}

func LoadWebAuthenticator(lookup EnvironmentLookup) (*TokenAuthenticator, error) {
	definitions := []struct {
		key   string
		actor string
		role  relations.Role
	}{
		{"DBGRAPH_WEB_VIEWER_TOKEN", "web-viewer", relations.RoleViewer},
		{"DBGRAPH_WEB_EDITOR_TOKEN", "web-editor", relations.RoleEditor},
		{"DBGRAPH_WEB_REVIEWER_TOKEN", "web-reviewer", relations.RoleReviewer},
		{"DBGRAPH_WEB_ADMIN_TOKEN", "web-admin", relations.RoleAdmin},
	}
	credentials := make([]Credential, 0, len(definitions))
	for _, definition := range definitions {
		if lookup == nil {
			continue
		}
		token, ok := lookup(definition.key)
		if !ok || strings.TrimSpace(token) == "" {
			continue
		}
		credentials = append(credentials, Credential{
			Token: token, Actor: definition.actor, Role: definition.role, Origin: audit.OriginWeb,
		})
	}
	return NewTokenAuthenticator(credentials)
}
