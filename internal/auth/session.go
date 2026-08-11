package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/benenen/dbgraph/internal/relations"
)

var (
	ErrInvalidSession = errors.New("invalid web session")
	ErrSessionLimit   = errors.New("web session limit reached")
)

const sessionTTL = 8 * time.Hour

const (
	maximumSessions         = 10_000
	maximumSessionsPerActor = 20
)

type Session struct {
	Principal relations.Principal
	CSRFToken string
	ExpiresAt time.Time
}

type sessionRecord struct {
	principal  relations.Principal
	csrfToken  string
	csrfDigest [sha256.Size]byte
	expiresAt  time.Time
}

type SessionManager struct {
	mu            sync.Mutex
	authenticator *TokenAuthenticator
	now           func() time.Time
	random        io.Reader
	sessions      map[[sha256.Size]byte]sessionRecord
}

func NewSessionManager(authenticator *TokenAuthenticator, now func() time.Time, random io.Reader) *SessionManager {
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &SessionManager{
		authenticator: authenticator, now: now, random: random,
		sessions: make(map[[sha256.Size]byte]sessionRecord),
	}
}

func (m *SessionManager) Create(token string) (string, Session, error) {
	principal, ok := m.authenticator.AuthenticateToken(token)
	if !ok {
		return "", Session{}, ErrInvalidCredential
	}
	sessionToken, err := randomToken(m.random)
	if err != nil {
		return "", Session{}, ErrInvalidSession
	}
	csrfToken, err := randomToken(m.random)
	if err != nil {
		return "", Session{}, ErrInvalidSession
	}
	expiresAt := m.now().UTC().Add(sessionTTL)
	sessionDigest := sha256.Sum256([]byte(sessionToken))
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteExpiredLocked(m.now().UTC())
	if len(m.sessions) >= maximumSessions {
		return "", Session{}, ErrSessionLimit
	}
	actorSessions := 0
	for _, existing := range m.sessions {
		if existing.principal.Actor == principal.Actor && existing.principal.Role == principal.Role {
			actorSessions++
		}
	}
	if actorSessions >= maximumSessionsPerActor {
		return "", Session{}, ErrSessionLimit
	}
	if _, exists := m.sessions[sessionDigest]; exists {
		return "", Session{}, ErrInvalidSession
	}
	m.sessions[sessionDigest] = sessionRecord{
		principal: principal, csrfToken: csrfToken,
		csrfDigest: sha256.Sum256([]byte(csrfToken)), expiresAt: expiresAt,
	}
	return sessionToken, Session{Principal: principal, CSRFToken: csrfToken, ExpiresAt: expiresAt}, nil
}

func (m *SessionManager) Get(sessionToken string) (Session, bool) {
	if m == nil || sessionToken == "" || len(sessionToken) > maximumTokenLength {
		return Session{}, false
	}
	digest := sha256.Sum256([]byte(sessionToken))
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.sessions[digest]
	if !ok || !record.expiresAt.After(now) {
		delete(m.sessions, digest)
		return Session{}, false
	}
	return Session{Principal: record.principal, CSRFToken: record.csrfToken, ExpiresAt: record.expiresAt}, true
}

func (m *SessionManager) ValidateCSRF(sessionToken string, csrfToken string) bool {
	if m == nil || sessionToken == "" || csrfToken == "" || len(csrfToken) > maximumTokenLength {
		return false
	}
	sessionDigest := sha256.Sum256([]byte(sessionToken))
	csrfDigest := sha256.Sum256([]byte(csrfToken))
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.sessions[sessionDigest]
	return ok && record.expiresAt.After(m.now().UTC()) &&
		subtle.ConstantTimeCompare(csrfDigest[:], record.csrfDigest[:]) == 1
}

func (m *SessionManager) Delete(sessionToken string) {
	if m == nil || sessionToken == "" {
		return
	}
	digest := sha256.Sum256([]byte(sessionToken))
	m.mu.Lock()
	delete(m.sessions, digest)
	m.mu.Unlock()
}

func (m *SessionManager) deleteExpiredLocked(now time.Time) {
	for digest, record := range m.sessions {
		if !record.expiresAt.After(now) {
			delete(m.sessions, digest)
		}
	}
}

func randomToken(reader io.Reader) (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
