package user

import "time"

type User struct {
	ID           uint64     `db:"id"`
	Username     string     `db:"username"`
	Name         string     `db:"name"`
	PasswordHash string     `db:"password_hash"`
	RoleID       uint64     `db:"role_id"`
	IsActive     bool       `db:"is_active"`
	LastLoginAt  *time.Time `db:"last_login_at"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

type CreateParams struct {
	Username     string
	Name         string
	PasswordHash string
	RoleID       uint64
	IsActive     bool
}
