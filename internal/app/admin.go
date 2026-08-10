package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/user"
)

var ErrPasswordConfirmation = errors.New("password confirmation does not match")

type AdministratorInput struct {
	Username             string
	Name                 string
	Password             string
	PasswordConfirmation string
}

type adminRoleFinder interface {
	FindRoleBySlug(context.Context, string) (access.Role, error)
}

type adminUserCreator interface {
	Create(context.Context, user.CreateParams, time.Time) (user.User, error)
}

func CreateAdministrator(
	ctx context.Context,
	roles adminRoleFinder,
	users adminUserCreator,
	input AdministratorInput,
	now time.Time,
) (user.User, error) {
	input.Username = user.NormalizeUsername(input.Username)
	input.Name = strings.TrimSpace(input.Name)
	if err := user.ValidateUsername(input.Username); err != nil {
		return user.User{}, err
	}
	if err := user.ValidateName(input.Name); err != nil {
		return user.User{}, err
	}
	if input.Password != input.PasswordConfirmation {
		return user.User{}, ErrPasswordConfirmation
	}
	if err := auth.ValidatePassword(input.Password); err != nil {
		return user.User{}, err
	}

	adminRole, err := roles.FindRoleBySlug(ctx, access.AdminRoleSlug)
	if err != nil {
		return user.User{}, fmt.Errorf("find admin role: %w", err)
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		return user.User{}, fmt.Errorf("hash administrator password: %w", err)
	}
	created, err := users.Create(ctx, user.CreateParams{
		Username:     input.Username,
		Name:         input.Name,
		PasswordHash: passwordHash,
		RoleID:       adminRole.ID,
		IsActive:     true,
	}, now.UTC())
	if err != nil {
		return user.User{}, fmt.Errorf("create administrator: %w", err)
	}
	return created, nil
}
