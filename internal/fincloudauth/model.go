package fincloudauth

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ibldzn/go-admin/internal/fincloud"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
	StatusArchived Status = "archived"
)

var (
	ErrNotFound              = errors.New("Fincloud Auth Profile not found")
	ErrConflict              = errors.New("Fincloud Auth Profile changed")
	ErrInvalid               = errors.New("invalid Fincloud Auth Profile")
	ErrInactive              = errors.New("Fincloud Auth Profile is inactive")
	ErrConfigurationRequired = errors.New("Fincloud authentication configuration is required")
)

type Profile struct {
	ID              uint64    `db:"id"`
	Name            string    `db:"name"`
	Username        string    `db:"username"`
	RoleID          string    `db:"role_id"`
	LocationID      string    `db:"location_id"`
	Status          Status    `db:"status"`
	Revision        uint64    `db:"revision"`
	CreatedByUserID uint64    `db:"created_by_user_id"`
	UpdatedByUserID uint64    `db:"updated_by_user_id"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
}

type Input struct {
	Name, Username, Password, RoleID, LocationID string
}

type secretRow struct {
	Profile
	PasswordCiphertext []byte `db:"password_ciphertext"`
}

func validateInput(input Input, passwordRequired bool) error {
	if err := validateDisplay("name", input.Name); err != nil {
		return err
	}
	for label, value := range map[string]string{"username": input.Username, "role ID": input.RoleID, "location ID": input.LocationID} {
		if value == "" || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > 128 {
			return fmt.Errorf("%w: %s must be 1-128 characters without leading or trailing whitespace", ErrInvalid, label)
		}
	}
	if passwordRequired && input.Password == "" {
		return fmt.Errorf("%w: password is required", ErrInvalid)
	}
	return nil
}

func validateDisplay(label, value string) error {
	if value == "" || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > 128 {
		return fmt.Errorf("%w: %s must be 1-128 characters", ErrInvalid, label)
	}
	return nil
}

func authContext(row secretRow, password string) fincloud.AuthContext {
	return fincloud.AuthContext{ProfileID: row.ID, Revision: row.Revision, ProfileName: row.Name, Username: row.Username,
		Password: password, RoleID: row.RoleID, LocationID: row.LocationID}
}
