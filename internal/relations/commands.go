package relations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/conditions"
)

const (
	maximumEvidenceItems = 20
	maximumEvidenceText  = 2000
)

type Commands struct {
	repository Repository
	ids        IDGenerator
	now        func() time.Time
}

func NewCommands(repository Repository, ids IDGenerator, now func() time.Time) *Commands {
	if now == nil {
		now = time.Now
	}
	return &Commands{repository: repository, ids: ids, now: now}
}

func (c *Commands) ProposeCreate(ctx context.Context, command ProposeCreate) (Relation, error) {
	record, err := c.PrepareCreate(ctx, command)
	if err != nil {
		return Relation{}, err
	}
	return c.repository.ProposeCreate(ctx, record)
}

func (c *Commands) PrepareCreate(ctx context.Context, command ProposeCreate) (ProposalRecord, error) {
	if !canCreateOrTombstone(command.Principal) {
		return ProposalRecord{}, ErrForbidden
	}
	if command.ProjectID <= 0 || !validType(command.Type) {
		return ProposalRecord{}, ErrInvalidCommand
	}
	content, references, err := validateAndCopyContent(
		command.SourceNodeID,
		command.TargetNodeID,
		command.Guard,
		command.Selector,
		command.Transform,
		command.Confidence,
		command.Evidence,
	)
	if err != nil {
		return ProposalRecord{}, err
	}
	actor, reason, requestID, err := validateMetadata(command.Principal, command.Reason, command.RequestID)
	if err != nil {
		return ProposalRecord{}, err
	}
	relationID, versionID, eventID, auditID, err := c.allocateCommandIDs(ctx, 4)
	if err != nil {
		return ProposalRecord{}, err
	}
	createdAt := c.now().UTC()
	revision := Revision{
		ID:           versionID,
		RelationID:   relationID,
		RevisionNo:   1,
		Kind:         ProposalContent,
		SourceNodeID: content.SourceNodeID,
		TargetNodeID: content.TargetNodeID,
		Guard:        content.Guard,
		Selector:     content.Selector,
		Transform:    content.Transform,
		Confidence:   content.Confidence,
		Evidence:     content.Evidence,
		Actor:        actor,
		Origin:       command.Principal.Origin,
		Reason:       reason,
		RequestID:    requestID,
		CreatedAt:    createdAt,
	}
	fingerprint, err := contentFingerprint(command.Type, revision)
	if err != nil {
		return ProposalRecord{}, err
	}
	return ProposalRecord{
		RelationID:  relationID,
		VersionID:   versionID,
		EventID:     eventID,
		AuditID:     auditID,
		ProjectID:   command.ProjectID,
		Type:        command.Type,
		Fingerprint: fingerprint,
		Revision:    revision,
		References:  references,
	}, nil
}

func (c *Commands) ProposeRevision(ctx context.Context, command ProposeRevision) (Relation, error) {
	if !canRevise(command.Principal) {
		return Relation{}, ErrForbidden
	}
	current, err := c.repository.Get(ctx, command.RelationID)
	if err != nil {
		return Relation{}, err
	}
	if err := requireRevision(current, command.ExpectedRevisionNo); err != nil {
		return Relation{}, err
	}
	if current.Type == TypeDeclaredForeignKey {
		return Relation{}, ErrForbidden
	}
	if current.Proposed != nil {
		return Relation{}, ErrPendingProposal
	}
	content, references, err := validateAndCopyContent(
		command.SourceNodeID,
		command.TargetNodeID,
		command.Guard,
		command.Selector,
		command.Transform,
		command.Confidence,
		command.Evidence,
	)
	if err != nil {
		return Relation{}, err
	}
	actor, reason, requestID, err := validateMetadata(command.Principal, command.Reason, command.RequestID)
	if err != nil {
		return Relation{}, err
	}
	versionID, eventID, auditID, _, err := c.allocateCommandIDs(ctx, 3)
	if err != nil {
		return Relation{}, err
	}
	expected := command.ExpectedRevisionNo
	revision := Revision{
		ID:                 versionID,
		RelationID:         command.RelationID,
		RevisionNo:         current.LatestRevisionNo + 1,
		Kind:               ProposalContent,
		SourceNodeID:       content.SourceNodeID,
		TargetNodeID:       content.TargetNodeID,
		Guard:              content.Guard,
		Selector:           content.Selector,
		Transform:          content.Transform,
		Confidence:         content.Confidence,
		Evidence:           content.Evidence,
		Actor:              actor,
		Origin:             command.Principal.Origin,
		Reason:             reason,
		RequestID:          requestID,
		ExpectedRevisionNo: &expected,
		CreatedAt:          c.now().UTC(),
	}
	fingerprint, err := contentFingerprint(current.Type, revision)
	if err != nil {
		return Relation{}, err
	}
	return c.repository.ProposeRevision(ctx, ProposalRecord{
		VersionID:   versionID,
		EventID:     eventID,
		AuditID:     auditID,
		ProjectID:   current.ProjectID,
		Type:        current.Type,
		Fingerprint: fingerprint,
		Revision:    revision,
		References:  references,
	})
}

func (c *Commands) ProposeTombstone(ctx context.Context, command ProposeTombstone) (Relation, error) {
	record, err := c.PrepareTombstone(ctx, command)
	if err != nil {
		return Relation{}, err
	}
	return c.repository.ProposeRevision(ctx, record)
}

func (c *Commands) PrepareTombstone(ctx context.Context, command ProposeTombstone) (ProposalRecord, error) {
	return c.prepareInvalidation(ctx, command, ProposalTombstone)
}

// PrepareStale prepares the reviewable invalidation used by completed relation-init sessions.
func (c *Commands) PrepareStale(ctx context.Context, command ProposeTombstone) (ProposalRecord, error) {
	return c.prepareInvalidation(ctx, command, ProposalStale)
}

func (c *Commands) prepareInvalidation(
	ctx context.Context,
	command ProposeTombstone,
	kind ProposalKind,
) (ProposalRecord, error) {
	if !canCreateOrTombstone(command.Principal) {
		return ProposalRecord{}, ErrForbidden
	}
	current, err := c.repository.Get(ctx, command.RelationID)
	if err != nil {
		return ProposalRecord{}, err
	}
	if err := requireRevision(current, command.ExpectedRevisionNo); err != nil {
		return ProposalRecord{}, err
	}
	if current.Type == TypeDeclaredForeignKey {
		return ProposalRecord{}, ErrForbidden
	}
	if current.Proposed != nil {
		return ProposalRecord{}, ErrPendingProposal
	}
	if current.Active == nil || current.Status == StatusTombstoned || current.Status == StatusStale {
		return ProposalRecord{}, ErrInvalidTransition
	}
	actor, reason, requestID, err := validateMetadata(command.Principal, command.Reason, command.RequestID)
	if err != nil {
		return ProposalRecord{}, err
	}
	versionID, eventID, auditID, _, err := c.allocateCommandIDs(ctx, 3)
	if err != nil {
		return ProposalRecord{}, err
	}
	expected := command.ExpectedRevisionNo
	revision := cloneRevision(*current.Active)
	revision.ID = versionID
	revision.RevisionNo = current.LatestRevisionNo + 1
	revision.Kind = kind
	revision.Actor = actor
	revision.Origin = command.Principal.Origin
	revision.Reason = reason
	revision.RequestID = requestID
	revision.ExpectedRevisionNo = &expected
	revision.CreatedAt = c.now().UTC()
	references := referencesForRevision(revision)
	fingerprint, err := contentFingerprint(current.Type, revision)
	if err != nil {
		return ProposalRecord{}, err
	}
	return ProposalRecord{
		VersionID:   versionID,
		EventID:     eventID,
		AuditID:     auditID,
		ProjectID:   current.ProjectID,
		Type:        current.Type,
		Fingerprint: fingerprint,
		Revision:    revision,
		References:  references,
	}, nil
}

func (c *Commands) Review(ctx context.Context, command Review) (Relation, error) {
	if !canReview(command.Principal) {
		return Relation{}, ErrForbidden
	}
	if command.RelationID <= 0 || command.ExpectedRevisionNo <= 0 ||
		(command.Decision != DecisionApprove && command.Decision != DecisionReject) {
		return Relation{}, ErrInvalidCommand
	}
	actor, reason, requestID, err := validateMetadata(command.Principal, command.Reason, command.RequestID)
	if err != nil {
		return Relation{}, err
	}
	eventID, supersededEventID, auditID, _, err := c.allocateCommandIDs(ctx, 3)
	if err != nil {
		return Relation{}, err
	}
	return c.repository.Review(ctx, ReviewRecord{
		RelationID:         command.RelationID,
		ExpectedRevisionNo: command.ExpectedRevisionNo,
		Decision:           command.Decision,
		EventID:            eventID,
		SupersededEventID:  supersededEventID,
		AuditID:            auditID,
		Principal:          Principal{Actor: actor, Role: command.Principal.Role, Origin: command.Principal.Origin},
		Reason:             reason,
		RequestID:          requestID,
		OccurredAt:         c.now().UTC(),
	})
}

func (c *Commands) Suppress(ctx context.Context, command ChangeState) (Relation, error) {
	return c.changeState(ctx, command, c.repository.Suppress)
}

func (c *Commands) Restore(ctx context.Context, command ChangeState) (Relation, error) {
	return c.changeState(ctx, command, c.repository.Restore)
}

func (c *Commands) Get(ctx context.Context, relationID int64) (Relation, error) {
	if relationID <= 0 {
		return Relation{}, ErrInvalidCommand
	}
	return c.repository.Get(ctx, relationID)
}

func (c *Commands) ListProposals(ctx context.Context, projectID int64, limit int) ([]Relation, error) {
	if projectID <= 0 || limit < 1 || limit > 100 {
		return nil, ErrInvalidCommand
	}
	return c.repository.ListProposals(ctx, projectID, limit)
}

func (c *Commands) changeState(
	ctx context.Context,
	command ChangeState,
	change func(context.Context, StateRecord) (Relation, error),
) (Relation, error) {
	if !canReview(command.Principal) {
		return Relation{}, ErrForbidden
	}
	if command.RelationID <= 0 || command.ExpectedRevisionNo <= 0 {
		return Relation{}, ErrInvalidCommand
	}
	actor, reason, requestID, err := validateMetadata(command.Principal, command.Reason, command.RequestID)
	if err != nil {
		return Relation{}, err
	}
	eventID, auditID, _, _, err := c.allocateCommandIDs(ctx, 2)
	if err != nil {
		return Relation{}, err
	}
	return change(ctx, StateRecord{
		RelationID:         command.RelationID,
		ExpectedRevisionNo: command.ExpectedRevisionNo,
		EventID:            eventID,
		AuditID:            auditID,
		Principal:          Principal{Actor: actor, Role: command.Principal.Role, Origin: command.Principal.Origin},
		Reason:             reason,
		RequestID:          requestID,
		OccurredAt:         c.now().UTC(),
	})
}

func (c *Commands) allocateCommandIDs(ctx context.Context, count int) (int64, int64, int64, int64, error) {
	allocated := [4]int64{}
	for index := 0; index < count; index++ {
		value, err := c.ids.Next(ctx)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("generate relation command ID: %w", err)
		}
		allocated[index] = value
	}
	return allocated[0], allocated[1], allocated[2], allocated[3], nil
}

type validatedContent struct {
	SourceNodeID int64
	TargetNodeID int64
	Guard        *conditions.Boolean
	Selector     *conditions.Boolean
	Transform    conditions.Value
	Confidence   float64
	Evidence     []EvidenceInput
}

func validateAndCopyContent(
	sourceNodeID int64,
	targetNodeID int64,
	guard *conditions.Boolean,
	selector *conditions.Boolean,
	transform conditions.Value,
	confidence float64,
	evidence []EvidenceInput,
) (validatedContent, []Reference, error) {
	if sourceNodeID <= 0 || targetNodeID <= 0 || sourceNodeID == targetNodeID ||
		math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
		return validatedContent{}, nil, ErrInvalidCommand
	}
	limits := conditions.DefaultLimits()
	if guard != nil {
		if err := conditions.ValidateBoolean(*guard, limits); err != nil {
			return validatedContent{}, nil, fmt.Errorf("%w: guard: %v", ErrInvalidCommand, err)
		}
	}
	if selector != nil {
		if err := conditions.ValidateBoolean(*selector, limits); err != nil {
			return validatedContent{}, nil, fmt.Errorf("%w: selector: %v", ErrInvalidCommand, err)
		}
	}
	if err := conditions.ValidateValue(transform, limits); err != nil {
		return validatedContent{}, nil, fmt.Errorf("%w: transform: %v", ErrInvalidCommand, err)
	}
	copiedEvidence, err := validateAndCopyEvidence(evidence)
	if err != nil {
		return validatedContent{}, nil, err
	}
	copiedGuard, err := copyBoolean(guard)
	if err != nil {
		return validatedContent{}, nil, err
	}
	copiedSelector, err := copyBoolean(selector)
	if err != nil {
		return validatedContent{}, nil, err
	}
	copiedTransform, err := copyValue(transform)
	if err != nil {
		return validatedContent{}, nil, err
	}
	content := validatedContent{
		SourceNodeID: sourceNodeID,
		TargetNodeID: targetNodeID,
		Guard:        copiedGuard,
		Selector:     copiedSelector,
		Transform:    copiedTransform,
		Confidence:   confidence,
		Evidence:     copiedEvidence,
	}
	return content, referencesForContent(content), nil
}

func validateAndCopyEvidence(evidence []EvidenceInput) ([]EvidenceInput, error) {
	if len(evidence) < 1 || len(evidence) > maximumEvidenceItems {
		return nil, ErrInvalidCommand
	}
	copied := make([]EvidenceInput, len(evidence))
	for index, item := range evidence {
		item.Repository = strings.TrimSpace(item.Repository)
		item.Commit = strings.TrimSpace(item.Commit)
		item.File = strings.TrimSpace(item.File)
		item.Symbol = strings.TrimSpace(item.Symbol)
		if item.Kind < EvidenceCode || item.Kind > EvidenceManual ||
			item.Repository == "" || item.Commit == "" || item.File == "" ||
			len(item.Repository) > 500 || len(item.Commit) > 200 ||
			len(item.File) > maximumEvidenceText || len(item.Symbol) > maximumEvidenceText ||
			item.StartLine < 1 || item.EndLine < item.StartLine || item.EndLine-item.StartLine > 10_000 {
			return nil, ErrInvalidCommand
		}
		copied[index] = item
	}
	return copied, nil
}

func validateMetadata(principal Principal, reason string, requestID string) (string, string, string, error) {
	actor := strings.TrimSpace(principal.Actor)
	reason = strings.TrimSpace(reason)
	requestID = strings.TrimSpace(requestID)
	if actor == "" || len(actor) > 200 || reason == "" || len(reason) > 2000 ||
		requestID == "" || len(requestID) > 200 ||
		principal.Origin < audit.OriginAgent || principal.Origin > audit.OriginSystem {
		return "", "", "", ErrInvalidCommand
	}
	return actor, reason, requestID, nil
}

func canCreateOrTombstone(principal Principal) bool {
	return principal.Role == RoleAgent || principal.Role == RoleEditor || principal.Role == RoleAdmin
}

func canRevise(principal Principal) bool {
	return canCreateOrTombstone(principal) || principal.Role == RoleReviewer
}

func canReview(principal Principal) bool {
	return principal.Role == RoleReviewer || principal.Role == RoleAdmin
}

func validType(relationType Type) bool {
	return relationType == TypeConditionalValueCopy
}

func requireRevision(relation Relation, expected int) error {
	if expected <= 0 || expected != relation.LatestRevisionNo {
		current := cloneRelation(relation)
		return &RevisionConflictError{CurrentRevisionNo: relation.LatestRevisionNo, Current: &current}
	}
	return nil
}

func copyBoolean(value *conditions.Boolean) (*conditions.Boolean, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode condition", ErrInvalidCommand)
	}
	var copied conditions.Boolean
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return nil, fmt.Errorf("%w: copy condition", ErrInvalidCommand)
	}
	return &copied, nil
}

func copyValue(value conditions.Value) (conditions.Value, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return conditions.Value{}, fmt.Errorf("%w: encode transform", ErrInvalidCommand)
	}
	var copied conditions.Value
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return conditions.Value{}, fmt.Errorf("%w: copy transform", ErrInvalidCommand)
	}
	return copied, nil
}

func contentFingerprint(relationType Type, revision Revision) (string, error) {
	canonical := struct {
		Type         Type                `json:"type"`
		Kind         ProposalKind        `json:"kind"`
		SourceNodeID int64               `json:"sourceNodeId"`
		TargetNodeID int64               `json:"targetNodeId"`
		Guard        *conditions.Boolean `json:"guard,omitempty"`
		Selector     *conditions.Boolean `json:"selector,omitempty"`
		Transform    conditions.Value    `json:"transform"`
	}{
		Type:         relationType,
		Kind:         revision.Kind,
		SourceNodeID: revision.SourceNodeID,
		TargetNodeID: revision.TargetNodeID,
		Guard:        revision.Guard,
		Selector:     revision.Selector,
		Transform:    revision.Transform,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: fingerprint relation", ErrInvalidCommand)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func referencesForRevision(revision Revision) []Reference {
	return referencesForContent(validatedContent{
		SourceNodeID: revision.SourceNodeID,
		TargetNodeID: revision.TargetNodeID,
		Guard:        revision.Guard,
		Selector:     revision.Selector,
		Transform:    revision.Transform,
	})
}

func referencesForContent(content validatedContent) []Reference {
	references := make([]Reference, 0)
	collectBooleanReferences(content.Guard, ReferenceGuard, &references)
	collectBooleanReferences(content.Selector, ReferenceSelector, &references)
	collectValueReferences(content.Transform, ReferenceTransform, &references)
	sort.Slice(references, func(i, j int) bool {
		if references[i].Role == references[j].Role {
			return references[i].NodeID < references[j].NodeID
		}
		return references[i].Role < references[j].Role
	})
	deduplicated := references[:0]
	for _, reference := range references {
		if len(deduplicated) > 0 && deduplicated[len(deduplicated)-1] == reference {
			continue
		}
		deduplicated = append(deduplicated, reference)
	}
	return append([]Reference(nil), deduplicated...)
}

func collectBooleanReferences(value *conditions.Boolean, role ReferenceRole, output *[]Reference) {
	if value == nil {
		return
	}
	for index := range value.Children {
		collectBooleanReferences(&value.Children[index], role, output)
	}
	collectBooleanReferences(value.Operand, role, output)
	if value.Left != nil {
		collectValueReferences(*value.Left, role, output)
	}
	if value.Right != nil {
		collectValueReferences(*value.Right, role, output)
	}
	for _, item := range value.Values {
		collectValueReferences(item, role, output)
	}
}

func collectValueReferences(value conditions.Value, role ReferenceRole, output *[]Reference) {
	if (value.Kind == conditions.ValueColumn || value.Kind == conditions.ValueColumnCopy) && value.NodeID > 0 {
		*output = append(*output, Reference{NodeID: value.NodeID, Role: role})
	}
	for index := range value.Cases {
		collectBooleanReferences(&value.Cases[index].When, role, output)
		collectValueReferences(value.Cases[index].Then, role, output)
	}
	if value.Else != nil {
		collectValueReferences(*value.Else, role, output)
	}
}

func cloneRevision(value Revision) Revision {
	copied := value
	copied.Guard, _ = copyBoolean(value.Guard)
	copied.Selector, _ = copyBoolean(value.Selector)
	copied.Transform, _ = copyValue(value.Transform)
	copied.Evidence = append([]EvidenceInput(nil), value.Evidence...)
	if value.ExpectedRevisionNo != nil {
		expected := *value.ExpectedRevisionNo
		copied.ExpectedRevisionNo = &expected
	}
	return copied
}

func cloneRelation(value Relation) Relation {
	copied := value
	if value.Active != nil {
		active := cloneRevision(*value.Active)
		copied.Active = &active
	}
	if value.Proposed != nil {
		proposed := cloneRevision(*value.Proposed)
		copied.Proposed = &proposed
	}
	return copied
}

func conflictOr(err error, relation Relation) error {
	if errors.Is(err, ErrRevisionConflict) {
		current := cloneRelation(relation)
		return &RevisionConflictError{CurrentRevisionNo: relation.LatestRevisionNo, Current: &current}
	}
	return err
}
