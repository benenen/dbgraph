package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/benenen/dbgraph/internal/audit"
	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/relations"
)

// codedFailure is an error that names why it happened. A runner implements it
// so the reason reaches the operator instead of being flattened to "failed".
type codedFailure interface {
	FailureCode() string
}

func failureCode(err error) string {
	var coded codedFailure
	if errors.As(err, &coded) {
		return coded.FailureCode()
	}
	return ""
}

var (
	ErrForbidden    = errors.New("schema scan command is forbidden")
	ErrNoPendingJob = errors.New("no pending schema scan job")
	ErrJobConflict  = errors.New("schema scan job revision conflict")
	ErrQueueFull    = errors.New("schema scan queue is full")
	ErrStoreBusy    = errors.New("schema scan store is temporarily busy")
)

const (
	maximumPendingSchemaScans = 100
	schemaScanPollInterval    = 30 * time.Second
	schemaScanRetryMinimum    = 10 * time.Millisecond
	schemaScanRetryMaximum    = time.Second
)

type StartSchemaScan struct {
	DataSourceID int64
	Mode         SchemaScanMode
	Tables       []string
	Principal    relations.Principal
	Reason       string
	RequestID    string
}

type SchemaScanMode int

const (
	SchemaScanFull SchemaScanMode = iota + 1
	SchemaScanIncremental
)

type SchemaScanCompletion struct {
	JobID              int64
	ExpectedRevisionNo int
	Status             Status
	Result             json.RawMessage
	ErrorCode          string
	ErrorMessage       string
	CompletedAt        time.Time
}

type SchemaScanStore interface {
	CreateSchemaScanJob(context.Context, Job, audit.Event, int) error
	RecoverRunningSchemaScans(context.Context, time.Time) (int, error)
	ClaimNextSchemaScan(context.Context, time.Time) (Job, error)
	FinishSchemaScan(context.Context, SchemaScanCompletion) (Job, error)
	GetJob(context.Context, int64) (Job, error)
}

type DataSourceCatalog interface {
	GetProjectDataSource(context.Context, int64) (catalog.DataSource, error)
}

type SchemaRunner interface {
	Run(context.Context, int64) (catalog.PublishedSnapshot, error)
}

type incrementalSchemaRunner interface {
	RunIncremental(context.Context, int64, []string) (catalog.PublishedSnapshot, error)
}

type SchemaScanCoordinator struct {
	store   SchemaScanStore
	catalog DataSourceCatalog
	runner  SchemaRunner
	ids     IDGenerator
	now     func() time.Time
	wake    chan struct{}
}

func NewSchemaScanCoordinator(
	store SchemaScanStore,
	catalog DataSourceCatalog,
	runner SchemaRunner,
	ids IDGenerator,
	now func() time.Time,
) *SchemaScanCoordinator {
	if now == nil {
		now = time.Now
	}
	return &SchemaScanCoordinator{
		store: store, catalog: catalog, runner: runner, ids: ids, now: now,
		wake: make(chan struct{}, 1),
	}
}

func (c *SchemaScanCoordinator) Start(ctx context.Context, command StartSchemaScan) (Job, error) {
	mode, tables, err := normalizeSchemaScanScope(command.Mode, command.Tables)
	if err != nil {
		return Job{}, err
	}
	command.Mode = mode
	command.Tables = tables
	if err := validateStartSchemaScan(command); err != nil {
		return Job{}, err
	}
	if c.store == nil || c.catalog == nil || c.ids == nil {
		return Job{}, ErrInvalidJob
	}
	// The link is the authorization: a project may only scan a source it has
	// adopted, and GetProjectDataSource fails when it has not.
	source, err := c.catalog.GetProjectDataSource(ctx, command.DataSourceID)
	if err != nil {
		return Job{}, err
	}
	if source.Kind != catalog.DataSourceMySQL {
		return Job{}, ErrInvalidJob
	}
	payload, err := json.Marshal(schemaScanPayload{
		DataSourceID: strconv.FormatInt(command.DataSourceID, 10),
		Mode:         schemaScanModeName(command.Mode), Tables: append([]string(nil), command.Tables...),
	})
	if err != nil {
		return Job{}, fmt.Errorf("encode schema scan job: %w", err)
	}
	jobID, err := c.ids.Next(ctx)
	if err != nil {
		return Job{}, fmt.Errorf("generate schema scan job ID: %w", err)
	}
	auditID, err := c.ids.Next(ctx)
	if err != nil {
		return Job{}, fmt.Errorf("generate schema scan audit ID: %w", err)
	}
	now := c.now().UTC()
	job := Job{
		ID: jobID, Type: TypeSchemaScan,
		Status: StatusPending, Payload: payload, CreatedAt: now, RevisionNo: 1,
	}
	event := audit.Event{
		ID:    auditID,
		Actor: strings.TrimSpace(command.Principal.Actor), Origin: command.Principal.Origin,
		Action: "SCHEMA_SCAN_QUEUED", SubjectType: "SCHEMA_SCAN_JOB", SubjectID: jobID,
		Reason: strings.TrimSpace(command.Reason), RequestID: strings.TrimSpace(command.RequestID),
		Details: append(json.RawMessage(nil), payload...), OccurredAt: now,
	}
	if err := c.store.CreateSchemaScanJob(ctx, job, event, maximumPendingSchemaScans); err != nil {
		return Job{}, err
	}
	c.signal()
	return job, nil
}

func (c *SchemaScanCoordinator) Get(ctx context.Context, jobID int64) (Job, error) {
	if jobID <= 0 || c.store == nil {
		return Job{}, ErrInvalidJob
	}
	return c.store.GetJob(ctx, jobID)
}

func (c *SchemaScanCoordinator) Run(ctx context.Context) error {
	if c.store == nil || c.runner == nil {
		return ErrInvalidJob
	}
	if err := c.recoverRunning(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(schemaScanPollInterval)
	defer ticker.Stop()
	temporaryFailures := 0
	for {
		processed, err := c.processNext(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, ErrStoreBusy) {
				if !waitForSchemaScanRetry(ctx, temporaryFailures) {
					return nil
				}
				temporaryFailures++
				continue
			}
			return err
		}
		temporaryFailures = 0
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.wake:
		case <-ticker.C:
		}
	}
}

func (c *SchemaScanCoordinator) recoverRunning(ctx context.Context) error {
	for temporaryFailures := 0; ; temporaryFailures++ {
		_, err := c.store.RecoverRunningSchemaScans(ctx, c.now().UTC())
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}
		if !errors.Is(err, ErrStoreBusy) {
			return err
		}
		if !waitForSchemaScanRetry(ctx, temporaryFailures) {
			return nil
		}
	}
}

func (c *SchemaScanCoordinator) processNext(ctx context.Context) (bool, error) {
	job, err := c.store.ClaimNextSchemaScan(ctx, c.now().UTC())
	if errors.Is(err, ErrNoPendingJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	scanRequest, err := decodeSchemaScanPayload(job.Payload)
	if err != nil {
		return true, c.finishFailure(ctx, job, StatusFailed, "INVALID_JOB_PAYLOAD")
	}
	var published catalog.PublishedSnapshot
	var runErr error
	if scanRequest.Mode == SchemaScanIncremental {
		incrementalRunner, ok := c.runner.(incrementalSchemaRunner)
		if !ok {
			return true, c.finishFailure(ctx, job, StatusFailed, "INCREMENTAL_SCAN_UNSUPPORTED")
		}
		published, runErr = incrementalRunner.RunIncremental(ctx, scanRequest.DataSourceID, scanRequest.Tables)
	} else {
		published, runErr = c.runner.Run(ctx, scanRequest.DataSourceID)
	}
	if runErr != nil {
		if ctx.Err() != nil {
			cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), schemaScanRetryMaximum)
			defer cancel()
			return true, c.finishFailure(cleanupContext, job, StatusCancelled, "SCAN_CANCELLED")
		}
		// The runner knows why it stopped; carry that through instead of
		// telling the operator only that a scan failed.
		code := "SCHEMA_SCAN_FAILED"
		if specific := failureCode(runErr); specific != "" {
			code = specific
		}
		return true, c.finishFailure(ctx, job, StatusFailed, code)
	}
	result, err := json.Marshal(map[string]any{
		"scanRunId": strconv.FormatInt(published.ScanRunID, 10),
		"nodeCount": published.NodeCount, "staleCount": published.StaleCount,
		"publishedAt": published.PublishedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return true, c.finishFailure(ctx, job, StatusFailed, "RESULT_ENCODING_FAILED")
	}
	err = c.finishWithRetry(ctx, SchemaScanCompletion{
		JobID: job.ID, ExpectedRevisionNo: job.RevisionNo, Status: StatusSucceeded,
		Result: result, CompletedAt: c.now().UTC(),
	})
	return true, err
}

func (c *SchemaScanCoordinator) finishFailure(
	ctx context.Context,
	job Job,
	status Status,
	code string,
) error {
	return c.finishWithRetry(ctx, SchemaScanCompletion{
		JobID: job.ID, ExpectedRevisionNo: job.RevisionNo, Status: status,
		ErrorCode: code, ErrorMessage: "schema scan did not complete",
		CompletedAt: c.now().UTC(),
	})
}

func (c *SchemaScanCoordinator) finishWithRetry(ctx context.Context, completion SchemaScanCompletion) error {
	for temporaryFailures := 0; ; temporaryFailures++ {
		_, err := c.store.FinishSchemaScan(ctx, completion)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrStoreBusy) {
			return err
		}
		if !waitForSchemaScanRetry(ctx, temporaryFailures) {
			return ctx.Err()
		}
	}
}

func waitForSchemaScanRetry(ctx context.Context, failures int) bool {
	delay := schemaScanRetryMinimum
	for remaining := failures; remaining > 0 && delay < schemaScanRetryMaximum; remaining-- {
		delay *= 2
	}
	if delay > schemaScanRetryMaximum {
		delay = schemaScanRetryMaximum
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *SchemaScanCoordinator) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func validateStartSchemaScan(command StartSchemaScan) error {
	if command.Principal.Role != relations.RoleAdmin {
		return ErrForbidden
	}
	actor := strings.TrimSpace(command.Principal.Actor)
	reason := strings.TrimSpace(command.Reason)
	requestID := strings.TrimSpace(command.RequestID)
	if command.DataSourceID <= 0 ||
		(command.Mode != SchemaScanFull && command.Mode != SchemaScanIncremental) ||
		(command.Mode == SchemaScanFull && len(command.Tables) != 0) ||
		(command.Mode == SchemaScanIncremental && len(command.Tables) == 0) ||
		(command.Principal.Origin != audit.OriginAgent && command.Principal.Origin != audit.OriginWeb) ||
		actor == "" || len(actor) > 200 || reason == "" || len(reason) > 2000 ||
		requestID == "" || len(requestID) > 200 {
		return ErrInvalidJob
	}
	return nil
}

type schemaScanPayload struct {
	DataSourceID string   `json:"dataSourceId"`
	Mode         string   `json:"mode"`
	Tables       []string `json:"tables,omitempty"`
}

type decodedSchemaScan struct {
	DataSourceID int64
	Mode         SchemaScanMode
	Tables       []string
}

func decodeSchemaScanPayload(payload json.RawMessage) (decodedSchemaScan, error) {
	var decoded schemaScanPayload
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return decodedSchemaScan{}, ErrInvalidJob
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return decodedSchemaScan{}, ErrInvalidJob
	}
	id, err := strconv.ParseInt(decoded.DataSourceID, 10, 64)
	if err != nil || id <= 0 {
		return decodedSchemaScan{}, ErrInvalidJob
	}
	mode := SchemaScanFull
	if decoded.Mode != "" {
		mode, err = parseSchemaScanMode(decoded.Mode)
		if err != nil {
			return decodedSchemaScan{}, err
		}
	}
	mode, tables, err := normalizeSchemaScanScope(mode, decoded.Tables)
	if err != nil {
		return decodedSchemaScan{}, err
	}
	return decodedSchemaScan{DataSourceID: id, Mode: mode, Tables: tables}, nil
}

func normalizeSchemaScanScope(mode SchemaScanMode, tables []string) (SchemaScanMode, []string, error) {
	if mode == 0 {
		mode = SchemaScanFull
	}
	if mode != SchemaScanFull && mode != SchemaScanIncremental {
		return 0, nil, ErrInvalidJob
	}
	if (mode == SchemaScanFull && len(tables) != 0) ||
		(mode == SchemaScanIncremental && (len(tables) == 0 || len(tables) > catalog.MaximumIncrementalTables)) {
		return 0, nil, ErrInvalidJob
	}
	normalized := make([]string, 0, len(tables))
	seen := make(map[string]struct{}, len(tables))
	for _, table := range tables {
		table = strings.TrimSpace(table)
		separator := strings.LastIndexByte(table, '.')
		if separator <= 0 || separator == len(table)-1 || len(table) > 1000 ||
			strings.ContainsAny(table, "\x00\r\n\t") {
			return 0, nil, ErrInvalidJob
		}
		if _, exists := seen[table]; exists {
			return 0, nil, ErrInvalidJob
		}
		seen[table] = struct{}{}
		normalized = append(normalized, table)
	}
	sort.Strings(normalized)
	return mode, normalized, nil
}

func schemaScanModeName(mode SchemaScanMode) string {
	if mode == SchemaScanIncremental {
		return "INCREMENTAL"
	}
	return "FULL"
}

func parseSchemaScanMode(value string) (SchemaScanMode, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "FULL":
		return SchemaScanFull, nil
	case "INCREMENTAL":
		return SchemaScanIncremental, nil
	default:
		return 0, ErrInvalidJob
	}
}
