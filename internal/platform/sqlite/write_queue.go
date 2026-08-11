package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	ErrStoreClosed    = errors.New("SQLite store is closed")
	ErrWriteQueueFull = errors.New("SQLite write queue is full")
)

type writeOperation func(*sql.Tx) error

type writeRequest struct {
	ctx       context.Context
	operation writeOperation
	result    chan error
}

func (s *Store) startWriteWorker(capacity int) {
	maximumOutstanding := capacity + 1
	s.writeQueue = make(chan writeRequest, maximumOutstanding)
	s.writeSlots = make(chan struct{}, maximumOutstanding)
	s.writeStop = make(chan struct{})
	s.writeDone = make(chan struct{})
	go s.runWriteWorker()
}

func (s *Store) write(ctx context.Context, operation writeOperation) error {
	if ctx == nil {
		return errors.New("SQLite write context is required")
	}
	if operation == nil {
		return errors.New("SQLite write operation is required")
	}
	request := writeRequest{
		ctx:       ctx,
		operation: operation,
		result:    make(chan error, 1),
	}

	s.writeMu.Lock()
	if s.closed {
		s.writeMu.Unlock()
		return ErrStoreClosed
	}
	select {
	case s.writeSlots <- struct{}{}:
	default:
		s.writeMu.Unlock()
		return ErrWriteQueueFull
	}
	s.writeQueue <- request
	s.writeMu.Unlock()

	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) runWriteWorker() {
	defer close(s.writeDone)
	for {
		select {
		case request := <-s.writeQueue:
			request.result <- s.executeWrite(request)
			<-s.writeSlots
		case <-s.writeStop:
			s.rejectQueuedWrites()
			return
		}
	}
}

func (s *Store) executeWrite(request writeRequest) error {
	if err := request.ctx.Err(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(request.ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQLite write: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := request.operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite write: %w", err)
	}
	return nil
}

func (s *Store) rejectQueuedWrites() {
	for {
		select {
		case request := <-s.writeQueue:
			request.result <- ErrStoreClosed
			<-s.writeSlots
		default:
			return
		}
	}
}

func (s *Store) stopWriteWorker() {
	s.writeMu.Lock()
	if s.closed {
		s.writeMu.Unlock()
		return
	}
	s.closed = true
	if s.writeStop == nil {
		s.writeMu.Unlock()
		return
	}
	close(s.writeStop)
	writeDone := s.writeDone
	s.writeMu.Unlock()
	<-writeDone
}
