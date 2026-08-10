package auditlogs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("audit log not found")

type Repository struct {
	database *sqlx.DB
}

type row struct {
	ID                uint64         `db:"id"`
	ActorUserID       sql.NullInt64  `db:"actor_user_id"`
	ActorUsername     sql.NullString `db:"actor_username"`
	EffectiveUserID   sql.NullInt64  `db:"effective_user_id"`
	EffectiveUsername sql.NullString `db:"effective_username"`
	Action            string         `db:"action"`
	ResourceType      sql.NullString `db:"resource_type"`
	ResourceID        sql.NullInt64  `db:"resource_id"`
	Metadata          []byte         `db:"metadata"`
	CreatedAt         sql.NullTime   `db:"created_at"`
}

const columns = `
	id, actor_user_id, actor_username, effective_user_id, effective_username,
	action, resource_type, resource_id, metadata, created_at`

func NewRepository(database *sqlx.DB) *Repository {
	return &Repository{database: database}
}

func (repository *Repository) Count(ctx context.Context, action string) (int64, error) {
	query := `SELECT COUNT(*) FROM audit_logs`
	arguments := []any(nil)
	if action != "" {
		query += ` WHERE action = ?`
		arguments = append(arguments, action)
	}
	var total int64
	if err := repository.database.GetContext(ctx, &total, query, arguments...); err != nil {
		return 0, fmt.Errorf("count audit logs: %w", err)
	}
	return total, nil
}

func (repository *Repository) List(ctx context.Context, action string, limit, offset int) ([]Record, error) {
	query := `SELECT ` + columns + ` FROM audit_logs`
	arguments := []any(nil)
	if action != "" {
		query += ` WHERE action = ?`
		arguments = append(arguments, action)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	arguments = append(arguments, limit, offset)
	rows := make([]row, 0, limit)
	if err := repository.database.SelectContext(ctx, &rows, query, arguments...); err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	result := make([]Record, len(rows))
	for index, value := range rows {
		result[index] = value.record()
	}
	return result, nil
}

func (repository *Repository) Find(ctx context.Context, id uint64) (Record, error) {
	var found row
	if err := repository.database.GetContext(ctx, &found, `SELECT `+columns+` FROM audit_logs WHERE id = ?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, fmt.Errorf("find audit log: %w", err)
	}
	return found.record(), nil
}

func (value row) record() Record {
	return Record{
		ID: value.ID, ActorUserID: nullableID(value.ActorUserID), ActorUsername: value.ActorUsername.String,
		EffectiveUserID: nullableID(value.EffectiveUserID), EffectiveUsername: value.EffectiveUsername.String,
		Action: value.Action, ResourceType: value.ResourceType.String, ResourceID: nullableID(value.ResourceID),
		Metadata: append([]byte(nil), value.Metadata...), CreatedAt: value.CreatedAt.Time,
	}
}

func nullableID(value sql.NullInt64) *uint64 {
	if !value.Valid {
		return nil
	}
	id := uint64(value.Int64)
	return &id
}
