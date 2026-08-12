package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/relations"
)

var (
	ErrInvalidDataSource  = errors.New("invalid data source")
	ErrDataSourceNotFound = errors.New("data source not found")
	// ErrDataSourceNameTaken reports a name already registered. Data sources are
	// shared across projects, so their names are unique service-wide and the
	// answer is usually to link the existing one.
	ErrDataSourceNameTaken = errors.New("a data source with that name already exists")
	// ErrDataSourceInUse reports a source that imported a catalog. Deleting it
	// would orphan the nodes and scan runs that record what was imported.
	ErrDataSourceInUse = errors.New("data source has imported catalog content")

	// ErrUnusableDSN reports a connection string the scanner would fail on.
	ErrUnusableDSN     = errors.New("connection string cannot be used")
	ErrInvalidSnapshot = errors.New("invalid schema snapshot")
	ErrNodeNotFound    = errors.New("catalog node not found")
	ErrForbidden       = errors.New("catalog command is forbidden")
)

var environmentNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// validEnvironmentName accepts a blank name. A source that carries its own
// sealed connection string needs no environment variable, and audit_events
// still records how the DSN resolves either way.
func validEnvironmentName(name string) bool {
	return name == "" || environmentNamePattern.MatchString(name)
}

// auditReason keeps the audit trail complete when an operator leaves the field
// blank. audit_events.reason is required and append-only, so a recorded
// default is better than refusing the change.
func auditReason(reason string, fallback string) string {
	if reason == "" {
		return fallback
	}
	return reason
}

type DataSourceKind int

const (
	DataSourceMySQL DataSourceKind = 1
)

type NodeKind int

const (
	NodeDatabase NodeKind = 1
	NodeSchema   NodeKind = 2
	NodeTable    NodeKind = 3
	NodeColumn   NodeKind = 4
)

const (
	MaximumSnapshotNodes       = 100_000
	MaximumSnapshotForeignKeys = 80_000
	MaximumIncrementalTables   = 100
)

const (
	MinimumListLimit = 1
	DefaultListLimit = 50
	MaximumListLimit = 200
)

// MaximumTableListLimit bounds a table browse. It is far above the list limit
// because browsing is the whole point: one database here holds 459 tables, and
// a reader scanning names for the right one needs all of them. The rows are
// three short fields read from an index, not a page of full records.
const MaximumTableListLimit = 5000

func clampListLimit(limit int) int {
	if limit < MinimumListLimit {
		return DefaultListLimit
	}
	if limit > MaximumListLimit {
		return MaximumListLimit
	}
	return limit
}

type NodeStatus int

const (
	NodeActive NodeStatus = 1
	NodeStale  NodeStatus = 2
)

type DataSource struct {
	ID int64
	// A data source is shared: projects link to it through
	// project_data_sources rather than owning it.
	Name string
	Kind DataSourceKind
	// DSNEnvironment names the environment variable holding the DSN. It is the
	// fallback used when no sealed DSN is stored for this source.
	DSNEnvironment string
	// DSNKeyID identifies the key that sealed DSNCiphertext; both are empty for
	// a source that resolves through the environment.
	DSNKeyID string
	// DSNCiphertext is the sealed DSN. It never leaves the process in a Web,
	// REST, or MCP response.
	DSNCiphertext []byte
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CreateDataSource struct {
	ProjectID      int64
	Name           string
	Kind           DataSourceKind
	DSNEnvironment string
	// DSN is the connection string itself; when set it is sealed before storage.
	DSN string
}

type AdminCreateDataSource struct {
	ProjectID int64
	Name      string
	Kind      DataSourceKind
	// DSNEnvironment names the environment variable holding the DSN. Required.
	DSNEnvironment string
	// DSN is the connection string itself. When set it is sealed and stored,
	// and it takes precedence over DSNEnvironment at scan time. It is never
	// returned by any adapter.
	DSN       string
	Principal relations.Principal
	Reason    string
	RequestID string
}

type NodeInput struct {
	StableKey       string
	ParentStableKey string
	Kind            NodeKind
	Name            string
	QualifiedName   string
	DataType        string
	Nullable        bool
	Ordinal         int
}

type DeclaredForeignKey struct {
	ConstraintSchema string
	Name             string
	SourceColumn     string
	TargetColumn     string
	Ordinal          int
}

type ScannedSnapshot struct {
	Nodes       []NodeInput
	ForeignKeys []DeclaredForeignKey
}

type Node struct {
	ID             int64
	VersionID      int64
	ProjectID      int64
	DataSourceID   int64
	ScanRunID      int64
	ParentNodeID   int64
	Kind           NodeKind
	Status         NodeStatus
	StableKey      string
	Name           string
	QualifiedName  string
	DataType       string
	Nullable       bool
	Ordinal        int
	VersionCreated time.Time
}

type PublishSnapshot struct {
	ProjectID    int64
	DataSourceID int64
	Nodes        []NodeInput
	ForeignKeys  []DeclaredForeignKey
	ScopeTables  []string
}

type SnapshotPublication struct {
	ScanRunID    int64
	ProjectID    int64
	DataSourceID int64
	Nodes        []NodeInput
	ForeignKeys  []DeclaredForeignKey
	ScopeTables  []string
	StartedAt    time.Time
	Prestarted   bool
}

type SchemaScanRun struct {
	ID           int64
	ProjectID    int64
	DataSourceID int64
	StartedAt    time.Time
}

type SchemaScanFailure struct {
	Run          SchemaScanRun
	ErrorCode    string
	ErrorMessage string
	CompletedAt  time.Time
}

type PublishedSnapshot struct {
	ScanRunID   int64
	NodeCount   int
	StaleCount  int
	PublishedAt time.Time
}

// TableSummary identifies one table for browsing. It is deliberately thin: a
// catalog holds 459 of these for a single database, and the console lists them
// before it knows which one the reader wants.
type TableSummary struct {
	ID            int64
	Name          string
	QualifiedName string
}

// TableLister is optional on a repository, in the same way the recursive graph
// reader is: a Service without it cannot browse tables.
type TableLister interface {
	ListTables(ctx context.Context, projectID int64, dataSourceID int64, filter string, limit int) ([]TableSummary, error)
}

type CatalogRepository interface {
	CreateDataSource(context.Context, DataSource, int64) error
	CreateDataSourceWithAudit(context.Context, DataSource, int64, audit.Event) error
	GetDataSource(context.Context, int64) (DataSource, error)
	GetProjectDataSource(context.Context, int64, int64) (DataSource, error)
	ListDataSources(context.Context, int64, int) ([]DataSource, error)
	ListAllDataSources(context.Context, int) ([]DataSource, error)
	LinkDataSource(context.Context, int64, int64, time.Time) error
	UnlinkDataSource(context.Context, int64, int64) error
	UpdateDataSourceWithAudit(context.Context, DataSource, bool, audit.Event) error
	DeleteDataSource(context.Context, int64) error
	BeginSchemaScan(context.Context, SchemaScanRun) error
	FailSchemaScan(context.Context, SchemaScanFailure) error
	PublishSnapshot(context.Context, SnapshotPublication) (PublishedSnapshot, error)
	FindCurrentNode(context.Context, int64, int64, string) (Node, error)
	GetCurrentNode(context.Context, int64, int64) (Node, error)
	SearchCurrentNodes(context.Context, int64, int64, string, int) ([]Node, error)
}

// GetProjectDataSource resolves a data source through the project that links
// it. Anything scanning or publishing goes through this rather than a bare id.
// ListAllDataSources returns the shared registry so a project can adopt an
// existing source rather than registering the same database twice.
func (s *Service) ListAllDataSources(ctx context.Context, limit int) ([]DataSource, error) {
	return s.repository.ListAllDataSources(ctx, clampListLimit(limit))
}

// LinkDataSource adopts a source into a project. Linking twice is harmless.
func (s *Service) LinkDataSource(ctx context.Context, projectID int64, dataSourceID int64) error {
	if projectID <= 0 || dataSourceID <= 0 {
		return ErrInvalidDataSource
	}
	return s.repository.LinkDataSource(ctx, projectID, dataSourceID, s.now().UTC())
}

// UnlinkDataSource removes the adoption. Nodes the project already scanned stay
// where they are; only the association goes, so nothing silently disappears
// from a catalog a relation may reference.
func (s *Service) UnlinkDataSource(ctx context.Context, projectID int64, dataSourceID int64) error {
	if projectID <= 0 || dataSourceID <= 0 {
		return ErrInvalidDataSource
	}
	return s.repository.UnlinkDataSource(ctx, projectID, dataSourceID)
}

// AdminUpdateDataSource renames a source and may rotate its stored DSN. An
// empty DSN leaves the stored credential untouched, because the connection
// string is write-only and the caller never sees the current value.
type AdminUpdateDataSource struct {
	DataSourceID   int64
	Name           string
	DSNEnvironment string
	DSN            string
	Principal      relations.Principal
	Reason         string
	RequestID      string
}

// UpdateAsAdmin applies the change and records who made it.
func (s *Service) UpdateDataSourceAsAdmin(
	ctx context.Context,
	command AdminUpdateDataSource,
) (DataSource, error) {
	if command.Principal.Role != relations.RoleAdmin {
		return DataSource{}, ErrForbidden
	}
	name := strings.TrimSpace(command.Name)
	dsnEnvironment := strings.TrimSpace(command.DSNEnvironment)
	actor := strings.TrimSpace(command.Principal.Actor)
	reason := strings.TrimSpace(command.Reason)
	requestID := strings.TrimSpace(command.RequestID)
	if command.DataSourceID <= 0 || name == "" || len(name) > 200 ||
		!validEnvironmentName(dsnEnvironment) ||
		actor == "" || len(actor) > 200 || len(reason) > 2000 ||
		requestID == "" || len(requestID) > 200 {
		return DataSource{}, ErrInvalidDataSource
	}
	reason = auditReason(reason, "Updated from the console")

	existing, err := s.repository.GetDataSource(ctx, command.DataSourceID)
	if err != nil {
		return DataSource{}, err
	}
	keyID, ciphertext, err := s.sealDSN(command.DSN)
	if err != nil {
		return DataSource{}, err
	}
	replaceSecret := strings.TrimSpace(command.DSN) != ""

	eventID, err := s.ids.Next(ctx)
	if err != nil {
		return DataSource{}, fmt.Errorf("generate audit event ID: %w", err)
	}
	now := s.now().UTC()
	updated := existing
	updated.Name = name
	updated.DSNEnvironment = dsnEnvironment
	updated.UpdatedAt = now
	if replaceSecret {
		updated.DSNKeyID = keyID
		updated.DSNCiphertext = ciphertext
	}

	// The record says what changed, never the credential itself.
	details, _ := json.Marshal(map[string]string{
		"name": name, "dsnEnvironment": dsnEnvironment,
		"dsnReplaced": storedLabel(DataSource{DSNCiphertext: ciphertext}),
	})
	event := audit.Event{
		ID: eventID, Actor: actor, Origin: command.Principal.Origin,
		Action: "DATA_SOURCE_UPDATED", SubjectType: "DATA_SOURCE", SubjectID: updated.ID,
		Reason: reason, RequestID: requestID, Details: details, OccurredAt: now,
	}
	if err := s.repository.UpdateDataSourceWithAudit(ctx, updated, replaceSecret, event); err != nil {
		return DataSource{}, err
	}
	return updated, nil
}

// ListTables browses the tables one data source imported. A catalog with no
// relations is still worth reading, which is the state every source is in
// between its first scan and its first approved relation.
func (s *Service) ListTables(
	ctx context.Context,
	projectID int64,
	dataSourceID int64,
	filter string,
	limit int,
) ([]TableSummary, error) {
	if projectID <= 0 || dataSourceID <= 0 {
		return nil, ErrInvalidDataSource
	}
	if len(filter) > 200 {
		return nil, ErrInvalidDataSource
	}
	lister, ok := s.repository.(TableLister)
	if !ok {
		return nil, ErrInvalidDataSource
	}
	if limit < MinimumListLimit || limit > MaximumTableListLimit {
		limit = MaximumTableListLimit
	}
	return lister.ListTables(ctx, projectID, dataSourceID, filter, limit)
}

// DeleteDataSource removes a source that has imported nothing yet.
func (s *Service) DeleteDataSource(ctx context.Context, dataSourceID int64) error {
	if dataSourceID <= 0 {
		return ErrInvalidDataSource
	}
	return s.repository.DeleteDataSource(ctx, dataSourceID)
}

func (s *Service) GetProjectDataSource(
	ctx context.Context,
	projectID int64,
	dataSourceID int64,
) (DataSource, error) {
	if projectID <= 0 || dataSourceID <= 0 {
		return DataSource{}, ErrInvalidDataSource
	}
	return s.repository.GetProjectDataSource(ctx, projectID, dataSourceID)
}

func (s *Service) GetDataSource(ctx context.Context, dataSourceID int64) (DataSource, error) {
	if dataSourceID <= 0 {
		return DataSource{}, ErrInvalidDataSource
	}
	return s.repository.GetDataSource(ctx, dataSourceID)
}

func (s *Service) ListDataSources(ctx context.Context, projectID int64, limit int) ([]DataSource, error) {
	if projectID <= 0 {
		return nil, ErrInvalidDataSource
	}
	return s.repository.ListDataSources(ctx, projectID, clampListLimit(limit))
}

type Service struct {
	repository  CatalogRepository
	ids         ProjectIDGenerator
	now         func() time.Time
	sealer      DSNSealer
	validateDSN DSNValidator
}

// DSNSealer encrypts a source-database DSN before it reaches storage. The
// service owns this invariant: a DSN never reaches the repository unsealed.
type DSNSealer interface {
	Seal(plaintext string) (keyID string, ciphertext []byte, err error)
}

// DSNValidator rejects a connection string the scanner could not use. The
// service holds it as a function so the domain never depends on a driver.
type DSNValidator func(dsn string) error

// ServiceOption adjusts an optional service dependency.
type ServiceOption func(*Service)

// WithDSNSealer lets the service accept a plaintext DSN and store it sealed.
// Without it, a data source can only reference an environment variable.
func WithDSNSealer(sealer DSNSealer) ServiceOption {
	return func(s *Service) { s.sealer = sealer }
}

// WithDSNValidator checks a connection string as it is saved, so an operator
// learns it is unusable while the field is still in front of them rather than
// from a scan that fails minutes later.
func WithDSNValidator(validate DSNValidator) ServiceOption {
	return func(s *Service) { s.validateDSN = validate }
}

func NewService(
	repository CatalogRepository,
	ids ProjectIDGenerator,
	now func() time.Time,
	options ...ServiceOption,
) *Service {
	if now == nil {
		now = time.Now
	}
	service := &Service{repository: repository, ids: ids, now: now}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) CreateDataSource(ctx context.Context, command CreateDataSource) (DataSource, error) {
	return s.createDataSource(ctx, command, nil)
}

func (s *Service) CreateDataSourceAsAdmin(
	ctx context.Context,
	command AdminCreateDataSource,
) (DataSource, error) {
	if command.Principal.Role != relations.RoleAdmin {
		return DataSource{}, ErrForbidden
	}
	actor := strings.TrimSpace(command.Principal.Actor)
	reason := strings.TrimSpace(command.Reason)
	requestID := strings.TrimSpace(command.RequestID)
	if actor == "" || len(actor) > 200 || len(reason) > 2000 ||
		requestID == "" || len(requestID) > 200 ||
		(command.Principal.Origin != audit.OriginAgent && command.Principal.Origin != audit.OriginWeb) {
		return DataSource{}, ErrInvalidDataSource
	}
	reason = auditReason(reason, "Registered from the console")
	return s.createDataSource(ctx, CreateDataSource{
		ProjectID: command.ProjectID, Name: command.Name, Kind: command.Kind,
		DSNEnvironment: command.DSNEnvironment, DSN: command.DSN,
	}, func(source DataSource, eventID int64, occurredAt time.Time) audit.Event {
		// The audit record names how the DSN resolves, never the DSN itself:
		// audit events are append-only, so a leak here could never be removed.
		details, _ := json.Marshal(map[string]string{
			"kind": "MYSQL", "dsnEnvironment": source.DSNEnvironment,
			"dsnStored": storedLabel(source),
		})
		return audit.Event{
			ID: eventID, ProjectID: command.ProjectID, Actor: actor, Origin: command.Principal.Origin,
			Action: "DATA_SOURCE_CREATED", SubjectType: "DATA_SOURCE", SubjectID: source.ID,
			Reason: reason, RequestID: requestID, Details: details, OccurredAt: occurredAt,
		}
	})
}

type dataSourceAuditBuilder func(DataSource, int64, time.Time) audit.Event

func (s *Service) createDataSource(
	ctx context.Context,
	command CreateDataSource,
	auditBuilder dataSourceAuditBuilder,
) (DataSource, error) {
	name := strings.TrimSpace(command.Name)
	dsnEnvironment := strings.TrimSpace(command.DSNEnvironment)
	if command.ProjectID <= 0 || command.Kind != DataSourceMySQL {
		return DataSource{}, ErrInvalidDataSource
	}
	if name == "" || len(name) > 200 || !validEnvironmentName(dsnEnvironment) {
		return DataSource{}, ErrInvalidDataSource
	}

	dataSourceID, err := s.ids.Next(ctx)
	if err != nil {
		return DataSource{}, fmt.Errorf("generate data source ID: %w", err)
	}
	now := s.now().UTC()
	keyID, ciphertext, err := s.sealDSN(command.DSN)
	if err != nil {
		return DataSource{}, err
	}
	dataSource := DataSource{
		ID:             dataSourceID,
		Name:           name,
		Kind:           command.Kind,
		DSNEnvironment: dsnEnvironment,
		DSNKeyID:       keyID,
		DSNCiphertext:  ciphertext,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if auditBuilder != nil {
		auditID, err := s.ids.Next(ctx)
		if err != nil {
			return DataSource{}, fmt.Errorf("generate data source audit ID: %w", err)
		}
		if err := s.repository.CreateDataSourceWithAudit(ctx, dataSource, command.ProjectID, auditBuilder(dataSource, auditID, now)); err != nil {
			return DataSource{}, err
		}
		return dataSource, nil
	}
	if err := s.repository.CreateDataSource(ctx, dataSource, command.ProjectID); err != nil {
		return DataSource{}, err
	}
	return dataSource, nil
}

func (s *Service) PublishSnapshot(ctx context.Context, command PublishSnapshot) (PublishedSnapshot, error) {
	scopeTables, err := normalizeScopeTables(command.ScopeTables)
	if err != nil {
		return PublishedSnapshot{}, err
	}
	command.ScopeTables = scopeTables
	if err := validateSnapshot(command); err != nil {
		return PublishedSnapshot{}, err
	}
	scanRunID, err := s.ids.Next(ctx)
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("generate scan run ID: %w", err)
	}
	nodes := append([]NodeInput(nil), command.Nodes...)
	foreignKeys := append([]DeclaredForeignKey(nil), command.ForeignKeys...)
	return s.repository.PublishSnapshot(ctx, SnapshotPublication{
		ScanRunID:    scanRunID,
		ProjectID:    command.ProjectID,
		DataSourceID: command.DataSourceID,
		Nodes:        nodes,
		ForeignKeys:  foreignKeys,
		ScopeTables:  append([]string(nil), scopeTables...),
		StartedAt:    s.now().UTC(),
	})
}

func (s *Service) BeginSchemaScan(
	ctx context.Context,
	projectID int64,
	dataSourceID int64,
) (SchemaScanRun, error) {
	if projectID <= 0 || dataSourceID <= 0 {
		return SchemaScanRun{}, ErrInvalidSnapshot
	}
	scanRunID, err := s.ids.Next(ctx)
	if err != nil {
		return SchemaScanRun{}, fmt.Errorf("generate scan run ID: %w", err)
	}
	run := SchemaScanRun{
		ID: scanRunID, ProjectID: projectID, DataSourceID: dataSourceID, StartedAt: s.now().UTC(),
	}
	if err := s.repository.BeginSchemaScan(ctx, run); err != nil {
		return SchemaScanRun{}, err
	}
	return run, nil
}

func (s *Service) FailSchemaScan(ctx context.Context, run SchemaScanRun, errorCode string) error {
	errorCode = strings.TrimSpace(errorCode)
	if run.ID <= 0 || run.ProjectID <= 0 || run.DataSourceID <= 0 || run.StartedAt.IsZero() ||
		errorCode == "" || len(errorCode) > 100 {
		return ErrInvalidSnapshot
	}
	return s.repository.FailSchemaScan(ctx, SchemaScanFailure{
		Run: run, ErrorCode: errorCode, ErrorMessage: "schema scan did not complete", CompletedAt: s.now().UTC(),
	})
}

func (s *Service) PublishStartedSnapshot(
	ctx context.Context,
	run SchemaScanRun,
	command PublishSnapshot,
) (PublishedSnapshot, error) {
	scopeTables, err := normalizeScopeTables(command.ScopeTables)
	if err != nil {
		return PublishedSnapshot{}, err
	}
	command.ScopeTables = scopeTables
	if run.ID <= 0 || run.ProjectID != command.ProjectID || run.DataSourceID != command.DataSourceID ||
		run.StartedAt.IsZero() {
		return PublishedSnapshot{}, ErrInvalidSnapshot
	}
	if err := validateSnapshot(command); err != nil {
		return PublishedSnapshot{}, err
	}
	return s.repository.PublishSnapshot(ctx, SnapshotPublication{
		ScanRunID: run.ID, ProjectID: command.ProjectID, DataSourceID: command.DataSourceID,
		Nodes:       append([]NodeInput(nil), command.Nodes...),
		ForeignKeys: append([]DeclaredForeignKey(nil), command.ForeignKeys...),
		ScopeTables: append([]string(nil), scopeTables...),
		StartedAt:   run.StartedAt, Prestarted: true,
	})
}

func normalizeScopeTables(scopeTables []string) ([]string, error) {
	if len(scopeTables) > MaximumIncrementalTables {
		return nil, ErrInvalidSnapshot
	}
	normalized := make([]string, 0, len(scopeTables))
	seen := make(map[string]struct{}, len(scopeTables))
	for _, table := range scopeTables {
		table = strings.TrimSpace(table)
		separator := strings.LastIndexByte(table, '.')
		if table == "" || len(table) > 1000 || separator <= 0 || separator == len(table)-1 ||
			strings.ContainsAny(table, "\x00\r\n\t") {
			return nil, ErrInvalidSnapshot
		}
		if _, exists := seen[table]; exists {
			return nil, ErrInvalidSnapshot
		}
		seen[table] = struct{}{}
		normalized = append(normalized, table)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func (s *Service) FindCurrentNode(
	ctx context.Context,
	projectID int64,
	dataSourceID int64,
	qualifiedName string,
) (Node, error) {
	qualifiedName = strings.TrimSpace(qualifiedName)
	if projectID <= 0 || dataSourceID <= 0 || qualifiedName == "" || len(qualifiedName) > 1000 {
		return Node{}, ErrInvalidSnapshot
	}
	return s.repository.FindCurrentNode(ctx, projectID, dataSourceID, qualifiedName)
}

func (s *Service) GetCurrentNode(ctx context.Context, projectID int64, nodeID int64) (Node, error) {
	if projectID <= 0 || nodeID <= 0 {
		return Node{}, ErrInvalidSnapshot
	}
	return s.repository.GetCurrentNode(ctx, projectID, nodeID)
}

func (s *Service) SearchCurrentNodes(
	ctx context.Context,
	projectID int64,
	dataSourceID int64,
	query string,
	limit int,
) ([]Node, error) {
	query = strings.TrimSpace(query)
	if projectID <= 0 || dataSourceID < 0 || query == "" || len(query) > 500 || limit <= 0 || limit > 100 {
		return nil, ErrInvalidSnapshot
	}
	return s.repository.SearchCurrentNodes(ctx, projectID, dataSourceID, query, limit)
}

func validateSnapshot(command PublishSnapshot) error {
	if command.ProjectID <= 0 || command.DataSourceID <= 0 || len(command.Nodes) == 0 ||
		len(command.Nodes) > MaximumSnapshotNodes || len(command.ForeignKeys) > MaximumSnapshotForeignKeys {
		return ErrInvalidSnapshot
	}
	stableKeys := make(map[string]struct{}, len(command.Nodes))
	qualifiedNames := make(map[string]struct{}, len(command.Nodes))
	for _, node := range command.Nodes {
		if node.Kind < NodeDatabase || node.Kind > NodeColumn {
			return ErrInvalidSnapshot
		}
		if strings.TrimSpace(node.StableKey) == "" || len(node.StableKey) > 1000 {
			return ErrInvalidSnapshot
		}
		if strings.TrimSpace(node.Name) == "" || len(node.Name) > 500 {
			return ErrInvalidSnapshot
		}
		if strings.TrimSpace(node.QualifiedName) == "" || len(node.QualifiedName) > 1000 {
			return ErrInvalidSnapshot
		}
		if _, exists := stableKeys[node.StableKey]; exists {
			return ErrInvalidSnapshot
		}
		if _, exists := qualifiedNames[node.QualifiedName]; exists {
			return ErrInvalidSnapshot
		}
		stableKeys[node.StableKey] = struct{}{}
		qualifiedNames[node.QualifiedName] = struct{}{}
	}
	for _, node := range command.Nodes {
		if node.ParentStableKey == "" {
			if node.Kind != NodeDatabase {
				return ErrInvalidSnapshot
			}
			continue
		}
		if _, exists := stableKeys[node.ParentStableKey]; !exists {
			return ErrInvalidSnapshot
		}
	}
	columnNames := make(map[string]struct{})
	for _, node := range command.Nodes {
		if node.Kind == NodeColumn {
			columnNames[node.QualifiedName] = struct{}{}
		}
	}
	foreignKeyKeys := make(map[string]struct{}, len(command.ForeignKeys))
	for _, foreignKey := range command.ForeignKeys {
		constraintSchema := strings.TrimSpace(foreignKey.ConstraintSchema)
		name := strings.TrimSpace(foreignKey.Name)
		source := strings.TrimSpace(foreignKey.SourceColumn)
		target := strings.TrimSpace(foreignKey.TargetColumn)
		key := constraintSchema + "\x00" + name + "\x00" + fmt.Sprint(foreignKey.Ordinal) + "\x00" + source + "\x00" + target
		_, sourceExists := columnNames[source]
		_, targetExists := columnNames[target]
		if constraintSchema == "" || len(constraintSchema) > 500 || name == "" || len(name) > 500 ||
			source == "" || len(source) > 1000 || target == "" || len(target) > 1000 ||
			source == target || foreignKey.Ordinal <= 0 || !sourceExists || !targetExists {
			return ErrInvalidSnapshot
		}
		if _, exists := foreignKeyKeys[key]; exists {
			return ErrInvalidSnapshot
		}
		foreignKeyKeys[key] = struct{}{}
	}
	return nil
}

// MaximumDSNLength bounds a stored connection string. A DSN is untrusted input
// arriving from a Web form.
const MaximumDSNLength = 2000

// ErrSealerUnavailable reports a stored DSN requested without a configured key.
var ErrSealerUnavailable = errors.New("no secret key is configured for stored DSNs")

func (s *Service) sealDSN(dsn string) (string, []byte, error) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return "", nil, nil
	}
	if len(trimmed) > MaximumDSNLength {
		return "", nil, ErrInvalidDataSource
	}
	if s.validateDSN != nil {
		if err := s.validateDSN(trimmed); err != nil {
			return "", nil, fmt.Errorf("%w: %s", ErrUnusableDSN, err)
		}
	}
	if s.sealer == nil {
		return "", nil, ErrSealerUnavailable
	}
	keyID, ciphertext, err := s.sealer.Seal(trimmed)
	if err != nil {
		return "", nil, fmt.Errorf("seal data source DSN: %w", err)
	}
	return keyID, ciphertext, nil
}

func storedLabel(source DataSource) string {
	if len(source.DSNCiphertext) > 0 {
		return "true"
	}
	return "false"
}
