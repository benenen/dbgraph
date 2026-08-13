package relations

import (
	"context"
	"testing"
	"time"
)

// A reviewer working through a queue is agreeing with a proposal that already
// carries the proposer's justification. Making them retype one of their own
// before every click buys nothing, so a blank reason is recorded as a stated
// default: the audit row still says who decided what, and why in the only sense
// a blank box can mean.
func TestReviewRecordsADefaultReasonWhenTheReviewerLeavesItBlank(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		decision Decision
		want     string
	}{
		"approve": {decision: DecisionApprove, want: DefaultApprovalReason},
		"reject":  {decision: DecisionReject, want: DefaultRejectionReason},
	} {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			repository := &repositoryStub{}
			commands := NewCommands(repository, &idGeneratorStub{}, time.Now)

			if _, err := commands.Review(context.Background(), Review{
				RelationID: 1, ExpectedRevisionNo: 1, Decision: testCase.decision,
				Principal: validPrincipal(RoleReviewer), Reason: "   ", RequestID: "request",
			}); err != nil {
				t.Fatalf("Review with a blank reason: %v", err)
			}
			if repository.review.Reason != testCase.want {
				t.Fatalf("recorded reason = %q, want %q", repository.review.Reason, testCase.want)
			}
		})
	}
}

// A reviewer who does write one keeps it. The default fills a gap; it never
// replaces a decision someone took the trouble to explain.
func TestReviewKeepsTheReasonTheReviewerWrote(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{}
	commands := NewCommands(repository, &idGeneratorStub{}, time.Now)

	if _, err := commands.Review(context.Background(), Review{
		RelationID: 1, ExpectedRevisionNo: 1, Decision: DecisionReject,
		Principal: validPrincipal(RoleReviewer),
		Reason:    "The guard reads BusinessType = 2, not 1.", RequestID: "request",
	}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if repository.review.Reason != "The guard reads BusinessType = 2, not 1." {
		t.Fatalf("recorded reason = %q, want the reviewer's own", repository.review.Reason)
	}
}

// Proposing is not reviewing. A proposal is a claim about source code that
// nobody else has seen yet, so it still has to come with a stated reason.
func TestProposeStillRequiresAReason(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{}
	commands := NewCommands(repository, &idGeneratorStub{}, time.Now)

	if _, err := commands.ProposeTombstone(context.Background(), ProposeTombstone{
		RelationID: 1, ExpectedRevisionNo: 1, Principal: validPrincipal(RoleEditor),
		Reason: "", RequestID: "request",
	}); err == nil {
		t.Fatal("ProposeTombstone accepted a blank reason")
	}
}
