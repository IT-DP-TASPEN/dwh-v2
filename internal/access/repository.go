package access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("access record not found")

type Repository struct {
	database sqlx.ExtContext
}

func NewRepository(database sqlx.ExtContext) *Repository {
	return &Repository{database: database}
}

func (r *Repository) FindRoleBySlug(ctx context.Context, slug string) (Role, error) {
	return r.findRole(ctx, `
		SELECT id, name, slug, is_system, created_at, updated_at
		FROM roles
		WHERE slug = ?`, slug, "slug")
}

func (r *Repository) FindRoleByID(ctx context.Context, id uint64) (Role, error) {
	return r.findRole(ctx, `
		SELECT id, name, slug, is_system, created_at, updated_at
		FROM roles
		WHERE id = ?`, id, "ID")
}

func (r *Repository) ListRoles(ctx context.Context) ([]Role, error) {
	const query = `
		SELECT id, name, slug, is_system, created_at, updated_at
		FROM roles
		ORDER BY is_system DESC, name ASC, id ASC`
	roles := make([]Role, 0)
	if err := sqlx.SelectContext(ctx, r.database, &roles, query); err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	return roles, nil
}

func (r *Repository) findRole(ctx context.Context, query string, value any, field string) (Role, error) {
	var role Role
	if err := sqlx.GetContext(ctx, r.database, &role, query, value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Role{}, ErrNotFound
		}
		return Role{}, fmt.Errorf("find role by %s: %w", field, err)
	}
	return role, nil
}

func (r *Repository) RoleHasPermission(ctx context.Context, roleID uint64, key string) (bool, error) {
	const query = `
		SELECT EXISTS (
			SELECT 1
			FROM role_permissions rp
			JOIN permissions p ON p.id = rp.permission_id
			WHERE rp.role_id = ? AND p.key = ?
		)`

	var allowed bool
	if err := sqlx.GetContext(ctx, r.database, &allowed, query, roleID, key); err != nil {
		return false, fmt.Errorf("check role %d permission %q: %w", roleID, key, err)
	}
	return allowed, nil
}

func (r *Repository) ListPermissionKeysForRole(ctx context.Context, roleID uint64) ([]string, error) {
	const query = `
		SELECT p.key
		FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = ?
		ORDER BY p.key`

	var keys []string
	if err := sqlx.SelectContext(ctx, r.database, &keys, query, roleID); err != nil {
		return nil, fmt.Errorf("list permissions for role %d: %w", roleID, err)
	}
	return keys, nil
}

func (r *Repository) syncPermission(ctx context.Context, definition PermissionDefinition, now time.Time) error {
	const query = `
		INSERT INTO permissions (` + "`key`" + `, name, group_name, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			updated_at = IF(
				name COLLATE utf8mb4_bin <> VALUES(name) COLLATE utf8mb4_bin OR
				group_name COLLATE utf8mb4_bin <> VALUES(group_name) COLLATE utf8mb4_bin OR
				description COLLATE utf8mb4_bin <> VALUES(description) COLLATE utf8mb4_bin,
				VALUES(updated_at),
				updated_at
			),
			name = VALUES(name),
			group_name = VALUES(group_name),
			description = VALUES(description)`

	if _, err := r.database.ExecContext(ctx, query,
		definition.Key,
		definition.Name,
		definition.Group,
		definition.Description,
		now,
		now,
	); err != nil {
		return fmt.Errorf("sync permission %q: %w", definition.Key, err)
	}
	return nil
}

func (r *Repository) ensureSystemRole(ctx context.Context, name, slug string, now time.Time) error {
	const query = `
		INSERT INTO roles (name, slug, is_system, created_at, updated_at)
		VALUES (?, ?, TRUE, ?, ?)
		ON DUPLICATE KEY UPDATE
			updated_at = IF(
				name COLLATE utf8mb4_bin <> VALUES(name) COLLATE utf8mb4_bin OR is_system <> TRUE,
				VALUES(updated_at),
				updated_at
			),
			name = VALUES(name),
			is_system = TRUE`

	if _, err := r.database.ExecContext(ctx, query, name, slug, now, now); err != nil {
		return fmt.Errorf("ensure system role %q: %w", slug, err)
	}
	return nil
}
