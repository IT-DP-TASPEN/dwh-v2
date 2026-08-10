package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

var (
	ErrNotFound      = errors.New("user not found")
	ErrUsernameTaken = errors.New("username already exists")
)

type Repository struct {
	database sqlx.ExtContext
}

func NewRepository(database sqlx.ExtContext) *Repository {
	return &Repository{database: database}
}

func (r *Repository) Create(ctx context.Context, params CreateParams, now time.Time) (User, error) {
	params.Username = NormalizeUsername(params.Username)
	params.Name = strings.TrimSpace(params.Name)
	if err := ValidateUsername(params.Username); err != nil {
		return User{}, err
	}
	if err := ValidateName(params.Name); err != nil {
		return User{}, err
	}
	if params.PasswordHash == "" {
		return User{}, fmt.Errorf("password hash must not be empty")
	}
	if params.RoleID == 0 {
		return User{}, fmt.Errorf("role ID must not be zero")
	}

	now = now.UTC()
	const query = `
		INSERT INTO users (username, name, password_hash, role_id, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	result, err := r.database.ExecContext(ctx, query,
		params.Username,
		params.Name,
		params.PasswordHash,
		params.RoleID,
		params.IsActive,
		now,
		now,
	)
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return User{}, ErrUsernameTaken
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("read created user ID: %w", err)
	}

	return User{
		ID:           uint64(id),
		Username:     params.Username,
		Name:         params.Name,
		PasswordHash: params.PasswordHash,
		RoleID:       params.RoleID,
		IsActive:     params.IsActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (r *Repository) FindByID(ctx context.Context, id uint64) (User, error) {
	return r.find(ctx, `
		SELECT id, username, name, password_hash, role_id, is_active, last_login_at, created_at, updated_at
		FROM users
		WHERE id = ?`, id)
}

func (r *Repository) FindByUsername(ctx context.Context, username string) (User, error) {
	username = NormalizeUsername(username)
	if err := ValidateUsername(username); err != nil {
		return User{}, err
	}
	return r.find(ctx, `
		SELECT id, username, name, password_hash, role_id, is_active, last_login_at, created_at, updated_at
		FROM users
		WHERE username = ?`, username)
}

func (r *Repository) UpdateLastLoginAt(ctx context.Context, id uint64, now time.Time) error {
	if _, err := r.database.ExecContext(ctx, `UPDATE users SET last_login_at = ?, updated_at = ? WHERE id = ?`, now.UTC(), now.UTC(), id); err != nil {
		return fmt.Errorf("update user last login: %w", err)
	}
	return nil
}

func (r *Repository) find(ctx context.Context, query string, argument any) (User, error) {
	var found User
	if err := sqlx.GetContext(ctx, r.database, &found, query, argument); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("find user: %w", err)
	}
	return found, nil
}
