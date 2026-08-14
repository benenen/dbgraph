package mcpapi

import (
	"errors"

	"github.com/benenen/dbgraph/internal/catalog"
	"github.com/benenen/dbgraph/internal/graph"
	"github.com/benenen/dbgraph/internal/jobs"
	"github.com/benenen/dbgraph/internal/reconcile"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/benenen/dbgraph/internal/sourcebinding"
)

var (
	errInvalidRequest = errors.New("dbgraph rejected the request")
	errForbidden      = errors.New("dbgraph permission denied")
	errNotFound       = errors.New("dbgraph resource not found")
	errConflict       = errors.New("dbgraph resource changed or conflicts with existing state")
	errOperation      = errors.New("dbgraph operation failed")
	errRateLimited    = errors.New("dbgraph rate limit exceeded")
	errResponseBudget = errors.New("dbgraph response exceeded its size budget")
)

func safeToolError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, errInvalidRequest), errors.Is(err, errForbidden), errors.Is(err, errNotFound),
		errors.Is(err, errConflict), errors.Is(err, errOperation), errors.Is(err, errRateLimited),
		errors.Is(err, errResponseBudget):
		return err
	case errors.Is(err, relations.ErrForbidden):
		return errForbidden
	case errors.Is(err, sourcebinding.ErrForbidden):
		return errForbidden
	case errors.Is(err, catalog.ErrNodeNotFound), errors.Is(err, catalog.ErrDataSourceNotFound),
		errors.Is(err, relations.ErrRelationNotFound), errors.Is(err, reconcile.ErrInitNotFound),
		errors.Is(err, jobs.ErrJobNotFound):
		return errNotFound
	case errors.Is(err, sourcebinding.ErrRepositoryNotFound), errors.Is(err, sourcebinding.ErrBindingNotFound):
		return errNotFound
	case errors.Is(err, relations.ErrRevisionConflict), errors.Is(err, relations.ErrPendingProposal),
		errors.Is(err, relations.ErrDuplicateRelation), errors.Is(err, reconcile.ErrBatchConflict),
		errors.Is(err, reconcile.ErrIdempotencyConflict), errors.Is(err, reconcile.ErrIncompleteBatches):
		return errConflict
	case errors.Is(err, sourcebinding.ErrRevisionConflict), errors.Is(err, sourcebinding.ErrBindingConflict):
		return errConflict
	case errors.Is(err, relations.ErrInvalidCommand), errors.Is(err, relations.ErrInvalidTransition),
		errors.Is(err, reconcile.ErrInvalidInit), errors.Is(err, reconcile.ErrInitNotOpen),
		errors.Is(err, catalog.ErrInvalidSnapshot), errors.Is(err, jobs.ErrInvalidJob),
		errors.Is(err, graph.ErrInvalidTrace), errors.Is(err, errInvalidToolInput):
		return errInvalidRequest
	case errors.Is(err, sourcebinding.ErrInvalidWorkspaceEvidence), errors.Is(err, sourcebinding.ErrInvalidBinding):
		return errInvalidRequest
	default:
		return errOperation
	}
}
