//go:build integration

package fincloudauth_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	sourcesfeature "github.com/ibldzn/go-admin/internal/features/sources"
	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/fincloudauth"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/secretcrypto"
	"github.com/ibldzn/go-admin/internal/securityctx"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func TestProfilePersistenceFreezeAndConfigurationFailureSemantics(t *testing.T) {
	db := integrationdb.Open(t)
	repository, requester, cleanup := authRepository(t, db)
	t.Cleanup(cleanup)
	primary := createActiveProfile(t, repository, requester, "Primary", "CaseSensitive", "old-secret", "Role-A", "Location-01")
	secondary := createActiveProfile(t, repository, requester, "Secondary", "CaseSensitive", "new-secret", "Role-B", "Location-02")

	var ciphertext []byte
	if err := db.Get(&ciphertext, `SELECT password_ciphertext FROM fincloud_auth_profiles WHERE id=?`, primary.ID); err != nil || len(ciphertext) == 0 || ciphertext[0] != 2 {
		t.Fatalf("profile ciphertext version=%v error=%v", ciphertext, err)
	}
	updated, err := repository.Update(context.Background(), requester, primary.ID, primary.Revision,
		fincloudauth.Input{Name: primary.Name, Username: primary.Username, RoleID: primary.RoleID, LocationID: primary.LocationID}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	auth, err := repository.Auth(context.Background(), primary.ID, updated.Revision, true)
	if err != nil || auth.Password != "old-secret" || auth.Username != "CaseSensitive" {
		t.Fatalf("preserved auth=%+v error=%v", auth, err)
	}
	primary = updated

	catalog, _ := ingestion.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	key := "cif_opening_report"
	rememberSource(t, db, key)
	if _, err := db.Exec(`UPDATE source_settings SET enabled=TRUE,fincloud_auth_profile_id=? WHERE source_id=?`, primary.ID, key); err != nil {
		t.Fatal(err)
	}

	// Mutation holds the source lock and commits first; the waiting freeze must capture the new binding.
	run, owner := claimedRun(t, runs, key)
	mutation, err := db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var lockedSource string
	if err := mutation.Get(&lockedSource, `SELECT source_id FROM source_settings WHERE source_id=? FOR UPDATE`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := mutation.Exec(`UPDATE source_settings SET fincloud_auth_profile_id=? WHERE source_id=?`, secondary.ID, key); err != nil {
		t.Fatal(err)
	}
	frozen := make(chan frozenResult, 1)
	go func() {
		value, freezeErr := repository.ResolveAndFreezeRunAuth(context.Background(), run.ID, owner, key)
		frozen <- frozenResult{value, freezeErr}
	}()
	assertBlocked(t, frozen)
	if err := mutation.Commit(); err != nil {
		t.Fatal(err)
	}
	first := <-frozen
	if first.err != nil || first.auth.ProfileID != secondary.ID || first.auth.Password != "new-secret" {
		t.Fatalf("mutation-first freeze=%+v error=%v", first.auth, first.err)
	}
	assertFrozenSnapshot(t, db, run.ID, secondary)
	if err := runs.Finish(context.Background(), run.ID, owner, ingestionrun.StatusFailed, ingestionrun.SafeError{Class: "test", Message: "test", Step: "test"}); err != nil {
		t.Fatal(err)
	}

	// Freeze takes source then profile locks; a concurrent assignment waits and affects only subsequent runs.
	if _, err := db.Exec(`UPDATE source_settings SET fincloud_auth_profile_id=? WHERE source_id=?`, primary.ID, key); err != nil {
		t.Fatal(err)
	}
	run, owner = claimedRun(t, runs, key)
	profileLock, err := db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var lockedProfile uint64
	if err := profileLock.Get(&lockedProfile, `SELECT id FROM fincloud_auth_profiles WHERE id=? FOR UPDATE`, primary.ID); err != nil {
		t.Fatal(err)
	}
	frozen = make(chan frozenResult, 1)
	go func() {
		value, freezeErr := repository.ResolveAndFreezeRunAuth(context.Background(), run.ID, owner, key)
		frozen <- frozenResult{value, freezeErr}
	}()
	time.Sleep(50 * time.Millisecond)
	sourceService, _ := sourcesfeature.NewService(db)
	assigned := make(chan error, 1)
	go func() {
		assigned <- sourceService.SetAuthProfile(context.Background(), key, &primary.ID, &secondary.ID, requester.Effective.UserID, requester)
	}()
	assertBlocked(t, frozen)
	assertErrorBlocked(t, assigned)
	if err := profileLock.Commit(); err != nil {
		t.Fatal(err)
	}
	second := <-frozen
	if second.err != nil || second.auth.ProfileID != primary.ID || second.auth.Password != "old-secret" {
		t.Fatalf("freeze-first auth=%+v error=%v", second.auth, second.err)
	}
	if err := <-assigned; err != nil {
		t.Fatal(err)
	}
	assertFrozenSnapshot(t, db, run.ID, primary)
	if err := runs.Finish(context.Background(), run.ID, owner, ingestionrun.StatusFailed, ingestionrun.SafeError{Class: "test", Message: "test", Step: "test"}); err != nil {
		t.Fatal(err)
	}
	var bound uint64
	if err := db.Get(&bound, `SELECT fincloud_auth_profile_id FROM source_settings WHERE source_id=?`, key); err != nil || bound != secondary.ID {
		t.Fatalf("subsequent binding=%d error=%v", bound, err)
	}

	// Captured invalid ciphertext fails after the fenced nonsecret snapshot commits.
	if _, err := db.Exec(`UPDATE fincloud_auth_profiles SET password_ciphertext=X'02' WHERE id=?`, secondary.ID); err != nil {
		t.Fatal(err)
	}
	run, owner = claimedRun(t, runs, key)
	if _, err := repository.ResolveAndFreezeRunAuth(context.Background(), run.ID, owner, key); !errors.Is(err, fincloudauth.ErrConfigurationRequired) {
		t.Fatalf("decrypt error=%v", err)
	}
	assertFrozenSnapshot(t, db, run.ID, secondary)
}

func TestManualSchedulerAndRunAllDistinguishConfigurationFromDisabledSource(t *testing.T) {
	db := integrationdb.Open(t)
	_, requester, cleanup := authRepository(t, db)
	t.Cleanup(cleanup)
	catalog, _ := ingestion.NewCatalog()
	runs, _ := ingestionrun.NewRepository(db, catalog)
	key := catalog.Jobs()[0].Key
	rememberAllSources(t, db)
	if _, err := db.Exec(`UPDATE source_settings SET enabled=FALSE,fincloud_auth_profile_id=NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_settings SET enabled=TRUE WHERE source_id=?`, key); err != nil {
		t.Fatal(err)
	}
	date, _ := ingestion.ParseCalendarDate("2026-09-01")
	parameters, _ := ingestionrun.NewRangeExecution(key, date, date)
	if _, err := runs.SubmitManual(context.Background(), key, parameters, ingestionrun.TriggerDirect, "manual", requester); !errors.Is(err, ingestionrun.ErrSourceConfigurationRequired) {
		t.Fatalf("manual preflight error=%v", err)
	}
	tx, _ := db.BeginTxx(context.Background(), nil)
	if _, err := runs.SubmitInTx(context.Background(), tx, key, parameters, ingestionrun.TriggerScheduler, "schedule:test", nil); !errors.Is(err, ingestionrun.ErrSourceConfigurationRequired) {
		t.Fatalf("scheduler preflight error=%v", err)
	}
	_ = tx.Rollback()

	parentID, err := runs.CreateRunAll(context.Background(), date, date, ingestionrun.TriggerDirect, "run-all-config", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := runs.Get(context.Background(), parentID)
	changed, err := runs.ReconcileParent(context.Background(), parentID, parent.OwnerID)
	if err != nil || !changed {
		t.Fatalf("queue child changed=%v error=%v", changed, err)
	}
	owner, _ := ingestionrun.NewOwnerID()
	child, err := runs.Claim(context.Background(), owner)
	if err != nil || child == nil || child.ParentRunID == nil || *child.ParentRunID != parentID {
		t.Fatalf("claim child=%+v error=%v", child, err)
	}
	if err := runs.Finish(context.Background(), child.ID, owner, ingestionrun.StatusFailed,
		ingestionrun.SafeError{Class: "configuration", Message: "Fincloud authentication configuration is required", Step: "resolve_fincloud_auth"}); err != nil {
		t.Fatal(err)
	}
	for {
		changed, err := runs.ReconcileParent(context.Background(), parentID, parent.OwnerID)
		if err != nil {
			t.Fatal(err)
		}
		parent, _ = runs.Get(context.Background(), parentID)
		if ingestionrun.IsTerminal(parent.Status) {
			break
		}
		if !changed {
			t.Fatal("Run All reconciliation stopped before terminal state")
		}
	}
	if parent.Status != ingestionrun.StatusFailed {
		t.Fatalf("Run All status=%s", parent.Status)
	}
	var disabledSkips, configFailures int
	if err := db.QueryRowx(`SELECT SUM(status='skipped' AND skip_reason='source_disabled'),SUM(status='failed' AND error_class='configuration')
		FROM ingestion_runs WHERE parent_run_id=?`, parentID).Scan(&disabledSkips, &configFailures); err != nil || disabledSkips == 0 || configFailures != 1 {
		t.Fatalf("disabled skips=%d configuration failures=%d error=%v", disabledSkips, configFailures, err)
	}
}

type frozenResult struct {
	auth fincloud.AuthContext
	err  error
}

func authRepository(t *testing.T, db *sqlx.DB) (*fincloudauth.Repository, securityctx.Requester, func()) {
	t.Helper()
	role := integrationdb.CustomRole(t, db, "Auth test", fmt.Sprintf("auth-test-%d", time.Now().UnixNano()))
	user := integrationdb.User(t, db, fmt.Sprintf("auth-test-%d", time.Now().UnixNano()), role.ID, true)
	requester := integrationdb.Requester(user, role)
	var key [32]byte
	key[0] = 47
	repository, err := fincloudauth.NewRepository(db, secretcrypto.New(key))
	if err != nil {
		t.Fatal(err)
	}
	return repository, requester, func() {
		_, _ = db.Exec(`DELETE FROM ingestion_run_errors WHERE run_id IN (SELECT id FROM ingestion_runs WHERE trigger_reference LIKE 'auth-test-%' OR trigger_reference='run-all-config')`)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE parent_run_id IN (SELECT id FROM ingestion_runs WHERE trigger_reference='run-all-config')`)
		_, _ = db.Exec(`DELETE FROM ingestion_runs WHERE trigger_reference LIKE 'auth-test-%' OR trigger_reference='run-all-config'`)
		_, _ = db.Exec(`UPDATE source_settings SET fincloud_auth_profile_id=NULL WHERE fincloud_auth_profile_id IN (SELECT id FROM fincloud_auth_profiles WHERE created_by_user_id=?)`, user.ID)
		_, _ = db.Exec(`DELETE FROM audit_logs WHERE actor_user_id=? OR effective_user_id=?`, user.ID, user.ID)
		_, _ = db.Exec(`DELETE FROM fincloud_auth_profiles WHERE created_by_user_id=?`, user.ID)
		_, _ = db.Exec(`DELETE FROM users WHERE id=?`, user.ID)
		_, _ = db.Exec(`DELETE FROM roles WHERE id=?`, role.ID)
	}
}

func createActiveProfile(t *testing.T, repository *fincloudauth.Repository, requester securityctx.Requester, name, username, password, roleID, locationID string) fincloudauth.Profile {
	t.Helper()
	profile, err := repository.Create(context.Background(), requester, fincloudauth.Input{Name: name, Username: username, Password: password, RoleID: roleID, LocationID: locationID}, integrationdb.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SetStatus(context.Background(), requester, profile.ID, profile.Revision, fincloudauth.StatusActive, integrationdb.Now()); err != nil {
		t.Fatal(err)
	}
	profile, err = repository.Find(context.Background(), profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func claimedRun(t *testing.T, runs *ingestionrun.Repository, key string) (ingestionrun.Run, string) {
	t.Helper()
	date, _ := ingestion.ParseCalendarDate("2026-09-01")
	parameters, _ := ingestionrun.NewRangeExecution(key, date, date)
	id, err := runs.Submit(context.Background(), key, parameters, ingestionrun.TriggerDirect, fmt.Sprintf("auth-test-%d", time.Now().UnixNano()), nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := ingestionrun.NewOwnerID()
	run, err := runs.Claim(context.Background(), owner)
	if err != nil || run == nil || run.ID != id {
		t.Fatalf("claim=%+v error=%v", run, err)
	}
	return *run, owner
}

func assertFrozenSnapshot(t *testing.T, db *sqlx.DB, runID uint64, profile fincloudauth.Profile) {
	t.Helper()
	var row struct {
		ID       uint64 `db:"fincloud_auth_profile_id"`
		Revision uint64 `db:"fincloud_auth_profile_revision"`
		Username string `db:"fincloud_auth_username"`
	}
	if err := db.Get(&row, `SELECT fincloud_auth_profile_id,fincloud_auth_profile_revision,fincloud_auth_username FROM ingestion_runs WHERE id=?`, runID); err != nil || row.ID != profile.ID || row.Revision != profile.Revision || row.Username != profile.Username {
		t.Fatalf("snapshot=%+v profile=%+v error=%v", row, profile, err)
	}
}

func assertBlocked(t *testing.T, channel <-chan frozenResult) {
	t.Helper()
	select {
	case result := <-channel:
		t.Fatalf("freeze completed before lock release: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
}

func assertErrorBlocked(t *testing.T, channel <-chan error) {
	t.Helper()
	select {
	case err := <-channel:
		t.Fatalf("mutation completed before freeze: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

type sourceState struct {
	Key       string        `db:"source_id"`
	Enabled   bool          `db:"enabled"`
	ProfileID sql.NullInt64 `db:"fincloud_auth_profile_id"`
}

func rememberSource(t *testing.T, db *sqlx.DB, key string) {
	t.Helper()
	var state sourceState
	if err := db.Get(&state, `SELECT source_id,enabled,fincloud_auth_profile_id FROM source_settings WHERE source_id=?`, key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE source_settings SET enabled=?,fincloud_auth_profile_id=? WHERE source_id=?`, state.Enabled, nullableID(state.ProfileID), state.Key)
	})
}

func rememberAllSources(t *testing.T, db *sqlx.DB) {
	t.Helper()
	var states []sourceState
	if err := db.Select(&states, `SELECT source_id,enabled,fincloud_auth_profile_id FROM source_settings`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, state := range states {
			_, _ = db.Exec(`UPDATE source_settings SET enabled=?,fincloud_auth_profile_id=? WHERE source_id=?`, state.Enabled, nullableID(state.ProfileID), state.Key)
		}
	})
}

func nullableID(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}
