package access

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

func Bootstrap(ctx context.Context, database *sqlx.DB, definitions []PermissionDefinition, now time.Time) error {
	if err := ValidateRegistry(definitions); err != nil {
		return fmt.Errorf("validate permission registry: %w", err)
	}

	transaction, err := database.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin access bootstrap: %w", err)
	}
	defer transaction.Rollback()

	repository := NewRepository(transaction)
	now = now.UTC()
	for _, definition := range definitions {
		if err := repository.syncPermission(ctx, definition, now); err != nil {
			return err
		}
	}
	for _, role := range []struct{ name, slug string }{
		{"Administrator", AdminRoleSlug},
		{"User", UserRoleSlug},
	} {
		if err := repository.ensureSystemRole(ctx, role.name, role.slug, now); err != nil {
			return err
		}
	}

	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit access bootstrap: %w", err)
	}
	return nil
}
