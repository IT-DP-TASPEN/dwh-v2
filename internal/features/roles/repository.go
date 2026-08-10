package roles

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

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

func (repository *Repository) List(ctx context.Context) ([]Record, error) {
	const statement = `
		SELECT r.id, r.name, r.slug, r.is_system, COUNT(u.id) AS user_count, r.created_at, r.updated_at
		FROM roles r
		LEFT JOIN users u ON u.role_id = r.id
		GROUP BY r.id, r.name, r.slug, r.is_system, r.created_at, r.updated_at
		ORDER BY r.is_system DESC, r.name ASC, r.id ASC`
	roles := make([]Record, 0)
	if err := repository.database.SelectContext(ctx, &roles, statement); err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	return roles, nil
}

func (repository *Repository) FindByID(ctx context.Context, id uint64) (Record, error) {
	return findRole(ctx, repository.database, id, false)
}

func (repository *Repository) Create(ctx context.Context, requester securityctx.Requester, name, slug string, now time.Time) (Record, error) {
	transaction, err := repository.beginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("begin role creation: %w", err)
	}
	defer transaction.Rollback()

	result, err := transaction.ExecContext(ctx, `INSERT INTO roles (name, slug, is_system, created_at, updated_at) VALUES (?, ?, FALSE, ?, ?)`, name, slug, now, now)
	if err != nil {
		if isMySQLError(err, 1062) {
			return Record{}, ErrRoleSlugTaken
		}
		return Record{}, fmt.Errorf("insert role: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Record{}, fmt.Errorf("read inserted role ID: %w", err)
	}
	created, err := findRole(ctx, transaction, uint64(id), false)
	if err != nil {
		return Record{}, err
	}
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: attribution(requester), Action: audit.ActionRoleCreated,
		Resource: audit.ResourceRole, ResourceID: created.ID, CreatedAt: now,
	}); err != nil {
		return Record{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit role creation: %w", err)
	}
	return created, nil
}

func (repository *Repository) UpdateName(ctx context.Context, requester securityctx.Requester, id uint64, name string, now time.Time) (Record, error) {
	transaction, err := repository.beginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("begin role update: %w", err)
	}
	defer transaction.Rollback()
	role, err := findRole(ctx, transaction, id, true)
	if err != nil {
		return Record{}, err
	}
	if role.IsSystem {
		return Record{}, ErrProtectedRole
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE roles SET name = ?, updated_at = ? WHERE id = ?`, name, now, id); err != nil {
		return Record{}, fmt.Errorf("update role name: %w", err)
	}
	role.Name = name
	role.UpdatedAt = now
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: attribution(requester), Action: audit.ActionRoleUpdated,
		Resource: audit.ResourceRole, ResourceID: id, CreatedAt: now,
	}); err != nil {
		return Record{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit role update: %w", err)
	}
	return role, nil
}

func (repository *Repository) Delete(ctx context.Context, requester securityctx.Requester, id uint64, now time.Time) error {
	transaction, err := repository.beginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin role deletion: %w", err)
	}
	defer transaction.Rollback()
	role, err := findRole(ctx, transaction, id, true)
	if err != nil {
		return err
	}
	if role.IsSystem {
		return ErrProtectedRole
	}
	var assigned bool
	if err := transaction.GetContext(ctx, &assigned, `SELECT EXISTS(SELECT 1 FROM users WHERE role_id = ?)`, id); err != nil {
		return fmt.Errorf("check role assignments: %w", err)
	}
	if assigned {
		return ErrRoleAssigned
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, id); err != nil {
		if isMySQLError(err, 1451) {
			return ErrRoleAssigned
		}
		return fmt.Errorf("delete role: %w", err)
	}
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: attribution(requester), Action: audit.ActionRoleDeleted,
		Resource: audit.ResourceRole, ResourceID: id, CreatedAt: now,
	}); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit role deletion: %w", err)
	}
	return nil
}

func (repository *Repository) ListPermissionKeys(ctx context.Context, roleID uint64) ([]string, error) {
	const statement = `
		SELECT p.key FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = ? ORDER BY p.key`
	keys := make([]string, 0)
	if err := repository.database.SelectContext(ctx, &keys, statement, roleID); err != nil {
		return nil, fmt.Errorf("list role permission keys: %w", err)
	}
	return keys, nil
}

func (repository *Repository) ReplacePermissions(ctx context.Context, requester securityctx.Requester, roleID uint64, canonical, selected []string, now time.Time) error {
	transaction, err := repository.beginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin permission replacement: %w", err)
	}
	defer transaction.Rollback()
	role, err := findRole(ctx, transaction, roleID, true)
	if err != nil {
		return err
	}
	if access.IsAdminRole(role.Slug) {
		return ErrAdminPermissions
	}
	var effectiveRoleID uint64
	if err := transaction.GetContext(ctx, &effectiveRoleID, `SELECT role_id FROM users WHERE id = ? FOR UPDATE`, requester.Effective.UserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock permission actor: %w", err)
	}
	if !requester.IsEffectiveAdmin() && effectiveRoleID == roleID {
		return ErrSelfRolePermissions
	}

	idsByKey, err := permissionIDs(ctx, transaction, canonical)
	if err != nil {
		return err
	}
	canonicalIDs := make([]uint64, 0, len(canonical))
	for _, key := range canonical {
		id, ok := idsByKey[key]
		if !ok {
			return fmt.Errorf("%w: canonical permission %q missing from database", ErrUnknownPermission, key)
		}
		canonicalIDs = append(canonicalIDs, id)
	}
	existing, err := assignedCanonicalPermissions(ctx, transaction, roleID, canonical)
	if err != nil {
		return err
	}
	deleteQuery, deleteArguments, err := sqlx.In(`DELETE FROM role_permissions WHERE role_id = ? AND permission_id IN (?)`, roleID, canonicalIDs)
	if err != nil {
		return fmt.Errorf("build permission deletion: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, transaction.Rebind(deleteQuery), deleteArguments...); err != nil {
		return fmt.Errorf("delete canonical role permissions: %w", err)
	}
	for _, key := range selected {
		id, ok := idsByKey[key]
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownPermission, key)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO role_permissions (role_id, permission_id) VALUES (?, ?)`, roleID, id); err != nil {
			return fmt.Errorf("insert role permission %q: %w", key, err)
		}
	}
	added, removed := permissionChanges(existing, selected)
	if err := repository.appendAudit(ctx, transaction, audit.Event{
		Attribution: attribution(requester), Action: audit.ActionRolePermissionsUpdated,
		Resource: audit.ResourceRole, ResourceID: roleID,
		Metadata: audit.PermissionsUpdatedMetadata{Added: added, Removed: removed}, CreatedAt: now,
	}); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit permission replacement: %w", err)
	}
	return nil
}

func assignedCanonicalPermissions(ctx context.Context, transaction *sqlx.Tx, roleID uint64, canonical []string) ([]string, error) {
	query, arguments, err := sqlx.In(`
		SELECT p.key FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = ? AND p.key IN (?)`, roleID, canonical)
	if err != nil {
		return nil, fmt.Errorf("build assigned permission lookup: %w", err)
	}
	var keys []string
	if err := transaction.SelectContext(ctx, &keys, transaction.Rebind(query), arguments...); err != nil {
		return nil, fmt.Errorf("list assigned canonical permissions: %w", err)
	}
	return keys, nil
}

func permissionChanges(existing, selected []string) ([]string, []string) {
	existingSet := make(map[string]struct{}, len(existing))
	selectedSet := make(map[string]struct{}, len(selected))
	for _, key := range existing {
		existingSet[key] = struct{}{}
	}
	for _, key := range selected {
		selectedSet[key] = struct{}{}
	}
	added := make([]string, 0)
	removed := make([]string, 0)
	for key := range selectedSet {
		if _, ok := existingSet[key]; !ok {
			added = append(added, key)
		}
	}
	for key := range existingSet {
		if _, ok := selectedSet[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func permissionIDs(ctx context.Context, transaction *sqlx.Tx, keys []string) (map[string]uint64, error) {
	query, arguments, err := sqlx.In("SELECT id, `key` FROM permissions WHERE `key` IN (?) FOR UPDATE", keys)
	if err != nil {
		return nil, fmt.Errorf("build permission lookup: %w", err)
	}
	rows := make([]struct {
		ID  uint64 `db:"id"`
		Key string `db:"key"`
	}, 0, len(keys))
	if err := transaction.SelectContext(ctx, &rows, transaction.Rebind(query), arguments...); err != nil {
		return nil, fmt.Errorf("resolve permission IDs: %w", err)
	}
	ids := make(map[string]uint64, len(rows))
	for _, row := range rows {
		ids[row.Key] = row.ID
	}
	return ids, nil
}

func findRole(ctx context.Context, database sqlx.ExtContext, id uint64, lock bool) (Record, error) {
	statement := `SELECT r.id, r.name, r.slug, r.is_system, 0 AS user_count, r.created_at, r.updated_at FROM roles r WHERE r.id = ?`
	if !lock {
		statement = `
			SELECT r.id, r.name, r.slug, r.is_system,
				(SELECT COUNT(*) FROM users u WHERE u.role_id = r.id) AS user_count,
				r.created_at, r.updated_at
			FROM roles r WHERE r.id = ?`
	}
	if lock {
		statement += ` FOR UPDATE`
	}
	var role Record
	if err := sqlx.GetContext(ctx, database, &role, statement, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, fmt.Errorf("find role: %w", err)
	}
	return role, nil
}

func attribution(requester securityctx.Requester) audit.Attribution {
	actor := audit.Identity{UserID: requester.Actor.UserID, Username: requester.Actor.Username}
	effective := audit.Identity{UserID: requester.Effective.UserID, Username: requester.Effective.Username}
	return audit.Attribution{Actor: &actor, Effective: &effective}
}

func isMySQLError(err error, number uint16) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == number
}
