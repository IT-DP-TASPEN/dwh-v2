# MFA implementation validation

Validated on 2026-10-05 against the current checkout: `master`, base commit
`b4b0ecf`. Changes remain uncommitted. Historical migrations and
`FINCLOUD_INSECURE_SKIP_VERIFY` were not changed.

## Commands and results

Go commands used `CGO_ENABLED=0 GOCACHE=/tmp/new-dwh-go-cache`.
Browser commands also used `PLAYWRIGHT_BROWSERS_PATH=/tmp/new-dwh-playwright`.
Integration commands used an isolated MySQL **8.4.10** server at
`127.0.0.1:33384`, a schema-scoped disposable account, and explicitly supplied
all five `TEST_DB_*` variables. No runtime database was used.

| Command | Result |
| --- | --- |
| `gofmt -w <changed-Go-files>` | Applied; final `gofmt -l` found zero unformatted files across 32 changed Go files. |
| `go vet ./...` | Passed, exit 0. |
| `go test ./...` | Passed, exit 0. |
| `go build ./...` | Passed, exit 0. |
| `make verify` | Passed, exit 0; canonical asset build, vet, unit tests, and build. |
| `npm run test:browser -- --reporter=line` | Passed: 40 tests passed, one DB-backed MFA test intentionally skipped by the ordinary UI-only fixture. |
| `npm run test:browser -- --config=playwright.mfa.config.js --reporter=line` | Passed: one end-to-end MFA test, with `MFA_BROWSER_TEST=1` and `TEST_DB_NAME=mfa_browser_test`. |
| `go test -tags=integration ./internal/mfa ./internal/browserauth ./internal/features/users ./internal/features/impersonation ./internal/dwhschema -count=1` | Passed all five packages against `mfa_feature_test`. |
| `go test -tags=integration ./...` | Failed: 34 top-level failures; the exact same 34 failures occur on clean base commit `b4b0ecf` under the same MySQL server/account privileges. No new failing test names. |

The clean baseline was extracted using `git archive HEAD` into
`/tmp/new-dwh-mfa-baseline` and tested against separate `mfa_baseline_test`.
Baseline and implementation failures were compared by top-level test name.
This establishes matching failure sets, not that every underlying behavior or
failure message is identical. Existing failures include stale topology/schema
expectations, reserved SQL identifiers, JSON representation expectations,
ingestion lifecycle assumptions, and binary-log/administrative privileges that
the schema-scoped test account does not possess. No unrelated fixes or database
privilege relaxations were applied.

The existing CSS format is retained after the canonical build by regenerating
Tailwind CSS without minification. Unchanged JavaScript was restored from the
clean base to avoid an unrelated minification diff. Final browser checks used
these resulting assets.

## Security coverage executed

- Password failures, unknown/inactive users, bounded hashing/throttling, mandatory
  pre-session enrollment and challenges, Remember Me final lifetime, normal
  middleware rejection of pre-auth tokens and missing MFA assurance.
- RFC 6238 SHA-1 vectors, six digits, ±1 timestep, deterministic inclusive
  10-minute freshness boundary, wrong user/purpose encryption authentication,
  encrypted pending/active storage, and recovery digest-only storage.
- One-time enrollment and ten recovery codes, challenge expiry, five-failure
  exhaustion, repeated exhausted requests without audit amplification, and no
  recovery-code redisplay after consumed enrollment.
- Real MySQL simultaneous recovery-code use, TOTP-counter use, challenge use,
  enrollment completion, expiry after lock waiting, and reset/login races.
- Actor factor and effective audit identity during impersonation; self-management
  rejection during impersonation; step-up exhaustion session revocation.
- Password-plus-factor management authorization, unverified replacement secret
  preserving old enrollment, verified rotation, recovery regeneration, old-code
  deletion, and all-session/challenge invalidation.
- Dedicated reset permission, ordinary update-permission rejection, stale actor
  rejection, self-reset rejection, administrator/operator reset and mandatory
  re-enrollment; CLI argument and exact-confirmation handling.
- Password reset and deactivation invalidate issued pre-auth challenges;
  reactivation does not resurrect them; concurrent reset leaves no old valid
  authenticated session.
- Fault-injected audit failure cannot roll back failed-attempt budgets or
  exhaustion; successful verification/audit failure rolls back session creation,
  challenge consumption, and replay advancement together, allowing safe retry.
- Actual datasource and report-template routes enforce stored MFA freshness and
  RBAC; metadata-only updates/disable retain existing behavior; fresh SQL and
  dynamic option tests retain independent read-only validation.
- Production HTTP/cache/CSP tests verify local QR, manual secret, once-only codes,
  secure challenge-cookie properties, safe redirects, stale direct POST,
  recovery step-up, and no secret/token/code in audit or redirect metadata.
- Real browser enrollment, QR/manual setup under CSP, recovery-code presentation,
  logout, password-to-MFA login, stale sensitive POST, successful recovery step-up,
  manual resubmission, recovery login, and mandatory MFA with Remember Me.
- Real Goose rollback/reapply proves existing sessions are deleted at rollout
  and password-only inserts omitting the required MFA column fail.

## Existing broader-suite failures

These names failed on both baseline and implementation:

- `TestAuthorTestsUsePersistedDatasource`
- `TestBulkScheduleStateAuditAndRollback`
- `TestBulkStopPreservesAttemptsAndUsesCanonicalCursorBehavior`
- `TestBusyDisabledFairnessAndConcurrentAttemptAllocation`
- `TestCancelledAndAbandonedAttemptsRemainUnresolved`
- `TestControlledBootstrapMatchesDWH2Topology`
- `TestCreateManyCreatesOrdinarySchedulesAndSkipsCurrentDuplicates`
- `TestCreateManySerializesIdenticalConcurrentRequestsAndUsesNormalRuntime`
- `TestCustomDatasetAtomicVisibilityOwnershipAndRollback`
- `TestDetailCleanupReclaimsRecoveredAndOrphanStagingWithoutRunFK`
- `TestDetailOutstandingMergedAndSplitPublicationIsAtomic`
- `TestDetailPublicationCrashBeforeSchedulerResolutionAdvancesOnce`
- `TestDetailPublicationFailureRollsBackFinalStateAndRunSuccess`
- `TestDetailStagingNoLongerLocksRunProgressRow`
- `TestDisableUpdateEnableFencesOldAttempt`
- `TestDurableQueueRunAllAndTerminalCAS`
- `TestDynamicOptionDefinitionPersistsCanonicalState`
- `TestExportAuthorizationClaimFencingAndDownloadRules`
- `TestExportOversightScopesHistoricalAccessAndAudit`
- `TestJournalTransactionPartitionsPublishAtomically`
- `TestMaintenanceDynamicAdditiveRetry`
- `TestManualSubmissionsCreateOnlyUserAuditEvents`
- `TestMasterExecutorPublishesReferenceAndMarketingWithoutSnapshotDate`
- `TestMySQLCanonicalParametersAndAllRunAllChildren`
- `TestMySQLReadOnlyPolicyAndTransaction`
- `TestNoDateCoalescesAndAdvancesFromSuccessfulFinish`
- `TestRetryUntilSuccessAndChronologicalCursor`
- `TestRuntimePrivilegesSupportDynamicAdditiveWithoutDrop`
- `TestSavingAccountStatementExactSetTextAndTimestamps`
- `TestSavingDetailFailurePathsReleaseSessionExactlyOnce`
- `TestScheduleManagementDerivesLiveSnapshotPolicy`
- `TestSchedulerParametersRoundTripMySQLForEveryCanonicalKind`
- `TestSourceStateCASPersistsActor`
- `TestStaleDetailWorkerCannotPublishAfterNewRunSucceeds`

## Changed files

- [`README.md`](../README.md)
- [`cmd/app/main.go`](../cmd/app/main.go)
- [`cmd/app/mfa.go`](../cmd/app/mfa.go)
- [`cmd/app/mfa_test.go`](../cmd/app/mfa_test.go)
- [`docs/MFA.md`](../docs/MFA.md)
- [`docs/MFA_VALIDATION.md`](../docs/MFA_VALIDATION.md)
- [`docs/PRODUCTION.md`](../docs/PRODUCTION.md)
- [`go.mod`](../go.mod)
- [`go.sum`](../go.sum)
- [`internal/app/app.go`](../internal/app/app.go)
- [`internal/app/navigation_test.go`](../internal/app/navigation_test.go)
- [`internal/audit/audit.go`](../internal/audit/audit.go)
- [`internal/auth/repository.go`](../internal/auth/repository.go)
- [`internal/auth/session.go`](../internal/auth/session.go)
- [`internal/browserauth/handler.go`](../internal/browserauth/handler.go)
- [`internal/browserauth/http_test.go`](../internal/browserauth/http_test.go)
- [`internal/browserauth/mfa.go`](../internal/browserauth/mfa.go)
- [`internal/browserauth/mfa_integration_test.go`](../internal/browserauth/mfa_integration_test.go)
- [`internal/browserauth/sensitive_integration_test.go`](../internal/browserauth/sensitive_integration_test.go)
- [`internal/browserauth/service.go`](../internal/browserauth/service.go)
- [`internal/browserauth/service_test.go`](../internal/browserauth/service_test.go)
- [`internal/browserauth/testdata/browser/main.go`](../internal/browserauth/testdata/browser/main.go)
- [`internal/dwhschema/runtime.go`](../internal/dwhschema/runtime.go)
- [`internal/features/datasources/handler.go`](../internal/features/datasources/handler.go)
- [`internal/features/datasources/handler_test.go`](../internal/features/datasources/handler_test.go)
- [`internal/features/reporttemplates/handler.go`](../internal/features/reporttemplates/handler.go)
- [`internal/features/reporttemplates/view_test.go`](../internal/features/reporttemplates/view_test.go)
- [`internal/features/users/permissions.go`](../internal/features/users/permissions.go)
- [`internal/features/users/repository.go`](../internal/features/users/repository.go)
- [`internal/mfa/reset_integration_test.go`](../internal/mfa/reset_integration_test.go)
- [`internal/mfa/store.go`](../internal/mfa/store.go)
- [`internal/mfa/store_integration_test.go`](../internal/mfa/store_integration_test.go)
- [`internal/mfa/totp.go`](../internal/mfa/totp.go)
- [`internal/mfa/totp_test.go`](../internal/mfa/totp_test.go)
- [`internal/render/notice.go`](../internal/render/notice.go)
- [`internal/secretcrypto/crypto.go`](../internal/secretcrypto/crypto.go)
- [`internal/server/router.go`](../internal/server/router.go)
- [`internal/testutil/integrationdb/integrationdb.go`](../internal/testutil/integrationdb/integrationdb.go)
- [`migrations/20261005120000_mandatory_totp_mfa.sql`](../migrations/20261005120000_mandatory_totp_mfa.sql)
- [`playwright.mfa.config.js`](../playwright.mfa.config.js)
- [`tests/browser/mfa.spec.js`](../tests/browser/mfa.spec.js)
- [`web/static/css/app.css`](../web/static/css/app.css)
- [`web/templates/features/users/show.html`](../web/templates/features/users/show.html)
- [`web/templates/pages/mfa.html`](../web/templates/pages/mfa.html)
- [`web/templates/pages/mfa_admin_reset.html`](../web/templates/pages/mfa_admin_reset.html)
- [`web/templates/pages/mfa_recovery.html`](../web/templates/pages/mfa_recovery.html)
- [`web/templates/pages/mfa_security.html`](../web/templates/pages/mfa_security.html)
- [`web/templates/partials/topbar.html`](../web/templates/partials/topbar.html)

## Deployment and operational limits

Follow [MFA deployment instructions](MFA.md#deployment-and-hard-cutover).
Back up the database and preserve `APP_SECRET_ENCRYPTION_KEY`; stop the old
binary, apply migration `20261005120000_mandatory_totp_mfa.sql` through the normal
migration process, and deploy/restart matching binary and assets. All old sessions
are invalidated. Users must log in and, if unenrolled, verify their authenticator
and save ten recovery codes. No startup auto-migrations were added.

TOTP remains susceptible to phishing. Server clock/NTP and the existing encryption
key must remain available. Recovery codes are delivered once; losing that HTTP
response requires authenticated regeneration rather than redisplay. Failed-factor
audit writes are best effort after committed attempt accounting; successful
security events are transactional. The 34 matching baseline integration failures
remain unresolved. The isolated test server was stopped after verification.
