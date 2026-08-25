package reportexport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/reporting"
)

func TestHeartbeatCancelsAttemptWhenFencedUpdateProvesClaimLoss(t *testing.T) {
	worker := heartbeatTestWorker(func(context.Context, uint64, string, uint32, time.Time) (bool, error) { return false, nil })
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go worker.heartbeat(ctx, cancel, Job{ID: 7, Attempt: 2}, done)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop")
	}
	if !errors.Is(context.Cause(ctx), reporting.ErrClaimLost) {
		t.Fatalf("cause=%v", context.Cause(ctx))
	}
}

func TestHeartbeatBoundsWorkWhenOwnershipCannotBeProven(t *testing.T) {
	worker := heartbeatTestWorker(func(context.Context, uint64, string, uint32, time.Time) (bool, error) {
		return false, errors.New("transient database failure")
	})
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	started := time.Now()
	go worker.heartbeat(ctx, cancel, Job{ID: 7, Attempt: 2}, done)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unproven lease continued indefinitely")
	}
	if !errors.Is(context.Cause(ctx), reporting.ErrLeaseUnproven) {
		t.Fatalf("cause=%v", context.Cause(ctx))
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("lease cancellation took %s", elapsed)
	}
}

func heartbeatTestWorker(heartbeat func(context.Context, uint64, string, uint32, time.Time) (bool, error)) *Worker {
	return &Worker{owner: "owner", config: WorkerConfig{HeartbeatInterval: 5 * time.Millisecond, StaleAfter: 25 * time.Millisecond}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), heartbeatFunc: heartbeat}
}
