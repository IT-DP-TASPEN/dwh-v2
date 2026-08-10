package access

import "time"

const (
	AdminRoleSlug = "admin"
	UserRoleSlug  = "user"
)

type Role struct {
	ID        uint64    `db:"id"`
	Name      string    `db:"name"`
	Slug      string    `db:"slug"`
	IsSystem  bool      `db:"is_system"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

type PermissionDefinition struct {
	Key         string
	Name        string
	Group       string
	Description string
}
