package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/jsoncheck"
	"github.com/benenen/dbgraph/internal/relations"
)

const maximumBatchItems = 100

type IDGenerator interface {
	Next(context.Context) (int64, error)
}

type Service struct {
	repository Repository
	commands   *relations.Commands
	ids        IDGenerator
	now        func() time.Time
}

func NewService(
	repository Repository,
	commands *relations.Commands,
	ids IDGenerator,
	now func() time.Time,
) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, commands: commands, ids: ids, now: now}
}

func (s *Service) Begin(ctx context.Context, command Begin) (Session, error) {
	if !canInitialize(command.Principal) || command.RepositoryID <= 0 ||
		(command.Mode != ModeFull && command.Mode != ModeIncremental) {
		return Session{}, ErrInvalidInit
	}
	sourceCommit := strings.TrimSpace(command.SourceCommit)
	requestID := strings.TrimSpace(command.RequestID)
	if sourceCommit == "" || len(sourceCommit) > 200 || requestID == "" || len(requestID) > 200 ||
		!validPrincipal(command.Principal) {
		return Session{}, ErrInvalidInit
	}
	scope := append(json.RawMessage(nil), command.Scope...)
	if len(scope) == 0 {
		scope = json.RawMessage(`{}`)
	}
	if jsoncheck.ValidateObject(scope, jsoncheck.Limits{MaxBytes: 20_000, MaxDepth: 16}) != nil {
		return Session{}, ErrInvalidInit
	}
	if command.Mode == ModeIncremental {
		if _, err := IncrementalRelationIDs(scope); err != nil {
			return Session{}, err
		}
	}
	sessionID, err := s.ids.Next(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("generate relation init session ID: %w", err)
	}
	return s.repository.Begin(ctx, Session{
		ID:           sessionID,
		RepositoryID: command.RepositoryID,
		Mode:         command.Mode,
		SourceCommit: sourceCommit,
		Scope:        scope,
		Status:       StatusOpen,
		Principal:    command.Principal,
		RequestID:    requestID,
		CreatedAt:    s.now().UTC(),
	})
}

func (s *Service) SubmitBatch(ctx context.Context, command SubmitBatch) (BatchResult, error) {
	session, err := s.repository.Get(ctx, command.SessionID)
	if err != nil {
		return BatchResult{}, err
	}
	if session.Status != StatusOpen {
		return BatchResult{}, ErrInitNotOpen
	}
	if !sameInitializer(session, command.Principal) || command.BatchNo <= 0 ||
		len(command.Proposals)+len(command.Unresolved) < 1 ||
		len(command.Proposals)+len(command.Unresolved) > maximumBatchItems {
		return BatchResult{}, ErrInvalidInit
	}
	idempotencyKey := strings.TrimSpace(command.IdempotencyKey)
	requestID := strings.TrimSpace(command.RequestID)
	if idempotencyKey == "" || len(idempotencyKey) > 200 || requestID == "" || len(requestID) > 180 {
		return BatchResult{}, ErrInvalidInit
	}
	digest, err := batchDigest(command)
	if err != nil {
		return BatchResult{}, err
	}

	prepared := make([]relations.ProposalRecord, 0, len(command.Proposals))
	for index, proposal := range command.Proposals {
		record, err := s.commands.PrepareCreate(ctx, relations.ProposeCreate{
			Type:         proposal.Type,
			SourceNodeID: proposal.SourceNodeID,
			TargetNodeID: proposal.TargetNodeID,
			Guard:        proposal.Guard,
			Selector:     proposal.Selector,
			Transform:    proposal.Transform,
			Confidence:   proposal.Confidence,
			Evidence:     proposal.Evidence,
			Principal:    command.Principal,
			Reason:       proposal.Reason,
			RequestID:    fmt.Sprintf("%s:%d", requestID, index+1),
		})
		if err != nil {
			return BatchResult{}, err
		}
		prepared = append(prepared, record)
	}

	batchID, err := s.ids.Next(ctx)
	if err != nil {
		return BatchResult{}, fmt.Errorf("generate relation init batch ID: %w", err)
	}
	unresolved := make([]Unresolved, 0, len(command.Unresolved))
	for _, input := range command.Unresolved {
		finding, err := s.prepareUnresolved(ctx, session, batchID, command.Principal, input)
		if err != nil {
			return BatchResult{}, err
		}
		unresolved = append(unresolved, finding)
	}
	return s.repository.SubmitBatch(ctx, BatchRecord{
		BatchID:        batchID,
		Session:        session,
		BatchNo:        command.BatchNo,
		IdempotencyKey: idempotencyKey,
		RequestID:      requestID,
		PayloadDigest:  digest,
		Proposals:      prepared,
		Unresolved:     unresolved,
		AcceptedAt:     s.now().UTC(),
	})
}

func (s *Service) Complete(ctx context.Context, command Complete) (Completion, error) {
	session, err := s.repository.Get(ctx, command.SessionID)
	if err != nil {
		return Completion{}, err
	}
	if session.Status != StatusOpen {
		return Completion{}, ErrInitNotOpen
	}
	if !sameInitializer(session, command.Principal) || command.ExpectedBatchCount < 1 {
		return Completion{}, ErrInvalidInit
	}
	reason := strings.TrimSpace(command.Reason)
	requestID := strings.TrimSpace(command.RequestID)
	if reason == "" || len(reason) > 2000 || requestID == "" || len(requestID) > 180 {
		return Completion{}, ErrInvalidInit
	}
	check, err := s.repository.CheckCompletion(ctx, session, command.ExpectedBatchCount)
	if err != nil {
		return Completion{}, err
	}
	if completionBudgetExceeded(check.DeferredCandidateCount, check.DeferredCandidateRawBytes) {
		return Completion{}, ErrCompletionBudgetExceeded
	}
	candidates := make([]relations.ProposalRecord, 0)
	if session.Mode == ModeFull || session.Mode == ModeIncremental {
		budget := CompletionBudget{
			CandidateLimit: MaximumCompletionCandidates - check.DeferredCandidateCount,
			RawByteLimit:   int64(MaximumCompletionCandidateRawBytes) - check.DeferredCandidateRawBytes,
		}
		omissionPlan, err := s.repository.ListOmittedRelations(ctx, session, budget)
		if err != nil {
			return Completion{}, err
		}
		omitted := omissionPlan.Relations
		if completionBudgetExceeded(
			check.DeferredCandidateCount+len(omitted),
			check.DeferredCandidateRawBytes+omissionPlan.RawBytes,
		) {
			return Completion{}, ErrCompletionBudgetExceeded
		}
		omittedRawBytes, err := omittedCandidateRawBytes(omitted)
		if err != nil {
			return Completion{}, err
		}
		if completionBudgetExceeded(
			check.DeferredCandidateCount+len(omitted),
			check.DeferredCandidateRawBytes+omittedRawBytes,
		) {
			return Completion{}, ErrCompletionBudgetExceeded
		}
		for index, relation := range omitted {
			record, err := s.commands.PrepareStale(ctx, relations.ProposeTombstone{
				RelationID:         relation.ID,
				ExpectedRevisionNo: relation.LatestRevisionNo,
				Principal:          command.Principal,
				Reason:             reason,
				RequestID:          fmt.Sprintf("%s:%d", requestID, index+1),
			})
			if err != nil {
				return Completion{}, err
			}
			candidates = append(candidates, record)
		}
	}
	auditID, err := s.ids.Next(ctx)
	if err != nil {
		return Completion{}, fmt.Errorf("generate relation init completion audit ID: %w", err)
	}
	return s.repository.Complete(ctx, CompletionRecord{
		Session:            session,
		ExpectedBatchCount: command.ExpectedBatchCount,
		Candidates:         candidates,
		AuditID:            auditID,
		Principal:          command.Principal,
		Reason:             reason,
		RequestID:          requestID,
		CompletedAt:        s.now().UTC(),
	})
}

func completionBudgetExceeded(candidateCount int, rawBytes int64) bool {
	return candidateCount > MaximumCompletionCandidates ||
		candidateCount < 0 || rawBytes > MaximumCompletionCandidateRawBytes || rawBytes < 0
}

func omittedCandidateRawBytes(omitted []relations.Relation) (int64, error) {
	var total int64
	for _, relation := range omitted {
		if relation.Active == nil {
			return 0, ErrInvalidInit
		}
		values := []any{relation.Active.Guard, relation.Active.Selector, relation.Active.Transform}
		for index, value := range values {
			if index < 2 && value == nil {
				continue
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return 0, fmt.Errorf("%w: encode omitted relation AST", ErrInvalidInit)
			}
			total += int64(len(encoded))
			if total > MaximumCompletionCandidateRawBytes {
				return total, nil
			}
		}
	}
	return total, nil
}

func (s *Service) Get(ctx context.Context, sessionID int64) (Session, error) {
	if sessionID <= 0 {
		return Session{}, ErrInvalidInit
	}
	return s.repository.Get(ctx, sessionID)
}

func (s *Service) ListUnresolved(ctx context.Context, limit int) ([]Unresolved, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalidInit
	}
	return s.repository.ListUnresolved(ctx, limit)
}

func (s *Service) prepareUnresolved(
	ctx context.Context,
	session Session,
	batchID int64,
	principal relations.Principal,
	input UnresolvedInput,
) (Unresolved, error) {
	findingType := strings.TrimSpace(input.Type)
	summary := strings.TrimSpace(input.Summary)
	evidence := append(json.RawMessage(nil), input.Evidence...)
	if findingType == "" || len(findingType) > 100 || summary == "" || len(summary) > 2000 ||
		jsoncheck.ValidateObject(evidence, jsoncheck.Limits{MaxBytes: 20_000, MaxDepth: 16}) != nil {
		return Unresolved{}, ErrInvalidInit
	}
	fingerprintPayload, err := json.Marshal(struct {
		RepositoryID int64           `json:"repositoryId"`
		Type         string          `json:"type"`
		Summary      string          `json:"summary"`
		Evidence     json.RawMessage `json:"evidence"`
	}{session.RepositoryID, findingType, summary, evidence})
	if err != nil {
		return Unresolved{}, ErrInvalidInit
	}
	digest := sha256.Sum256(fingerprintPayload)
	findingID, err := s.ids.Next(ctx)
	if err != nil {
		return Unresolved{}, fmt.Errorf("generate unresolved finding ID: %w", err)
	}
	return Unresolved{
		ID:           findingID,
		RepositoryID: session.RepositoryID,
		SessionID:    session.ID,
		BatchID:      batchID,
		Fingerprint:  hex.EncodeToString(digest[:]),
		Type:         findingType,
		Summary:      summary,
		Evidence:     evidence,
		Status:       1,
		Principal:    principal,
		CreatedAt:    s.now().UTC(),
	}, nil
}

func batchDigest(command SubmitBatch) (string, error) {
	payload := struct {
		BatchNo    int               `json:"batchNo"`
		Proposals  []Proposal        `json:"proposals"`
		Unresolved []UnresolvedInput `json:"unresolved"`
	}{command.BatchNo, command.Proposals, command.Unresolved}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", ErrInvalidInit
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canInitialize(principal relations.Principal) bool {
	return principal.Role == relations.RoleAgent || principal.Role == relations.RoleAdmin
}

func validPrincipal(principal relations.Principal) bool {
	actor := strings.TrimSpace(principal.Actor)
	return actor != "" && len(actor) <= 200 &&
		principal.Origin >= audit.OriginAgent && principal.Origin <= audit.OriginSystem
}

func sameInitializer(session Session, principal relations.Principal) bool {
	return canInitialize(principal) && validPrincipal(principal) &&
		principal.Actor == session.Principal.Actor && principal.Origin == session.Principal.Origin
}
