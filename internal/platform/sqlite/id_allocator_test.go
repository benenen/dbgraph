package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/id"
	dbsqlite "github.com/benenen/dbgraph/internal/platform/sqlite"
)

func TestPersistentAllocatorDoesNotReuseIDsAfterRestartAndClockRollback(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "dbgraph.sqlite")
	rollbackTime := time.Date(2026, time.August, 11, 12, 0, 0, 100_000_000, time.UTC)
	laterTime := rollbackTime.Add(100 * time.Millisecond)
	currentTime := rollbackTime

	firstStore, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	firstAllocator, err := id.NewPersistentAllocator(
		7,
		func() time.Time { return currentTime },
		firstStore,
		1,
	)
	if err != nil {
		t.Fatalf("create first allocator: %v", err)
	}
	oldAtRollbackTime, err := firstAllocator.Next(ctx)
	if err != nil {
		t.Fatalf("allocate first ID: %v", err)
	}
	currentTime = laterTime
	lastBeforeRestart, err := firstAllocator.Next(ctx)
	if err != nil {
		t.Fatalf("allocate second ID: %v", err)
	}
	if oldAtRollbackTime >= lastBeforeRestart {
		t.Fatalf("IDs before restart are not ordered: %d, %d", oldAtRollbackTime, lastBeforeRestart)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	currentTime = rollbackTime
	secondStore, err := dbsqlite.Open(ctx, dbsqlite.Config{Path: databasePath})
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() {
		if err := secondStore.Close(); err != nil {
			t.Errorf("close second store: %v", err)
		}
	})
	secondAllocator, err := id.NewPersistentAllocator(
		7,
		func() time.Time { return currentTime },
		secondStore,
		1,
	)
	if err != nil {
		t.Fatalf("create second allocator: %v", err)
	}
	afterRestart, err := secondAllocator.Next(ctx)
	if err != nil {
		t.Fatalf("allocate after restart: %v", err)
	}
	if afterRestart == oldAtRollbackTime {
		t.Fatalf("allocator replayed prior ID %d after restart", afterRestart)
	}
	if afterRestart <= lastBeforeRestart {
		t.Fatalf("ID after restart = %d, want greater than %d", afterRestart, lastBeforeRestart)
	}
}
