package mfa

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/secretcrypto"
	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/logging"
)

var ErrInvalid = errors.New("invalid verification code")
var ErrExhausted = errors.New("MFA challenge exhausted")
var ErrPending = errors.New("MFA challenge already pending; use the original browser or sign in again")

const Login = "login"
const Enrollment = "enrollment"
const StepUp = "step_up"
const Rotation = "rotation"
const RotateAuthorize = "rotate_authorize"
const Regenerate = "regenerate"

type Store struct {
	DB                                      *sqlx.DB
	Cipher                                  *secretcrypto.Cipher
	Lifetime, RememberLifetime, IdleTimeout time.Duration
	clock                                   func(time.Time) time.Time
}

type Challenge struct {
	ID            uint64        `db:"id"`
	TokenHash     []byte        `db:"token_hash"`
	UserID        uint64        `db:"user_id"`
	SessionID     sql.NullInt64 `db:"session_id"`
	Purpose       string        `db:"purpose"`
	PendingSecret []byte        `db:"pending_secret"`
	RememberMe    bool          `db:"remember_me"`
	Next          string        `db:"next_path"`
	Failures      int           `db:"failures"`
	ExpiresAt     time.Time     `db:"expires_at"`
	ConsumedAt    sql.NullTime  `db:"consumed_at"`
	CreatedAt     time.Time     `db:"created_at"`
}

type Issued struct {
	Token     string
	Challenge Challenge
}
type Verified struct {
	RawToken      string
	Session       auth.Session
	Next          string
	RecoveryCodes []string
	Pending       *Issued
	Logout        bool
}
type lockedUser struct {
	ID           uint64 `db:"id"`
	Username     string `db:"username"`
	PasswordHash string `db:"password_hash"`
	Active       bool   `db:"is_active"`
}
type enrollment struct {
	Secret []byte `db:"secret_ciphertext"`
	Last   int64  `db:"last_counter"`
}
type Status struct {
	Enabled    bool
	EnrolledAt time.Time
	Remaining  int
}

// currentTime samples after lock waits; tests may substitute a deterministic clock.
func (s *Store) currentTime(input time.Time) time.Time {
	if s.clock != nil {
		return s.clock(input)
	}
	return time.Now().UTC()
}

// All credential mutations lock users first. Reset and verification serialize on
// this row; a reset that runs second revokes any session created by verification.
func lockUser(ctx context.Context, tx *sqlx.Tx, id uint64) (lockedUser, error) {
	var u lockedUser
	err := tx.GetContext(ctx, &u, `SELECT id,username,password_hash,is_active FROM users WHERE id=? FOR UPDATE`, id)
	return u, err
}

func (s *Store) BeginLogin(ctx context.Context, id uint64, passwordHash string, remember bool, next string, now time.Time) (Issued, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Issued{}, err
	}
	defer tx.Rollback()
	u, err := lockUser(ctx, tx, id)
	if err != nil {
		return Issued{}, err
	}
	if !u.Active || u.PasswordHash != passwordHash {
		return Issued{}, ErrInvalid
	}
	var count int
	if err = tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM user_totp_enrollments WHERE user_id=?`, id); err != nil {
		return Issued{}, err
	}
	now = s.currentTime(now)
	purpose := Login
	if count == 0 {
		purpose = Enrollment
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM mfa_challenges WHERE user_id=? AND session_id IS NULL`, id); err != nil {
		return Issued{}, err
	}
	issued, err := s.issue(ctx, tx, id, 0, purpose, remember, next, now)
	if err != nil {
		return Issued{}, err
	}
	if err = tx.Commit(); err != nil {
		return Issued{}, err
	}
	return issued, nil
}

func (s *Store) issue(ctx context.Context, tx *sqlx.Tx, id, sessionID uint64, purpose string, remember bool, next string, now time.Time) (Issued, error) {
	token, err := auth.GenerateToken()
	if err != nil {
		return Issued{}, err
	}
	hash := auth.HashToken(token)
	var session any
	if sessionID != 0 {
		session = sessionID
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO mfa_challenges(token_hash,user_id,session_id,purpose,remember_me,next_path,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?)`, hash[:], id, session, purpose, remember, next, now.Add(ChallengeLifetime), now)
	if err != nil {
		return Issued{}, err
	}
	newID, err := result.LastInsertId()
	if err != nil {
		return Issued{}, err
	}
	challenge := Challenge{ID: uint64(newID), UserID: id, Purpose: purpose, RememberMe: remember, Next: next, ExpiresAt: now.Add(ChallengeLifetime), CreatedAt: now}
	if sessionID != 0 {
		challenge.SessionID = sql.NullInt64{Int64: int64(sessionID), Valid: true}
	}
	if purpose == Enrollment || purpose == Rotation {
		secret, err := NewSecret()
		if err != nil {
			return Issued{}, err
		}
		encoded, err := s.Cipher.Encrypt(secretcrypto.PurposeMFATOTPPending, challenge.ID, secret)
		if err != nil {
			return Issued{}, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mfa_challenges SET pending_secret=? WHERE id=?`, encoded, challenge.ID); err != nil {
			return Issued{}, err
		}
		challenge.PendingSecret = encoded
	}
	return Issued{Token: token, Challenge: challenge}, nil
}

func (s *Store) Get(ctx context.Context, token string, now time.Time) (Challenge, error) {
	hash := auth.HashToken(token)
	var c Challenge
	err := s.DB.GetContext(ctx, &c, `SELECT * FROM mfa_challenges WHERE token_hash=? AND consumed_at IS NULL AND expires_at>? AND failures<5`, hash[:], now)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrInvalid
	}
	return c, err
}
func (s *Store) PendingSecret(c Challenge) (string, error) {
	if c.Purpose != Enrollment && c.Purpose != Rotation {
		return "", ErrInvalid
	}
	return s.Cipher.Decrypt(secretcrypto.PurposeMFATOTPPending, c.ID, c.PendingSecret)
}

func (s *Store) BeginAuthenticated(ctx context.Context, id, sessionID uint64, purpose, next, existingToken string, now time.Time) (Issued, error) {
	if purpose != StepUp && purpose != RotateAuthorize && purpose != Regenerate {
		return Issued{}, ErrInvalid
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Issued{}, err
	}
	defer tx.Rollback()
	u, err := lockUser(ctx, tx, id)
	if err != nil {
		return Issued{}, err
	}
	if !u.Active {
		return Issued{}, ErrInvalid
	}
	session, err := s.lockSession(ctx, tx, id, sessionID, now)
	if err != nil {
		return Issued{}, err
	}
	if purpose != StepUp && session.ImpersonatedUserID != nil {
		return Issued{}, ErrInvalid
	}
	now = s.currentTime(now)
	if !session.ExpiresAt.After(now) || !session.LastSeenAt.Add(s.IdleTimeout).After(now) {
		return Issued{}, ErrInvalid
	}
	var pending Challenge
	err = tx.GetContext(ctx, &pending, `SELECT * FROM mfa_challenges WHERE session_id=? AND consumed_at IS NULL AND expires_at>? ORDER BY id LIMIT 1 FOR UPDATE`, sessionID, now)
	if err == nil {
		hash := auth.HashToken(existingToken)
		if string(hash[:]) != string(pending.TokenHash) || pending.Purpose != purpose {
			return Issued{}, ErrPending
		}
		if err = tx.Commit(); err != nil {
			return Issued{}, err
		}
		return Issued{Token: existingToken, Challenge: pending}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Issued{}, err
	}
	issued, err := s.issue(ctx, tx, id, sessionID, purpose, false, next, now)
	if err != nil {
		return Issued{}, err
	}
	if err = tx.Commit(); err != nil {
		return Issued{}, err
	}
	return issued, nil
}

func (s *Store) lockSession(ctx context.Context, tx *sqlx.Tx, id, sessionID uint64, now time.Time) (auth.Session, error) {
	var hash []byte
	if err := tx.GetContext(ctx, &hash, `SELECT token_hash FROM sessions WHERE id=? AND user_id=?`, sessionID, id); err != nil {
		return auth.Session{}, ErrInvalid
	}
	var h [32]byte
	copy(h[:], hash)
	session, err := auth.FindValidSession(ctx, tx, h, now, true)
	if err != nil {
		return auth.Session{}, ErrInvalid
	}
	if session.MFAVerifiedAt.IsZero() || !session.LastSeenAt.Add(s.IdleTimeout).After(now) {
		return auth.Session{}, ErrInvalid
	}
	return session, nil
}

// Verify commits factor consumption, challenge consumption, audit, and session
// creation/update together. No plaintext factor is persisted or audited.
func (s *Store) Verify(ctx context.Context, token, input string, sessionID uint64, passwordHash string, now time.Time) (Verified, error) {
	hash := auth.HashToken(token)
	// Unlocked lookup locates the user only. Authoritative checks happen under locks.
	var id uint64
	if err := s.DB.GetContext(ctx, &id, `SELECT user_id FROM mfa_challenges WHERE token_hash=?`, hash[:]); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Verified{}, ErrInvalid
		}
		return Verified{}, err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Verified{}, err
	}
	defer tx.Rollback()
	u, err := lockUser(ctx, tx, id)
	if err != nil {
		return Verified{}, err
	}
	var c Challenge
	if err = tx.GetContext(ctx, &c, `SELECT * FROM mfa_challenges WHERE token_hash=? FOR UPDATE`, hash[:]); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Verified{}, ErrInvalid
		}
		return Verified{}, err
	}
	now = s.currentTime(now)
	if !u.Active || c.ConsumedAt.Valid || !c.ExpiresAt.After(now) || c.Failures >= MaxFailures {
		return Verified{}, ErrInvalid
	}
	var session auth.Session
	if c.SessionID.Valid {
		if uint64(c.SessionID.Int64) != sessionID {
			return Verified{}, ErrInvalid
		}
		session, err = s.lockSession(ctx, tx, id, sessionID, now)
		if err != nil {
			return Verified{}, err
		}
		if c.Purpose != StepUp && session.ImpersonatedUserID != nil {
			return Verified{}, ErrInvalid
		}
	} else if sessionID != 0 {
		return Verified{}, ErrInvalid
	}
	now = s.currentTime(now)
	if !c.ExpiresAt.After(now) || c.SessionID.Valid && (!session.ExpiresAt.After(now) || !session.LastSeenAt.Add(s.IdleTimeout).After(now)) {
		return Verified{}, ErrInvalid
	}
	attribution := audit.Attribution{Actor: &audit.Identity{UserID: id, Username: u.Username}, Effective: &audit.Identity{UserID: id, Username: u.Username}}
	if session.ImpersonatedUserID != nil {
		var username string
		if err = tx.GetContext(ctx, &username, `SELECT username FROM users WHERE id=?`, *session.ImpersonatedUserID); err != nil {
			return Verified{}, err
		}
		attribution.Effective = &audit.Identity{UserID: *session.ImpersonatedUserID, Username: username}
	}
	emit := func(action audit.Action) error {
		return audit.Append(ctx, tx, audit.Event{Attribution: attribution, Action: action, Resource: audit.ResourceUser, ResourceID: id, CreatedAt: now})
	}
	var counter int64
	valid := false
	recovery := false
	if c.Purpose == Enrollment || c.Purpose == Rotation {
		secret, err := s.PendingSecret(c)
		if err != nil {
			return Verified{}, err
		}
		counter, valid = Match(secret, input, now, -1)
	} else {
		var active enrollment
		err = tx.GetContext(ctx, &active, `SELECT secret_ciphertext,last_counter FROM user_totp_enrollments WHERE user_id=? FOR UPDATE`, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Verified{}, ErrInvalid
			}
			return Verified{}, err
		}
		secret, err := s.Cipher.Decrypt(secretcrypto.PurposeMFATOTPActive, id, active.Secret)
		if err != nil {
			return Verified{}, err
		}
		counter, valid = Match(secret, input, now, active.Last)
		if !valid {
			codeHash := RecoveryHash(id, input)
			var used sql.NullTime
			err = tx.GetContext(ctx, &used, `SELECT used_at FROM user_mfa_recovery_codes WHERE user_id=? AND code_hash=? FOR UPDATE`, id, codeHash[:])
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return Verified{}, err
			}
			recovery = err == nil && !used.Valid
			valid = recovery
		}
	}
	if c.Purpose == RotateAuthorize || c.Purpose == Regenerate {
		valid = valid && passwordHash != "" && passwordHash == u.PasswordHash
	}
	if !valid {
		failures := c.Failures + 1
		var consumed any
		if failures == MaxFailures {
			consumed = now
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mfa_challenges SET failures=?,consumed_at=? WHERE id=?`, failures, consumed, c.ID); err != nil {
			return Verified{}, err
		}
		if failures == MaxFailures {
			if c.SessionID.Valid {
				if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, sessionID); err != nil {
					return Verified{}, err
				}
			}
		}
		if err = tx.Commit(); err != nil {
			return Verified{}, err
		}
		// Failed-attempt accounting must survive an unavailable audit sink.
		// The committed challenge bounds these best-effort audit writes to five.
		actions := []audit.Action{audit.ActionMFAFailed}
		if failures == MaxFailures {
			actions = append(actions, audit.ActionMFAExhausted)
		}
		for _, action := range actions {
			if auditErr := audit.Append(ctx, s.DB, audit.Event{Attribution: attribution, Action: action, Resource: audit.ResourceUser, ResourceID: id, CreatedAt: now}); auditErr != nil {
				slog.WarnContext(ctx, "append MFA failure audit", "action", action, logging.Err(auditErr))
			}
		}
		if failures == MaxFailures {
			return Verified{}, ErrExhausted
		}
		return Verified{}, ErrInvalid
	}
	result := Verified{Next: c.Next}
	if c.Purpose == Enrollment || c.Purpose == Rotation {
		secret, err := s.PendingSecret(c)
		if err != nil {
			return Verified{}, err
		}
		encoded, err := s.Cipher.Encrypt(secretcrypto.PurposeMFATOTPActive, id, secret)
		if err != nil {
			return Verified{}, err
		}
		if c.Purpose == Enrollment {
			_, err = tx.ExecContext(ctx, `INSERT INTO user_totp_enrollments(user_id,secret_ciphertext,last_counter,enrolled_at,updated_at) VALUES(?,?,?,?,?)`, id, encoded, counter, now, now)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE user_totp_enrollments SET secret_ciphertext=?,last_counter=?,updated_at=? WHERE user_id=?`, encoded, counter, now, id)
		}
		if err != nil {
			return Verified{}, err
		}
		result.RecoveryCodes, err = replaceRecovery(ctx, tx, id, now)
		if err != nil {
			return Verified{}, err
		}
		action := audit.ActionMFAEnrolled
		if c.Purpose == Rotation {
			action = audit.ActionMFARotated
		}
		if err = emit(action); err != nil {
			return Verified{}, err
		}
	} else if recovery {
		codeHash := RecoveryHash(id, input)
		if _, err = tx.ExecContext(ctx, `UPDATE user_mfa_recovery_codes SET used_at=? WHERE user_id=? AND code_hash=? AND used_at IS NULL`, now, id, codeHash[:]); err != nil {
			return Verified{}, err
		}
		if err = emit(audit.ActionMFARecoveryUsed); err != nil {
			return Verified{}, err
		}
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE user_totp_enrollments SET last_counter=?,updated_at=? WHERE user_id=?`, counter, now, id); err != nil {
			return Verified{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mfa_challenges SET consumed_at=?,pending_secret=NULL WHERE id=?`, now, c.ID); err != nil {
		return Verified{}, err
	}
	switch c.Purpose {
	case Login, Enrollment:
		raw, err := auth.GenerateToken()
		if err != nil {
			return Verified{}, err
		}
		lifetime := s.Lifetime
		if c.RememberMe {
			lifetime = s.RememberLifetime
		}
		result.Session, err = auth.CreateSessionIn(ctx, tx, auth.CreateSessionParams{UserID: id, TokenHash: auth.HashToken(raw), RememberMe: c.RememberMe, ExpiresAt: now.Add(lifetime), LastSeenAt: now, MFAVerifiedAt: now}, now)
		if err != nil {
			return Verified{}, err
		}
		result.RawToken = raw
		if _, err = tx.ExecContext(ctx, `UPDATE users SET last_login_at=? WHERE id=?`, now, id); err != nil {
			return Verified{}, err
		}
		if err = emit(audit.ActionAuthLogin); err != nil {
			return Verified{}, err
		}
	case StepUp:
		if _, err = tx.ExecContext(ctx, `UPDATE sessions SET mfa_verified_at=?,updated_at=? WHERE id=?`, now, now, sessionID); err != nil {
			return Verified{}, err
		}
		if err = emit(audit.ActionMFAStepUp); err != nil {
			return Verified{}, err
		}
	case RotateAuthorize:
		pending, err := s.issue(ctx, tx, id, sessionID, Rotation, false, "/login", now)
		if err != nil {
			return Verified{}, err
		}
		result.Pending = &pending
	case Regenerate:
		result.RecoveryCodes, err = replaceRecovery(ctx, tx, id, now)
		if err != nil {
			return Verified{}, err
		}
		if err = emit(audit.ActionMFARecoveryRegenerated); err != nil {
			return Verified{}, err
		}
		fallthrough
	case Rotation:
		if err = clearChallengesSessions(ctx, tx, id); err != nil {
			return Verified{}, err
		}
		result.Logout = true
		result.Next = "/login"
	default:
		return Verified{}, ErrInvalid
	}
	if err = tx.Commit(); err != nil {
		return Verified{}, err
	}
	return result, nil
}

func replaceRecovery(ctx context.Context, tx *sqlx.Tx, id uint64, now time.Time) ([]string, error) {
	codes, err := NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM user_mfa_recovery_codes WHERE user_id=?`, id); err != nil {
		return nil, err
	}
	for _, code := range codes {
		hash := RecoveryHash(id, code)
		if _, err = tx.ExecContext(ctx, `INSERT INTO user_mfa_recovery_codes(user_id,code_hash,created_at) VALUES(?,?,?)`, id, hash[:], now); err != nil {
			return nil, err
		}
	}
	return codes, nil
}
func clearChallengesSessions(ctx context.Context, tx *sqlx.Tx, id uint64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM mfa_challenges WHERE user_id=?`, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id)
	return err
}
func (s *Store) Status(ctx context.Context, id uint64) (Status, error) {
	var result Status
	err := s.DB.GetContext(ctx, &result.EnrolledAt, `SELECT enrolled_at FROM user_totp_enrollments WHERE user_id=?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Enabled = true
	err = s.DB.GetContext(ctx, &result.Remaining, `SELECT COUNT(*) FROM user_mfa_recovery_codes WHERE user_id=? AND used_at IS NULL`, id)
	return result, err
}

// Reset only removes MFA credentials. It never creates a session or bypass.
// Operator reset has no browser attribution; administrative reset revalidates
// permission and freshness against the locked actor's live browser session.
func (s *Store) Reset(ctx context.Context, targetID, actorID, sessionID uint64, attribution audit.Attribution, now time.Time) error {
	if targetID == 0 || actorID != 0 && targetID == actorID {
		return ErrInvalid
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids := []uint64{targetID}
	if actorID != 0 {
		ids = append(ids, actorID)
		if ids[0] > ids[1] {
			ids[0], ids[1] = ids[1], ids[0]
		}
	}
	for _, id := range ids {
		u, lockErr := lockUser(ctx, tx, id)
		if lockErr != nil {
			return lockErr
		}
		if id == actorID && !u.Active {
			return ErrInvalid
		}
	}
	now = s.currentTime(now)
	action := audit.ActionMFAOperatorReset
	if actorID != 0 {
		session, err := s.lockSession(ctx, tx, actorID, sessionID, now)
		if err != nil {
			return err
		}
		now = s.currentTime(now)
		if !session.ExpiresAt.After(now) || !session.LastSeenAt.Add(s.IdleTimeout).After(now) || session.ImpersonatedUserID != nil || !Recent(session.MFAVerifiedAt, now) {
			return ErrInvalid
		}
		var allowed int
		if err = tx.GetContext(ctx, &allowed, `SELECT COUNT(*) FROM users u JOIN roles r ON r.id=u.role_id WHERE u.id=? AND (r.slug='admin' OR EXISTS(SELECT 1 FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=r.id AND p.`+"`key`"+`='users.mfa.reset'))`, actorID); err != nil {
			return err
		}
		if allowed != 1 {
			return ErrInvalid
		}
		action = audit.ActionMFAAdminReset
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM user_totp_enrollments WHERE user_id=?`, targetID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM user_mfa_recovery_codes WHERE user_id=?`, targetID); err != nil {
		return err
	}
	if err = clearChallengesSessions(ctx, tx, targetID); err != nil {
		return err
	}
	if err = audit.Append(ctx, tx, audit.Event{Attribution: attribution, Action: action, Resource: audit.ResourceUser, ResourceID: targetID, CreatedAt: now}); err != nil {
		return err
	}
	return tx.Commit()
}
