package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/relations"
)

type completionOrderRepository struct {
	Repository
	session       Session
	omissionReads int
	check         CompletionCheck
	checkError    error
	omitted       []relations.Relation
	completeCalls int
	lastBudget    CompletionBudget
}

func (r *completionOrderRepository) Get(context.Context, int64) (Session, error) {
	return r.session, nil
}

func (r *completionOrderRepository) ListOmittedRelations(
	_ context.Context,
	_ Session,
	budget CompletionBudget,
) (OmissionPlan, error) {
	r.omissionReads++
	r.lastBudget = budget
	if r.omitted != nil {
		return OmissionPlan{Relations: r.omitted}, nil
	}
	return OmissionPlan{}, errors.New("omissions must not be read before batch validation")
}

func (r *completionOrderRepository) CheckCompletion(context.Context, Session, int) (CompletionCheck, error) {
	return r.check, r.checkError
}

func (r *completionOrderRepository) Complete(context.Context, CompletionRecord) (Completion, error) {
	r.completeCalls++
	return Completion{}, ErrIncompleteBatches
}

func TestCompleteValidatesBatchIntegrityBeforeReadingOmissions(t *testing.T) {
	t.Parallel()

	principal := relations.Principal{Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent}
	repository := &completionOrderRepository{session: Session{
		ID: 41, ProjectID: 42, RepositoryID: 43, Mode: ModeFull, Status: StatusOpen,
		Principal: principal,
	}, checkError: ErrIncompleteBatches}
	service := NewService(repository, nil, nil, func() time.Time { return time.Unix(1, 0) })

	_, err := service.Complete(t.Context(), Complete{
		SessionID: 41, ExpectedBatchCount: 2, Principal: principal,
		Reason: "Finish a complete repository analysis.", RequestID: "complete-order",
	})
	if !errors.Is(err, ErrIncompleteBatches) {
		t.Fatalf("complete error = %v, want %v", err, ErrIncompleteBatches)
	}
	if repository.omissionReads != 0 {
		t.Fatalf("omission reads = %d, want zero before batch validation", repository.omissionReads)
	}
}

func TestCompleteRejectsCombinedCandidateBudgetsBeforeCreatingRevisions(t *testing.T) {
	t.Parallel()

	principal := relations.Principal{Actor: "agent", Role: relations.RoleAgent, Origin: audit.OriginAgent}
	session := Session{
		ID: 51, ProjectID: 52, RepositoryID: 53, Mode: ModeFull, Status: StatusOpen,
		Principal: principal,
	}
	minimalOmission := relations.Relation{Active: &relations.Revision{
		Transform: conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
			Type: conditions.LiteralString, Value: json.RawMessage(`"x"`),
		}},
	}}

	testCases := []struct {
		name       string
		check      CompletionCheck
		omitted    []relations.Relation
		wantBudget CompletionBudget
	}{
		{
			name: "candidate count combines deferred and omitted",
			check: CompletionCheck{
				DeferredCandidateCount: 1,
			},
			omitted: make([]relations.Relation, MaximumCompletionCandidates),
			wantBudget: CompletionBudget{
				CandidateLimit: MaximumCompletionCandidates - 1,
				RawByteLimit:   MaximumCompletionCandidateRawBytes,
			},
		},
		{
			name: "raw bytes combine deferred JSON and omitted AST",
			check: CompletionCheck{
				DeferredCandidateRawBytes: MaximumCompletionCandidateRawBytes,
			},
			omitted: []relations.Relation{minimalOmission},
			wantBudget: CompletionBudget{
				CandidateLimit: MaximumCompletionCandidates,
				RawByteLimit:   0,
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &completionOrderRepository{
				session: session, check: testCase.check, omitted: testCase.omitted,
			}
			service := NewService(repository, nil, nil, func() time.Time { return time.Unix(1, 0) })

			_, err := service.Complete(t.Context(), Complete{
				SessionID: session.ID, ExpectedBatchCount: 1, Principal: principal,
				Reason: "Finish a bounded repository analysis.", RequestID: "complete-budget",
			})
			if !errors.Is(err, ErrCompletionBudgetExceeded) {
				t.Fatalf("complete error = %v, want %v", err, ErrCompletionBudgetExceeded)
			}
			if repository.completeCalls != 0 {
				t.Fatalf("repository completion calls = %d, want zero", repository.completeCalls)
			}
			if repository.lastBudget != testCase.wantBudget {
				t.Fatalf("omission budget = %#v, want %#v", repository.lastBudget, testCase.wantBudget)
			}
			current, getErr := service.Get(t.Context(), session.ID)
			if getErr != nil || current.Status != StatusOpen {
				t.Fatalf("session after budget rejection = %#v, error = %v", current, getErr)
			}
		})
	}
}
