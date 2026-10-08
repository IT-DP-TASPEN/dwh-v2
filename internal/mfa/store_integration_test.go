//go:build integration

package mfa

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/secretcrypto"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
)

func fixture(t *testing.T) (*Store, uint64, time.Time) {
	t.Helper()
	db := integrationdb.Open(t)
	integrationdb.Reset(t, db, []access.PermissionDefinition{{Key: "users.mfa.reset", Name: "Reset MFA", Group: "Users"}})
	role := integrationdb.Role(t, db, access.AdminRoleSlug)
	u := integrationdb.User(t, db, "mfa-admin", role.ID, true)
	return &Store{clock: func(input time.Time) time.Time { return input }, DB: db, Cipher: secretcrypto.New([32]byte{1}), Lifetime: time.Hour, RememberLifetime: 30 * 24 * time.Hour, IdleTimeout: 2 * time.Hour}, u.ID, integrationdb.Now()
}
func enroll(t *testing.T, s *Store, id uint64, now time.Time) (Verified, string) {
	t.Helper()
	issued, err := s.BeginLogin(context.Background(), id, "integration-hash", false, "/", now)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Challenge.Purpose != Enrollment {
		t.Fatal("pending enrollment")
	}
	secret, err := s.PendingSecret(issued.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := Code(secret, now.Unix()/30)
	result, err := s.Verify(context.Background(), issued.Token, code, 0, "", now)
	if err != nil {
		t.Fatal(err)
	}
	return result, secret
}
func count(t *testing.T, db *sqlx.DB, table string) int {
	t.Helper()
	var n int
	if err := db.Get(&n, "SELECT COUNT(*) FROM "+table); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestEnrollmentStorageAndOneTimeChallenge(t *testing.T) {
	s, id, now := fixture(t)
	ctx := context.Background()
	issued, err := s.BeginLogin(ctx, id, "integration-hash", true, "/reports", now)
	if err != nil {
		t.Fatal(err)
	}
	if count(t, s.DB, "sessions") != 0 || count(t, s.DB, "user_totp_enrollments") != 0 {
		t.Fatal("password/pending enrollment authenticated")
	}
	secret, err := s.PendingSecret(issued.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err = s.DB.Get(&stored, `SELECT pending_secret FROM mfa_challenges WHERE id=?`, issued.Challenge.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), secret) || string(issued.Challenge.TokenHash) == issued.Token {
		t.Fatal("plaintext persisted")
	}
	if _, err = s.Verify(ctx, issued.Token, "invalid", 0, "", now); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	code, _ := Code(secret, now.Unix()/30)
	result, err := s.Verify(ctx, issued.Token, code, 0, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Session.MFAVerifiedAt.Equal(now) || !result.Session.RememberMe || !result.Session.ExpiresAt.Equal(now.Add(s.RememberLifetime)) || len(result.RecoveryCodes) != 10 {
		t.Fatal("session assurance/recovery/lifetime")
	}
	if count(t, s.DB, "user_mfa_recovery_codes") != 10 {
		t.Fatal("recovery count")
	}
	var active []byte
	s.DB.Get(&active, `SELECT secret_ciphertext FROM user_totp_enrollments WHERE user_id=?`, id)
	if strings.Contains(string(active), secret) {
		t.Fatal("active plaintext")
	}
	for _, recovery := range result.RecoveryCodes {
		var hash []byte
		if err = s.DB.Get(&hash, `SELECT code_hash FROM user_mfa_recovery_codes WHERE user_id=? AND code_hash=?`, id, RecoveryHashBytes(id, recovery)); err != nil || string(hash) == recovery || len(hash) != 32 {
			t.Fatal("hash storage", err)
		}
	}
	if _, err = s.Verify(ctx, issued.Token, code, 0, "", now); !errors.Is(err, ErrInvalid) {
		t.Fatal("reused challenge", err)
	}
	if _, err = s.Get(ctx, issued.Token, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("consumed material redisplay", err)
	}
	if count(t, s.DB, "sessions") != 1 {
		t.Fatal("duplicate session")
	}
}
func RecoveryHashBytes(id uint64, code string) []byte { h := RecoveryHash(id, code); return h[:] }
func TestLoginRecoveryReplayAndTOTPReplay(t *testing.T) {
	s, id, now := fixture(t)
	result, secret := enroll(t, s, id, now)
	ctx := context.Background()
	now = now.Add(30 * time.Second)
	issued, err := s.BeginLogin(ctx, id, "integration-hash", false, "/", now)
	if err != nil || issued.Challenge.Purpose != Login {
		t.Fatal(err)
	}
	if count(t, s.DB, "sessions") != 1 {
		t.Fatal("password created session")
	}
	code, _ := Code(secret, now.Unix()/30)
	verified, err := s.Verify(ctx, issued.Token, code, 0, "", now)
	if err != nil || verified.Session.MFAVerifiedAt.IsZero() {
		t.Fatal(err)
	}
	issued, _ = s.BeginLogin(ctx, id, "integration-hash", false, "/", now)
	if _, err = s.Verify(ctx, issued.Token, code, 0, "", now); !errors.Is(err, ErrInvalid) {
		t.Fatal("TOTP replay", err)
	}
	verified, err = s.Verify(ctx, issued.Token, result.RecoveryCodes[0], 0, "", now)
	if err != nil || verified.Session.ID == 0 {
		t.Fatal(err)
	}
	issued, _ = s.BeginLogin(ctx, id, "integration-hash", false, "/", now)
	if _, err = s.Verify(ctx, issued.Token, result.RecoveryCodes[0], 0, "", now); !errors.Is(err, ErrInvalid) {
		t.Fatal("recovery replay", err)
	}
}
func TestChallengeExpiryAndExhaustion(t *testing.T) {
	s, id, now := fixture(t)
	ctx := context.Background()
	issued, _ := s.BeginLogin(ctx, id, "integration-hash", false, "/", now)
	for i := 1; i <= 5; i++ {
		_, err := s.Verify(ctx, issued.Token, "invalid", 0, "", now)
		want := ErrInvalid
		if i == 5 {
			want = ErrExhausted
		}
		if !errors.Is(err, want) {
			t.Fatal(i, err)
		}
	}
	before := count(t, s.DB, "audit_logs")
	for i := 0; i < 8; i++ {
		s.Verify(ctx, issued.Token, "invalid", 0, "", now)
	}
	if count(t, s.DB, "audit_logs") != before || count(t, s.DB, "sessions") != 0 {
		t.Fatal("audit amplification or session")
	}
	issued, _ = s.BeginLogin(ctx, id, "integration-hash", false, "/", now)
	secret, _ := s.PendingSecret(issued.Challenge)
	code, _ := Code(secret, now.Add(ChallengeLifetime).Unix()/30)
	if _, err := s.Verify(ctx, issued.Token, code, 0, "", now.Add(ChallengeLifetime)); !errors.Is(err, ErrInvalid) {
		t.Fatal("expiry boundary", err)
	}
}
func TestConcurrentFactorsAndChallenge(t *testing.T) {
	for _, factor := range []string{"totp", "recovery", "challenge"} {
		t.Run(factor, func(t *testing.T) {
			s, id, now := fixture(t)
			result, secret := enroll(t, s, id, now)
			ctx := context.Background()
			now = now.Add(30 * time.Second)
			// Session-bound challenges allow two live challenges, exercising factor locks independently.
			second := integrationdb.Session(t, auth.NewSessionRepository(s.DB), id, false, "second-token", now)
			a, err := s.BeginAuthenticated(ctx, id, result.Session.ID, StepUp, "/", "", now)
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.BeginAuthenticated(ctx, id, second.ID, StepUp, "/", "", now)
			if err != nil {
				t.Fatal(err)
			}
			input, _ := Code(secret, now.Unix()/30)
			if factor == "recovery" {
				input = result.RecoveryCodes[0]
			}
			if factor == "challenge" {
				b = a
				second = result.Session
			}
			var success atomic.Int32
			start := make(chan struct{})
			var wg sync.WaitGroup
			for _, v := range []struct {
				token   string
				session uint64
			}{{a.Token, result.Session.ID}, {b.Token, second.ID}} {
				wg.Add(1)
				go func(token string, session uint64) {
					defer wg.Done()
					<-start
					if _, err := s.Verify(ctx, token, input, session, "", now); err == nil {
						success.Add(1)
					} else if !errors.Is(err, ErrInvalid) {
						t.Errorf("verify: %v", err)
					}
				}(v.token, v.session)
			}
			close(start)
			wg.Wait()
			if success.Load() != 1 {
				t.Fatalf("success=%d", success.Load())
			}
		})
	}
}
func TestStepUpFreshnessExhaustionAndActorFactor(t *testing.T) {
	s, id, now := fixture(t)
	result, secret := enroll(t, s, id, now)
	ctx := context.Background()
	now = now.Add(11 * time.Minute)
	target := integrationdb.User(t, s.DB, "target", integrationdb.Role(t, s.DB, access.UserRoleSlug).ID, true)
	if _, err := s.DB.Exec(`UPDATE sessions SET impersonated_user_id=? WHERE id=?`, target.ID, result.Session.ID); err != nil {
		t.Fatal(err)
	}
	issued, err := s.BeginAuthenticated(ctx, id, result.Session.ID, StepUp, "/reports", "", now)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := Code(secret, now.Unix()/30)
	if _, err = s.Verify(ctx, issued.Token, code, result.Session.ID, "", now); err != nil {
		t.Fatal(err)
	}
	var verified time.Time
	s.DB.Get(&verified, `SELECT mfa_verified_at FROM sessions WHERE id=?`, result.Session.ID)
	if !verified.Equal(now) {
		t.Fatal("freshness")
	}
	var effective uint64
	if err = s.DB.Get(&effective, `SELECT effective_user_id FROM audit_logs WHERE action='mfa.step_up' ORDER BY id DESC LIMIT 1`); err != nil || effective != target.ID {
		t.Fatal("attribution", err)
	}
	if _, err = s.BeginAuthenticated(ctx, id, result.Session.ID, RotateAuthorize, "/", "", now); !errors.Is(err, ErrInvalid) {
		t.Fatal("impersonated self management")
	}
	issued, err = s.BeginAuthenticated(ctx, id, result.Session.ID, StepUp, "/", "", now)
	if err != nil {
		t.Fatal(err)
	}
	// Dropping the cookie cannot replenish attempts or issue a replacement challenge.
	if _, err = s.BeginAuthenticated(ctx, id, result.Session.ID, StepUp, "/", "", now); !errors.Is(err, ErrPending) {
		t.Fatal("unbounded replacement", err)
	}
	for i := 0; i < 5; i++ {
		s.Verify(ctx, issued.Token, "invalid", result.Session.ID, "", now)
	}
	if count(t, s.DB, "sessions") != 0 {
		t.Fatal("exhausted step-up session survives")
	}
}
func TestRotationRegenerationAndOperatorReset(t *testing.T) {
	for _, purpose := range []string{RotateAuthorize, Regenerate, "operator"} {
		t.Run(purpose, func(t *testing.T) {
			s, id, now := fixture(t)
			result, oldSecret := enroll(t, s, id, now)
			ctx := context.Background()
			now = now.Add(time.Minute)
			if purpose == "operator" {
				if err := s.Reset(ctx, id, 0, 0, audit.Attribution{}, now); err != nil {
					t.Fatal(err)
				}
				if count(t, s.DB, "sessions") != 0 || count(t, s.DB, "user_totp_enrollments") != 0 || count(t, s.DB, "user_mfa_recovery_codes") != 0 || count(t, s.DB, "mfa_challenges") != 0 {
					t.Fatal("operator reset")
				}
				next, err := s.BeginLogin(ctx, id, "integration-hash", false, "/", now)
				if err != nil || next.Challenge.Purpose != Enrollment {
					t.Fatal("mandatory re-enrollment", err)
				}
				return
			}
			issued, err := s.BeginAuthenticated(ctx, id, result.Session.ID, purpose, "/", "", now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Verify(ctx, issued.Token, result.RecoveryCodes[0], result.Session.ID, "wrong-password-hash", now); !errors.Is(err, ErrInvalid) {
				t.Fatal("password required", err)
			}
			rotated, err := s.Verify(ctx, issued.Token, result.RecoveryCodes[0], result.Session.ID, "integration-hash", now)
			if err != nil {
				t.Fatal(err)
			}
			if purpose == RotateAuthorize {
				var encrypted []byte
				s.DB.Get(&encrypted, `SELECT secret_ciphertext FROM user_totp_enrollments WHERE user_id=?`, id)
				active, _ := s.Cipher.Decrypt(secretcrypto.PurposeMFATOTPActive, id, encrypted)
				if active != oldSecret {
					t.Fatal("premature rotation")
				}
				pending := rotated.Pending
				if pending == nil {
					t.Fatal("pending missing")
				}
				secret, _ := s.PendingSecret(pending.Challenge)
				code, _ := Code(secret, now.Unix()/30)
				rotated, err = s.Verify(ctx, pending.Token, code, result.Session.ID, "", now)
				if err != nil {
					t.Fatal(err)
				}
				s.DB.Get(&encrypted, `SELECT secret_ciphertext FROM user_totp_enrollments WHERE user_id=?`, id)
				active, _ = s.Cipher.Decrypt(secretcrypto.PurposeMFATOTPActive, id, encrypted)
				if active != secret || active == oldSecret {
					t.Fatal("rotation")
				}
			}
			if !rotated.Logout || len(rotated.RecoveryCodes) != 10 || count(t, s.DB, "sessions") != 0 || count(t, s.DB, "mfa_challenges") != 0 {
				t.Fatal("revocation/new codes")
			}
			var old int
			s.DB.Get(&old, `SELECT COUNT(*) FROM user_mfa_recovery_codes WHERE user_id=? AND code_hash=?`, id, RecoveryHashBytes(id, result.RecoveryCodes[1]))
			if old != 0 {
				t.Fatal("old codes retained")
			}
		})
	}
}

func TestConcurrentEnrollmentCreatesExactlyOneSession(t *testing.T) {
	s, id, now := fixture(t)
	issued, err := s.BeginLogin(context.Background(), id, "integration-hash", false, "/", now)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := s.PendingSecret(issued.Challenge)
	code, _ := Code(secret, now.Unix()/30)
	var success atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.Verify(context.Background(), issued.Token, code, 0, "", now); err == nil {
				success.Add(1)
			} else if !errors.Is(err, ErrInvalid) {
				t.Errorf("verify: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if success.Load() != 1 || count(t, s.DB, "sessions") != 1 || count(t, s.DB, "user_mfa_recovery_codes") != 10 {
		t.Fatal("duplicate enrollment/session")
	}
}

func TestExpiryRecheckedAfterLockWait(t *testing.T) {
	s, id, now := fixture(t)
	ctx := context.Background()
	issued, err := s.BeginLogin(ctx, id, "integration-hash", false, "/", now)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := s.PendingSecret(issued.Challenge)
	code, _ := Code(secret, now.Unix()/30)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	s.clock = func(time.Time) time.Time { return time.Unix(0, clock.Load()).UTC() }
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = lockUser(ctx, tx, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := s.Verify(ctx, issued.Token, code, 0, "", now); done <- err }()
	}
	clock.Store(now.Add(ChallengeLifetime).UnixNano())
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = <-done; !errors.Is(err, ErrInvalid) {
			t.Fatal("expired lock waiter accepted", err)
		}
	}
	if count(t, s.DB, "sessions") != 0 || count(t, s.DB, "user_totp_enrollments") != 0 {
		t.Fatal("expired challenge created session")
	}
}

func TestMigrationHardCutoverRevokesPasswordOnlySessions(t *testing.T) {
	s, id, now := fixture(t)
	ctx := context.Background()
	directory := filepath.Join(integrationdb.Root(t), "migrations")
	// Exercise the MFA cutover itself; newer migrations may be irreversible.
	const mfaVersion = 20261005120000
	migrations, err := goose.CollectMigrations(directory, mfaVersion-1, mfaVersion)
	if err != nil || len(migrations) != 1 {
		t.Fatalf("collect MFA migration: count=%d error=%v", len(migrations), err)
	}
	var originalID int64
	if err := s.DB.Get(&originalID, `SELECT id FROM goose_db_version WHERE version_id=?`, mfaVersion); err != nil {
		t.Fatal(err)
	}
	version, err := goose.GetDBVersionContext(ctx, s.DB.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations[0].DownContext(ctx, s.DB.DB); err != nil {
		t.Fatal(err)
	}
	applied := false
	restore := func() error {
		if !applied {
			if err := migrations[0].UpContext(ctx, s.DB.DB); err != nil {
				return err
			}
			applied = true
		}
		// Goose chooses current version by ledger ID. Restore this older
		// migration's position without touching newer migration records.
		_, err := s.DB.Exec(`UPDATE goose_db_version SET id=? WHERE version_id=?`, originalID, mfaVersion)
		return err
	}
	defer func() {
		if err := restore(); err != nil {
			t.Error(err)
		}
	}()
	token := auth.HashToken("pre-feature-session")
	if _, err := s.DB.Exec(`INSERT INTO sessions(user_id,token_hash,remember_me,expires_at,last_seen_at,created_at,updated_at) VALUES(?,?,FALSE,?,?,?,?)`, id, token[:], now.Add(time.Hour), now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if current, err := goose.GetDBVersionContext(ctx, s.DB.DB); err != nil || current != version {
		t.Fatalf("newer migration version changed: before=%d after=%d error=%v", version, current, err)
	}
	if count(t, s.DB, "sessions") != 0 {
		t.Fatal("pre-feature session survives rollout")
	}
	if _, err := s.DB.Exec(`INSERT INTO sessions(user_id,token_hash,remember_me,expires_at,last_seen_at,created_at,updated_at) VALUES(?,?,FALSE,?,?,?,?)`, id, token[:], now.Add(time.Hour), now, now, now); err == nil {
		t.Fatal("old binary can create password-only session after migration")
	}
}

func TestMFAAuditFailurePreservesAttemptBudgetAndRollsBackSuccess(t *testing.T) {
	for _, kind := range []string{"failure-budget", "success-atomicity"} {
		t.Run(kind, func(t *testing.T) {
			s, id, now := fixture(t)
			result, secret := enroll(t, s, id, now)
			ctx := context.Background()
			now = now.Add(30 * time.Second)
			issued, err := s.BeginLogin(ctx, id, "integration-hash", false, "/", now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.DB.Exec(`DELETE FROM audit_logs WHERE action='auth.login'`); err != nil {
				t.Fatal(err)
			}
			if _, err = s.DB.Exec(`ALTER TABLE audit_logs ADD CONSTRAINT chk_test_mfa_audit CHECK (action NOT IN ('mfa.verification_failed','mfa.challenge_exhausted','auth.login'))`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.DB.Exec(`ALTER TABLE audit_logs DROP CHECK chk_test_mfa_audit`) })
			if kind == "failure-budget" {
				for i := 1; i <= 5; i++ {
					_, err = s.Verify(ctx, issued.Token, "invalid", 0, "", now)
					want := ErrInvalid
					if i == 5 {
						want = ErrExhausted
					}
					if !errors.Is(err, want) {
						t.Fatal("audit failure reset budget", i, err)
					}
				}
				var failures int
				s.DB.Get(&failures, `SELECT failures FROM mfa_challenges WHERE id=?`, issued.Challenge.ID)
				if failures != 5 {
					t.Fatal("attempts not persisted")
				}
				if _, err = s.Get(ctx, issued.Token, now); !errors.Is(err, ErrInvalid) {
					t.Fatal("exhausted challenge alive")
				}
				return
			}
			code, _ := Code(secret, now.Unix()/30)
			if _, err = s.Verify(ctx, issued.Token, code, 0, "", now); err == nil {
				t.Fatal("audit failure committed authentication")
			}
			if count(t, s.DB, "sessions") != 1 {
				t.Fatal("session creation not rolled back")
			}
			var consumed sql.NullTime
			if err = s.DB.Get(&consumed, `SELECT consumed_at FROM mfa_challenges WHERE id=?`, issued.Challenge.ID); err != nil || consumed.Valid {
				t.Fatal("challenge consumption not rolled back", err)
			}
			var last int64
			s.DB.Get(&last, `SELECT last_counter FROM user_totp_enrollments WHERE user_id=?`, id)
			if last != now.Add(-30*time.Second).Unix()/30 {
				t.Fatal("TOTP consumption not rolled back")
			}
			if _, err = s.DB.Exec(`ALTER TABLE audit_logs DROP CHECK chk_test_mfa_audit`); err != nil {
				t.Fatal(err)
			}
			if verified, err := s.Verify(ctx, issued.Token, code, 0, "", now); err != nil || verified.Session.ID == result.Session.ID {
				t.Fatal("atomic retry failed", err)
			}
		})
	}
}
