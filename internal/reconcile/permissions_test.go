package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestOnlyAgentOrAdminCanBeginRelationInitialization(t *testing.T) {
	t.Parallel()

	service := NewService(nil, nil, nil, nil)
	for _, role := range []relations.Role{
		relations.RoleViewer,
		relations.RoleEditor,
		relations.RoleReviewer,
	} {
		_, err := service.Begin(context.Background(), Begin{
			RepositoryID: 2, Mode: ModeFull, SourceCommit: "abc",
			Scope:     json.RawMessage(`{}`),
			Principal: relations.Principal{Actor: "user", Role: role, Origin: audit.OriginAgent},
			RequestID: "begin-1",
		})
		if !errors.Is(err, ErrInvalidInit) {
			t.Fatalf("role %v begin error = %v, want ErrInvalidInit", role, err)
		}
	}
}
