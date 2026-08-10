package roles

import (
	"fmt"
	"time"
)

type Record struct {
	ID        uint64    `db:"id"`
	Name      string    `db:"name"`
	Slug      string    `db:"slug"`
	IsSystem  bool      `db:"is_system"`
	UserCount uint64    `db:"user_count"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

type PermissionOption struct {
	Key         string
	Name        string
	Description string
	Selected    bool
}

type PermissionGroup struct {
	Name        string
	Permissions []PermissionOption
}

type Detail struct {
	Role             Record
	PermissionGroups []PermissionGroup
}

type CreateInput struct {
	Name string
	Slug string
}

type ValidationErrors map[string]string

func (errors ValidationErrors) Error() string {
	return fmt.Sprintf("validation failed for %d field(s)", len(errors))
}
