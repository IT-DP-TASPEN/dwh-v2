package impersonation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
)

type Repository struct {
	database    *sqlx.DB
	appendAudit audit.AppendFunc
}

type sessionIdentity struct {
	ID       uint64 `db:"id"`
	IsActive bool   `db:"is_active"`
	RoleSlug string `db:"role_slug"`
}

func NewRepository(database *sqlx.DB, appendAudit audit.AppendFunc) *Repository {
	if appendAudit == nil {
		appendAudit = audit.Append
	}
	return &Repository{database: database, appendAudit: appendAudit}
}

func (repository *Repository) Start(ctx context.Context, tokenHash [32]byte, targetUserID uint64, attribution audit.Attribution, now time.Time, nextTokenHash func() ([32]byte, error)) (auth.Session, error) {
	transaction, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return auth.Session{}, fmt.Errorf("begin impersonation: %w", err)
	}
	defer transaction.Rollback()

	session, err := auth.FindValidSession(ctx, transaction, tokenHash, now, true)
	if err != nil {
		return auth.Session{}, err
	}
	if session.ImpersonatedUserID != nil {
		return auth.Session{}, ErrAlreadyActive
	}
	actor, err := lockIdentity(ctx, transaction, session.UserID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!actor.IsActive || !access.IsAdminRole(actor.RoleSlug)) {
		return auth.Session{}, revokeInvalidSession(ctx, transaction, session.ID)
	}
	if err != nil {
		return auth.Session{}, fmt.Errorf("lock impersonation actor: %w", err)
	}
	if targetUserID == actor.ID {
		return auth.Session{}, ErrSelf
	}
	target, err := lockIdentity(ctx, transaction, targetUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Session{}, ErrTargetNotFound
	}
	if err != nil {
		return auth.Session{}, fmt.Errorf("lock impersonation target: %w", err)
	}
	if !target.IsActive {
		return auth.Session{}, ErrTargetInactive
	}
	if access.IsAdminRole(target.RoleSlug) {
		return auth.Session{}, ErrTargetAdmin
	}
	if !matchesAttribution(attribution, actor.ID, target.ID) {
		return auth.Session{}, fmt.Errorf("impersonation audit attribution does not match locked identities")
	}

	newTokenHash, err := nextTokenHash()
	if err != nil {
		return auth.Session{}, err
	}
	if newTokenHash == ([32]byte{}) {
		return auth.Session{}, fmt.Errorf("new session token hash must not be empty")
	}
	now = now.UTC()
	if _, err := transaction.ExecContext(ctx, `UPDATE sessions SET token_hash = ?, impersonated_user_id = ?, updated_at = ? WHERE id = ?`, newTokenHash[:], targetUserID, now, session.ID); err != nil {
		return auth.Session{}, fmt.Errorf("start impersonation: %w", err)
	}
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: attribution,
		Action:      audit.ActionImpersonationStarted, Resource: audit.ResourceUser, ResourceID: target.ID,
		Metadata: audit.ImpersonationStartedMetadata{TargetRole: target.RoleSlug}, CreatedAt: now,
	}); err != nil {
		return auth.Session{}, fmt.Errorf("audit impersonation start: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return auth.Session{}, fmt.Errorf("commit impersonation start: %w", err)
	}
	session.TokenHash = newTokenHash
	session.ImpersonatedUserID = &targetUserID
	session.UpdatedAt = now
	return session, nil
}

func (repository *Repository) Stop(ctx context.Context, tokenHash [32]byte, attribution audit.Attribution, now time.Time, nextTokenHash func() ([32]byte, error)) (auth.Session, error) {
	transaction, err := repository.database.BeginTxx(ctx, nil)
	if err != nil {
		return auth.Session{}, fmt.Errorf("begin stop impersonation: %w", err)
	}
	defer transaction.Rollback()

	session, err := auth.FindValidSession(ctx, transaction, tokenHash, now, true)
	if err != nil {
		return auth.Session{}, err
	}
	if session.ImpersonatedUserID == nil {
		return auth.Session{}, ErrNotActive
	}
	actor, err := lockIdentity(ctx, transaction, session.UserID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!actor.IsActive || !access.IsAdminRole(actor.RoleSlug)) {
		return auth.Session{}, revokeInvalidSession(ctx, transaction, session.ID)
	}
	if err != nil {
		return auth.Session{}, fmt.Errorf("lock stop impersonation actor: %w", err)
	}
	target, err := lockIdentity(ctx, transaction, *session.ImpersonatedUserID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!target.IsActive || access.IsAdminRole(target.RoleSlug)) {
		return auth.Session{}, revokeInvalidSession(ctx, transaction, session.ID)
	}
	if err != nil {
		return auth.Session{}, fmt.Errorf("lock stop impersonation target: %w", err)
	}
	if !matchesAttribution(attribution, actor.ID, target.ID) {
		return auth.Session{}, fmt.Errorf("stop impersonation audit attribution does not match locked identities")
	}

	newTokenHash, err := nextTokenHash()
	if err != nil {
		return auth.Session{}, err
	}
	if newTokenHash == ([32]byte{}) {
		return auth.Session{}, fmt.Errorf("new session token hash must not be empty")
	}
	now = now.UTC()
	if _, err := transaction.ExecContext(ctx, `UPDATE sessions SET token_hash = ?, impersonated_user_id = NULL, updated_at = ? WHERE id = ?`, newTokenHash[:], now, session.ID); err != nil {
		return auth.Session{}, fmt.Errorf("stop impersonation: %w", err)
	}
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: attribution,
		Action:      audit.ActionImpersonationStopped, Resource: audit.ResourceUser, ResourceID: target.ID, CreatedAt: now,
	}); err != nil {
		return auth.Session{}, fmt.Errorf("audit impersonation stop: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return auth.Session{}, fmt.Errorf("commit impersonation stop: %w", err)
	}
	session.TokenHash = newTokenHash
	session.ImpersonatedUserID = nil
	session.UpdatedAt = now
	return session, nil
}

func lockIdentity(ctx context.Context, transaction *sqlx.Tx, userID uint64) (sessionIdentity, error) {
	const query = `
		SELECT u.id, u.is_active, r.slug AS role_slug
		FROM users u
		JOIN roles r ON r.id = u.role_id
		WHERE u.id = ?
		FOR UPDATE`
	var identity sessionIdentity
	if err := transaction.GetContext(ctx, &identity, query, userID); err != nil {
		return sessionIdentity{}, err
	}
	return identity, nil
}

func revokeInvalidSession(ctx context.Context, transaction *sqlx.Tx, sessionID uint64) error {
	if _, err := transaction.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		return fmt.Errorf("revoke invalid impersonation session: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit invalid impersonation revocation: %w", err)
	}
	return auth.ErrSessionIdentityInvalid
}

func matchesAttribution(attribution audit.Attribution, actorID, effectiveID uint64) bool {
	return attribution.Actor != nil && attribution.Effective != nil && attribution.Actor.UserID == actorID && attribution.Effective.UserID == effectiveID
}
