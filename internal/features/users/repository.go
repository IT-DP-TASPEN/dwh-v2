package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/user"
)

const userColumns = `
	u.id, u.username, u.name, u.role_id,
	r.name AS role_name, r.slug AS role_slug, r.is_system AS role_is_system,
	u.is_active, u.last_login_at, u.created_at, u.updated_at`

type Repository struct {
	database    *sqlx.DB
	appendAudit audit.AppendFunc
	beginTx     func(context.Context, *sql.TxOptions) (*sqlx.Tx, error)
}

func NewRepository(database *sqlx.DB, appendAudit audit.AppendFunc) *Repository {
	if appendAudit == nil {
		appendAudit = audit.Append
	}
	return &Repository{database: database, appendAudit: appendAudit, beginTx: database.BeginTxx}
}

func (repository *Repository) CountUsers(ctx context.Context, query string) (int64, error) {
	statement := `SELECT COUNT(*) FROM users u`
	arguments := []any(nil)
	if query != "" {
		username, name := searchPatterns(query)
		statement += ` WHERE u.username LIKE ? ESCAPE '!' OR u.name LIKE ? ESCAPE '!'`
		arguments = []any{username, name}
	}
	var total int64
	if err := repository.database.GetContext(ctx, &total, statement, arguments...); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return total, nil
}

func (repository *Repository) ListUsers(ctx context.Context, query string, limit, offset int) ([]UserRecord, error) {
	statement := `SELECT ` + userColumns + ` FROM users u JOIN roles r ON r.id = u.role_id`
	arguments := []any(nil)
	if query != "" {
		username, name := searchPatterns(query)
		statement += ` WHERE u.username LIKE ? ESCAPE '!' OR u.name LIKE ? ESCAPE '!'`
		arguments = append(arguments, username, name)
	}
	statement += ` ORDER BY u.username ASC, u.id ASC LIMIT ? OFFSET ?`
	arguments = append(arguments, limit, offset)
	users := make([]UserRecord, 0)
	if err := repository.database.SelectContext(ctx, &users, statement, arguments...); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

func searchPatterns(query string) (string, string) {
	return "%" + escapeLike(user.NormalizeUsername(query)) + "%", "%" + escapeLike(strings.TrimSpace(query)) + "%"
}

func (repository *Repository) FindUserByID(ctx context.Context, id uint64) (UserRecord, error) {
	return findUser(ctx, repository.database, id, false)
}

func (repository *Repository) CreateUser(ctx context.Context, requester securityctx.Requester, params user.CreateParams, now time.Time) (UserRecord, error) {
	transaction, err := repository.beginTx(ctx, nil)
	if err != nil {
		return UserRecord{}, fmt.Errorf("begin user creation: %w", err)
	}
	defer transaction.Rollback()

	const statement = `
		INSERT INTO users (username, name, password_hash, role_id, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	result, err := transaction.ExecContext(ctx, statement, params.Username, params.Name, params.PasswordHash, params.RoleID, params.IsActive, now, now)
	if err != nil {
		if isMySQLError(err, 1062) {
			return UserRecord{}, user.ErrUsernameTaken
		}
		return UserRecord{}, fmt.Errorf("insert user: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return UserRecord{}, fmt.Errorf("read inserted user ID: %w", err)
	}
	created, err := findUser(ctx, transaction, uint64(id), false)
	if err != nil {
		return UserRecord{}, err
	}
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: auditAttribution(requester), Action: audit.ActionUserCreated,
		Resource: audit.ResourceUser, ResourceID: created.ID, CreatedAt: now,
	}); err != nil {
		return UserRecord{}, err
	}
	if err := transaction.Commit(); err != nil {
		return UserRecord{}, fmt.Errorf("commit user creation: %w", err)
	}
	return created, nil
}

func (repository *Repository) UpdateUserProfile(ctx context.Context, requester securityctx.Requester, id uint64, username, name string, now time.Time) (UserRecord, error) {
	transaction, err := repository.beginTx(ctx, nil)
	if err != nil {
		return UserRecord{}, fmt.Errorf("begin profile update: %w", err)
	}
	defer transaction.Rollback()
	target, err := findUser(ctx, transaction, id, true)
	if err != nil {
		return UserRecord{}, err
	}
	if access.IsAdminRole(target.RoleSlug) && !requester.IsEffectiveAdmin() {
		return UserRecord{}, ErrAdminMutation
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE users SET username = ?, name = ?, updated_at = ? WHERE id = ?`, username, name, now, id); err != nil {
		if isMySQLError(err, 1062) {
			return UserRecord{}, user.ErrUsernameTaken
		}
		return UserRecord{}, fmt.Errorf("update user profile: %w", err)
	}
	updated, err := findUser(ctx, transaction, id, false)
	if err != nil {
		return UserRecord{}, err
	}
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: auditAttribution(requester), Action: audit.ActionUserProfileUpdated,
		Resource: audit.ResourceUser, ResourceID: id, CreatedAt: now,
	}); err != nil {
		return UserRecord{}, err
	}
	if err := transaction.Commit(); err != nil {
		return UserRecord{}, fmt.Errorf("commit profile update: %w", err)
	}
	return updated, nil
}

func (repository *Repository) AssignUserRole(ctx context.Context, requester securityctx.Requester, userID, roleID uint64, now time.Time) error {
	transaction, err := repository.beginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin role assignment: %w", err)
	}
	defer transaction.Rollback()
	adminRole, err := lockRoleBySlug(ctx, transaction, access.AdminRoleSlug)
	if err != nil {
		return err
	}
	target, err := findUser(ctx, transaction, userID, true)
	if err != nil {
		return err
	}
	requestedRole, err := findRole(ctx, transaction, roleID, true)
	if err != nil {
		return err
	}
	if !requester.IsEffectiveAdmin() && requester.Effective.UserID == userID {
		return ErrSelfRoleChange
	}
	if (access.IsAdminRole(target.RoleSlug) || access.IsAdminRole(requestedRole.Slug)) && !requester.IsEffectiveAdmin() {
		return ErrAdminMutation
	}
	if target.IsActive && target.RoleID == adminRole.ID && requestedRole.ID != adminRole.ID {
		ids, err := lockActiveAdministrators(ctx, transaction, adminRole.ID)
		if err != nil {
			return err
		}
		if len(ids) <= 1 {
			return ErrLastActiveAdmin
		}
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE users SET role_id = ?, updated_at = ? WHERE id = ?`, requestedRole.ID, now, userID); err != nil {
		return fmt.Errorf("update user role: %w", err)
	}
	if access.IsAdminRole(requestedRole.Slug) {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM sessions WHERE impersonated_user_id = ?`, userID); err != nil {
			return fmt.Errorf("revoke sessions impersonating promoted administrator: %w", err)
		}
	}
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: auditAttribution(requester), Action: audit.ActionUserRoleChanged,
		Resource: audit.ResourceUser, ResourceID: userID,
		Metadata: audit.RoleChangeMetadata{FromRole: target.RoleSlug, ToRole: requestedRole.Slug}, CreatedAt: now,
	}); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit role assignment: %w", err)
	}
	return nil
}

func (repository *Repository) SetUserActive(ctx context.Context, requester securityctx.Requester, userID uint64, active bool, now time.Time) error {
	transaction, err := repository.beginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin status update: %w", err)
	}
	defer transaction.Rollback()
	adminRole, err := lockRoleBySlug(ctx, transaction, access.AdminRoleSlug)
	if err != nil {
		return err
	}
	target, err := findUser(ctx, transaction, userID, true)
	if err != nil {
		return err
	}
	if !active && requester.Effective.UserID == userID {
		return ErrSelfDeactivation
	}
	if access.IsAdminRole(target.RoleSlug) && !requester.IsEffectiveAdmin() {
		return ErrAdminMutation
	}
	if !active && target.IsActive && target.RoleID == adminRole.ID {
		ids, err := lockActiveAdministrators(ctx, transaction, adminRole.ID)
		if err != nil {
			return err
		}
		if len(ids) <= 1 {
			return ErrLastActiveAdmin
		}
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE users SET is_active = ?, updated_at = ? WHERE id = ?`, active, now, userID); err != nil {
		return fmt.Errorf("update user status: %w", err)
	}
	if !active {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? OR impersonated_user_id = ?`, userID, userID); err != nil {
			return fmt.Errorf("revoke deactivated user sessions: %w", err)
		}
	}
	action := audit.ActionUserDeactivated
	if active {
		action = audit.ActionUserActivated
	}
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: auditAttribution(requester), Action: action,
		Resource: audit.ResourceUser, ResourceID: userID,
		Metadata: audit.StatusChangeMetadata{From: activeStatus(target.IsActive), To: activeStatus(active)}, CreatedAt: now,
	}); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit status update: %w", err)
	}
	return nil
}

func (repository *Repository) ResetUserPassword(ctx context.Context, requester securityctx.Requester, userID uint64, passwordHash string, now time.Time) error {
	transaction, err := repository.beginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin password reset: %w", err)
	}
	defer transaction.Rollback()
	target, err := findUser(ctx, transaction, userID, true)
	if err != nil {
		return err
	}
	if access.IsAdminRole(target.RoleSlug) && !requester.IsEffectiveAdmin() {
		return ErrAdminMutation
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, passwordHash, now, userID); err != nil {
		return fmt.Errorf("update user password: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("revoke reset user sessions: %w", err)
	}
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: auditAttribution(requester), Action: audit.ActionUserPasswordReset,
		Resource: audit.ResourceUser, ResourceID: userID, CreatedAt: now,
	}); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit password reset: %w", err)
	}
	return nil
}

func findUser(ctx context.Context, database sqlx.ExtContext, id uint64, lock bool) (UserRecord, error) {
	statement := `SELECT ` + userColumns + ` FROM users u JOIN roles r ON r.id = u.role_id WHERE u.id = ?`
	if lock {
		statement += ` FOR UPDATE`
	}
	var found UserRecord
	if err := sqlx.GetContext(ctx, database, &found, statement, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UserRecord{}, ErrNotFound
		}
		return UserRecord{}, fmt.Errorf("find user: %w", err)
	}
	return found, nil
}

func activeStatus(active bool) string {
	if active {
		return "active"
	}
	return "inactive"
}

func findRole(ctx context.Context, database sqlx.ExtContext, id uint64, lock bool) (access.Role, error) {
	statement := `SELECT id, name, slug, is_system, created_at, updated_at FROM roles WHERE id = ?`
	if lock {
		statement += ` FOR UPDATE`
	}
	var role access.Role
	if err := sqlx.GetContext(ctx, database, &role, statement, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return access.Role{}, ErrNotFound
		}
		return access.Role{}, fmt.Errorf("find role: %w", err)
	}
	return role, nil
}

func lockRoleBySlug(ctx context.Context, transaction *sqlx.Tx, slug string) (access.Role, error) {
	const statement = `
		SELECT id, name, slug, is_system, created_at, updated_at
		FROM roles WHERE slug = ? FOR UPDATE`
	var role access.Role
	if err := transaction.GetContext(ctx, &role, statement, slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return access.Role{}, ErrNotFound
		}
		return access.Role{}, fmt.Errorf("lock role by slug: %w", err)
	}
	return role, nil
}

func lockActiveAdministrators(ctx context.Context, transaction *sqlx.Tx, roleID uint64) ([]uint64, error) {
	ids := make([]uint64, 0, 2)
	if err := transaction.SelectContext(ctx, &ids, `SELECT id FROM users WHERE role_id = ? AND is_active = TRUE ORDER BY id FOR UPDATE`, roleID); err != nil {
		return nil, fmt.Errorf("lock active administrators: %w", err)
	}
	return ids, nil
}

func isMySQLError(err error, number uint16) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == number
}

func auditAttribution(requester securityctx.Requester) audit.Attribution {
	actor := audit.Identity{UserID: requester.Actor.UserID, Username: requester.Actor.Username}
	effective := audit.Identity{UserID: requester.Effective.UserID, Username: requester.Effective.Username}
	return audit.Attribution{Actor: &actor, Effective: &effective}
}
