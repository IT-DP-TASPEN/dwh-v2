package impersonation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/user"
)

type userStore interface {
	FindByID(context.Context, uint64) (user.User, error)
}

type roleStore interface {
	FindRoleByID(context.Context, uint64) (access.Role, error)
}

type transitionStore interface {
	Start(context.Context, [32]byte, uint64, audit.Attribution, time.Time, func() ([32]byte, error)) (auth.Session, error)
	Stop(context.Context, [32]byte, audit.Attribution, time.Time, func() ([32]byte, error)) (auth.Session, error)
}

type Service struct {
	users         userStore
	roles         roleStore
	transitions   transitionStore
	generateToken func() (string, error)
}

type Result struct {
	RawToken string
	Session  auth.Session
}

func NewService(users userStore, roles roleStore, transitions transitionStore) *Service {
	return &Service{users: users, roles: roles, transitions: transitions, generateToken: auth.GenerateToken}
}

func CanStart(principal browserauth.Principal, targetID uint64, targetRoleSlug string, active bool) bool {
	return access.IsAdminRole(principal.Actor.RoleSlug) && !principal.IsImpersonating && active &&
		targetID != principal.Actor.UserID && !access.IsAdminRole(targetRoleSlug)
}

func (service *Service) Start(ctx context.Context, principal browserauth.Principal, tokenHash [32]byte, targetUserID uint64, now time.Time) (Result, error) {
	if principal.IsImpersonating {
		return Result{}, ErrAlreadyActive
	}
	if !access.IsAdminRole(principal.Actor.RoleSlug) {
		return Result{}, ErrForbidden
	}
	if targetUserID == principal.Actor.UserID {
		return Result{}, ErrSelf
	}
	target, err := service.users.FindByID(ctx, targetUserID)
	if errors.Is(err, user.ErrNotFound) {
		return Result{}, ErrTargetNotFound
	}
	if err != nil {
		return Result{}, fmt.Errorf("find impersonation target: %w", err)
	}
	role, err := service.roles.FindRoleByID(ctx, target.RoleID)
	if errors.Is(err, access.ErrNotFound) {
		return Result{}, ErrTargetNotFound
	}
	if err != nil {
		return Result{}, fmt.Errorf("find impersonation target role: %w", err)
	}
	if !target.IsActive {
		return Result{}, ErrTargetInactive
	}
	if access.IsAdminRole(role.Slug) {
		return Result{}, ErrTargetAdmin
	}

	actorIdentity := audit.Identity{UserID: principal.Actor.UserID, Username: principal.Actor.Username}
	targetIdentity := audit.Identity{UserID: target.ID, Username: target.Username}
	var rawToken string
	session, err := service.transitions.Start(ctx, tokenHash, targetUserID, audit.Attribution{Actor: &actorIdentity, Effective: &targetIdentity}, now.UTC(), func() ([32]byte, error) {
		generated, err := service.generateToken()
		if err != nil {
			return [32]byte{}, err
		}
		rawToken = generated
		return auth.HashToken(generated), nil
	})
	if errors.Is(err, auth.ErrSessionNotFound) || errors.Is(err, auth.ErrSessionIdentityInvalid) {
		return Result{}, ErrUnauthenticated
	}
	return Result{RawToken: rawToken, Session: session}, err
}

func (service *Service) Stop(ctx context.Context, principal browserauth.Principal, tokenHash [32]byte, now time.Time) (Result, error) {
	if !principal.IsImpersonating {
		return Result{}, ErrNotActive
	}
	if !access.IsAdminRole(principal.Actor.RoleSlug) {
		return Result{}, ErrForbidden
	}
	actor := audit.Identity{UserID: principal.Actor.UserID, Username: principal.Actor.Username}
	effective := audit.Identity{UserID: principal.UserID, Username: principal.Username}
	var rawToken string
	session, err := service.transitions.Stop(ctx, tokenHash, audit.Attribution{Actor: &actor, Effective: &effective}, now.UTC(), func() ([32]byte, error) {
		generated, err := service.generateToken()
		if err != nil {
			return [32]byte{}, err
		}
		rawToken = generated
		return auth.HashToken(generated), nil
	})
	if errors.Is(err, auth.ErrSessionNotFound) || errors.Is(err, auth.ErrSessionIdentityInvalid) {
		return Result{}, ErrUnauthenticated
	}
	return Result{RawToken: rawToken, Session: session}, err
}
