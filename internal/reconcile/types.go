package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/relations"
)

var (
	ErrInvalidInit              = errors.New("invalid relation init command")
	ErrInitNotFound             = errors.New("relation init session not found")
	ErrInitNotOpen              = errors.New("relation init session is not open")
	ErrBatchConflict            = errors.New("relation init batch conflict")
	ErrIdempotencyConflict      = errors.New("relation init idempotency conflict")
	ErrIncompleteBatches        = errors.New("relation init batches are incomplete")
	ErrCompletionBudgetExceeded = fmt.Errorf(
		"%w: relation init completion candidate budget exceeded",
		ErrInvalidInit,
	)
)

const (
	MaximumCompletionCandidates        = 100
	MaximumCompletionCandidateRawBytes = 1 << 20
)

type Mode int

const (
	ModeFull Mode = iota + 1
	ModeIncremental
)

type Status int

const (
	StatusOpen Status = iota + 1
	StatusCompleted
	StatusFailed
	StatusCancelled
)

type Session struct {
	ID           int64
	RepositoryID int64
	Mode         Mode
	SourceCommit string
	Scope        json.RawMessage
	Status       Status
	Principal    relations.Principal
	RequestID    string
	CreatedAt    time.Time
	CompletedAt  *time.Time
}

type Begin struct {
	RepositoryID int64
	Mode         Mode
	SourceCommit string
	Scope        json.RawMessage
	Principal    relations.Principal
	RequestID    string
}

type Proposal struct {
	Type         relations.Type
	SourceNodeID int64
	TargetNodeID int64
	Guard        *conditions.Boolean
	Selector     *conditions.Boolean
	Transform    conditions.Value
	Confidence   float64
	Evidence     []relations.EvidenceInput
	Reason       string
}

type UnresolvedInput struct {
	Type     string
	Summary  string
	Evidence json.RawMessage
}

type Unresolved struct {
	ID           int64
	RepositoryID int64
	SessionID    int64
	BatchID      int64
	Fingerprint  string
	Type         string
	Summary      string
	Evidence     json.RawMessage
	Status       int
	Principal    relations.Principal
	CreatedAt    time.Time
}

type SubmitBatch struct {
	SessionID      int64
	BatchNo        int
	IdempotencyKey string
	Proposals      []Proposal
	Unresolved     []UnresolvedInput
	Principal      relations.Principal
	RequestID      string
}

type ItemStatus string

const (
	ItemCreated      ItemStatus = "CREATED"
	ItemDeduplicated ItemStatus = "DEDUPLICATED"
	ItemReproposed   ItemStatus = "REPROPOSED"
)

type ItemResult struct {
	RelationID int64      `json:"relationId"`
	Status     ItemStatus `json:"status"`
}

type BatchResult struct {
	BatchID       int64        `json:"batchId"`
	SessionID     int64        `json:"sessionId"`
	BatchNo       int          `json:"batchNo"`
	Items         []ItemResult `json:"items"`
	UnresolvedIDs []int64      `json:"unresolvedIds"`
	AcceptedAt    time.Time    `json:"acceptedAt"`
}

type BatchRecord struct {
	BatchID        int64
	Session        Session
	BatchNo        int
	IdempotencyKey string
	RequestID      string
	PayloadDigest  string
	Proposals      []relations.ProposalRecord
	Unresolved     []Unresolved
	AcceptedAt     time.Time
}

type Complete struct {
	SessionID          int64
	ExpectedBatchCount int
	Principal          relations.Principal
	Reason             string
	RequestID          string
}

type Completion struct {
	Session              Session
	CandidateRelationIDs []int64
}

type CompletionRecord struct {
	Session            Session
	ExpectedBatchCount int
	Candidates         []relations.ProposalRecord
	AuditID            int64
	Principal          relations.Principal
	Reason             string
	RequestID          string
	CompletedAt        time.Time
}

type CompletionCheck struct {
	DeferredCandidateCount    int
	DeferredCandidateRawBytes int64
}

// CompletionBudget is the capacity left for omission candidates after
// deferred candidates have been counted without decoding them.
type CompletionBudget struct {
	CandidateLimit int
	RawByteLimit   int64
}

type OmissionPlan struct {
	Relations []relations.Relation
	RawBytes  int64
}

func PrepareReproposal(
	current relations.Relation,
	prepared relations.ProposalRecord,
) (relations.ProposalRecord, bool) {
	if current.ID <= 0 || current.LatestRevisionNo <= 0 || current.Proposed != nil ||
		current.Type != prepared.Type ||
		prepared.Revision.Kind != relations.ProposalContent {
		return relations.ProposalRecord{}, false
	}
	tombstoned := current.Status == relations.StatusTombstoned && current.Active != nil &&
		current.Active.Kind == relations.ProposalTombstone
	stale := current.Status == relations.StatusStale && current.Active != nil &&
		current.Active.Kind == relations.ProposalStale
	rejectedCreate := current.Status == relations.StatusPending && current.Active == nil
	if !tombstoned && !stale && !rejectedCreate {
		return relations.ProposalRecord{}, false
	}

	expected := current.LatestRevisionNo
	revision := prepared.Revision
	revision.RelationID = current.ID
	revision.RevisionNo = expected + 1
	revision.ExpectedRevisionNo = &expected
	reproposal := prepared
	reproposal.RelationID = current.ID
	reproposal.Revision = revision
	reproposal.References = append([]relations.Reference(nil), prepared.References...)
	return reproposal, true
}

type Repository interface {
	Begin(context.Context, Session) (Session, error)
	Get(context.Context, int64) (Session, error)
	SubmitBatch(context.Context, BatchRecord) (BatchResult, error)
	CheckCompletion(context.Context, Session, int) (CompletionCheck, error)
	ListOmittedRelations(context.Context, Session, CompletionBudget) (OmissionPlan, error)
	Complete(context.Context, CompletionRecord) (Completion, error)
	ListUnresolved(context.Context, int) ([]Unresolved, error)
}
