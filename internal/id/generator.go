package id

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	nodeBits               = 10
	sequenceBits           = 12
	maxNode                = (1 << nodeBits) - 1
	maxSequence            = (1 << sequenceBits) - 1
	maxTimestamp           = (1 << 41) - 1
	nodeShift              = sequenceBits
	timestampShift         = nodeBits + sequenceBits
	MaximumReservationSize = 1_000_000
)

var (
	ErrInvalidNode        = errors.New("invalid Snowflake node")
	ErrClockBeforeEpoch   = errors.New("clock is before Snowflake epoch")
	ErrClockMovedBackward = errors.New("clock moved backward")
	ErrSequenceExhausted  = errors.New("snowflake sequence exhausted")
	ErrInvalidReservation = errors.New("invalid Snowflake reservation")
	ErrTimestampExhausted = errors.New("snowflake timestamp exhausted")
)

var epoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

type Clock func() time.Time

type Reservation struct {
	FirstID int64
	LastID  int64
	Count   int
}

type StateStore interface {
	ReserveIDRange(
		context.Context,
		uint16,
		int64,
		int,
	) (Reservation, error)
}

type Generator struct {
	mu            sync.Mutex
	node          int64
	now           Clock
	lastTimestamp int64
	sequence      int64
}

func NewGenerator(node uint16, now Clock) (*Generator, error) {
	if err := validateNode(node); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Generator{
		node:          int64(node),
		now:           now,
		lastTimestamp: -1,
	}, nil
}

func (g *Generator) Next(ctx context.Context) (int64, error) {
	if ctx == nil {
		return 0, errors.New("ID allocation context is required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	timestamp, err := timestampFor(g.now())
	if err != nil {
		return 0, err
	}
	if timestamp > g.lastTimestamp {
		g.lastTimestamp = timestamp
		g.sequence = 0
		return compose(g.lastTimestamp, g.node, g.sequence), nil
	}
	if g.sequence < maxSequence {
		g.sequence++
		return compose(g.lastTimestamp, g.node, g.sequence), nil
	}
	if g.lastTimestamp == maxTimestamp {
		return 0, ErrTimestampExhausted
	}
	g.lastTimestamp++
	g.sequence = 0
	return compose(g.lastTimestamp, g.node, g.sequence), nil
}

type PersistentAllocator struct {
	mu        sync.Mutex
	node      uint16
	now       Clock
	state     StateStore
	leaseSize int
	nextID    int64
	remaining int
}

func NewPersistentAllocator(
	node uint16,
	now Clock,
	state StateStore,
	leaseSize int,
) (*PersistentAllocator, error) {
	if err := validateNode(node); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, errors.New("persistent ID state store is required")
	}
	if leaseSize < 1 || leaseSize > MaximumReservationSize {
		return nil, ErrInvalidReservation
	}
	if now == nil {
		now = time.Now
	}
	return &PersistentAllocator{
		node:      node,
		now:       now,
		state:     state,
		leaseSize: leaseSize,
	}, nil
}

func (a *PersistentAllocator) Next(ctx context.Context) (int64, error) {
	if ctx == nil {
		return 0, errors.New("ID allocation context is required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.remaining == 0 {
		if err := a.reserve(ctx, a.leaseSize); err != nil {
			return 0, err
		}
	}
	allocated := a.nextID
	a.remaining--
	if a.remaining > 0 {
		next, err := successor(a.nextID, a.node)
		if err != nil {
			return 0, err
		}
		a.nextID = next
	}
	return allocated, nil
}

func (a *PersistentAllocator) Ensure(ctx context.Context, count int) error {
	if ctx == nil {
		return errors.New("ID allocation context is required")
	}
	if count < 1 || count > MaximumReservationSize {
		return ErrInvalidReservation
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.remaining >= count {
		return nil
	}
	reservationSize := max(count, a.leaseSize)
	return a.reserve(ctx, reservationSize)
}

func (a *PersistentAllocator) reserve(ctx context.Context, count int) error {
	timestamp, err := timestampFor(a.now())
	if err != nil {
		return err
	}
	reservation, err := a.state.ReserveIDRange(ctx, a.node, timestamp, count)
	if err != nil {
		return fmt.Errorf("reserve persistent ID range: %w", err)
	}
	if reservation.Count != count || reservation.FirstID <= 0 || reservation.LastID < reservation.FirstID {
		return ErrInvalidReservation
	}
	a.nextID = reservation.FirstID
	a.remaining = reservation.Count
	return nil
}

func ReserveRange(
	node uint16,
	observedTimestamp int64,
	highWaterID int64,
	count int,
) (Reservation, error) {
	if err := validateNode(node); err != nil {
		return Reservation{}, err
	}
	if observedTimestamp < 0 {
		return Reservation{}, ErrClockBeforeEpoch
	}
	if observedTimestamp > maxTimestamp {
		return Reservation{}, ErrTimestampExhausted
	}
	if count < 1 || count > MaximumReservationSize {
		return Reservation{}, ErrInvalidReservation
	}

	firstID := compose(observedTimestamp, int64(node), 0)
	if highWaterID > 0 && firstID <= highWaterID {
		var err error
		firstID, err = successor(highWaterID, node)
		if err != nil {
			return Reservation{}, err
		}
	}
	lastID := firstID
	for step := 1; step < count; step++ {
		var err error
		lastID, err = successor(lastID, node)
		if err != nil {
			return Reservation{}, err
		}
	}
	return Reservation{FirstID: firstID, LastID: lastID, Count: count}, nil
}

func successor(currentID int64, node uint16) (int64, error) {
	if currentID <= 0 {
		return 0, ErrInvalidReservation
	}
	timestamp := currentID >> timestampShift
	encodedNode := (currentID >> nodeShift) & maxNode
	if encodedNode != int64(node) {
		return 0, ErrInvalidReservation
	}
	sequence := currentID & maxSequence
	if sequence < maxSequence {
		return compose(timestamp, encodedNode, sequence+1), nil
	}
	if timestamp >= maxTimestamp {
		return 0, ErrTimestampExhausted
	}
	return compose(timestamp+1, encodedNode, 0), nil
}

func timestampFor(value time.Time) (int64, error) {
	timestamp := value.UTC().UnixMilli() - epoch.UnixMilli()
	if timestamp < 0 {
		return 0, ErrClockBeforeEpoch
	}
	if timestamp > maxTimestamp {
		return 0, ErrTimestampExhausted
	}
	return timestamp, nil
}

func validateNode(node uint16) error {
	if node > maxNode {
		return fmt.Errorf("%w: %d exceeds %d", ErrInvalidNode, node, maxNode)
	}
	return nil
}

func compose(timestamp int64, node int64, sequence int64) int64 {
	return (timestamp << timestampShift) | (node << nodeShift) | sequence
}
