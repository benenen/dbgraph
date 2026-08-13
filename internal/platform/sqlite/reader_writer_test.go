package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/benenen/dbgraph/internal/catalog"
)

func TestCommittedCatalogReadsContinueWhileWriterTransactionIsOpen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "dbgraph.sqlite")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	timestamp := time.Date(2026, time.August, 11, 15, 0, 0, 0, time.UTC)
	repository := NewCatalogRepository(store, nil)
	source := catalog.DataSource{
		ID:             42,
		Name:           "concurrent readers",
		Kind:           catalog.DataSourceMySQL,
		DSNEnvironment: "READER_WRITER_DSN",
		CreatedAt:      timestamp,
		UpdatedAt:      timestamp,
	}
	if err := repository.CreateDataSource(ctx, source); err != nil {
		t.Fatalf("seed data source: %v", err)
	}

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	writerFinished := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWriter) }) }
	defer func() {
		release()
		<-writerFinished
	}()
	go func() {
		err := store.write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `
UPDATE data_sources SET name = name WHERE id = ?
`, 0); err != nil {
				return err
			}
			close(writerEntered)
			<-releaseWriter
			return nil
		})
		writerDone <- err
		close(writerFinished)
	}()
	select {
	case <-writerEntered:
	case err := <-writerDone:
		t.Fatalf("writer stopped before holding its transaction: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not enter transaction")
	}

	readCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	got, err := repository.GetDataSource(readCtx, source.ID)
	if err != nil {
		t.Fatalf("read committed catalog data while writer is open: %v", err)
	}
	if got.ID != source.ID || got.Name != source.Name {
		t.Fatalf("data source = %+v, want committed data source %+v", got, source)
	}

	release()
	if err := <-writerDone; err != nil {
		t.Fatalf("finish writer: %v", err)
	}
}
