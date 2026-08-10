package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/user"
)

type fakeRoleFinder struct {
	role access.Role
	err  error
}

func (finder fakeRoleFinder) FindRoleBySlug(context.Context, string) (access.Role, error) {
	return finder.role, finder.err
}

type fakeUserCreator struct {
	params user.CreateParams
	err    error
}

func (creator *fakeUserCreator) Create(_ context.Context, params user.CreateParams, now time.Time) (user.User, error) {
	creator.params = params
	if creator.err != nil {
		return user.User{}, creator.err
	}
	return user.User{ID: 1, Username: params.Username, Name: params.Name, RoleID: params.RoleID, IsActive: params.IsActive, CreatedAt: now}, nil
}

func TestCreateAdministrator(t *testing.T) {
	users := &fakeUserCreator{}
	created, err := CreateAdministrator(
		context.Background(),
		fakeRoleFinder{role: access.Role{ID: 9, Slug: access.AdminRoleSlug}},
		users,
		AdministratorInput{
			Username:             "  ADMIN  ",
			Name:                 "  Administrator  ",
			Password:             "correct horse battery staple",
			PasswordConfirmation: "correct horse battery staple",
		},
		time.Date(2026, 8, 9, 1, 2, 3, 0, time.FixedZone("test", 7*60*60)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Username != "admin" || users.params.Name != "Administrator" || users.params.RoleID != 9 || !users.params.IsActive {
		t.Fatalf("unexpected created administrator: %+v params=%+v", created, users.params)
	}
	verified, err := auth.VerifyPassword("correct horse battery staple", users.params.PasswordHash)
	if err != nil || !verified {
		t.Fatalf("expected stored Argon2id hash, got verified=%v err=%v", verified, err)
	}
	if created.CreatedAt.Location() != time.UTC {
		t.Fatalf("expected UTC creation time, got %v", created.CreatedAt.Location())
	}
}

func TestCreateAdministratorValidationAndErrors(t *testing.T) {
	_, err := CreateAdministrator(
		context.Background(),
		fakeRoleFinder{role: access.Role{ID: 9}},
		&fakeUserCreator{},
		AdministratorInput{Username: "admin", Name: "Admin", Password: "correct horse battery staple", PasswordConfirmation: "different password value"},
		time.Now(),
	)
	if !errors.Is(err, ErrPasswordConfirmation) {
		t.Fatalf("expected confirmation error, got %v", err)
	}

	users := &fakeUserCreator{err: user.ErrUsernameTaken}
	_, err = CreateAdministrator(
		context.Background(),
		fakeRoleFinder{role: access.Role{ID: 9}},
		users,
		AdministratorInput{Username: "admin", Name: "Admin", Password: "correct horse battery staple", PasswordConfirmation: "correct horse battery staple"},
		time.Now(),
	)
	if !errors.Is(err, user.ErrUsernameTaken) {
		t.Fatalf("expected duplicate username error, got %v", err)
	}
}
