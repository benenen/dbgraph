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
	ErrInvalidSnapshot    = errors.New("invalid schema snapshot")
	ErrNodeNotFound       = errors.New("catalog node not found")
	ErrForbidden          = errors.New("catalog command is forbidden")
)

var environmentNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

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

type NodeStatus int

const (
	NodeActive NodeStatus = 1
	NodeStale  NodeStatus = 2
)

type DataSource struct {
	ID             int64
	ProjectID      int64
	Name           string
	Kind           DataSourceKind
	DSNEnvironment string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateDataSource struct {
	ProjectID      int64
	Name           string
	Kind           DataSourceKind
	DSNEnvironment string
}

type AdminCreateDataSource struct {
	ProjectID      int64
	Name           string
	Kind           DataSourceKind
	DSNEnvironment string
	Principal      relations.Principal
	Reason         string
	RequestID      string
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

type CatalogRepository interface {
	CreateDataSource(context.Context, DataSource) error
	CreateDataSourceWithAudit(context.Context, DataSource, audit.Event) error
	GetDataSource(context.Context, int64) (DataSource, error)
	BeginSchemaScan(context.Context, SchemaScanRun) error
	FailSchemaScan(context.Context, SchemaScanFailure) error
	PublishSnapshot(context.Context, SnapshotPublication) (PublishedSnapshot, error)
	FindCurrentNode(context.Context, int64, int64, string) (Node, error)
	GetCurrentNode(context.Context, int64, int64) (Node, error)
	SearchCurrentNodes(context.Context, int64, string, int) ([]Node, error)
}

func (s *Service) GetDataSource(ctx context.Context, dataSourceID int64) (DataSource, error) {
	if dataSourceID <= 0 {
		return DataSource{}, ErrInvalidDataSource
	}
	return s.repository.GetDataSource(ctx, dataSourceID)
}

type Service struct {
	repository CatalogRepository
	ids        ProjectIDGenerator
	now        func() time.Time
}

func NewService(repository CatalogRepository, ids ProjectIDGenerator, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, ids: ids, now: now}
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
	if actor == "" || len(actor) > 200 || reason == "" || len(reason) > 2000 ||
		requestID == "" || len(requestID) > 200 ||
		(command.Principal.Origin != audit.OriginAgent && command.Principal.Origin != audit.OriginWeb) {
		return DataSource{}, ErrInvalidDataSource
	}
	return s.createDataSource(ctx, CreateDataSource{
		ProjectID: command.ProjectID, Name: command.Name, Kind: command.Kind,
		DSNEnvironment: command.DSNEnvironment,
	}, func(source DataSource, eventID int64, occurredAt time.Time) audit.Event {
		details, _ := json.Marshal(map[string]string{
			"kind": "MYSQL", "dsnEnvironment": source.DSNEnvironment,
		})
		return audit.Event{
			ID: eventID, ProjectID: source.ProjectID, Actor: actor, Origin: command.Principal.Origin,
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
	if name == "" || len(name) > 200 || !environmentNamePattern.MatchString(dsnEnvironment) {
		return DataSource{}, ErrInvalidDataSource
	}

	dataSourceID, err := s.ids.Next(ctx)
	if err != nil {
		return DataSource{}, fmt.Errorf("generate data source ID: %w", err)
	}
	now := s.now().UTC()
	dataSource := DataSource{
		ID:             dataSourceID,
		ProjectID:      command.ProjectID,
		Name:           name,
		Kind:           command.Kind,
		DSNEnvironment: dsnEnvironment,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if auditBuilder != nil {
		auditID, err := s.ids.Next(ctx)
		if err != nil {
			return DataSource{}, fmt.Errorf("generate data source audit ID: %w", err)
		}
		if err := s.repository.CreateDataSourceWithAudit(ctx, dataSource, auditBuilder(dataSource, auditID, now)); err != nil {
			return DataSource{}, err
		}
		return dataSource, nil
	}
	if err := s.repository.CreateDataSource(ctx, dataSource); err != nil {
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
	query string,
	limit int,
) ([]Node, error) {
	query = strings.TrimSpace(query)
	if projectID <= 0 || query == "" || len(query) > 500 || limit <= 0 || limit > 100 {
		return nil, ErrInvalidSnapshot
	}
	return s.repository.SearchCurrentNodes(ctx, projectID, query, limit)
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
