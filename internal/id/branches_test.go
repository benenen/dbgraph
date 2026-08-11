package id

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stateStoreFunc func(context.Context, uint16, int64, int) (Reservation, error)

func (function stateStoreFunc) ReserveIDRange(
	ctx context.Context,
	node uint16,
	timestamp int64,
	count int,
) (Reservation, error) {
	return function(ctx, node, timestamp, count)
}

func TestGeneratorValidatesInputsAndHandlesLogicalClock(t *testing.T) {
	t.Parallel()

	if _, err := NewGenerator(maxNode+1, nil); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("invalid node error = %v", err)
	}

	beforeEpoch, err := NewGenerator(1, func() time.Time { return epoch.Add(-time.Millisecond) })
	if err != nil {
		t.Fatalf("NewGenerator before epoch: %v", err)
	}
	if _, err := beforeEpoch.Next(context.Background()); !errors.Is(err, ErrClockBeforeEpoch) {
		t.Fatalf("before epoch error = %v", err)
	}

	afterRange, err := NewGenerator(1, func() time.Time { return epoch.Add((maxTimestamp + 1) * time.Millisecond) })
	if err != nil {
		t.Fatalf("NewGenerator after range: %v", err)
	}
	if _, err := afterRange.Next(context.Background()); !errors.Is(err, ErrTimestampExhausted) {
		t.Fatalf("after range error = %v", err)
	}

	generator, err := NewGenerator(2, func() time.Time { return epoch.Add(time.Millisecond) })
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}
	//lint:ignore SA1012 This assertion deliberately exercises the nil-context boundary.
	if _, err := generator.Next(nil); err == nil { //nolint:staticcheck // Intentionally exercises the nil-context boundary.
		t.Fatal("Next(nil) returned nil")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := generator.Next(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}

	var last int64
	for index := 0; index <= maxSequence+1; index++ {
		value, nextErr := generator.Next(context.Background())
		if nextErr != nil {
			t.Fatalf("Next %d: %v", index, nextErr)
		}
		if index > 0 && value <= last {
			t.Fatalf("ID %d = %d, want greater than %d", index, value, last)
		}
		last = value
	}

	exhausted, err := NewGenerator(3, func() time.Time { return epoch.Add(maxTimestamp * time.Millisecond) })
	if err != nil {
		t.Fatalf("NewGenerator exhausted: %v", err)
	}
	exhausted.lastTimestamp = maxTimestamp
	exhausted.sequence = maxSequence
	if _, err := exhausted.Next(context.Background()); !errors.Is(err, ErrTimestampExhausted) {
		t.Fatalf("sequence exhausted timestamp error = %v", err)
	}
}

func TestPersistentAllocatorConstructorAndContextValidation(t *testing.T) {
	t.Parallel()

	validState := stateStoreFunc(func(context.Context, uint16, int64, int) (Reservation, error) {
		return Reservation{FirstID: 1, LastID: 1, Count: 1}, nil
	})
	for name, constructor := range map[string]func() error{
		"node":        func() error { _, err := NewPersistentAllocator(maxNode+1, nil, validState, 1); return err },
		"state":       func() error { _, err := NewPersistentAllocator(1, nil, nil, 1); return err },
		"small lease": func() error { _, err := NewPersistentAllocator(1, nil, validState, 0); return err },
		"large lease": func() error {
			_, err := NewPersistentAllocator(1, nil, validState, MaximumReservationSize+1)
			return err
		},
	} {
		name, constructor := name, constructor
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := constructor(); err == nil {
				t.Fatal("constructor returned nil")
			}
		})
	}

	allocator, err := NewPersistentAllocator(1, func() time.Time { return epoch.Add(time.Millisecond) }, validState, 1)
	if err != nil {
		t.Fatalf("NewPersistentAllocator: %v", err)
	}
	//lint:ignore SA1012 This assertion deliberately exercises the nil-context boundary.
	if _, err := allocator.Next(nil); err == nil { //nolint:staticcheck // Intentionally exercises the nil-context boundary.
		t.Fatal("Next(nil) returned nil")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := allocator.Next(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next(cancelled) = %v", err)
	}
	//lint:ignore SA1012 This assertion deliberately exercises the nil-context boundary.
	if err := allocator.Ensure(nil, 1); err == nil { //nolint:staticcheck // Intentionally exercises the nil-context boundary.
		t.Fatal("Ensure(nil) returned nil")
	}
	if err := allocator.Ensure(context.Background(), 0); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("Ensure zero = %v", err)
	}
	if err := allocator.Ensure(cancelled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure(cancelled) = %v", err)
	}
}

func TestPersistentAllocatorLeasesAndValidatesStateResponse(t *testing.T) {
	t.Parallel()

	observed := int64(5)
	reservations := 0
	state := stateStoreFunc(func(_ context.Context, node uint16, timestamp int64, count int) (Reservation, error) {
		reservations++
		if node != 4 || timestamp != observed || count < 2 {
			t.Fatalf("reservation request = node %d timestamp %d count %d", node, timestamp, count)
		}
		return ReserveRange(node, timestamp, 0, count)
	})
	allocator, err := NewPersistentAllocator(4, func() time.Time { return epoch.Add(time.Duration(observed) * time.Millisecond) }, state, 2)
	if err != nil {
		t.Fatalf("NewPersistentAllocator: %v", err)
	}
	if err := allocator.Ensure(context.Background(), 1); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if reservations != 1 {
		t.Fatalf("reservations = %d, want 1", reservations)
	}
	if err := allocator.Ensure(context.Background(), 2); err != nil {
		t.Fatalf("Ensure cached: %v", err)
	}
	first, err := allocator.Next(context.Background())
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	second, err := allocator.Next(context.Background())
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if second != first+1 || reservations != 1 {
		t.Fatalf("IDs = %d,%d reservations=%d", first, second, reservations)
	}
	if err := allocator.Ensure(context.Background(), 3); err != nil {
		t.Fatalf("Ensure larger than lease: %v", err)
	}
	if reservations != 2 || allocator.remaining != 3 {
		t.Fatalf("reservations=%d remaining=%d", reservations, allocator.remaining)
	}

	stateFailure := errors.New("state unavailable")
	for name, state := range map[string]StateStore{
		"error": stateStoreFunc(func(context.Context, uint16, int64, int) (Reservation, error) {
			return Reservation{}, stateFailure
		}),
		"wrong count": stateStoreFunc(func(context.Context, uint16, int64, int) (Reservation, error) {
			return Reservation{FirstID: 1, LastID: 1, Count: 2}, nil
		}),
		"invalid first": stateStoreFunc(func(context.Context, uint16, int64, int) (Reservation, error) {
			return Reservation{FirstID: 0, LastID: 1, Count: 1}, nil
		}),
		"invalid order": stateStoreFunc(func(context.Context, uint16, int64, int) (Reservation, error) {
			return Reservation{FirstID: 2, LastID: 1, Count: 1}, nil
		}),
	} {
		name, state := name, state
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate, createErr := NewPersistentAllocator(1, func() time.Time { return epoch.Add(time.Millisecond) }, state, 1)
			if createErr != nil {
				t.Fatalf("NewPersistentAllocator: %v", createErr)
			}
			if _, nextErr := candidate.Next(context.Background()); nextErr == nil {
				t.Fatal("Next returned nil")
			}
		})
	}
}

func TestReserveRangeCoversValidationRolloverAndWatermarks(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]func() error{
		"node":                func() error { _, err := ReserveRange(maxNode+1, 0, 0, 1); return err },
		"negative timestamp":  func() error { _, err := ReserveRange(1, -1, 0, 1); return err },
		"timestamp exhausted": func() error { _, err := ReserveRange(1, maxTimestamp+1, 0, 1); return err },
		"count":               func() error { _, err := ReserveRange(1, 0, 0, 0); return err },
	} {
		name, call := name, call
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := call(); err == nil {
				t.Fatal("ReserveRange returned nil")
			}
		})
	}

	highWater := compose(10, 6, maxSequence)
	reservation, err := ReserveRange(6, 1, highWater, 2)
	if err != nil {
		t.Fatalf("ReserveRange watermark: %v", err)
	}
	if reservation.FirstID <= highWater || reservation.LastID <= reservation.FirstID || reservation.Count != 2 {
		t.Fatalf("reservation = %#v", reservation)
	}

	if _, err := successor(0, 1); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("successor zero = %v", err)
	}
	if _, err := successor(compose(1, 2, 0), 1); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("successor node mismatch = %v", err)
	}
	if _, err := successor(compose(maxTimestamp, 1, maxSequence), 1); !errors.Is(err, ErrTimestampExhausted) {
		t.Fatalf("successor exhausted = %v", err)
	}
}
