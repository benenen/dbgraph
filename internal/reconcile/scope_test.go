package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/relations"
)

type scopeRepository struct {
	Repository
	beginCalls int
}

func (r *scopeRepository) Begin(_ context.Context, session Session) (Session, error) {
	r.beginCalls++
	return session, nil
}

type scopeIDGenerator struct{ next int64 }

func (g *scopeIDGenerator) Next(context.Context) (int64, error) {
	g.next++
	return g.next, nil
}

func TestIncrementalBeginRequiresExplicitBoundedDecimalRelationIDs(t *testing.T) {
	t.Parallel()

	principal := relations.Principal{Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent}
	valid := []json.RawMessage{
		json.RawMessage(`{"relationIds":[]}`),
		json.RawMessage(`{"relationIds":["1","9223372036854775807"]}`),
	}
	for index, scope := range valid {
		repository := &scopeRepository{}
		service := NewService(repository, nil, &scopeIDGenerator{}, func() time.Time { return time.Unix(1, 0) })
		if _, err := service.Begin(t.Context(), Begin{
			ProjectID: 1, RepositoryID: 2, Mode: ModeIncremental, SourceCommit: "abc",
			Scope: scope, Principal: principal, RequestID: "valid-" + strconv.Itoa(index),
		}); err != nil || repository.beginCalls != 1 {
			t.Fatalf("valid scope %s error=%v calls=%d", scope, err, repository.beginCalls)
		}
	}

	tooMany := make([]string, maximumIncrementalRelationIDs+1)
	for index := range tooMany {
		tooMany[index] = strconv.Itoa(index + 1)
	}
	encodedTooMany, err := json.Marshal(map[string]any{"relationIds": tooMany})
	if err != nil {
		t.Fatal(err)
	}
	invalid := []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"relationIds":null}`),
		json.RawMessage(`{"relationIds":[1]}`),
		json.RawMessage(`{"relationIds":["0"]}`),
		json.RawMessage(`{"relationIds":["01"]}`),
		json.RawMessage(`{"relationIds":["1","1"]}`),
		json.RawMessage(`{"relationIds":["9223372036854775808"]}`),
		json.RawMessage(`{"relationIds":[],"module":"service"}`),
		json.RawMessage(encodedTooMany),
		json.RawMessage(`{"relationIds":["` + strings.Repeat("9", 20) + `"]}`),
	}
	for index, scope := range invalid {
		repository := &scopeRepository{}
		service := NewService(repository, nil, &scopeIDGenerator{}, nil)
		_, err := service.Begin(t.Context(), Begin{
			ProjectID: 1, RepositoryID: 2, Mode: ModeIncremental, SourceCommit: "abc",
			Scope: scope, Principal: principal, RequestID: "invalid-" + strconv.Itoa(index),
		})
		if !errors.Is(err, ErrInvalidInit) || repository.beginCalls != 0 {
			t.Fatalf("invalid scope %s error=%v calls=%d", scope, err, repository.beginCalls)
		}
	}
}
