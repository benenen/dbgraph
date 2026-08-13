package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/benenen/dbgraph/internal/conditions"
	"github.com/benenen/dbgraph/internal/relations"
)

var ErrInvalidTrace = errors.New("invalid graph trace request")

type Direction int

const (
	DirectionDownstream Direction = iota + 1
	DirectionUpstream
)

type Limits struct {
	MaxDepth int
	MaxNodes int
	MaxPaths int
}

func DefaultLimits() Limits {
	return Limits{MaxDepth: 8, MaxNodes: 1000, MaxPaths: 1000}
}

const (
	maximumFrontierStates  = 5_000
	maximumPathStates      = 10_000
	maximumEdgeExpansions  = 100_000
	maximumLoadedEdgeBytes = 16 << 20
	maximumResultSteps     = 20_000
	maximumResultBytes     = 8 << 20
)

type Edge struct {
	RelationID         int64
	VersionID          int64
	SourceNodeID       int64
	TargetNodeID       int64
	Type               relations.Type
	Status             relations.Status
	HasPendingProposal bool
	Guard              *conditions.Boolean
	Selector           *conditions.Boolean
	Transform          conditions.Value
	Confidence         float64
}

type Step struct {
	Edge       Edge                  `json:"edge"`
	Evaluation conditions.Evaluation `json:"evaluation"`
}

type Path struct {
	Nodes []int64 `json:"nodes"`
	Steps []Step  `json:"steps"`
}

type TraceRequest struct {
	StartNodeID  int64
	TargetNodeID int64
	Direction    Direction
	Context      conditions.Context
	Limits       Limits
}

type TraceResult struct {
	Paths         []Path `json:"paths"`
	VisitedNodes  int    `json:"visitedNodes"`
	CycleDetected bool   `json:"cycleDetected"`
	Truncated     bool   `json:"truncated"`
}

type Repository interface {
	LoadEdges(context.Context, []int64, Direction, int, int) ([]Edge, bool, int, error)
}

type RecursiveTraceRequest struct {
	StartNodeID       int64
	TargetNodeID      int64
	Direction         Direction
	MaxDepth          int
	MaxEdgeExpansions int
	MaxLoadedBytes    int
}

type RecursiveEdgeState struct {
	StateKey       string
	ParentStateKey string
	Depth          int
	NextNodeID     int64
	Cycle          bool
	Edge           Edge
}

type RecursiveRepository interface {
	LoadRecursiveEdges(
		context.Context,
		RecursiveTraceRequest,
		func(RecursiveEdgeState) error,
	) (bool, int, error)
}

type Service struct {
	repository Repository
}

type traversalState struct {
	current int64
	path    Path
}

type resultBudget struct {
	steps     int
	bytes     int
	stepSizes map[[2]int64]int
}

type edgeEvaluationKey struct {
	relationID int64
	versionID  int64
}

type edgeEvaluator struct {
	context conditions.PreparedContext
	cache   map[edgeEvaluationKey]conditions.Evaluation
}

var errStopRecursiveTraversal = errors.New("stop recursive graph traversal")

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) Trace(ctx context.Context, request TraceRequest) (TraceResult, error) {
	if request.Limits == (Limits{}) {
		request.Limits = DefaultLimits()
	}
	if err := validateTraceRequest(request); err != nil {
		return TraceResult{}, err
	}
	preparedContext, err := conditions.PrepareContext(request.Context, conditions.DefaultContextLimits())
	if err != nil {
		return TraceResult{}, ErrInvalidTrace
	}
	if err := ctx.Err(); err != nil {
		return TraceResult{}, err
	}
	evaluator := newEdgeEvaluator(preparedContext)
	if recursive, ok := s.repository.(RecursiveRepository); ok && traceContextIsEmpty(request.Context) {
		return s.traceRecursive(ctx, recursive, request, evaluator)
	}

	frontier := []traversalState{{
		current: request.StartNodeID,
		path:    Path{Nodes: []int64{request.StartNodeID}},
	}}
	visited := map[int64]struct{}{request.StartNodeID: {}}
	result := TraceResult{}
	outputBudget := resultBudget{stepSizes: make(map[[2]int64]int)}
	pathStates := 1
	edgeExpansions := 0
	loadedEdgeBytes := 0

	for depth := 0; depth < request.Limits.MaxDepth && len(frontier) > 0; depth++ {
		if err := ctx.Err(); err != nil {
			return TraceResult{}, err
		}
		frontierNodeIDs := uniqueFrontierNodes(frontier)
		remainingEdgeBudget := maximumEdgeExpansions - edgeExpansions
		remainingByteBudget := maximumLoadedEdgeBytes - loadedEdgeBytes
		if remainingByteBudget <= 0 {
			return truncatedResult(result, visited), nil
		}
		edges, edgesTruncated, consumedBytes, err := s.repository.LoadEdges(
			ctx, frontierNodeIDs, request.Direction,
			remainingEdgeBudget, remainingByteBudget,
		)
		if err != nil {
			return TraceResult{}, err
		}
		if consumedBytes < 0 || consumedBytes > remainingByteBudget {
			return TraceResult{}, errors.New("graph repository returned invalid byte accounting")
		}
		loadedEdgeBytes += consumedBytes
		if err := ctx.Err(); err != nil {
			return TraceResult{}, err
		}
		edgesByNode := groupEdges(edges, request.Direction)
		nextFrontier := make([]traversalState, 0)
		for _, state := range frontier {
			if err := ctx.Err(); err != nil {
				return TraceResult{}, err
			}
			for _, edge := range edgesByNode[state.current] {
				if err := ctx.Err(); err != nil {
					return TraceResult{}, err
				}
				if edgeExpansions >= maximumEdgeExpansions {
					return truncatedResult(result, visited), nil
				}
				edgeExpansions++
				evaluation, err := evaluator.evaluate(edge)
				if err != nil {
					return TraceResult{}, fmt.Errorf("evaluate relation %d: %w", edge.RelationID, err)
				}
				if evaluation.Truth == conditions.TruthFalse {
					continue
				}
				nextNodeID := edge.TargetNodeID
				if request.Direction == DirectionUpstream {
					nextNodeID = edge.SourceNodeID
				}
				if pathContains(state.path.Nodes, nextNodeID) {
					result.CycleDetected = true
					continue
				}
				_, seen := visited[nextNodeID]
				if !seen {
					if len(visited) >= request.Limits.MaxNodes {
						result.Truncated = true
						continue
					}
				}
				if pathStates >= maximumPathStates {
					return truncatedResult(result, visited), nil
				}
				pathStates++
				if !seen {
					visited[nextNodeID] = struct{}{}
				}
				nextPath := Path{
					Nodes: append(append([]int64(nil), state.path.Nodes...), nextNodeID),
					Steps: append(append([]Step(nil), state.path.Steps...), Step{
						Edge:       edge,
						Evaluation: evaluation,
					}),
				}
				targetReached := request.TargetNodeID == 0 || request.TargetNodeID == nextNodeID
				if targetReached {
					if len(result.Paths) >= request.Limits.MaxPaths {
						return truncatedResult(result, visited), nil
					}
					accepted, err := outputBudget.accept(nextPath)
					if err != nil {
						return TraceResult{}, fmt.Errorf("estimate graph result: %w", err)
					}
					if !accepted {
						return truncatedResult(result, visited), nil
					}
					result.Paths = append(result.Paths, nextPath)
				}
				if request.TargetNodeID == 0 || request.TargetNodeID != nextNodeID {
					if len(nextFrontier) >= maximumFrontierStates {
						return truncatedResult(result, visited), nil
					}
					nextFrontier = append(nextFrontier, traversalState{current: nextNodeID, path: nextPath})
				}
			}
		}
		if edgesTruncated {
			return truncatedResult(result, visited), nil
		}
		frontier = nextFrontier
	}
	if len(frontier) > 0 {
		result.Truncated = true
	}
	result.VisitedNodes = len(visited)
	return result, nil
}

func (s *Service) traceRecursive(
	ctx context.Context,
	repository RecursiveRepository,
	request TraceRequest,
	evaluator *edgeEvaluator,
) (TraceResult, error) {
	root := traversalState{current: request.StartNodeID, path: Path{Nodes: []int64{request.StartNodeID}}}
	states := map[string]traversalState{"": root}
	seenStateKeys := map[string]struct{}{"": {}}
	visited := map[int64]struct{}{request.StartNodeID: {}}
	frontierStates := make(map[int]int)
	result := TraceResult{}
	outputBudget := resultBudget{stepSizes: make(map[[2]int64]int)}
	pathStates := 1
	edgeExpansions := 0

	repositoryTruncated, consumedBytes, err := repository.LoadRecursiveEdges(
		ctx,
		RecursiveTraceRequest{
			StartNodeID: request.StartNodeID, TargetNodeID: request.TargetNodeID,
			Direction: request.Direction,
			MaxDepth:  request.Limits.MaxDepth, MaxEdgeExpansions: maximumEdgeExpansions,
			MaxLoadedBytes: maximumLoadedEdgeBytes,
		},
		func(candidate RecursiveEdgeState) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			edgeExpansions++
			if edgeExpansions > maximumEdgeExpansions {
				result.Truncated = true
				return errStopRecursiveTraversal
			}
			if candidate.StateKey == "" || candidate.Depth < 1 || candidate.Depth > request.Limits.MaxDepth {
				return errors.New("graph repository returned invalid recursive state")
			}
			if _, duplicate := seenStateKeys[candidate.StateKey]; duplicate {
				return errors.New("graph repository returned duplicate recursive state")
			}
			seenStateKeys[candidate.StateKey] = struct{}{}
			parent, activeParent := states[candidate.ParentStateKey]
			if !activeParent {
				if _, parentSeen := seenStateKeys[candidate.ParentStateKey]; !parentSeen {
					return errors.New("graph repository returned recursive state before its parent")
				}
				return nil
			}
			if candidate.Depth != len(parent.path.Steps)+1 ||
				candidate.NextNodeID <= 0 || recursiveNextNode(candidate.Edge, request.Direction) != candidate.NextNodeID ||
				recursiveCurrentNode(candidate.Edge, request.Direction) != parent.current {
				return errors.New("graph repository returned inconsistent recursive state")
			}
			cycle := pathContains(parent.path.Nodes, candidate.NextNodeID)
			if cycle != candidate.Cycle {
				return errors.New("graph repository returned invalid recursive cycle state")
			}
			evaluation, err := evaluator.evaluate(candidate.Edge)
			if err != nil {
				return fmt.Errorf("evaluate relation %d: %w", candidate.Edge.RelationID, err)
			}
			if evaluation.Truth == conditions.TruthFalse {
				return nil
			}
			if candidate.Cycle {
				result.CycleDetected = true
				return nil
			}
			if pathStates >= maximumPathStates {
				result.Truncated = true
				return errStopRecursiveTraversal
			}
			_, nodeSeen := visited[candidate.NextNodeID]
			if !nodeSeen && len(visited) >= request.Limits.MaxNodes {
				result.Truncated = true
				return nil
			}
			pathStates++
			if !nodeSeen {
				visited[candidate.NextNodeID] = struct{}{}
			}
			nextPath := Path{
				Nodes: append(append([]int64(nil), parent.path.Nodes...), candidate.NextNodeID),
				Steps: append(append([]Step(nil), parent.path.Steps...), Step{
					Edge: candidate.Edge, Evaluation: evaluation,
				}),
			}
			targetReached := request.TargetNodeID == 0 || request.TargetNodeID == candidate.NextNodeID
			if targetReached {
				if len(result.Paths) >= request.Limits.MaxPaths {
					result.Truncated = true
					return errStopRecursiveTraversal
				}
				accepted, err := outputBudget.accept(nextPath)
				if err != nil {
					return fmt.Errorf("estimate graph result: %w", err)
				}
				if !accepted {
					result.Truncated = true
					return errStopRecursiveTraversal
				}
				result.Paths = append(result.Paths, nextPath)
			}
			canExpand := request.TargetNodeID == 0 || request.TargetNodeID != candidate.NextNodeID
			if canExpand {
				if frontierStates[candidate.Depth] >= maximumFrontierStates {
					result.Truncated = true
					return errStopRecursiveTraversal
				}
				frontierStates[candidate.Depth]++
				states[candidate.StateKey] = traversalState{current: candidate.NextNodeID, path: nextPath}
				if candidate.Depth == request.Limits.MaxDepth {
					result.Truncated = true
				}
			}
			return nil
		},
	)
	if errors.Is(err, errStopRecursiveTraversal) {
		err = nil
	}
	if err != nil {
		return TraceResult{}, err
	}
	if consumedBytes < 0 || consumedBytes > maximumLoadedEdgeBytes {
		return TraceResult{}, errors.New("graph repository returned invalid byte accounting")
	}
	if repositoryTruncated {
		result.Truncated = true
	}
	result.VisitedNodes = len(visited)
	return result, nil
}

func traceContextIsEmpty(value conditions.Context) bool {
	return len(value.Columns) == 0 && len(value.Parameters) == 0
}

func recursiveCurrentNode(edge Edge, direction Direction) int64 {
	if direction == DirectionUpstream {
		return edge.TargetNodeID
	}
	return edge.SourceNodeID
}

func recursiveNextNode(edge Edge, direction Direction) int64 {
	if direction == DirectionUpstream {
		return edge.SourceNodeID
	}
	return edge.TargetNodeID
}

func (b *resultBudget) accept(path Path) (bool, error) {
	if b.steps+len(path.Steps) > maximumResultSteps {
		return false, nil
	}
	pathBytes := 64 + len(path.Nodes)*24
	for _, step := range path.Steps {
		key := [2]int64{step.Edge.RelationID, step.Edge.VersionID}
		size, exists := b.stepSizes[key]
		if !exists {
			encoded, err := json.Marshal(step)
			if err != nil {
				return false, err
			}
			size = len(encoded) + 32
			b.stepSizes[key] = size
		}
		pathBytes += size
		if b.bytes+pathBytes > maximumResultBytes {
			return false, nil
		}
	}
	b.steps += len(path.Steps)
	b.bytes += pathBytes
	return true, nil
}

func (s *Service) Impact(ctx context.Context, request TraceRequest) (TraceResult, error) {
	request.Direction = DirectionDownstream
	return s.Trace(ctx, request)
}

func validateTraceRequest(request TraceRequest) error {
	if request.StartNodeID <= 0 || request.TargetNodeID < 0 ||
		(request.Direction != DirectionDownstream && request.Direction != DirectionUpstream) ||
		request.Limits.MaxDepth < 1 || request.Limits.MaxDepth > 64 ||
		request.Limits.MaxNodes < 1 || request.Limits.MaxNodes > 10_000 ||
		request.Limits.MaxPaths < 1 || request.Limits.MaxPaths > 10_000 ||
		len(request.Context.Columns) > 1000 || len(request.Context.Parameters) > 1000 {
		return ErrInvalidTrace
	}
	return nil
}

func truncatedResult(result TraceResult, visited map[int64]struct{}) TraceResult {
	result.Truncated = true
	result.VisitedNodes = len(visited)
	return result
}

func newEdgeEvaluator(context conditions.PreparedContext) *edgeEvaluator {
	return &edgeEvaluator{context: context, cache: make(map[edgeEvaluationKey]conditions.Evaluation)}
}

func (e *edgeEvaluator) evaluate(edge Edge) (conditions.Evaluation, error) {
	key := edgeEvaluationKey{relationID: edge.RelationID, versionID: edge.VersionID}
	cacheable := key.relationID > 0 && key.versionID > 0
	if cacheable {
		if cached, ok := e.cache[key]; ok {
			return cloneEvaluation(cached), nil
		}
	}
	evaluation, err := evaluateEdge(edge, e.context)
	if err != nil {
		return conditions.Evaluation{}, err
	}
	if cacheable {
		e.cache[key] = cloneEvaluation(evaluation)
	}
	return evaluation, nil
}

func cloneEvaluation(value conditions.Evaluation) conditions.Evaluation {
	return conditions.Evaluation{
		Truth: value.Truth, Missing: append([]conditions.MissingReference(nil), value.Missing...),
	}
}

func evaluateEdge(edge Edge, context conditions.PreparedContext) (conditions.Evaluation, error) {
	evaluations := make([]conditions.Evaluation, 0, 3)
	for _, expression := range []*conditions.Boolean{edge.Guard, edge.Selector} {
		if expression == nil {
			continue
		}
		evaluation, err := conditions.EvaluateBooleanPrepared(*expression, context)
		if err != nil {
			return conditions.Evaluation{}, err
		}
		evaluations = append(evaluations, evaluation)
	}
	if edge.Transform.Kind != "" {
		evaluation, err := conditions.EvaluateValuePrepared(edge.Transform, context)
		if err != nil {
			return conditions.Evaluation{}, err
		}
		evaluations = append(evaluations, evaluation)
	}
	missing := make([]conditions.MissingReference, 0)
	for _, evaluation := range evaluations {
		if evaluation.Truth == conditions.TruthFalse {
			return conditions.Evaluation{Truth: conditions.TruthFalse}, nil
		}
		if evaluation.Truth == conditions.TruthUnknown {
			missing = append(missing, evaluation.Missing...)
		}
	}
	if len(missing) == 0 {
		return conditions.Evaluation{Truth: conditions.TruthTrue}, nil
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].NodeID == missing[j].NodeID {
			return missing[i].Parameter < missing[j].Parameter
		}
		return missing[i].NodeID < missing[j].NodeID
	})
	deduplicated := missing[:0]
	for _, reference := range missing {
		if len(deduplicated) > 0 && deduplicated[len(deduplicated)-1] == reference {
			continue
		}
		deduplicated = append(deduplicated, reference)
	}
	return conditions.Evaluation{Truth: conditions.TruthUnknown, Missing: append([]conditions.MissingReference(nil), deduplicated...)}, nil
}

func groupEdges(edges []Edge, direction Direction) map[int64][]Edge {
	grouped := make(map[int64][]Edge)
	for _, edge := range edges {
		nodeID := edge.SourceNodeID
		if direction == DirectionUpstream {
			nodeID = edge.TargetNodeID
		}
		grouped[nodeID] = append(grouped[nodeID], edge)
	}
	return grouped
}

func pathContains(nodes []int64, candidate int64) bool {
	for _, nodeID := range nodes {
		if nodeID == candidate {
			return true
		}
	}
	return false
}

func uniqueFrontierNodes(frontier []traversalState) []int64 {
	seen := make(map[int64]struct{}, len(frontier))
	nodeIDs := make([]int64, 0, len(frontier))
	for _, state := range frontier {
		if _, exists := seen[state.current]; exists {
			continue
		}
		seen[state.current] = struct{}{}
		nodeIDs = append(nodeIDs, state.current)
	}
	return nodeIDs
}
