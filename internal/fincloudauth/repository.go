package fincloudauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/secretcrypto"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

const profileColumns = `id,name,username,role_id,location_id,status,revision,created_by_user_id,updated_by_user_id,created_at,updated_at`

type Repository struct {
	db     *sqlx.DB
	cipher *secretcrypto.Cipher
}

func NewRepository(db *sqlx.DB, cipher *secretcrypto.Cipher) (*Repository, error) {
	if db == nil || cipher == nil {
		return nil, fmt.Errorf("Fincloud Auth Profile database and cipher are required")
	}
	return &Repository{db: db, cipher: cipher}, nil
}

func (repository *Repository) List(ctx context.Context) ([]Profile, error) {
	rows := []Profile{}
	if err := repository.db.SelectContext(ctx, &rows, `SELECT `+profileColumns+` FROM fincloud_auth_profiles ORDER BY name,id`); err != nil {
		return nil, fmt.Errorf("list Fincloud Auth Profiles: %w", err)
	}
	return rows, nil
}

func (repository *Repository) ListAssignable(ctx context.Context) ([]Profile, error) {
	rows := []Profile{}
	if err := repository.db.SelectContext(ctx, &rows, `SELECT `+profileColumns+` FROM fincloud_auth_profiles WHERE status<>'archived' ORDER BY name,id`); err != nil {
		return nil, fmt.Errorf("list assignable Fincloud Auth Profiles: %w", err)
	}
	return rows, nil
}

func (repository *Repository) Find(ctx context.Context, id uint64) (Profile, error) {
	var value Profile
	if err := repository.db.GetContext(ctx, &value, `SELECT `+profileColumns+` FROM fincloud_auth_profiles WHERE id=?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Profile{}, ErrNotFound
		}
		return Profile{}, fmt.Errorf("find Fincloud Auth Profile: %w", err)
	}
	return value, nil
}

func (repository *Repository) Create(ctx context.Context, requester securityctx.Requester, input Input, now time.Time) (Profile, error) {
	if err := validateInput(input, true); err != nil {
		return Profile{}, err
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return Profile{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO fincloud_auth_profiles
		(name,username,role_id,location_id,password_ciphertext,status,created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES (?,?,?,?,NULL,'disabled',?,?,?,?)`, input.Name, input.Username, input.RoleID, input.LocationID,
		requester.Effective.UserID, requester.Effective.UserID, now.UTC(), now.UTC())
	if err != nil {
		return Profile{}, fmt.Errorf("insert Fincloud Auth Profile: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Profile{}, err
	}
	ciphertext, err := repository.cipher.Encrypt(secretcrypto.PurposeFincloudAuthPassword, uint64(id), input.Password)
	if err != nil {
		return Profile{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fincloud_auth_profiles SET password_ciphertext=? WHERE id=?`, ciphertext, id); err != nil {
		return Profile{}, err
	}
	metadata := audit.FincloudAuthProfileUpdatedMetadata{Name: input.Name, Username: input.Username, RoleID: input.RoleID,
		LocationID: input.LocationID, Status: string(StatusDisabled), Revision: 1, PasswordChanged: true}
	if err := appendAudit(ctx, tx, requester, audit.ActionFincloudAuthProfileCreated, uint64(id), metadata, now); err != nil {
		return Profile{}, err
	}
	if err := tx.Commit(); err != nil {
		return Profile{}, err
	}
	return repository.Find(ctx, uint64(id))
}

func (repository *Repository) Update(ctx context.Context, requester securityctx.Requester, id, expectedRevision uint64, input Input, now time.Time) (Profile, error) {
	if err := validateInput(input, false); err != nil {
		return Profile{}, err
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return Profile{}, err
	}
	defer tx.Rollback()
	existing, err := lockProfile(ctx, tx, id)
	if err != nil {
		return Profile{}, err
	}
	if existing.Revision != expectedRevision {
		return Profile{}, ErrConflict
	}
	if existing.Status == StatusArchived {
		return Profile{}, ErrInactive
	}
	ciphertext := existing.PasswordCiphertext
	if input.Password != "" {
		ciphertext, err = repository.cipher.Encrypt(secretcrypto.PurposeFincloudAuthPassword, id, input.Password)
		if err != nil {
			return Profile{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE fincloud_auth_profiles SET name=?,username=?,role_id=?,location_id=?,password_ciphertext=?,
		revision=revision+1,updated_by_user_id=?,updated_at=? WHERE id=? AND revision=?`, input.Name, input.Username, input.RoleID,
		input.LocationID, ciphertext, requester.Effective.UserID, now.UTC(), id, expectedRevision)
	if err != nil {
		return Profile{}, fmt.Errorf("update Fincloud Auth Profile: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Profile{}, ErrConflict
	}
	metadata := audit.FincloudAuthProfileUpdatedMetadata{Name: input.Name, Username: input.Username, RoleID: input.RoleID,
		LocationID: input.LocationID, Status: string(existing.Status), Revision: expectedRevision + 1, PasswordChanged: input.Password != ""}
	if err := appendAudit(ctx, tx, requester, audit.ActionFincloudAuthProfileUpdated, id, metadata, now); err != nil {
		return Profile{}, err
	}
	if err := tx.Commit(); err != nil {
		return Profile{}, err
	}
	return repository.Find(ctx, id)
}

func (repository *Repository) SetStatus(ctx context.Context, requester securityctx.Requester, id, expectedRevision uint64, status Status, now time.Time) error {
	if status != StatusActive && status != StatusDisabled && status != StatusArchived {
		return fmt.Errorf("%w: invalid status", ErrInvalid)
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing, err := lockProfile(ctx, tx, id)
	if err != nil {
		return err
	}
	if existing.Revision != expectedRevision {
		return ErrConflict
	}
	if existing.Status == StatusArchived {
		return ErrInactive
	}
	result, err := tx.ExecContext(ctx, `UPDATE fincloud_auth_profiles SET status=?,revision=revision+1,updated_by_user_id=?,updated_at=?
		WHERE id=? AND revision=?`, status, requester.Effective.UserID, now.UTC(), id, expectedRevision)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrConflict
	}
	if err := appendAudit(ctx, tx, requester, audit.ActionFincloudAuthProfileStateChanged, id,
		audit.FincloudAuthProfileStateMetadata{From: string(existing.Status), To: string(status), Revision: expectedRevision + 1}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (repository *Repository) Auth(ctx context.Context, id, expectedRevision uint64, requireActive bool) (fincloud.AuthContext, error) {
	var row secretRow
	if err := repository.db.GetContext(ctx, &row, `SELECT `+profileColumns+`,password_ciphertext FROM fincloud_auth_profiles WHERE id=?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fincloud.AuthContext{}, ErrNotFound
		}
		return fincloud.AuthContext{}, err
	}
	if expectedRevision != 0 && row.Revision != expectedRevision {
		return fincloud.AuthContext{}, ErrConflict
	}
	if row.Status == StatusArchived || requireActive && row.Status != StatusActive {
		return fincloud.AuthContext{}, ErrInactive
	}
	password, err := repository.cipher.Decrypt(secretcrypto.PurposeFincloudAuthPassword, row.ID, row.PasswordCiphertext)
	if err != nil || password == "" {
		return fincloud.AuthContext{}, fmt.Errorf("%w: profile secret cannot be decrypted", ErrConfigurationRequired)
	}
	auth := authContext(row, password)
	if err := auth.Validate(); err != nil {
		return fincloud.AuthContext{}, fmt.Errorf("%w: profile fields are invalid", ErrConfigurationRequired)
	}
	return auth, nil
}

func (repository *Repository) ResolveAndFreezeRunAuth(ctx context.Context, runID uint64, ownerID, jobKey string) (fincloud.AuthContext, error) {
	if runID == 0 || ownerID == "" || jobKey == "" {
		return fincloud.AuthContext{}, fmt.Errorf("run owner and job are required")
	}
	tx, err := repository.db.BeginTxx(ctx, nil)
	if err != nil {
		return fincloud.AuthContext{}, err
	}
	defer tx.Rollback()
	var source struct {
		Enabled   bool          `db:"enabled"`
		ProfileID sql.NullInt64 `db:"fincloud_auth_profile_id"`
	}
	if err := tx.GetContext(ctx, &source, `SELECT enabled,fincloud_auth_profile_id FROM source_settings WHERE source_id=? FOR UPDATE`, jobKey); err != nil {
		return fincloud.AuthContext{}, err
	}
	if !source.Enabled {
		return fincloud.AuthContext{}, ingestionrun.ErrSourceDisabled
	}
	if !source.ProfileID.Valid {
		return fincloud.AuthContext{}, ErrConfigurationRequired
	}
	var row secretRow
	if err := tx.GetContext(ctx, &row, `SELECT `+profileColumns+`,password_ciphertext FROM fincloud_auth_profiles WHERE id=? FOR SHARE`, source.ProfileID.Int64); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fincloud.AuthContext{}, ErrConfigurationRequired
		}
		return fincloud.AuthContext{}, err
	}
	if row.Status != StatusActive {
		return fincloud.AuthContext{}, ErrConfigurationRequired
	}
	if err := validateInput(Input{Name: row.Name, Username: row.Username, RoleID: row.RoleID, LocationID: row.LocationID}, false); err != nil || len(row.PasswordCiphertext) == 0 {
		return fincloud.AuthContext{}, ErrConfigurationRequired
	}
	result, err := tx.ExecContext(ctx, `UPDATE ingestion_runs SET fincloud_auth_profile_id=?,fincloud_auth_profile_revision=?,
		fincloud_auth_profile_name=?,fincloud_auth_username=?,fincloud_auth_role_id=?,fincloud_auth_location_id=?
		WHERE id=? AND job_key=? AND status='running' AND owner_id=? AND fincloud_auth_profile_id IS NULL`, row.ID, row.Revision,
		row.Name, row.Username, row.RoleID, row.LocationID, runID, jobKey, ownerID)
	if err != nil {
		return fincloud.AuthContext{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fincloud.AuthContext{}, ingestionrun.ErrOwnershipLost
	}
	ciphertext := append([]byte(nil), row.PasswordCiphertext...)
	if err := tx.Commit(); err != nil {
		return fincloud.AuthContext{}, err
	}
	password, err := repository.cipher.Decrypt(secretcrypto.PurposeFincloudAuthPassword, row.ID, ciphertext)
	if err != nil || password == "" {
		return fincloud.AuthContext{}, fmt.Errorf("%w: frozen profile secret cannot be decrypted", ErrConfigurationRequired)
	}
	auth := authContext(row, password)
	if err := auth.Validate(); err != nil {
		return fincloud.AuthContext{}, fmt.Errorf("%w: frozen profile fields are invalid", ErrConfigurationRequired)
	}
	return auth, nil
}

func lockProfile(ctx context.Context, tx *sqlx.Tx, id uint64) (secretRow, error) {
	var row secretRow
	if err := tx.GetContext(ctx, &row, `SELECT `+profileColumns+`,password_ciphertext FROM fincloud_auth_profiles WHERE id=? FOR UPDATE`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return secretRow{}, ErrNotFound
		}
		return secretRow{}, err
	}
	return row, nil
}

func (repository *Repository) RecordTest(ctx context.Context, requester securityctx.Requester, id, revision uint64, outcome string, now time.Time) error {
	return appendAudit(ctx, repository.db, requester, audit.ActionFincloudAuthProfileTested, id,
		audit.FincloudAuthProfileTestedMetadata{Revision: revision, Outcome: outcome}, now)
}

func appendAudit(ctx context.Context, executor sqlx.ExtContext, requester securityctx.Requester, action audit.Action, id uint64, metadata audit.Metadata, now time.Time) error {
	actor := audit.Identity{UserID: requester.Actor.UserID, Username: requester.Actor.Username}
	effective := audit.Identity{UserID: requester.Effective.UserID, Username: requester.Effective.Username}
	return audit.Append(ctx, executor, audit.Event{Attribution: audit.Attribution{Actor: &actor, Effective: &effective}, Action: action,
		Resource: audit.ResourceFincloudAuthProfile, ResourceID: id, Metadata: metadata, CreatedAt: now.UTC()})
}
