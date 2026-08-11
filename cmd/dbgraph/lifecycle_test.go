package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestRunServeLifecycleCancelsWorkerAfterHTTPFailure(t *testing.T) {
	t.Parallel()

	httpFailure := errors.New("accept failed")
	workerStopped := make(chan struct{})
	shutdownCalled := make(chan struct{}, 1)
	err := runServeLifecycle(
		context.Background(),
		func() error { return httpFailure },
		func(context.Context) error {
			shutdownCalled <- struct{}{}
			return nil
		},
		func() error { return nil },
		func(ctx context.Context) error {
			<-ctx.Done()
			close(workerStopped)
			return nil
		},
	)
	if !errors.Is(err, httpFailure) {
		t.Fatalf("lifecycle error = %v, want HTTP failure", err)
	}
	select {
	case <-workerStopped:
	default:
		t.Fatal("lifecycle returned before worker stopped")
	}
	select {
	case <-shutdownCalled:
	default:
		t.Fatal("HTTP shutdown was not called")
	}
}

func TestRunServeLifecycleShutsDownHTTPAndWaitsAfterWorkerFailure(t *testing.T) {
	t.Parallel()

	workerFailure := errors.New("worker failed")
	serverStopped := make(chan struct{})
	var stopOnce sync.Once
	err := runServeLifecycle(
		context.Background(),
		func() error {
			<-serverStopped
			return http.ErrServerClosed
		},
		func(context.Context) error {
			stopOnce.Do(func() { close(serverStopped) })
			return nil
		},
		func() error {
			stopOnce.Do(func() { close(serverStopped) })
			return nil
		},
		func(context.Context) error { return workerFailure },
	)
	if !errors.Is(err, workerFailure) {
		t.Fatalf("lifecycle error = %v, want worker failure", err)
	}
	select {
	case <-serverStopped:
	default:
		t.Fatal("lifecycle returned before HTTP server stopped")
	}
}

func TestRunServeLifecycleStopsCleanlyWhenParentIsCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	serverStopped := make(chan struct{})
	var stopOnce sync.Once
	workerStopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runServeLifecycle(
			ctx,
			func() error {
				<-serverStopped
				return http.ErrServerClosed
			},
			func(context.Context) error {
				stopOnce.Do(func() { close(serverStopped) })
				return nil
			},
			func() error {
				stopOnce.Do(func() { close(serverStopped) })
				return nil
			},
			func(workerContext context.Context) error {
				<-workerContext.Done()
				close(workerStopped)
				return nil
			},
		)
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful lifecycle error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not stop after cancellation")
	}
	select {
	case <-workerStopped:
	default:
		t.Fatal("lifecycle returned before cancelled worker stopped")
	}
}
