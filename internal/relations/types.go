package relations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/conditions"
)

var (
	ErrInvalidCommand    = errors.New("invalid relation command")
	ErrForbidden         = errors.New("relation command is forbidden")
	ErrRelationNotFound  = errors.New("relation not found")
	ErrRevisionConflict  = errors.New("relation revision conflict")
	ErrPendingProposal   = errors.New("relation already has a pending proposal")
	ErrInvalidTransition = errors.New("invalid relation state transition")
	ErrDuplicateRelation = errors.New("duplicate relation")
)

type RevisionConflictError struct {
	CurrentRevisionNo int
	Current           *Relation
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("%s: current revision is %d", ErrRevisionConflict, e.CurrentRevisionNo)
}

func (e *RevisionConflictError) Unwrap() error {
	return ErrRevisionConflict
}

type Role int

const (
	RoleViewer Role = iota + 1
	RoleAgent
	RoleEditor
	RoleReviewer
	RoleAdmin
)

type Principal struct {
	Actor  string
	Role   Role
	Origin audit.Origin
}

type Type int

const (
	TypeConditionalValueCopy Type = iota + 1
	TypeDeclaredForeignKey
)

type ProposalKind int

const (
	ProposalContent ProposalKind = iota + 1
	ProposalTombstone
	ProposalStale
)

type Status int

const (
	StatusPending Status = iota
	StatusApproved
	StatusSuppressed
	StatusTombstoned
	StatusStale
)

type Decision int

const (
	DecisionApprove Decision = iota + 1
	DecisionReject
)

type EvidenceKind int

const (
	EvidenceCode EvidenceKind = iota + 1
	EvidenceSQLMapping
	EvidenceManual
	EvidenceDatabaseConstraint
)

type EvidenceInput struct {
	Kind             EvidenceKind
	Repository       string
	Commit           string
	File             string
	Symbol           string
	StartLine        int
	EndLine          int
	DataSourceID     int64
	ConstraintSchema string
	ConstraintName   string
	ScanRunID        int64
}

type Revision struct {
	ID                 int64
	RelationID         int64
	RevisionNo         int
	Kind               ProposalKind
	SourceNodeID       int64
	TargetNodeID       int64
	Guard              *conditions.Boolean
	Selector           *conditions.Boolean
	Transform          conditions.Value
	Confidence         float64
	Evidence           []EvidenceInput
	Actor              string
	Origin             audit.Origin
	Reason             string
	RequestID          string
	ExpectedRevisionNo *int
	CreatedAt          time.Time
}

type Relation struct {
	ID               int64
	ProjectID        int64
	Type             Type
	LatestRevisionNo int
	Status           Status
	Active           *Revision
	Proposed         *Revision
	Effective        bool
	CreatedAt        time.Time
}

type ProposeCreate struct {
	ProjectID    int64
	Type         Type
	SourceNodeID int64
	TargetNodeID int64
	Guard        *conditions.Boolean
	Selector     *conditions.Boolean
	Transform    conditions.Value
	Confidence   float64
	Evidence     []EvidenceInput
	Principal    Principal
	Reason       string
	RequestID    string
}

type ProposeRevision struct {
	RelationID         int64
	ExpectedRevisionNo int
	SourceNodeID       int64
	TargetNodeID       int64
	Guard              *conditions.Boolean
	Selector           *conditions.Boolean
	Transform          conditions.Value
	Confidence         float64
	Evidence           []EvidenceInput
	Principal          Principal
	Reason             string
	RequestID          string
}

type ProposeTombstone struct {
	RelationID         int64
	ExpectedRevisionNo int
	Principal          Principal
	Reason             string
	RequestID          string
}

type Review struct {
	RelationID         int64
	ExpectedRevisionNo int
	Decision           Decision
	Principal          Principal
	Reason             string
	RequestID          string
}

type ChangeState struct {
	RelationID         int64
	ExpectedRevisionNo int
	Principal          Principal
	Reason             string
	RequestID          string
}

type EventType int

const (
	EventProposed EventType = iota + 1
	EventApproved
	EventRejected
	EventSuperseded
	EventTombstoned
	EventSuppressed
	EventRestored
	EventStale
)

type ReferenceRole int

const (
	ReferenceGuard ReferenceRole = iota + 1
	ReferenceSelector
	ReferenceTransform
)

type Reference struct {
	NodeID int64
	Role   ReferenceRole
}

type ProposalRecord struct {
	RelationID  int64
	VersionID   int64
	EventID     int64
	AuditID     int64
	ProjectID   int64
	Type        Type
	Fingerprint string
	Revision    Revision
	References  []Reference
}

type ReviewRecord struct {
	RelationID         int64
	ExpectedRevisionNo int
	Decision           Decision
	EventID            int64
	SupersededEventID  int64
	AuditID            int64
	Principal          Principal
	Reason             string
	RequestID          string
	OccurredAt         time.Time
}

type StateRecord struct {
	RelationID         int64
	ExpectedRevisionNo int
	EventID            int64
	AuditID            int64
	Principal          Principal
	Reason             string
	RequestID          string
	OccurredAt         time.Time
}

type Repository interface {
	ProposeCreate(context.Context, ProposalRecord) (Relation, error)
	ProposeRevision(context.Context, ProposalRecord) (Relation, error)
	Review(context.Context, ReviewRecord) (Relation, error)
	Suppress(context.Context, StateRecord) (Relation, error)
	Restore(context.Context, StateRecord) (Relation, error)
	Get(context.Context, int64) (Relation, error)
	ListProposals(context.Context, int64, int) ([]Relation, error)
}

type IDGenerator interface {
	Next(context.Context) (int64, error)
}
