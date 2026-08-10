package browserauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/user"
)

const LastSeenTouchInterval = 5 * time.Minute

var (
	ErrInvalidCredentials   = errors.New("invalid username or password")
	ErrUnauthenticated      = errors.New("unauthenticated")
	ErrPasswordConfirmation = errors.New("password confirmation does not match")
)

type userStore interface {
	Create(context.Context, user.CreateParams, time.Time) (user.User, error)
	FindByID(context.Context, uint64) (user.User, error)
	FindByUsername(context.Context, string) (user.User, error)
	UpdateLastLoginAt(context.Context, uint64, time.Time) error
}

type roleStore interface {
	FindRoleByID(context.Context, uint64) (access.Role, error)
	FindRoleBySlug(context.Context, string) (access.Role, error)
	ListPermissionKeysForRole(context.Context, uint64) ([]string, error)
}

type sessionStore interface {
	Create(context.Context, auth.CreateSessionParams, time.Time) (auth.Session, error)
	FindValidByTokenHash(context.Context, [32]byte, time.Time) (auth.Session, error)
	UpdateLastSeenAt(context.Context, uint64, time.Time) error
	Revoke(context.Context, [32]byte) error
}

type Service struct {
	users            userStore
	roles            roleStore
	sessions         sessionStore
	lifetime         time.Duration
	rememberLifetime time.Duration
	dummyHash        string
	verifyPassword   func(string, string) (bool, error)
	generateToken    func() (string, error)
	logger           *slog.Logger
}

type LoginInput struct {
	Username   string
	Password   string
	RememberMe bool
}

type LoginResult struct {
	RawToken string
	Session  auth.Session
}

type RegisterInput struct {
	Name                 string
	Username             string
	Password             string
	PasswordConfirmation string
}

type Identity struct {
	UserID   uint64
	Username string
	Name     string
	RoleID   uint64
	RoleName string
	RoleSlug string
}

type Principal struct {
	UserID          uint64
	Username        string
	Name            string
	RoleID          uint64
	RoleName        string
	RoleSlug        string
	Permissions     access.PermissionSet
	SessionID       uint64
	RememberMe      bool
	Actor           Identity
	IsImpersonating bool
}

func (principal Principal) Can(permission string) bool {
	return access.IsAdminRole(principal.RoleSlug) || principal.Permissions.Has(permission)
}

func (principal Principal) SecurityContext() securityctx.Requester {
	return securityctx.Requester{
		Actor:             securityctx.Identity{UserID: principal.Actor.UserID, Username: principal.Actor.Username},
		Effective:         securityctx.Identity{UserID: principal.UserID, Username: principal.Username},
		EffectiveRoleID:   principal.RoleID,
		EffectiveRoleSlug: principal.RoleSlug,
		Permissions:       principal.Permissions,
	}
}

func auditAttributionFromPrincipal(principal Principal) audit.Attribution {
	actor := audit.Identity{UserID: principal.Actor.UserID, Username: principal.Actor.Username}
	effective := audit.Identity{UserID: principal.UserID, Username: principal.Username}
	return audit.Attribution{Actor: &actor, Effective: &effective}
}

func NewService(
	users userStore,
	roles roleStore,
	sessions sessionStore,
	lifetime time.Duration,
	rememberLifetime time.Duration,
	logger *slog.Logger,
) (*Service, error) {
	if lifetime <= 0 || rememberLifetime <= 0 {
		return nil, fmt.Errorf("session lifetimes must be positive")
	}
	dummyHash, err := auth.HashPassword("dummy authentication password")
	if err != nil {
		return nil, fmt.Errorf("create dummy password hash: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		users:            users,
		roles:            roles,
		sessions:         sessions,
		lifetime:         lifetime,
		rememberLifetime: rememberLifetime,
		dummyHash:        dummyHash,
		verifyPassword:   auth.VerifyPassword,
		generateToken:    auth.GenerateToken,
		logger:           logger,
	}, nil
}

func (s *Service) Login(ctx context.Context, input LoginInput, now time.Time) (LoginResult, error) {
	input.Username = user.NormalizeUsername(input.Username)
	if err := user.ValidateUsername(input.Username); err != nil || input.Password == "" || len(input.Password) > auth.MaxPasswordBytes {
		return LoginResult{}, ErrInvalidCredentials
	}

	found, err := s.users.FindByUsername(ctx, input.Username)
	if errors.Is(err, user.ErrNotFound) {
		if _, verifyErr := s.verifyPassword(input.Password, s.dummyHash); verifyErr != nil {
			return LoginResult{}, fmt.Errorf("verify dummy password: %w", verifyErr)
		}
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("find login user: %w", err)
	}

	valid, err := s.verifyPassword(input.Password, found.PasswordHash)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify password: %w", err)
	}
	if !valid || !found.IsActive {
		return LoginResult{}, ErrInvalidCredentials
	}

	rawToken, err := s.generateToken()
	if err != nil {
		return LoginResult{}, err
	}
	now = now.UTC()
	lifetime := s.lifetime
	if input.RememberMe {
		lifetime = s.rememberLifetime
	}
	session, err := s.sessions.Create(ctx, auth.CreateSessionParams{
		UserID:     found.ID,
		TokenHash:  auth.HashToken(rawToken),
		RememberMe: input.RememberMe,
		ExpiresAt:  now.Add(lifetime),
		LastSeenAt: now,
	}, now)
	if err != nil {
		return LoginResult{}, fmt.Errorf("create browser session: %w", err)
	}
	if err := s.users.UpdateLastLoginAt(ctx, found.ID, now); err != nil {
		s.logger.WarnContext(ctx, "update last login", "user_id", found.ID, "error", err)
	}
	return LoginResult{RawToken: rawToken, Session: session}, nil
}

func (s *Service) Register(ctx context.Context, input RegisterInput, now time.Time) (user.User, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Username = user.NormalizeUsername(input.Username)
	if err := user.ValidateName(input.Name); err != nil {
		return user.User{}, err
	}
	if err := user.ValidateUsername(input.Username); err != nil {
		return user.User{}, err
	}
	if input.Password != input.PasswordConfirmation {
		return user.User{}, ErrPasswordConfirmation
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		return user.User{}, err
	}
	role, err := s.roles.FindRoleBySlug(ctx, access.UserRoleSlug)
	if err != nil {
		return user.User{}, fmt.Errorf("find registration role: %w", err)
	}
	created, err := s.users.Create(ctx, user.CreateParams{
		Username:     input.Username,
		Name:         input.Name,
		PasswordHash: passwordHash,
		RoleID:       role.ID,
		IsActive:     true,
	}, now.UTC())
	if err != nil {
		return user.User{}, fmt.Errorf("create registered user: %w", err)
	}
	return created, nil
}

func (s *Service) ResolveSession(ctx context.Context, tokenHash [32]byte, now time.Time) (Principal, error) {
	now = now.UTC()
	session, err := s.sessions.FindValidByTokenHash(ctx, tokenHash, now)
	if errors.Is(err, auth.ErrSessionNotFound) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("find browser session: %w", err)
	}

	actor, found, err := s.findIdentity(ctx, session.UserID)
	if err != nil {
		return Principal{}, fmt.Errorf("find session actor: %w", err)
	}
	if !found || !actor.User.IsActive {
		return Principal{}, s.revokeUnauthenticated(ctx, tokenHash)
	}

	effective := actor
	if session.ImpersonatedUserID != nil {
		if !access.IsAdminRole(actor.Role.Slug) {
			return Principal{}, s.revokeUnauthenticated(ctx, tokenHash)
		}
		effective, found, err = s.findIdentity(ctx, *session.ImpersonatedUserID)
		if err != nil {
			return Principal{}, fmt.Errorf("find impersonated session target: %w", err)
		}
		if !found || !effective.User.IsActive || access.IsAdminRole(effective.Role.Slug) {
			return Principal{}, s.revokeUnauthenticated(ctx, tokenHash)
		}
	}
	permissions := access.NewPermissionSet(nil)
	if !access.IsAdminRole(effective.Role.Slug) {
		keys, err := s.roles.ListPermissionKeysForRole(ctx, effective.Role.ID)
		if err != nil {
			return Principal{}, fmt.Errorf("list session role permissions: %w", err)
		}
		permissions = access.NewPermissionSet(keys)
	}
	if now.Sub(session.LastSeenAt) >= LastSeenTouchInterval {
		if err := s.sessions.UpdateLastSeenAt(ctx, session.ID, now); err != nil {
			s.logger.WarnContext(ctx, "update session activity", "session_id", session.ID, "error", err)
		}
	}

	return Principal{
		UserID:          effective.User.ID,
		Username:        effective.User.Username,
		Name:            effective.User.Name,
		RoleID:          effective.Role.ID,
		RoleName:        effective.Role.Name,
		RoleSlug:        effective.Role.Slug,
		Permissions:     permissions,
		SessionID:       session.ID,
		RememberMe:      session.RememberMe,
		Actor:           actor.Identity(),
		IsImpersonating: session.ImpersonatedUserID != nil,
	}, nil
}

type resolvedIdentity struct {
	User user.User
	Role access.Role
}

func (identity resolvedIdentity) Identity() Identity {
	return Identity{
		UserID: identity.User.ID, Username: identity.User.Username, Name: identity.User.Name,
		RoleID: identity.Role.ID, RoleName: identity.Role.Name, RoleSlug: identity.Role.Slug,
	}
}

func (s *Service) findIdentity(ctx context.Context, userID uint64) (resolvedIdentity, bool, error) {
	found, err := s.users.FindByID(ctx, userID)
	if errors.Is(err, user.ErrNotFound) {
		return resolvedIdentity{}, false, nil
	}
	if err != nil {
		return resolvedIdentity{}, false, err
	}
	role, err := s.roles.FindRoleByID(ctx, found.RoleID)
	if errors.Is(err, access.ErrNotFound) {
		return resolvedIdentity{}, false, nil
	}
	if err != nil {
		return resolvedIdentity{}, false, err
	}
	return resolvedIdentity{User: found, Role: role}, true, nil
}

func (s *Service) Logout(ctx context.Context, tokenHash [32]byte) error {
	if err := s.sessions.Revoke(ctx, tokenHash); err != nil {
		return fmt.Errorf("revoke browser session: %w", err)
	}
	return nil
}

func (s *Service) revokeUnauthenticated(ctx context.Context, tokenHash [32]byte) error {
	if err := s.sessions.Revoke(ctx, tokenHash); err != nil {
		return fmt.Errorf("revoke unusable session: %w", err)
	}
	return ErrUnauthenticated
}
