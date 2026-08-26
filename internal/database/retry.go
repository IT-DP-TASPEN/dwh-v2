package database

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

const ReplaySafeMaxAttempts = 3

// RetryReplaySafeTx retries only DB-local, replay-safe transaction work. The
// callback receives a fresh transaction on every attempt. It must not perform
// network/file I/O, start goroutines, notify external systems, or cause any
// other effect which a rollback cannot undo.
func RetryReplaySafeTx(ctx context.Context, db *sqlx.DB, work func(*sqlx.Tx) error) (int, error) {
	for attempt := 1; attempt <= ReplaySafeMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return attempt - 1, err
		}
		tx, err := db.BeginTxx(ctx, nil)
		if err == nil {
			err = work(tx)
			if err == nil {
				err = tx.Commit()
			}
			if err != nil {
				_ = tx.Rollback()
			}
		}
		if err == nil || !IsRetryableConcurrencyError(err) || attempt == ReplaySafeMaxAttempts {
			return attempt, err
		}
		if err := retryBackoff(ctx, attempt); err != nil {
			return attempt, err
		}
	}
	panic("unreachable")
}

// RetryReplaySafeConnTx is RetryReplaySafeTx for a pinned connection (for
// example, while holding a MySQL named lock). The callback is DB-only and gets
// a fresh transaction on every attempt; the same side-effect restrictions
// apply.
func RetryReplaySafeConnTx(ctx context.Context, connection *sql.Conn, work func(*sql.Tx) error) (int, error) {
	for attempt := 1; attempt <= ReplaySafeMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return attempt - 1, err
		}
		tx, err := connection.BeginTx(ctx, nil)
		if err == nil {
			err = work(tx)
			if err == nil {
				err = tx.Commit()
			}
			if err != nil {
				_ = tx.Rollback()
			}
		}
		if err == nil || !IsRetryableConcurrencyError(err) || attempt == ReplaySafeMaxAttempts {
			return attempt, err
		}
		if err := retryBackoff(ctx, attempt); err != nil {
			return attempt, err
		}
	}
	panic("unreachable")
}

// RetryReplaySafeExec retries one replay-safe autocommit SQL statement. Do not
// use it for inserts or effects whose successful outcome cannot be identified
// safely after replay.
func RetryReplaySafeExec(ctx context.Context, db *sqlx.DB, query string, args ...any) (sql.Result, int, error) {
	for attempt := 1; attempt <= ReplaySafeMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, attempt - 1, err
		}
		result, err := db.ExecContext(ctx, query, args...)
		if err == nil || !IsRetryableConcurrencyError(err) || attempt == ReplaySafeMaxAttempts {
			return result, attempt, err
		}
		if err := retryBackoff(ctx, attempt); err != nil {
			return nil, attempt, err
		}
	}
	panic("unreachable")
}

func IsRetryableConcurrencyError(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && (mysqlError.Number == 1205 || mysqlError.Number == 1213)
}

func retryBackoff(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt*25+rand.IntN(26)) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
