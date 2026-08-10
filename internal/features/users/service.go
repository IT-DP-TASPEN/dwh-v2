package users

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/user"
)

type store interface {
	CountUsers(context.Context, string) (int64, error)
	ListUsers(context.Context, string, int, int) ([]UserRecord, error)
	FindUserByID(context.Context, uint64) (UserRecord, error)
	CreateUser(context.Context, securityctx.Requester, user.CreateParams, time.Time) (UserRecord, error)
	UpdateUserProfile(context.Context, securityctx.Requester, uint64, string, string, time.Time) (UserRecord, error)
	AssignUserRole(context.Context, securityctx.Requester, uint64, uint64, time.Time) error
	SetUserActive(context.Context, securityctx.Requester, uint64, bool, time.Time) error
	ResetUserPassword(context.Context, securityctx.Requester, uint64, string, time.Time) error
}

type roleStore interface {
	ListRoles(context.Context) ([]access.Role, error)
	FindRoleByID(context.Context, uint64) (access.Role, error)
	FindRoleBySlug(context.Context, string) (access.Role, error)
}

type Service struct {
	store                store
	roles                roleStore
	assignRolePermission string
	hashPassword         func(string) (string, error)
}

func NewService(store store, roles roleStore, assignRolePermission string) *Service {
	return &Service{store: store, roles: roles, assignRolePermission: assignRolePermission, hashPassword: auth.HashPassword}
}

func (service *Service) ListUsers(ctx context.Context, query string, page int) (UserPage, error) {
	query = strings.TrimSpace(query)
	total, err := service.store.CountUsers(ctx, query)
	if err != nil {
		return UserPage{}, fmt.Errorf("count managed users: %w", err)
	}
	pageInfo := pagination.New(page, UserPageSize, total)
	users, err := service.store.ListUsers(ctx, query, pageInfo.PerPage, pageInfo.Offset())
	if err != nil {
		return UserPage{}, fmt.Errorf("list managed users: %w", err)
	}
	return UserPage{Users: users, Query: query, Pagination: pageInfo}, nil
}

func (service *Service) FindUser(ctx context.Context, id uint64) (UserRecord, error) {
	if id == 0 {
		return UserRecord{}, ErrNotFound
	}
	found, err := service.store.FindUserByID(ctx, id)
	if err != nil {
		return UserRecord{}, fmt.Errorf("find managed user: %w", err)
	}
	return found, nil
}

func (service *Service) AvailableRoles(ctx context.Context, requester securityctx.Requester) ([]access.Role, error) {
	roles, err := service.roles.ListRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list assignable roles: %w", err)
	}
	if requester.IsEffectiveAdmin() {
		return roles, nil
	}
	filtered := roles[:0]
	for _, role := range roles {
		if !access.IsAdminRole(role.Slug) {
			filtered = append(filtered, role)
		}
	}
	return filtered, nil
}

func (service *Service) CreateUser(ctx context.Context, requester securityctx.Requester, input CreateUserInput, now time.Time) (UserRecord, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Username = user.NormalizeUsername(input.Username)
	validation := ValidationErrors{}
	if err := user.ValidateName(input.Name); err != nil {
		validation["name"] = err.Error()
	}
	if err := user.ValidateUsername(input.Username); err != nil {
		validation["username"] = err.Error()
	}
	if err := auth.ValidatePassword(input.Password); err != nil {
		validation["password"] = err.Error()
	}
	if input.Password != input.PasswordConfirmation {
		validation["password_confirmation"] = "password confirmation does not match"
	}
	if len(validation) != 0 {
		return UserRecord{}, validation
	}
	if !requester.Can(service.assignRolePermission) && input.RoleID != nil {
		return UserRecord{}, ErrRoleSubmissionForbidden
	}

	var role access.Role
	var err error
	if input.RoleID == nil {
		role, err = service.roles.FindRoleBySlug(ctx, access.UserRoleSlug)
	} else if *input.RoleID == 0 {
		return UserRecord{}, ValidationErrors{"role_id": "role must be selected"}
	} else {
		role, err = service.roles.FindRoleByID(ctx, *input.RoleID)
	}
	if err != nil {
		if errors.Is(err, access.ErrNotFound) {
			return UserRecord{}, ValidationErrors{"role_id": "selected role does not exist"}
		}
		return UserRecord{}, fmt.Errorf("find new user role: %w", err)
	}
	if access.IsAdminRole(role.Slug) && !requester.IsEffectiveAdmin() {
		return UserRecord{}, ErrAdminMutation
	}

	passwordHash, err := service.hashPassword(input.Password)
	if err != nil {
		return UserRecord{}, fmt.Errorf("hash managed user password: %w", err)
	}
	created, err := service.store.CreateUser(ctx, requester, user.CreateParams{
		Username:     input.Username,
		Name:         input.Name,
		PasswordHash: passwordHash,
		RoleID:       role.ID,
		IsActive:     true,
	}, now.UTC())
	if err != nil {
		return UserRecord{}, fmt.Errorf("create managed user: %w", err)
	}
	return created, nil
}

func (service *Service) UpdateUser(ctx context.Context, requester securityctx.Requester, id uint64, input UpdateUserInput, now time.Time) (UserRecord, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Username = user.NormalizeUsername(input.Username)
	validation := ValidationErrors{}
	if err := user.ValidateName(input.Name); err != nil {
		validation["name"] = err.Error()
	}
	if err := user.ValidateUsername(input.Username); err != nil {
		validation["username"] = err.Error()
	}
	if len(validation) != 0 {
		return UserRecord{}, validation
	}
	found, err := service.FindUser(ctx, id)
	if err != nil {
		return UserRecord{}, err
	}
	if access.IsAdminRole(found.RoleSlug) && !requester.IsEffectiveAdmin() {
		return UserRecord{}, ErrAdminMutation
	}
	updated, err := service.store.UpdateUserProfile(ctx, requester, id, input.Username, input.Name, now.UTC())
	if err != nil {
		return UserRecord{}, fmt.Errorf("update managed user: %w", err)
	}
	return updated, nil
}

func (service *Service) AssignRole(ctx context.Context, requester securityctx.Requester, userID, roleID uint64, now time.Time) error {
	if userID == 0 || roleID == 0 {
		return ErrNotFound
	}
	if !requester.IsEffectiveAdmin() && requester.Effective.UserID == userID {
		return ErrSelfRoleChange
	}
	target, err := service.FindUser(ctx, userID)
	if err != nil {
		return err
	}
	role, err := service.roles.FindRoleByID(ctx, roleID)
	if err != nil {
		return fmt.Errorf("find assigned role: %w", err)
	}
	if (access.IsAdminRole(target.RoleSlug) || access.IsAdminRole(role.Slug)) && !requester.IsEffectiveAdmin() {
		return ErrAdminMutation
	}
	if err := service.store.AssignUserRole(ctx, requester, userID, roleID, now.UTC()); err != nil {
		return fmt.Errorf("assign managed user role: %w", err)
	}
	return nil
}

func (service *Service) SetActive(ctx context.Context, requester securityctx.Requester, userID uint64, active bool, now time.Time) error {
	if userID == 0 {
		return ErrNotFound
	}
	if !active && requester.Effective.UserID == userID {
		return ErrSelfDeactivation
	}
	target, err := service.FindUser(ctx, userID)
	if err != nil {
		return err
	}
	if access.IsAdminRole(target.RoleSlug) && !requester.IsEffectiveAdmin() {
		return ErrAdminMutation
	}
	if err := service.store.SetUserActive(ctx, requester, userID, active, now.UTC()); err != nil {
		return fmt.Errorf("change managed user status: %w", err)
	}
	return nil
}

func (service *Service) ResetPassword(ctx context.Context, requester securityctx.Requester, userID uint64, input ResetPasswordInput, now time.Time) error {
	if userID == 0 {
		return ErrNotFound
	}
	target, err := service.FindUser(ctx, userID)
	if err != nil {
		return err
	}
	if access.IsAdminRole(target.RoleSlug) && !requester.IsEffectiveAdmin() {
		return ErrAdminMutation
	}
	validation := ValidationErrors{}
	if err := auth.ValidatePassword(input.Password); err != nil {
		validation["password"] = err.Error()
	}
	if input.Password != input.PasswordConfirmation {
		validation["password_confirmation"] = "password confirmation does not match"
	}
	if len(validation) != 0 {
		return validation
	}
	passwordHash, err := service.hashPassword(input.Password)
	if err != nil {
		return fmt.Errorf("hash reset password: %w", err)
	}
	if err := service.store.ResetUserPassword(ctx, requester, userID, passwordHash, now.UTC()); err != nil {
		return fmt.Errorf("reset managed user password: %w", err)
	}
	return nil
}
