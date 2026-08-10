package auth

import (
	"context"
	"log/slog"
	"time"
)

type expiredSessionDeleter interface {
	DeleteExpired(context.Context, time.Time) (int64, error)
}

func RunSessionCleanup(ctx context.Context, sessions expiredSessionDeleter, interval time.Duration, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	runSessionCleanup(ctx, sessions, time.Now().UTC(), ticker.C, logger)
}

func runSessionCleanup(ctx context.Context, sessions expiredSessionDeleter, initial time.Time, ticks <-chan time.Time, logger *slog.Logger) {
	deleteExpiredSessions(ctx, sessions, initial, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			deleteExpiredSessions(ctx, sessions, now, logger)
		}
	}
}

func deleteExpiredSessions(ctx context.Context, sessions expiredSessionDeleter, now time.Time, logger *slog.Logger) {
	deleted, err := sessions.DeleteExpired(ctx, now.UTC())
	if err != nil {
		logger.WarnContext(ctx, "delete expired sessions", "error", err)
		return
	}
	if deleted != 0 {
		logger.InfoContext(ctx, "expired sessions deleted", "count", deleted)
	}
}
