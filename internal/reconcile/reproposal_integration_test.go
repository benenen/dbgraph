package reconcile_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
)

func TestRelationInitReproposesTombstonedFingerprintWithoutPublishing(t *testing.T) {
	t.Parallel()

	fixture := newReproposalFixture(t, 18)
	first := fixture.submit(t, "initial")
	if len(first.Items) != 1 || first.Items[0].Status != reconcile.ItemCreated {
		t.Fatalf("initial batch = %#v", first)
	}
	relationID := first.Items[0].RelationID

	pendingDuplicate := fixture.submit(t, "pending-duplicate")
	if len(pendingDuplicate.Items) != 1 || pendingDuplicate.Items[0].RelationID != relationID ||
		pendingDuplicate.Items[0].Status != reconcile.ItemDeduplicated {
		t.Fatalf("pending duplicate batch = %#v", pendingDuplicate)
	}
	pending, err := fixture.commands.Get(fixture.ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.LatestRevisionNo != 1 || pending.Proposed == nil || pending.Proposed.RevisionNo != 1 || pending.Effective {
		t.Fatalf("pending duplicate changed relation = %#v", pending)
	}

	approved, err := fixture.commands.Review(fixture.ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 1, Decision: relations.DecisionApprove,
		Principal: fixture.reviewer, Reason: "Approve the initialized relation.", RequestID: "approve-initial",
	})
	if err != nil {
		t.Fatalf("approve initial relation: %v", err)
	}
	if approved.Status != relations.StatusApproved || approved.Active == nil || approved.Active.RevisionNo != 1 || !approved.Effective {
		t.Fatalf("approved initial relation = %#v", approved)
	}
	approvedDuplicate := fixture.submit(t, "approved-duplicate")
	if len(approvedDuplicate.Items) != 1 || approvedDuplicate.Items[0].RelationID != relationID ||
		approvedDuplicate.Items[0].Status != reconcile.ItemDeduplicated {
		t.Fatalf("approved duplicate batch = %#v", approvedDuplicate)
	}
	unchanged, err := fixture.commands.Get(fixture.ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.LatestRevisionNo != 1 || unchanged.Proposed != nil || !unchanged.Effective ||
		unchanged.Active == nil || unchanged.Active.RevisionNo != 1 {
		t.Fatalf("approved duplicate changed relation = %#v", unchanged)
	}

	if _, err := fixture.commands.ProposeTombstone(fixture.ctx, relations.ProposeTombstone{
		RelationID: relationID, ExpectedRevisionNo: 1, Principal: fixture.agent,
		Reason: "The code relation disappeared.", RequestID: "propose-tombstone",
	}); err != nil {
		t.Fatalf("propose tombstone: %v", err)
	}
	tombstoned, err := fixture.commands.Review(fixture.ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 2, Decision: relations.DecisionApprove,
		Principal: fixture.reviewer, Reason: "Confirm the code relation removal.", RequestID: "approve-tombstone",
	})
	if err != nil {
		t.Fatalf("approve tombstone: %v", err)
	}
	if tombstoned.Status != relations.StatusTombstoned || tombstoned.Active == nil ||
		tombstoned.Active.RevisionNo != 2 || tombstoned.Active.Kind != relations.ProposalTombstone || tombstoned.Effective {
		t.Fatalf("tombstoned relation = %#v", tombstoned)
	}

	reappearedSession, reappeared := fixture.beginAndSubmit(t, "reappeared")
	if len(reappeared.Items) != 1 || reappeared.Items[0].RelationID != relationID ||
		reappeared.Items[0].Status != reconcile.ItemReproposed {
		t.Fatalf("reappeared batch = %#v", reappeared)
	}
	retried, err := fixture.service.SubmitBatch(fixture.ctx, fixture.batchCommand("reappeared", reappearedSession.ID))
	if err != nil {
		t.Fatalf("retry deferred reappearance: %v", err)
	}
	if retried.BatchID != reappeared.BatchID || len(retried.Items) != 1 ||
		retried.Items[0] != reappeared.Items[0] {
		t.Fatalf("deferred reappearance retry changed result: first=%#v retry=%#v", reappeared, retried)
	}
	stillTombstoned, err := fixture.commands.Get(fixture.ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if stillTombstoned.LatestRevisionNo != 2 || stillTombstoned.Status != relations.StatusTombstoned ||
		stillTombstoned.Active == nil || stillTombstoned.Active.RevisionNo != 2 ||
		stillTombstoned.Active.Kind != relations.ProposalTombstone || stillTombstoned.Proposed != nil || stillTombstoned.Effective {
		t.Fatalf("incomplete reappearance changed relation = %#v", stillTombstoned)
	}
	if _, err := fixture.service.Complete(fixture.ctx, reconcile.Complete{
		SessionID: reappearedSession.ID, ExpectedBatchCount: 1, Principal: fixture.agent,
		Reason: "Complete the reappearance analysis.", RequestID: "complete-reappeared",
	}); err != nil {
		t.Fatalf("complete reappeared init: %v", err)
	}
	reproposed, err := fixture.commands.Get(fixture.ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if reproposed.LatestRevisionNo != 3 || reproposed.Status != relations.StatusTombstoned ||
		reproposed.Active == nil || reproposed.Active.RevisionNo != 2 || reproposed.Active.Kind != relations.ProposalTombstone ||
		reproposed.Proposed == nil || reproposed.Proposed.RevisionNo != 3 ||
		reproposed.Proposed.Kind != relations.ProposalContent || reproposed.Proposed.ExpectedRevisionNo == nil ||
		*reproposed.Proposed.ExpectedRevisionNo != 2 || reproposed.Effective {
		t.Fatalf("reappeared relation was not an isolated proposed revision = %#v", reproposed)
	}

	auditEvents, err := dbsqlite.NewAuditRepository(fixture.store).ListAuditEvents(fixture.ctx, fixture.project.ID, 100)
	if err != nil {
		t.Fatalf("list reproposal audit events: %v", err)
	}
	var reproposalAudit *audit.Event
	for index := range auditEvents {
		event := &auditEvents[index]
		if event.SubjectType == "RELATION" && event.SubjectID == relationID &&
			event.Action == "RELATION_REVISION_PROPOSED" && event.RequestID == "batch-reappeared:1" {
			reproposalAudit = event
			break
		}
	}
	if reproposalAudit == nil || reproposalAudit.ExpectedRevision == nil || *reproposalAudit.ExpectedRevision != 2 {
		t.Fatalf("reproposal audit = %#v", reproposalAudit)
	}
	var details struct {
		RevisionNo int `json:"revisionNo"`
	}
	if err := json.Unmarshal(reproposalAudit.Details, &details); err != nil || details.RevisionNo != 3 {
		t.Fatalf("reproposal audit details = %s, error = %v", reproposalAudit.Details, err)
	}

	restored, err := fixture.commands.Review(fixture.ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 3, Decision: relations.DecisionApprove,
		Principal: fixture.reviewer, Reason: "Approve the reappeared relation.", RequestID: "approve-reappeared",
	})
	if err != nil {
		t.Fatalf("approve reappeared relation: %v", err)
	}
	if restored.Status != relations.StatusApproved || restored.Active == nil || restored.Active.RevisionNo != 3 ||
		restored.Proposed != nil || !restored.Effective {
		t.Fatalf("approved reappeared relation = %#v", restored)
	}
}

func TestRelationInitReproposesRejectedCreateWithoutActiveRevision(t *testing.T) {
	t.Parallel()

	fixture := newReproposalFixture(t, 19)
	first := fixture.submit(t, "rejected-initial")
	if len(first.Items) != 1 || first.Items[0].Status != reconcile.ItemCreated {
		t.Fatalf("initial rejected batch = %#v", first)
	}
	relationID := first.Items[0].RelationID
	rejected, err := fixture.commands.Review(fixture.ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 1, Decision: relations.DecisionReject,
		Principal: fixture.reviewer, Reason: "Reject the initial evidence.", RequestID: "reject-initial",
	})
	if err != nil {
		t.Fatalf("reject initial relation: %v", err)
	}
	if rejected.Status != relations.StatusPending || rejected.Active != nil || rejected.Proposed != nil || rejected.Effective {
		t.Fatalf("rejected initial relation = %#v", rejected)
	}

	reappearedSession, reappeared := fixture.beginAndSubmit(t, "rejected-reappeared")
	if len(reappeared.Items) != 1 || reappeared.Items[0].RelationID != relationID ||
		reappeared.Items[0].Status != reconcile.ItemReproposed {
		t.Fatalf("reappeared rejected batch = %#v", reappeared)
	}
	stillRejected, err := fixture.commands.Get(fixture.ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if stillRejected.LatestRevisionNo != 1 || stillRejected.Status != relations.StatusPending ||
		stillRejected.Active != nil || stillRejected.Proposed != nil || stillRejected.Effective {
		t.Fatalf("incomplete rejected reappearance changed relation = %#v", stillRejected)
	}
	if _, err := fixture.service.Complete(fixture.ctx, reconcile.Complete{
		SessionID: reappearedSession.ID, ExpectedBatchCount: 1, Principal: fixture.agent,
		Reason: "Complete the rejected reappearance analysis.", RequestID: "complete-rejected-reappeared",
	}); err != nil {
		t.Fatalf("complete rejected reappearance init: %v", err)
	}
	reproposed, err := fixture.commands.Get(fixture.ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if reproposed.LatestRevisionNo != 2 || reproposed.Status != relations.StatusPending || reproposed.Active != nil ||
		reproposed.Proposed == nil || reproposed.Proposed.RevisionNo != 2 ||
		reproposed.Proposed.ExpectedRevisionNo == nil || *reproposed.Proposed.ExpectedRevisionNo != 1 || reproposed.Effective {
		t.Fatalf("reappeared rejected relation = %#v", reproposed)
	}
}

func TestRelationInitReproposesStaleFingerprintAfterCompletion(t *testing.T) {
	t.Parallel()

	fixture := newReproposalFixture(t, 22)
	initialSession, initial := fixture.beginAndSubmit(t, "stale-initial")
	if len(initial.Items) != 1 || initial.Items[0].Status != reconcile.ItemCreated {
		t.Fatalf("initial stale batch = %#v", initial)
	}
	relationID := initial.Items[0].RelationID
	if _, err := fixture.service.Complete(fixture.ctx, reconcile.Complete{
		SessionID: initialSession.ID, ExpectedBatchCount: 1, Principal: fixture.agent,
		Reason: "Complete the initial relation analysis.", RequestID: "complete-stale-initial",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.commands.Review(fixture.ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 1, Decision: relations.DecisionApprove,
		Principal: fixture.reviewer, Reason: "Approve the initial relation.", RequestID: "approve-stale-initial",
	}); err != nil {
		t.Fatal(err)
	}

	omissionSession, err := fixture.service.Begin(fixture.ctx, reconcile.Begin{
		SourceCommit: "commit-stale-omission",
		Scope:        json.RawMessage(fmt.Sprintf(`{"relationIds":["%d"]}`, relationID)),
		Principal:    fixture.agent, RequestID: "begin-stale-omission",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SubmitBatch(fixture.ctx, reconcile.SubmitBatch{
		SessionID: omissionSession.ID, BatchNo: 1, IdempotencyKey: "key-stale-omission",
		Principal: fixture.agent, RequestID: "batch-stale-omission",
		Unresolved: []reconcile.UnresolvedInput{{
			Type: "NO_RELATION", Summary: "The scoped relation was not present.", Evidence: json.RawMessage(`{}`),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Complete(fixture.ctx, reconcile.Complete{
		SessionID: omissionSession.ID, ExpectedBatchCount: 1, Principal: fixture.agent,
		Reason: "Complete the omission analysis.", RequestID: "complete-stale-omission",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.commands.Review(fixture.ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 2, Decision: relations.DecisionApprove,
		Principal: fixture.reviewer, Reason: "Confirm the relation is stale.", RequestID: "approve-stale-omission",
	}); err != nil {
		t.Fatal(err)
	}

	reappearedSession, reappeared := fixture.beginAndSubmit(t, "stale-reappeared")
	if len(reappeared.Items) != 1 || reappeared.Items[0].RelationID != relationID ||
		reappeared.Items[0].Status != reconcile.ItemReproposed {
		t.Fatalf("stale reappearance batch = %#v", reappeared)
	}
	stillStale, err := fixture.commands.Get(fixture.ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if stillStale.Status != relations.StatusStale || stillStale.Active == nil ||
		stillStale.Active.Kind != relations.ProposalStale || stillStale.Proposed != nil || stillStale.Effective {
		t.Fatalf("incomplete stale reappearance changed relation = %#v", stillStale)
	}
	if _, err := fixture.service.Complete(fixture.ctx, reconcile.Complete{
		SessionID: reappearedSession.ID, ExpectedBatchCount: 1, Principal: fixture.agent,
		Reason: "Complete the stale reappearance analysis.", RequestID: "complete-stale-reappeared",
	}); err != nil {
		t.Fatal(err)
	}
	reproposed, err := fixture.commands.Get(fixture.ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if reproposed.Status != relations.StatusStale || reproposed.Proposed == nil ||
		reproposed.Proposed.Kind != relations.ProposalContent || reproposed.Proposed.RevisionNo != 3 || reproposed.Effective {
		t.Fatalf("stale reappearance proposal = %#v", reproposed)
	}
	restored, err := fixture.commands.Review(fixture.ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 3, Decision: relations.DecisionApprove,
		Principal: fixture.reviewer, Reason: "Approve the reappeared relation.", RequestID: "approve-stale-reappeared",
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != relations.StatusApproved || restored.Active == nil ||
		restored.Active.RevisionNo != 3 || restored.Proposed != nil || !restored.Effective {
		t.Fatalf("restored stale relation = %#v", restored)
	}
}

func TestRelationInitCompletionLeavesSessionOpenWhenDeferredReproposalConflicts(t *testing.T) {
	t.Parallel()

	fixture := newReproposalFixture(t, 21)
	initial := fixture.submit(t, "conflict-initial")
	if len(initial.Items) != 1 {
		t.Fatalf("initial batch = %#v", initial)
	}
	relationID := initial.Items[0].RelationID
	if _, err := fixture.commands.Review(fixture.ctx, relations.Review{
		RelationID: relationID, ExpectedRevisionNo: 1, Decision: relations.DecisionReject,
		Principal: fixture.reviewer, Reason: "Reject initial evidence.", RequestID: "conflict-reject-initial",
	}); err != nil {
		t.Fatal(err)
	}

	secondGuard := &conditions.Boolean{
		Kind: conditions.BooleanCompare, Operator: conditions.CompareEqual,
		Left: &conditions.Value{Kind: conditions.ValueColumn, NodeID: fixture.source.ID},
		Right: &conditions.Value{Kind: conditions.ValueLiteral, Literal: &conditions.Literal{
			Type: conditions.LiteralInteger, Value: json.RawMessage(`2`),
		}},
	}
	second, err := fixture.commands.ProposeCreate(fixture.ctx, relations.ProposeCreate{
		SourceNodeID: fixture.source.ID, TargetNodeID: fixture.target.ID, Guard: secondGuard,
		Transform: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: fixture.source.ID}, Confidence: 0.8,
		Evidence: []relations.EvidenceInput{{
			Kind: relations.EvidenceCode, Repository: fixture.repository.Name, Commit: "second-initial",
			File: "src/Service.java", Symbol: "Service.copySecond", StartLine: 30, EndLine: 30,
		}},
		Principal: fixture.agent, Reason: "Create a second relation for atomic conflict coverage.", RequestID: "second-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.commands.Review(fixture.ctx, relations.Review{
		RelationID: second.ID, ExpectedRevisionNo: 1, Decision: relations.DecisionReject,
		Principal: fixture.reviewer, Reason: "Reject second initial evidence.", RequestID: "second-reject",
	}); err != nil {
		t.Fatal(err)
	}

	session, err := fixture.service.Begin(fixture.ctx, reconcile.Begin{
		SourceCommit: "conflict-deferred", Scope: json.RawMessage(`{"relationIds":[]}`),
		Principal: fixture.agent, RequestID: "begin-conflict-deferred",
	})
	if err != nil {
		t.Fatal(err)
	}
	deferredCommand := fixture.batchCommand("conflict-deferred", session.ID)
	deferredCommand.Proposals = append(deferredCommand.Proposals, reconcile.Proposal{
		Type: relations.TypeConditionalValueCopy, SourceNodeID: fixture.source.ID, TargetNodeID: fixture.target.ID,
		Guard: secondGuard, Transform: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: fixture.source.ID}, Confidence: 0.8,
		Evidence: []relations.EvidenceInput{{
			Kind: relations.EvidenceCode, Repository: fixture.repository.Name, Commit: "second-reappeared",
			File: "src/Service.java", Symbol: "Service.copySecond", StartLine: 30, EndLine: 30,
		}},
		Reason: "The second relation reappeared.",
	})
	deferred, err := fixture.service.SubmitBatch(fixture.ctx, deferredCommand)
	if err != nil {
		t.Fatal(err)
	}
	if len(deferred.Items) != 2 || deferred.Items[0].Status != reconcile.ItemReproposed ||
		deferred.Items[1].Status != reconcile.ItemReproposed {
		t.Fatalf("deferred batch = %#v", deferred)
	}
	competing, err := fixture.commands.ProposeRevision(fixture.ctx, relations.ProposeRevision{
		RelationID: second.ID, ExpectedRevisionNo: 1,
		SourceNodeID: fixture.source.ID, TargetNodeID: fixture.target.ID,
		Guard:     secondGuard,
		Transform: conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: fixture.source.ID}, Confidence: 0.8,
		Evidence: []relations.EvidenceInput{{
			Kind: relations.EvidenceCode, Repository: fixture.repository.Name, Commit: "competing",
			File: "src/Service.java", Symbol: "Service.copy", StartLine: 20, EndLine: 20,
		}},
		Principal: fixture.agent, Reason: "A competing observation arrived.", RequestID: "competing-revision",
	})
	if err != nil || competing.LatestRevisionNo != 2 || competing.Proposed == nil {
		t.Fatalf("competing revision = %#v, error = %v", competing, err)
	}

	_, err = fixture.service.Complete(fixture.ctx, reconcile.Complete{
		SessionID: session.ID, ExpectedBatchCount: 1, Principal: fixture.agent,
		Reason: "Attempt to complete stale deferred work.", RequestID: "complete-conflicting-deferred",
	})
	if !errors.Is(err, relations.ErrRevisionConflict) {
		t.Fatalf("completion error = %v, want revision conflict", err)
	}
	stillOpen, err := fixture.service.Get(fixture.ctx, session.ID)
	if err != nil || stillOpen.Status != reconcile.StatusOpen {
		t.Fatalf("session after conflict = %#v, error = %v", stillOpen, err)
	}
	rolledBack, err := fixture.commands.Get(fixture.ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.LatestRevisionNo != 1 || rolledBack.Active != nil || rolledBack.Proposed != nil || rolledBack.Effective {
		t.Fatalf("earlier candidate was not rolled back = %#v", rolledBack)
	}
	unchanged, err := fixture.commands.Get(fixture.ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.LatestRevisionNo != 2 || unchanged.Proposed == nil ||
		unchanged.Proposed.RequestID != "competing-revision" || unchanged.Effective {
		t.Fatalf("conflicting relation after completion = %#v", unchanged)
	}
}

type reproposalFixture struct {
	ctx        context.Context
	store      *dbsqlite.Store
	repository catalog.CodeRepository
	source     catalog.Node
	target     catalog.Node
	service    *reconcile.Service
	commands   *relations.Commands
	agent      relations.Principal
	reviewer   relations.Principal
}

func newReproposalFixture(t *testing.T, generatorNode uint16) reproposalFixture {
	t.Helper()
	ctx := context.Background()
	store, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixedTime := time.Date(2026, time.August, 11, 19, 0, 0, 0, time.UTC)
	ids, err := id.NewGenerator(generatorNode, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatal(err)
	}
	project, repository, source, target := createInitFixture(t, ctx, store, ids, fixedTime)
	commands := relations.NewCommands(dbsqlite.NewRelationRepository(store), ids, func() time.Time { return fixedTime })
	return reproposalFixture{
		ctx: ctx, store: store, project: project, repository: repository, source: source, target: target,
		commands: commands,
		service: reconcile.NewService(
			dbsqlite.NewReconcileRepository(store), commands, ids, func() time.Time { return fixedTime },
		),
		agent:    relations.Principal{Actor: "agent@example.test", Role: relations.RoleAgent, Origin: audit.OriginAgent},
		reviewer: relations.Principal{Actor: "reviewer@example.test", Role: relations.RoleReviewer, Origin: audit.OriginWeb},
	}
}

func (f reproposalFixture) submit(t *testing.T, suffix string) reconcile.BatchResult {
	t.Helper()
	_, result := f.beginAndSubmit(t, suffix)
	return result
}

func (f reproposalFixture) beginAndSubmit(t *testing.T, suffix string) (reconcile.Session, reconcile.BatchResult) {
	t.Helper()
	session, err := f.service.Begin(f.ctx, reconcile.Begin{
		SourceCommit: "commit-" + suffix, Scope: json.RawMessage(`{"relationIds":[]}`),
		Principal: f.agent, RequestID: "begin-" + suffix,
	})
	if err != nil {
		t.Fatalf("begin %s init: %v", suffix, err)
	}
	result, err := f.service.SubmitBatch(f.ctx, f.batchCommand(suffix, session.ID))
	if err != nil {
		t.Fatalf("submit %s init: %v", suffix, err)
	}
	return session, result
}

func (f reproposalFixture) batchCommand(suffix string, sessionID int64) reconcile.SubmitBatch {
	return reconcile.SubmitBatch{
		SessionID: sessionID, BatchNo: 1, IdempotencyKey: "key-" + suffix,
		Principal: f.agent, RequestID: "batch-" + suffix,
		Proposals: []reconcile.Proposal{{
			Type: relations.TypeConditionalValueCopy, SourceNodeID: f.source.ID, TargetNodeID: f.target.ID,
			Transform:  conditions.Value{Kind: conditions.ValueColumnCopy, NodeID: f.source.ID},
			Confidence: 0.91,
			Evidence: []relations.EvidenceInput{{
				Kind: relations.EvidenceCode, Repository: f.repository.Name, Commit: "commit-" + suffix,
				File: "src/Service.java", Symbol: "Service.copy", StartLine: 10, EndLine: 12,
			}},
			Reason: fmt.Sprintf("Agent observed the relation in %s.", suffix),
		}},
	}
}
