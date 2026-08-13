package relations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/conditions"
)

type repositoryStub struct {
	relation  Relation
	list      []Relation
	err       error
	state     StateRecord
	review    ReviewRecord
	proposal  ProposalRecord
	getCalls  int
	listCalls int
}

func (repository *repositoryStub) ProposeCreate(_ context.Context, record ProposalRecord) (Relation, error) {
	repository.proposal = record
	return repository.relation, repository.err
}

func (repository *repositoryStub) ProposeRevision(_ context.Context, record ProposalRecord) (Relation, error) {
	repository.proposal = record
	return repository.relation, repository.err
}

func (repository *repositoryStub) Review(_ context.Context, record ReviewRecord) (Relation, error) {
	repository.review = record
	return repository.relation, repository.err
}

func (repository *repositoryStub) Suppress(_ context.Context, record StateRecord) (Relation, error) {
	repository.state = record
	return repository.relation, repository.err
}

func (repository *repositoryStub) Restore(_ context.Context, record StateRecord) (Relation, error) {
	repository.state = record
	return repository.relation, repository.err
}

func (repository *repositoryStub) Get(context.Context, int64) (Relation, error) {
	repository.getCalls++
	return repository.relation, repository.err
}

func (repository *repositoryStub) ListProposals(context.Context, int) ([]Relation, error) {
	repository.listCalls++
	return repository.list, repository.err
}

type idGeneratorStub struct {
	next int64
	err  error
}

func (generator *idGeneratorStub) Next(context.Context) (int64, error) {
	if generator.err != nil {
		return 0, generator.err
	}
	generator.next++
	return generator.next, nil
}

func validPrincipal(role Role) Principal {
	return Principal{Actor: "actor", Role: role, Origin: audit.OriginWeb}
}

func validContent() (conditions.Value, []EvidenceInput) {
	return conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 11}, []EvidenceInput{{
		Kind: EvidenceCode, Repository: "repo", Commit: "abc", File: "service.go", StartLine: 1, EndLine: 2,
	}}
}

func TestCommandsGetAndListValidateBeforeRepository(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{relation: Relation{ID: 7}, list: []Relation{{ID: 8}}}
	commands := NewCommands(repository, &idGeneratorStub{}, nil)
	if _, err := commands.Get(context.Background(), 0); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("Get invalid = %v", err)
	}
	result, err := commands.Get(context.Background(), 7)
	if err != nil || result.ID != 7 || repository.getCalls != 1 {
		t.Fatalf("Get result = %#v, error = %v, calls = %d", result, err, repository.getCalls)
	}
	for _, input := range []struct {
		limit int
	}{{0}, {101}} {
		if _, err := commands.ListProposals(context.Background(), input.limit); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("ListProposals(%d) = %v", input.limit, err)
		}
	}
	listed, err := commands.ListProposals(context.Background(), 100)
	if err != nil || len(listed) != 1 || repository.listCalls != 1 {
		t.Fatalf("ListProposals = %#v, error = %v, calls = %d", listed, err, repository.listCalls)
	}
}

func TestProposalWrappersReturnPreparationErrorsWithoutRepositoryWrites(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{}
	commands := NewCommands(repository, &idGeneratorStub{}, time.Now)
	if _, err := commands.ProposeCreate(context.Background(), ProposeCreate{
		Principal: validPrincipal(RoleViewer),
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ProposeCreate error = %v", err)
	}
	if _, err := commands.ProposeTombstone(context.Background(), ProposeTombstone{
		Principal: validPrincipal(RoleViewer),
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ProposeTombstone error = %v", err)
	}
	if repository.proposal.VersionID != 0 {
		t.Fatalf("repository received proposal %#v", repository.proposal)
	}
}

func TestReviewerMayReviseContentButCannotCreateOrTombstoneRelations(t *testing.T) {
	t.Parallel()

	transform, evidence := validContent()
	active := &Revision{
		ID: 10, RelationID: 9, RevisionNo: 1, Kind: ProposalContent,
		SourceNodeID: 11, TargetNodeID: 12, Transform: transform,
		Confidence: 1, Evidence: evidence,
	}
	repository := &repositoryStub{relation: Relation{
		ID: 9, Type: TypeConditionalValueCopy,
		LatestRevisionNo: 1, Status: StatusApproved, Active: active, Effective: true,
	}}
	commands := NewCommands(repository, &idGeneratorStub{next: 100}, time.Now)
	principal := validPrincipal(RoleReviewer)

	if _, err := commands.ProposeCreate(context.Background(), ProposeCreate{
		Type: TypeConditionalValueCopy, SourceNodeID: 11, TargetNodeID: 12,
		Transform: transform, Confidence: 1, Evidence: evidence, Principal: principal,
		Reason: "Reviewer create", RequestID: "reviewer-create",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reviewer create error = %v, want ErrForbidden", err)
	}
	if _, err := commands.ProposeTombstone(context.Background(), ProposeTombstone{
		RelationID: 9, ExpectedRevisionNo: 1, Principal: principal,
		Reason: "Reviewer tombstone", RequestID: "reviewer-tombstone",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reviewer tombstone error = %v, want ErrForbidden", err)
	}
	if _, err := commands.ProposeRevision(context.Background(), ProposeRevision{
		RelationID: 9, ExpectedRevisionNo: 1, SourceNodeID: 11, TargetNodeID: 12,
		Transform: transform, Confidence: 1, Evidence: evidence, Principal: principal,
		Reason: "Reviewer correction", RequestID: "reviewer-revision",
	}); err != nil {
		t.Fatalf("reviewer revision error = %v", err)
	}
	if repository.proposal.Revision.RevisionNo != 2 || repository.proposal.Revision.Actor != principal.Actor ||
		repository.proposal.Revision.Origin != principal.Origin {
		t.Fatalf("reviewer revision proposal = %#v", repository.proposal)
	}
}

func TestCommandsRejectForbiddenAndInvalidStateChangesWithoutIDs(t *testing.T) {
	t.Parallel()

	ids := &idGeneratorStub{}
	repository := &repositoryStub{}
	commands := NewCommands(repository, ids, time.Now)
	for name, call := range map[string]func() error{
		"review role": func() error {
			_, err := commands.Review(context.Background(), Review{RelationID: 1, ExpectedRevisionNo: 1, Decision: DecisionApprove, Principal: validPrincipal(RoleEditor), Reason: "reason", RequestID: "request"})
			return err
		},
		"review decision": func() error {
			_, err := commands.Review(context.Background(), Review{RelationID: 1, ExpectedRevisionNo: 1, Decision: 99, Principal: validPrincipal(RoleReviewer), Reason: "reason", RequestID: "request"})
			return err
		},
		"suppress role": func() error {
			_, err := commands.Suppress(context.Background(), ChangeState{RelationID: 1, ExpectedRevisionNo: 1, Principal: validPrincipal(RoleEditor), Reason: "reason", RequestID: "request"})
			return err
		},
		"restore ID": func() error {
			_, err := commands.Restore(context.Background(), ChangeState{RelationID: 0, ExpectedRevisionNo: 1, Principal: validPrincipal(RoleReviewer), Reason: "reason", RequestID: "request"})
			return err
		},
	} {
		name, call := name, call
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("command returned nil")
			}
		})
	}
	if ids.next != 0 {
		t.Fatalf("allocated %d IDs for rejected commands", ids.next)
	}
}

func TestCommandsForwardReviewAndStateRecordsAndErrors(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, 8, 11, 15, 0, 0, 0, time.UTC)
	repository := &repositoryStub{relation: Relation{ID: 9}}
	ids := &idGeneratorStub{next: 100}
	commands := NewCommands(repository, ids, func() time.Time { return fixedTime })
	principal := validPrincipal(RoleReviewer)

	reviewed, err := commands.Review(context.Background(), Review{
		RelationID: 9, ExpectedRevisionNo: 2, Decision: DecisionReject,
		Principal: principal, Reason: " reject ", RequestID: " review-1 ",
	})
	if err != nil || reviewed.ID != 9 || repository.review.EventID == 0 || repository.review.OccurredAt != fixedTime || repository.review.Reason != "reject" {
		t.Fatalf("Review result = %#v, record = %#v, error = %v", reviewed, repository.review, err)
	}
	restored, err := commands.Restore(context.Background(), ChangeState{
		RelationID: 9, ExpectedRevisionNo: 3, Principal: principal, Reason: " restore ", RequestID: " restore-1 ",
	})
	if err != nil || restored.ID != 9 || repository.state.EventID == 0 || repository.state.RequestID != "restore-1" {
		t.Fatalf("Restore result = %#v, record = %#v, error = %v", restored, repository.state, err)
	}

	sentinel := errors.New("repository failed")
	repository.err = sentinel
	if _, err := commands.Suppress(context.Background(), ChangeState{
		RelationID: 9, ExpectedRevisionNo: 3, Principal: principal, Reason: "suppress", RequestID: "suppress-1",
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Suppress error = %v", err)
	}
}

func TestProposeRevisionRejectsProtectedAndConflictingRelations(t *testing.T) {
	t.Parallel()

	transform, evidence := validContent()
	base := ProposeRevision{
		RelationID: 5, ExpectedRevisionNo: 2, SourceNodeID: 11, TargetNodeID: 12,
		Transform: transform, Confidence: 1, Evidence: evidence,
		Principal: validPrincipal(RoleAgent), Reason: "update", RequestID: "update-1",
	}

	for name, relation := range map[string]Relation{
		"revision conflict":    {ID: 5, Type: TypeConditionalValueCopy, LatestRevisionNo: 3},
		"declared foreign key": {ID: 5, Type: TypeDeclaredForeignKey, LatestRevisionNo: 2},
		"pending proposal":     {ID: 5, Type: TypeConditionalValueCopy, LatestRevisionNo: 2, Proposed: &Revision{ID: 1}},
	} {
		name, relation := name, relation
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			commands := NewCommands(&repositoryStub{relation: relation}, &idGeneratorStub{}, time.Now)
			if _, err := commands.ProposeRevision(context.Background(), base); err == nil {
				t.Fatal("ProposeRevision returned nil")
			}
		})
	}
}

func TestRevisionConflictErrorSupportsErrorsIs(t *testing.T) {
	t.Parallel()

	err := &RevisionConflictError{CurrentRevisionNo: 4}
	if !errors.Is(err, ErrRevisionConflict) || err.Error() == "" || !errors.Is(err.Unwrap(), ErrRevisionConflict) {
		t.Fatalf("revision conflict error = %v", err)
	}
	if _, err := json.Marshal(err.Error()); err != nil {
		t.Fatalf("marshal error text: %v", err)
	}
}

func TestComplexContentCopiesAndCollectsAllReferenceKinds(t *testing.T) {
	t.Parallel()

	column := func(id int64) conditions.Value { return conditions.Value{Kind: conditions.ValueColumn, NodeID: id} }
	literal := func(raw string) conditions.Value {
		return conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{Type: conditions.LiteralInteger, Value: json.RawMessage(raw)}}
	}
	guard := &conditions.Boolean{Kind: conditions.BooleanAnd, Children: []conditions.Boolean{
		{Kind: conditions.BooleanCompare, Operator: conditions.CompareEqual, Left: pointerConditionValue(column(21)), Right: pointerConditionValue(literal("1"))},
		{Kind: conditions.BooleanIn, Left: pointerConditionValue(column(22)), Values: []conditions.Value{literal("1"), literal("2")}},
	}}
	selectorOperand := conditions.Boolean{Kind: conditions.BooleanCompare, Operator: conditions.CompareEqual, Left: pointerConditionValue(column(23)), Right: pointerConditionValue(column(24))}
	selector := &conditions.Boolean{Kind: conditions.BooleanNot, Operand: &selectorOperand}
	transform := conditions.Value{Kind: conditions.ValueCase, Cases: []conditions.Case{{
		When: conditions.Boolean{Kind: conditions.BooleanIsNotNull, Left: pointerConditionValue(column(25))},
		Then: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 26},
	}}, Else: pointerConditionValue(conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: 27})}
	evidence := []EvidenceInput{{Kind: EvidenceManual, Repository: " repo ", Commit: " commit ", File: " file ", StartLine: 2, EndLine: 3}}

	content, references, err := validateAndCopyContent(11, 12, guard, selector, transform, 0.8, evidence)
	if err != nil {
		t.Fatalf("validateAndCopyContent: %v", err)
	}
	if len(references) != 7 || content.Evidence[0].Repository != "repo" {
		t.Fatalf("content = %#v, references = %#v", content, references)
	}
	guard.Children[0].Left.NodeID = 999
	evidence[0].Repository = "mutated"
	if content.Guard.Children[0].Left.NodeID != 21 || content.Evidence[0].Repository != "repo" {
		t.Fatal("validated content retained caller-owned memory")
	}

	expected := 3
	revision := Revision{Guard: content.Guard, Selector: content.Selector, Transform: content.Transform, Evidence: content.Evidence, ExpectedRevisionNo: &expected}
	clone := cloneRevision(revision)
	expected = 4
	revision.Evidence[0].Repository = "changed"
	if *clone.ExpectedRevisionNo != 3 || clone.Evidence[0].Repository != "repo" {
		t.Fatalf("clone = %#v", clone)
	}
}

func TestConflictOrAddsCurrentRevisionOnlyForRevisionConflicts(t *testing.T) {
	t.Parallel()

	wrapped := conflictOr(ErrRevisionConflict, Relation{LatestRevisionNo: 8})
	var conflict *RevisionConflictError
	if !errors.As(wrapped, &conflict) || conflict.CurrentRevisionNo != 8 {
		t.Fatalf("wrapped conflict = %#v", wrapped)
	}
	sentinel := errors.New("storage unavailable")
	if returned := conflictOr(sentinel, Relation{LatestRevisionNo: 8}); !errors.Is(returned, sentinel) {
		t.Fatalf("non-conflict = %v", returned)
	}
}

func pointerConditionValue(value conditions.Value) *conditions.Value { return &value }
