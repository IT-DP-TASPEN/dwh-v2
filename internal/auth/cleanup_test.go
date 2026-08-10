package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type cleanupStore struct {
	mu    sync.Mutex
	times []time.Time
	err   error
	calls chan struct{}
}

func (store *cleanupStore) DeleteExpired(_ context.Context, now time.Time) (int64, error) {
	store.mu.Lock()
	store.times = append(store.times, now)
	err := store.err
	store.err = nil
	store.mu.Unlock()
	store.calls <- struct{}{}
	return 2, err
}

func TestSessionCleanupRunsImmediatelyContinuesAfterFailureAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	store := &cleanupStore{err: errors.New("database unavailable"), calls: make(chan struct{}, 2)}
	done := make(chan struct{})
	initial := time.Date(2026, 8, 9, 10, 0, 0, 0, time.FixedZone("local", 7*60*60))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() {
		runSessionCleanup(ctx, store, initial, ticks, logger)
		close(done)
	}()

	<-store.calls
	next := initial.Add(time.Hour)
	ticks <- next
	<-store.calls
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup loop did not stop after cancellation")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.times) != 2 || !store.times[0].Equal(initial.UTC()) || !store.times[1].Equal(next.UTC()) {
		t.Fatalf("unexpected cleanup times: %v", store.times)
	}
}
