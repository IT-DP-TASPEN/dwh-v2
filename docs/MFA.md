# Mandatory TOTP MFA

MFA is mandatory for every user, including administrators. Every new browser
session requires a valid username/password followed by TOTP or an unused recovery
code. Users without an active enrollment must verify a newly provisioned
secret before receiving a session. Pending challenges are not authenticated
sessions. There is no grace period, trusted device, remembered device,
administrator bypass, or user option to disable MFA. Remember Me changes only
the final session's absolute lifetime; idle expiry still applies.

## Deployment and hard cutover

**Migration `20261005120000_mandatory_totp_mfa.sql` deletes all existing browser
sessions. Every user must sign in again.** The new session MFA timestamp is
required at the database layer, so an old binary cannot create password-only
sessions after this migration. Deploy the migration, binary, and assets together.

1. Back up the database and preserve the existing `APP_SECRET_ENCRYPTION_KEY`.
2. Build and deploy the new binary, migrations, and frontend assets together.
3. Stop serving the old binary. Apply migrations through the normal process:
   `go run ./cmd/migrate up` (or the deployed migration binary).
4. Restart the application. There are no startup auto-migrations.
5. All previous browser sessions are invalid. Users sign in with their passwords.
6. Users without MFA complete mandatory enrollment and save their recovery codes.
7. Normal access resumes after MFA verification.

The down migration also deletes all sessions. Rolling back removes the mandatory
MFA implementation; it is not an MFA recovery procedure.

## Authenticator setup and login

Use Google Authenticator, Microsoft Authenticator, 1Password, Bitwarden, Authy,
or another RFC 6238-compatible authenticator. V1 supports TOTP only: SHA-1,
six digits, 30-second period, previous/current/next counter (±1 step). Provisioning
uses the configured `APP_NAME` and the user's username. QR generation is local;
no secret is sent to a QR service. Manual Base32 entry remains available.

Keep server clocks synchronized with NTP. Clock drift outside the narrow window
causes verification failures. A previously accepted counter cannot be reused,
even by another session. If enrollment/login just used a counter, wait for a new
code before another operation, or use an unused recovery code.

Challenges expire after five minutes. Each challenge permits at most five failed
factor attempts. Login/enrollment exhaustion requires restarting password login.
Step-up exhaustion revokes the current browser session and requires full login.
Authenticated management challenges use the same session revocation rule.
Clearing a challenge cookie cannot issue a replacement live step-up challenge;
complete the existing challenge in its original browser or sign out and sign in
again. Expired challenge records are cleaned hourly.

Opaque 256-bit challenge tokens use a distinct HttpOnly cookie under `/mfa`,
SameSite=Lax, and the same production Secure policy as the session cookie. Only
SHA-256 token hashes are stored. MFA setup, QR, management, and recovery responses
use `Cache-Control: no-store` and `Referrer-Policy: strict-origin`. Referrers
contain only the origin, never the MFA path or query string. This also preserves
the `Origin` header on same-origin form submissions for HTTP LAN development;
`no-referrer` makes those form origins `null`, which CSRF protection rejects when
the browser omits `Sec-Fetch-Site`. Production still requires HTTPS.

## Recovery codes and self-service management

Enrollment generates exactly 10 cryptographically random, 128-bit recovery
codes. They are shown in one response only, in a selectable plain-text textarea
that can be copied or printed using the browser. Refreshing does not redisplay
them. Store them securely outside the account they recover. Each code works once
for login, step-up, or self-service recovery. Input is case-insensitive; hyphens
are optional. The database stores user-bound SHA-256 digests, never plaintext.

Open **Security / MFA** from the account menu. Status shows enrollment time and
unused code count; it never displays the active secret or digests.

- **Rotate authenticator:** confirm the current password and current TOTP or
  recovery code, then verify a code from the new pending authenticator. The old
  enrollment stays active until the new secret is verified. Successful rotation
  replaces the active secret, resets replay state to the newly accepted counter,
  replaces all recovery codes, invalidates challenges, and revokes all sessions.
- **Regenerate recovery codes:** confirm the current password and current TOTP or
  recovery code. This creates 10 new codes, invalidates every old code and
  outstanding challenge, and revokes all sessions.

Both operations show the new codes exactly once and require fresh login afterward.
If that response is lost, authenticate again and regenerate the codes; plaintext
is never retained to allow redisplay.
They also support recovery when the authenticator is lost but a recovery code is
available. Self-service management is rejected during impersonation; return to
the actor's account first.

## Sensitive action verification

A browser session needs server-side `mfa_verified_at` within 10 minutes for
sensitive actions. Exactly 10 minutes is accepted; more than 10 minutes is stale.
Future timestamps are rejected. Existing RBAC permission is checked first.
Successful step-up validates the session owner's factor, consumes the challenge
and factor atomically, and refreshes only that session's timestamp.

Datasource actions protected:

- Creation (including opening the creation form).
- Changes to network, host, port, Unix socket, database, username, password, or TLS.
- Connection testing.
- Activation/enabling.

Report template actions protected:

- Creation (including opening the creation form).
- Changes to SQL, parameters (including defaults/options/order/dynamic option SQL),
  or datasource assignment.
- Activation.
- Draft query and dynamic option testing, because these execute unsaved definitions.

Name/description-only updates and disable/archive retain existing permission
behavior. Step-up does not weaken SQL read-only validation, pinned READ ONLY
transactions, destination allowlists, bounded results, or dataset ownership.

Freshness is rechecked on each mutation POST. If verification expires while a
form is open, the server redirects to step-up and then to a known safe GET/form
route. The user must submit again. Unsaved form values may be lost. Passwords,
SQL, and arbitrary POST bodies are never stored or automatically replayed.

During impersonation, step-up always challenges the actor/session owner's MFA,
not the effective user's authenticator. Audit records retain actor/effective
identities. Administrative reset is also rejected during impersonation.

## Administrative and emergency recovery

Use **Reset MFA (require re-enrollment)** on another user's detail page. Dedicated
permission `users.mfa.reset` is registered; only the administrator role receives
access by default through the existing administrator permission policy. Custom
roles can be explicitly granted that permission. Reset requires the actor's own
recent MFA, and the route revalidates the live actor session and permission in
the reset transaction. Administrators cannot reset their own MFA through this
route; use self-service rotation/regeneration.

Reset removes the target's enrollment and recovery codes, invalidates challenges,
revokes all target-owned browser sessions, and audits actor/effective/target
identities. It reveals no credentials. Next password login requires fresh
verified enrollment. Reset never disables mandatory MFA.

When all usable administrator factors and recovery codes are lost, an operator
with application configuration and database access can run:

```sh
go run ./cmd/app user mfa-reset --username administrator
# Or: ./bin/app user mfa-reset --username administrator
```

Type the exact prompted phrase `RESET MFA administrator`. The command resets only
MFA, challenges, and sessions; it preserves the password, active status, and role.
It writes `mfa.operator_reset`, prints no secrets, creates no session, and requires
password plus new enrollment on the next login. No direct SQL recovery is needed.
Treat host/configuration/database access as privileged operator access.

Ordinary password resets preserve the active MFA enrollment but revoke sessions
and invalidate all pending MFA challenges in the same transaction. Deactivation
also invalidates challenges; reactivation cannot restore them. User-row locking
serializes reset and factor verification: a reset that follows verification
revokes the newly created session, and verification after reset rejects the old
challenge. Challenge issuance rechecks the locked password hash after password
verification, closing issuance/reset races.

## Storage and audit

`APP_SECRET_ENCRYPTION_KEY` remains the single encryption master key. Active TOTP
secrets use AES-GCM purpose `mfa-totp-active` bound to the user ID. Pending secrets
use purpose `mfa-totp-pending` bound to the challenge ID. Ciphertext transplanted
between users, challenges, or purposes fails authentication. Preserve the key
across deployment; replacing it without re-encrypting credentials makes factors
unreadable.

New tables are `user_totp_enrollments`, `user_mfa_recovery_codes`, and
`mfa_challenges`. Sessions gain required `mfa_verified_at DATETIME(6)`. Primary,
unique, foreign-key, expiry indexes, purpose constraints, and failure constraints
support one active enrollment, one-time digests/tokens, and cleanup. Runtime schema
verification includes the MFA tables, safety indexes, and required timestamp.

Transactions lock the user before challenge/session/factor rows. Accepted TOTP
counters increase monotonically. Recovery consumption, challenge consumption,
session creation/freshness, enrollment promotion, and successful audit events
commit together. Authenticated challenge issuance reuses the matching live
challenge rather than resetting its failed attempts.

Typed audit actions: `mfa.enrolled`, `mfa.step_up`, `mfa.recovery_used`,
`mfa.recovery_regenerated`, `mfa.rotated`, `mfa.admin_reset`, `mfa.operator_reset`,
`mfa.verification_failed`, and `mfa.challenge_exhausted`. Failure writes are bounded
by the challenge's five-attempt budget. Failed-attempt counters and exhaustion
commit before best-effort failure auditing, so an audit outage cannot replenish
the factor attempt budget. Successful security events remain transactional.
Secrets, OTPs, codes, raw tokens, and passwords are never included in MFA metadata.
Successful login is audited only
after MFA, in the session creation transaction.

TOTP is susceptible to phishing and does not bind confirmation to a particular
POST body. Freshness intentionally covers all protected actions in that session
for 10 minutes. TLS, trusted host operators, secure recovery-code storage, and
NTP remain operational requirements. Reverse-proxy protection remains appropriate
for request-volume attacks; existing bounded password hashing and username
throttling are preserved. There are no passkeys in V1.

## Verification

Run normal checks:

```sh
gofmt -w <changed-Go-files>
go vet ./...
go test ./...
go build ./...
make verify
npm run test:browser -- --reporter=line
```

Set all five `TEST_DB_*` values explicitly to a disposable MySQL 8.4 database.
The integration helper refuses the runtime database and reserved production
schema names. Then run:

```sh
go test -tags=integration ./internal/mfa ./internal/browserauth \
  ./internal/features/users ./internal/features/impersonation ./internal/dwhschema -count=1
go test -tags=integration ./...
```

MFA browser coverage uses the production HTTP handlers, real MySQL storage, local
QR generation, and the production CSP, with a separate disposable schema:

```sh
MFA_BROWSER_TEST=1 TEST_DB_NAME=mfa_browser_test \
  npm run test:browser -- --config=playwright.mfa.config.js --reporter=line
```

Supply `TEST_DB_HOST`, `TEST_DB_PORT`, `TEST_DB_USER`, and `TEST_DB_PASSWORD` as well.
The fixture refuses any schema other than `mfa_browser_test` and resets only its
fixture user. The ordinary UI-only browser suite skips this one DB-backed test;
the dedicated command executes it. Fixture-only stale-session controls are not
registered in the production application. The dedicated browser config maps
`mfa-lan.test` to loopback in Chromium, exercising an HTTP LAN origin where
Fetch Metadata headers are absent. It checks that real MFA form submissions
preserve their origin and send no referrer path/query string.
