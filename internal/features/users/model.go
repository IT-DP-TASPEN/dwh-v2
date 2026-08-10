package users

import (
	"fmt"
	"time"

	"github.com/ibldzn/go-admin/internal/platform/pagination"
)

const UserPageSize = 20

type UserRecord struct {
	ID           uint64     `db:"id"`
	Username     string     `db:"username"`
	Name         string     `db:"name"`
	RoleID       uint64     `db:"role_id"`
	RoleName     string     `db:"role_name"`
	RoleSlug     string     `db:"role_slug"`
	RoleIsSystem bool       `db:"role_is_system"`
	IsActive     bool       `db:"is_active"`
	LastLoginAt  *time.Time `db:"last_login_at"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

type UserPage struct {
	Users      []UserRecord
	Query      string
	Pagination pagination.Page
}

type CreateUserInput struct {
	Name                 string
	Username             string
	Password             string
	PasswordConfirmation string
	RoleID               *uint64
}

type UpdateUserInput struct {
	Name     string
	Username string
}

type ResetPasswordInput struct {
	Password             string
	PasswordConfirmation string
}

type ValidationErrors map[string]string

func (errors ValidationErrors) Error() string {
	return fmt.Sprintf("validation failed for %d field(s)", len(errors))
}
