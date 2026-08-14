// Package sourcebinding resolves transient workspace evidence to an audited,
// revisioned set of registered data sources.
package sourcebinding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/gitremote"
	"github.com/benenen/dbgraph/internal/relations"
)

const (
	maximumRemotes         = 10
	maximumRemoteLength    = 2000
	maximumContextLength   = 100
	maximumDataSourceCount = 50
	// MaximumExpectedRevisionNo bounds revision arithmetic at every adapter.
	MaximumExpectedRevisionNo = 1_000_000
)

var (
	ErrInvalidWorkspaceEvidence = errors.New("invalid workspace evidence")
	ErrInvalidBinding           = errors.New("invalid source binding")
	ErrForbidden                = errors.New("source binding operation forbidden")
	ErrBindingNotFound          = errors.New("source binding not found")
	ErrRepositoryNotFound       = errors.New("source binding repository not found")
	ErrRevisionConflict         = errors.New("source binding revision conflict")
	ErrBindingConflict          = errors.New("source binding conflicts with existing state")
)

var contextPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

type ResolutionStatus string

const (
	StatusResolved            ResolutionStatus = "RESOLVED"
	StatusRepositoryNotFound  ResolutionStatus = "REPOSITORY_NOT_FOUND"
	StatusAmbiguousRepository ResolutionStatus = "AMBIGUOUS_REPOSITORY"
	StatusContextRequired     ResolutionStatus = "CONTEXT_REQUIRED"
	StatusUnbound             ResolutionStatus = "UNBOUND"
)

type WorkspaceEvidence struct {
	Remotes []string
	Context string
}

type DataSource struct {
	ID   int64
	Name string
	Kind string
}

type Resolution struct {
	Status            ResolutionStatus
	RepositoryID      int64
	RepositoryName    string
	Context           string
	BindingRevisionID int64
	BindingRevisionNo int
	DataSources       []DataSource
}

type ReplaceBindingSet struct {
	RepositoryID       int64
	Context            string
	DataSourceIDs      []int64
	ExpectedRevisionNo int
	Principal          relations.Principal
	Reason             string
	RequestID          string
}

type BindingRevision struct {
	ID             int64
	RepositoryID   int64
	RepositoryName string
	Context        string
	RevisionNo     int
	DataSources    []DataSource
	CreatedAt      time.Time
}

type RevisionConflictError struct {
	CurrentRevisionNo int
}

func (e *RevisionConflictError) Error() string { return ErrRevisionConflict.Error() }
func (e *RevisionConflictError) Unwrap() error { return ErrRevisionConflict }

type RepositoryRecord struct {
	ID        int64
	Name      string
	RemoteURL string
}

type PersistBindingRevision struct {
	CandidateBindingSetID int64
	Revision              BindingRevision
	CanonicalRemote       string
	DataSourceIDs         []int64
	ExpectedRevisionNo    int
	Actor                 string
	Origin                audit.Origin
	Reason                string
	RequestID             string
}

type validatedReplacement struct {
	repositoryID       int64
	context            string
	dataSourceIDs      []int64
	expectedRevisionNo int
	principal          relations.Principal
	actor              string
	reason             string
	requestID          string
}

type replacementIDs struct {
	bindingSet int64
	revision   int64
	audit      int64
}

type Repository interface {
	GetRepository(context.Context, int64) (RepositoryRecord, error)
	FindRepositoriesByCanonicalRemotes(context.Context, []string, int) ([]RepositoryRecord, error)
	GetCurrentBinding(context.Context, int64, string) (BindingRevision, error)
	ReplaceBinding(context.Context, PersistBindingRevision, audit.Event) (BindingRevision, error)
}

type IDGenerator interface {
	Next(context.Context) (int64, error)
}

type Service struct {
	repository Repository
	ids        IDGenerator
	now        func() time.Time
}

func NewService(repository Repository, ids IDGenerator, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, ids: ids, now: now}
}

func (s *Service) ResolveWorkspace(ctx context.Context, evidence WorkspaceEvidence) (Resolution, error) {
	if s == nil || s.repository == nil {
		return Resolution{}, ErrInvalidWorkspaceEvidence
	}
	canonicalRemotes, err := normalizeRemotes(evidence.Remotes)
	if err != nil {
		return Resolution{}, err
	}
	contextName, err := normalizeOptionalContext(evidence.Context)
	if err != nil {
		return Resolution{}, err
	}
	repositories, err := s.repository.FindRepositoriesByCanonicalRemotes(ctx, canonicalRemotes, maximumRemotes+1)
	if err != nil {
		return Resolution{}, err
	}
	if len(repositories) == 0 {
		return Resolution{Status: StatusRepositoryNotFound, Context: contextName}, nil
	}
	if len(repositories) != 1 {
		return Resolution{Status: StatusAmbiguousRepository, Context: contextName}, nil
	}
	repository := repositories[0]
	base := Resolution{
		RepositoryID: repository.ID, RepositoryName: repository.Name, Context: contextName,
	}
	if contextName == "" {
		base.Status = StatusContextRequired
		return base, nil
	}
	binding, err := s.repository.GetCurrentBinding(ctx, repository.ID, contextName)
	if errors.Is(err, ErrBindingNotFound) {
		base.Status = StatusUnbound
		return base, nil
	}
	if err != nil {
		return Resolution{}, err
	}
	base.BindingRevisionID = binding.ID
	base.BindingRevisionNo = binding.RevisionNo
	base.DataSources = copyDataSources(binding.DataSources)
	if len(binding.DataSources) == 0 {
		base.Status = StatusUnbound
		return base, nil
	}
	base.Status = StatusResolved
	return base, nil
}

func (s *Service) ReplaceBindingSet(ctx context.Context, command ReplaceBindingSet) (BindingRevision, error) {
	if command.Principal.Role != relations.RoleAdmin {
		return BindingRevision{}, ErrForbidden
	}
	if s == nil || s.repository == nil || s.ids == nil {
		return BindingRevision{}, ErrInvalidBinding
	}
	validated, err := validateReplacement(command)
	if err != nil {
		return BindingRevision{}, err
	}
	repository, err := s.repository.GetRepository(ctx, validated.repositoryID)
	if err != nil {
		return BindingRevision{}, err
	}
	canonicalRemote, err := gitremote.Canonicalize(repository.RemoteURL)
	if err != nil {
		return BindingRevision{}, ErrInvalidBinding
	}
	record, event, err := s.buildReplacement(ctx, validated, repository, canonicalRemote)
	if err != nil {
		return BindingRevision{}, err
	}
	return s.repository.ReplaceBinding(ctx, record, event)
}

func validateReplacement(command ReplaceBindingSet) (validatedReplacement, error) {
	contextName, contextError := normalizeRequiredContext(command.Context)
	dataSourceIDs, dataSourceError := normalizeDataSourceIDs(command.DataSourceIDs)
	validated := validatedReplacement{
		repositoryID: command.RepositoryID, context: contextName, dataSourceIDs: dataSourceIDs,
		expectedRevisionNo: command.ExpectedRevisionNo, principal: command.Principal,
		actor: strings.TrimSpace(command.Principal.Actor), reason: strings.TrimSpace(command.Reason),
		requestID: strings.TrimSpace(command.RequestID),
	}
	if contextError != nil || dataSourceError != nil || validated.repositoryID <= 0 ||
		validated.expectedRevisionNo < 0 || validated.expectedRevisionNo > MaximumExpectedRevisionNo ||
		validated.actor == "" || len(validated.actor) > 200 || validated.reason == "" || len(validated.reason) > 2000 ||
		validated.requestID == "" || len(validated.requestID) > 200 ||
		(validated.principal.Origin != audit.OriginAgent && validated.principal.Origin != audit.OriginWeb) {
		return validatedReplacement{}, ErrInvalidBinding
	}
	return validated, nil
}

func (s *Service) buildReplacement(
	ctx context.Context,
	command validatedReplacement,
	repository RepositoryRecord,
	canonicalRemote string,
) (PersistBindingRevision, audit.Event, error) {
	ids, err := s.nextReplacementIDs(ctx)
	if err != nil {
		return PersistBindingRevision{}, audit.Event{}, err
	}
	now := s.now().UTC()
	expectedRevision := command.expectedRevisionNo
	details, err := json.Marshal(map[string]any{
		"repositoryId":  strconv.FormatInt(command.repositoryID, 10),
		"context":       command.context,
		"revisionNo":    expectedRevision + 1,
		"dataSourceIds": formatIDs(command.dataSourceIDs),
	})
	if err != nil {
		return PersistBindingRevision{}, audit.Event{}, fmt.Errorf("encode source binding audit details: %w", err)
	}
	revision := BindingRevision{
		ID: ids.revision, RepositoryID: repository.ID, RepositoryName: repository.Name,
		Context: command.context, RevisionNo: expectedRevision + 1, CreatedAt: now,
	}
	record := PersistBindingRevision{
		CandidateBindingSetID: ids.bindingSet,
		Revision:              revision,
		CanonicalRemote:       canonicalRemote,
		DataSourceIDs:         command.dataSourceIDs,
		ExpectedRevisionNo:    expectedRevision,
		Actor:                 command.actor,
		Origin:                command.principal.Origin,
		Reason:                command.reason,
		RequestID:             command.requestID,
	}
	event := audit.Event{
		ID: ids.audit, Actor: command.actor, Origin: command.principal.Origin,
		Action: "SOURCE_BINDING_SET_REPLACED", SubjectType: "SOURCE_BINDING_REVISION", SubjectID: ids.revision,
		Reason: command.reason, RequestID: command.requestID, ExpectedRevision: &expectedRevision,
		Details: details, OccurredAt: now,
	}
	return record, event, nil
}

func (s *Service) nextReplacementIDs(ctx context.Context) (replacementIDs, error) {
	bindingSetID, err := s.ids.Next(ctx)
	if err != nil {
		return replacementIDs{}, fmt.Errorf("generate source binding set ID: %w", err)
	}
	revisionID, err := s.ids.Next(ctx)
	if err != nil {
		return replacementIDs{}, fmt.Errorf("generate source binding revision ID: %w", err)
	}
	auditID, err := s.ids.Next(ctx)
	if err != nil {
		return replacementIDs{}, fmt.Errorf("generate source binding audit ID: %w", err)
	}
	return replacementIDs{bindingSet: bindingSetID, revision: revisionID, audit: auditID}, nil
}

func normalizeRemotes(remotes []string) ([]string, error) {
	if len(remotes) == 0 || len(remotes) > maximumRemotes {
		return nil, ErrInvalidWorkspaceEvidence
	}
	unique := make(map[string]struct{}, len(remotes))
	for _, remote := range remotes {
		if len(remote) > maximumRemoteLength {
			return nil, ErrInvalidWorkspaceEvidence
		}
		canonical, err := gitremote.Canonicalize(remote)
		if err != nil {
			return nil, ErrInvalidWorkspaceEvidence
		}
		unique[canonical] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for remote := range unique {
		result = append(result, remote)
	}
	slices.Sort(result)
	return result, nil
}

func normalizeOptionalContext(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	if len(value) > maximumContextLength || !contextPattern.MatchString(value) {
		return "", ErrInvalidWorkspaceEvidence
	}
	return value, nil
}

func normalizeRequiredContext(value string) (string, error) {
	value, err := normalizeOptionalContext(value)
	if err != nil || value == "" {
		return "", ErrInvalidBinding
	}
	return value, nil
}

func normalizeDataSourceIDs(values []int64) ([]int64, error) {
	if len(values) > maximumDataSourceCount {
		return nil, ErrInvalidBinding
	}
	result := append([]int64(nil), values...)
	slices.Sort(result)
	for index, value := range result {
		if value <= 0 || (index > 0 && result[index-1] == value) {
			return nil, ErrInvalidBinding
		}
	}
	return result, nil
}

func copyDataSources(values []DataSource) []DataSource {
	return append([]DataSource(nil), values...)
}

func formatIDs(values []int64) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strconv.FormatInt(value, 10)
	}
	return result
}
