package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

var (
	ErrSessionNotFound        = errors.New("session not found")
	ErrSessionIdentityInvalid = errors.New("session identity is invalid")
)

type SessionRepository struct {
	database *sqlx.DB
}

type sessionRow struct {
	ID                 uint64        `db:"id"`
	UserID             uint64        `db:"user_id"`
	ImpersonatedUserID sql.NullInt64 `db:"impersonated_user_id"`
	TokenHash          []byte        `db:"token_hash"`
	RememberMe         bool          `db:"remember_me"`
	ExpiresAt          time.Time     `db:"expires_at"`
	LastSeenAt         time.Time     `db:"last_seen_at"`
	CreatedAt          time.Time     `db:"created_at"`
	UpdatedAt          time.Time     `db:"updated_at"`
}

func NewSessionRepository(database *sqlx.DB) *SessionRepository {
	return &SessionRepository{database: database}
}

func (r *SessionRepository) Create(ctx context.Context, params CreateSessionParams, now time.Time) (Session, error) {
	if params.UserID == 0 {
		return Session{}, fmt.Errorf("user ID must not be zero")
	}
	if params.TokenHash == ([32]byte{}) {
		return Session{}, fmt.Errorf("token hash must not be empty")
	}
	now = now.UTC()
	params.ExpiresAt = params.ExpiresAt.UTC()
	params.LastSeenAt = params.LastSeenAt.UTC()
	if params.LastSeenAt.IsZero() {
		return Session{}, fmt.Errorf("session last seen time must not be zero")
	}
	if !params.ExpiresAt.After(now) {
		return Session{}, fmt.Errorf("session expiry must be after creation time")
	}

	const query = `
		INSERT INTO sessions (user_id, token_hash, remember_me, expires_at, last_seen_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	result, err := r.database.ExecContext(ctx, query,
		params.UserID,
		params.TokenHash[:],
		params.RememberMe,
		params.ExpiresAt,
		params.LastSeenAt,
		now,
		now,
	)
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Session{}, fmt.Errorf("read created session ID: %w", err)
	}
	return Session{
		ID:         uint64(id),
		UserID:     params.UserID,
		TokenHash:  params.TokenHash,
		RememberMe: params.RememberMe,
		ExpiresAt:  params.ExpiresAt,
		LastSeenAt: params.LastSeenAt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

func (r *SessionRepository) FindValidByTokenHash(ctx context.Context, tokenHash [32]byte, now time.Time) (Session, error) {
	return FindValidSession(ctx, r.database, tokenHash, now, false)
}

func (r *SessionRepository) UpdateLastSeenAt(ctx context.Context, id uint64, now time.Time) error {
	const query = `UPDATE sessions SET last_seen_at = ?, updated_at = ? WHERE id = ?`
	if _, err := r.database.ExecContext(ctx, query, now.UTC(), now.UTC(), id); err != nil {
		return fmt.Errorf("update session last seen: %w", err)
	}
	return nil
}

func (r *SessionRepository) Revoke(ctx context.Context, tokenHash [32]byte) error {
	if _, err := r.database.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash[:]); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func (r *SessionRepository) RevokeAllForUser(ctx context.Context, userID uint64) error {
	if _, err := r.database.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}

func (r *SessionRepository) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	result, err := r.database.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted sessions: %w", err)
	}
	return deleted, nil
}

func (row sessionRow) session() (Session, error) {
	if len(row.TokenHash) != 32 {
		return Session{}, fmt.Errorf("find valid session: token hash has length %d", len(row.TokenHash))
	}
	var tokenHash [32]byte
	copy(tokenHash[:], row.TokenHash)
	return Session{
		ID:                 row.ID,
		UserID:             row.UserID,
		ImpersonatedUserID: nullableUserID(row.ImpersonatedUserID),
		TokenHash:          tokenHash,
		RememberMe:         row.RememberMe,
		ExpiresAt:          row.ExpiresAt,
		LastSeenAt:         row.LastSeenAt,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func FindValidSession(ctx context.Context, database sqlx.ExtContext, tokenHash [32]byte, now time.Time, lock bool) (Session, error) {
	query := `
		SELECT id, user_id, impersonated_user_id, token_hash, remember_me, expires_at, last_seen_at, created_at, updated_at
		FROM sessions
		WHERE token_hash = ? AND expires_at > ?`
	if lock {
		query += ` FOR UPDATE`
	}
	var row sessionRow
	if err := sqlx.GetContext(ctx, database, &row, query, tokenHash[:], now.UTC()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrSessionNotFound
		}
		return Session{}, fmt.Errorf("find valid session: %w", err)
	}
	return row.session()
}

func nullableUserID(value sql.NullInt64) *uint64 {
	if !value.Valid {
		return nil
	}
	id := uint64(value.Int64)
	return &id
}
