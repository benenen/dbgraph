package reconcile_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/conditions"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestSQLiteCompletionBudgetFailureIsAtomicAndLeavesSessionOpen(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		candidates func(relations.ProposalRecord) []relations.ProposalRecord
	}{
		{
			name: "candidate count",
			candidates: func(candidate relations.ProposalRecord) []relations.ProposalRecord {
				result := make([]relations.ProposalRecord, reconcile.MaximumCompletionCandidates+1)
				for index := range result {
					result[index] = candidate
				}
				return result
			},
		},
		{
			name: "candidate raw AST bytes",
			candidates: func(candidate relations.ProposalRecord) []relations.ProposalRecord {
				candidate.Revision.Transform = conditions.Value{
					Kind: conditions.ValueLiteral,
					Literal: &conditions.Literal{
						Type:  conditions.LiteralString,
						Value: json.RawMessage(`"` + strings.Repeat("x", reconcile.MaximumCompletionCandidateRawBytes) + `"`),
					},
				}
				return []relations.ProposalRecord{candidate}
			},
		},
	}
	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newReproposalFixture(t, uint16(30+index))
			initialSession, initial := fixture.beginAndSubmit(t, "budget-initial")
			relationID := initial.Items[0].RelationID
			if _, err := fixture.service.Complete(fixture.ctx, reconcile.Complete{
				SessionID: initialSession.ID, ExpectedBatchCount: 1, Principal: fixture.agent,
				Reason: "Complete the initial relation analysis.", RequestID: "complete-budget-initial",
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.commands.Review(fixture.ctx, relations.Review{
				RelationID: relationID, ExpectedRevisionNo: 1, Decision: relations.DecisionApprove,
				Principal: fixture.reviewer, Reason: "Approve the initial relation.", RequestID: "approve-budget-initial",
			}); err != nil {
				t.Fatal(err)
			}

			budgetSession, _ := fixture.beginAndSubmit(t, "budget-overflow-"+testCase.name)
			candidate, err := fixture.commands.PrepareStale(fixture.ctx, relations.ProposeTombstone{
				RelationID: relationID, ExpectedRevisionNo: 1, Principal: fixture.agent,
				Reason: "Prepare an omission candidate.", RequestID: "prepare-budget-candidate",
			})
			if err != nil {
				t.Fatal(err)
			}
			auditRepository := dbsqlite.NewAuditRepository(fixture.store)
			beforeAudit, err := auditRepository.ListAuditEvents(fixture.ctx, fixture.project.ID, 100)
			if err != nil {
				t.Fatal(err)
			}

			_, err = dbsqlite.NewReconcileRepository(fixture.store).Complete(fixture.ctx, reconcile.CompletionRecord{
				Session: budgetSession, ExpectedBatchCount: 1,
				Candidates: testCase.candidates(candidate), AuditID: candidate.AuditID + 10_000,
				Principal: fixture.agent, Reason: "Attempt an over-budget completion.",
				RequestID: "complete-budget-overflow", CompletedAt: time.Unix(500, 0).UTC(),
			})
			if !errors.Is(err, reconcile.ErrCompletionBudgetExceeded) {
				t.Fatalf("complete error = %v, want %v", err, reconcile.ErrCompletionBudgetExceeded)
			}
			currentSession, getErr := fixture.service.Get(fixture.ctx, budgetSession.ID)
			if getErr != nil || currentSession.Status != reconcile.StatusOpen {
				t.Fatalf("session after budget rejection = %#v, error = %v", currentSession, getErr)
			}
			relation, getErr := fixture.commands.Get(fixture.ctx, relationID)
			if getErr != nil || relation.LatestRevisionNo != 1 || relation.Proposed != nil || !relation.Effective {
				t.Fatalf("relation after budget rejection = %#v, error = %v", relation, getErr)
			}
			afterAudit, getErr := auditRepository.ListAuditEvents(fixture.ctx, fixture.project.ID, 100)
			if getErr != nil || len(afterAudit) != len(beforeAudit) {
				t.Fatalf("audit count after budget rejection = %d, want %d, error = %v", len(afterAudit), len(beforeAudit), getErr)
			}
		})
	}
}

func TestSQLiteOmissionListingStopsAtRemainingBudget(t *testing.T) {
	t.Parallel()

	fixture := newReproposalFixture(t, 32)
	initialSession, initial := fixture.beginAndSubmit(t, "bounded-omission-initial")
	relationID := initial.Items[0].RelationID
	if _, err := fixture.service.Complete(fixture.ctx, reconcile.Complete{
		SessionID: initialSession.ID, ExpectedBatchCount: 1, Principal: fixture.agent,
		Reason: "Complete the initial relation analysis.", RequestID: "complete-bounded-omission-initial",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.commands.Review(fixture.ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 1, Decision: relations.DecisionApprove,
		Principal: fixture.reviewer, Reason: "Approve the initial relation.", RequestID: "approve-bounded-omission-initial",
	}); err != nil {
		t.Fatal(err)
	}
	omissionSession, err := fixture.service.Begin(fixture.ctx, reconcile.Begin{
		ProjectID: fixture.project.ID, RepositoryID: fixture.repository.ID, Mode: reconcile.ModeFull,
		SourceCommit: "bounded-omission", Scope: json.RawMessage(`{}`), Principal: fixture.agent,
		RequestID: "begin-bounded-omission",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := dbsqlite.NewReconcileRepository(fixture.store)

	for _, testCase := range []struct {
		name   string
		budget reconcile.CompletionBudget
	}{
		{
			name: "candidate count",
			budget: reconcile.CompletionBudget{
				CandidateLimit: 0,
				RawByteLimit:   reconcile.MaximumCompletionCandidateRawBytes,
			},
		},
		{
			name: "raw AST bytes",
			budget: reconcile.CompletionBudget{
				CandidateLimit: 1,
				RawByteLimit:   0,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, listErr := repository.ListOmittedRelations(fixture.ctx, omissionSession, testCase.budget)
			if !errors.Is(listErr, reconcile.ErrCompletionBudgetExceeded) {
				t.Fatalf("list omissions error = %v, want %v", listErr, reconcile.ErrCompletionBudgetExceeded)
			}
		})
	}
}
