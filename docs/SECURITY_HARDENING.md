# Security hardening audit and implementation

Audited the current checkout of IT-DP-TASPEN/dwh-v2 at HEAD `0b8515664508d5fbc7c6cd79746fbfb1ca656202`, then implemented against that code. No commit created. No schema migration added. `FINCLOUD_INSECURE_SKIP_VERIFY`, its parsing, and existing Fincloud TLS behavior are unchanged.

## Audit findings and fixes

| Finding in current code | Implemented boundary |
| --- | --- |
| Prepared arbitrary report SQL was writable; multiStatements=false did not prevent writes. | One MySQL-mode-aware lexical policy for SELECT / WITH ... SELECT, report and dynamic-option templates, activation, active updates, tests, runs and exports. Reject writes, administration, executable comments, multiple statements, output files, locking reads, assignments and native session-mutating lock/last-insert-ID calls. Unsafe SQL is not rewritten. Public invalid-report handling is retained. |
| Report streaming used pinned connections without database read-only transactions. | Begin READ ONLY on that physical connection before preparing/executing. Successful EOF closes rows/statements and rolls back with a two-second cleanup deadline. Begin/prepare/query/protocol/close/cleanup errors, cancellation and bounded abort discard the physical connection. No writable fallback. |
| Login Argon2 verification had no application concurrency bound or failed-username lockout. | Real and dummy verification share a bounded semaphore; waiting follows request cancellation. Synchronous hashing retains its slot until completion. Normalized-username failure counts, expiry, lockout and success reset use a synchronized map capped at 10,000 entries with stale cleanup. Actual failed credential verifications create one audit event with only attempted normalized username and event time, without account identity or secrets. Already locked requests skip verification, failure-count updates and audit writes, with the same public response, including password-slot waiters. |
| Fresh upload lookup accepted a numeric ID without effective-user ownership. | Requester-aware repository lookup protects configure/preview and submission. Submission rechecks creator ownership and expiry under the same upload row lock/transaction that retains it. Retained uploads require the original provisioning dataset association. Unavailable paths use generic Not Found; published dataset permissions remain unchanged. |
| Datasource destination selection allowed broad hosts/socket paths. | Shared immutable production policy validates saves/tests, stored records before pool reuse, and every new physical dial. Every resolved address is checked; TCP dials validated IPs directly. Exact cleaned absolute socket allowlist preserves Unix sockets. Production empty policy denies all destinations. |
| CSP omitted script/style/default/connect restrictions; frontend used eval-dependent Alpine and inline application handlers/scripts. | Self-hosted official Alpine CSP build; HTMX eval and script-tag execution disabled. Inline application scripts/handlers and indicator CSS moved to static assets. Frontend expressions adapted to CSP parsing. Full header policy enforced and browser-tested. |
| Sessions checked absolute expiry without using last_seen_at. | Session must satisfy both absolute and idle bounds; unusable idle sessions revoked. Persisted activity refreshed at min(5m, idle_timeout/2). Remember-me retains its absolute lifetime and the same idle bound. |

CSP: `default-src 'self'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'`. The narrow style-attribute exception supports Alpine visibility/transitions and context-menu placement; inline script and unsafe-eval are absent. Existing X-Content-Type-Options, X-Frame-Options, Referrer-Policy and Permissions-Policy remain.

## Configuration added

| Variable | Default / required production action |
| --- | --- |
| AUTH_MAX_CONCURRENT_PASSWORD_HASHES | 4; positive integer |
| AUTH_LOGIN_FAILURE_WINDOW | 15m; positive duration |
| AUTH_LOGIN_MAX_FAILURES | 5; positive integer |
| AUTH_LOGIN_LOCKOUT | 15m; positive duration |
| SESSION_IDLE_TIMEOUT | 2h; positive duration; remember-me does not bypass it |
| REPORT_DATASOURCE_ALLOWED_TCP_CIDRS | Empty in production; comma-separated permitted CIDRs |
| REPORT_DATASOURCE_ALLOWED_HOSTS | Empty; comma-separated exact hostnames or literal IPs; no wildcards |
| REPORT_DATASOURCE_ALLOWED_UNIX_SOCKETS | Empty; comma-separated exact cleaned absolute socket paths |

Existing configuration variables, including session absolute lifetimes and Fincloud settings, are retained. Development/test uses loopback TCP CIDRs only when all destination lists are empty. Every resolved address must be authorized either by an allowed CIDR/exact literal IP, or by an exact allowlisted hostname resolving to a public destination. These rules are independent even when CIDRs are configured. Private, localhost and link-local destinations still require explicit literal-IP/CIDR permission; any disallowed DNS answer rejects the entire hostname.

## Deployment actions and remaining limits

- Provision production report accounts with SELECT-only privileges: no INSERT/UPDATE/DELETE, CREATE/ALTER/DROP, FILE, or dangerous routine invocation (normally no EXECUTE). Do not reuse runtime/migration accounts. SQL validation and READ ONLY transactions supplement database least privilege; they cannot prove every routine/UDF side effect or effective inherited privilege. No SHOW GRANTS parser was added.
- Configure narrow destination allowlists before restart. Stored datasources are checked again under the new process policy. Use an exact socket path and a separate read-only socket-authenticated database account; local socket authentication can otherwise inherit strong privileges. Protect allowlisted paths/directories against untrusted replacement/symlinks. The documented auth_socket OS-user mapping was exercised on MySQL 8.4.
- Review SESSION_IDLE_TIMEOUT and AUTH_* values for workload. No migration is needed. Existing sessions are subject to idle expiry immediately. With default activity-touch settings, persisted activity can lag requests by up to five minutes.
- Build/deploy the binary and rebuilt frontend assets together (`npm ci && make verify`). The official Alpine CSP package is now the direct frontend dependency; existing generated assets are rebuilt/minified by the established build command.
- Throttle/hash limits are process-local, reset on restart and do not coordinate replicas. Username lockout can be used for denial of service; map saturation leaves new names untracked until stale entries are reclaimed, while allowing bounded password verification and preserving existing live lockouts. Per-IP protection remains a reverse-proxy responsibility; no X-Forwarded-For trust was added. Argon2 is synchronous and cannot be interrupted mid-hash, but canceled work cannot release a slot early and create excess concurrent working memory. Audit append remains best effort, as existing successful-login audit did.
- The style-attribute CSP exception remains for current frontend behavior. No script unsafe-inline or unsafe-eval exception was added.
- Full integration acceptance remains limited by pre-existing suite failures listed below. Focused new security tests pass on real MySQL 8.4.10.

## Tests and validation

Changed Go files were formatted with gofmt. `git diff --check` passes. `CGO_ENABLED=0 GOCACHE=/tmp/new-dwh-go-cache make verify` passed: npm production asset build, `go vet ./...`, **`go test ./...`**, and `go build ./...`. These environment overrides avoid read-only host compiler caches; network-requiring tests ran with approved localhost access.

`PLAYWRIGHT_BROWSERS_PATH=/tmp/new-dwh-playwright CGO_ENABLED=0 GOCACHE=/tmp/new-dwh-go-cache npm run test:browser -- --reporter=line` passed **40 tests**. Two new CSP browser tests exercise theme/sidebar, navigation, HTMX swaps, folder rename, datasource controls, CSV controls, report parameter editing, inert JSON results and dynamic option fetching under the actual production SecurityHeaders middleware, and assert no CSP violations/runtime errors.

Added/expanded Go coverage:

- `readonly_test.go`: SELECT/CTE/case/comment/quoted-token acceptance in ANSI_QUOTES and NO_BACKSLASH_ESCAPES modes; rejected write/DDL/admin/multiple/locking/output forms; executable comments and quoted native lock functions; shared dynamic-option policy.
- `query_test.go`: mandatory ReadOnly BeginTx, successful rollback/reuse, and physical discard on injected begin/prepare/query/rows-close/statement-close/rollback failures; existing bounded streaming checks retained.
- `security_test.go`: username failure counts, normalization, lockout, window/lockout expiry, success reset, capped map cleanup, maximum concurrent verification, slot release on error/cancellation, dummy verification outside lockout, session active/absolute/idle/activity/remember bounds. Existing revoked/inactive/session revocation tests remain.
- `http_test.go`: failed-login audit carries no password/hash/token/account-existence identity; successful audit retained.
- `config_test.go`: positive authentication/session bounds, defaults and destination configuration validation.
- `destination_test.go`: exact IP/host, CIDR/IPv6, every DNS address, resolution failure, loopback/private/link-local rejection unless explicit, socket exact/traversal/alternate paths and stale stored record rejection before dialing.
- `middleware_test.go`: full CSP and existing security headers on success and error responses. Template tests reflect external scripts/delegated handlers while preserving behavior.

Focused real-MySQL command (TEST_DB_* configured for the isolated disposable MySQL 8.4.10 server, with optional socket test settings):

```sh
go test -tags=integration ./internal/reporting ./internal/features/customdatasets \
  -run 'TestMySQLReadOnlyPolicyAndTransaction|TestBoundedRawMySQLExecutionDiscardsOnlyAbortedConnections|TestDynamicOptionMySQLContractAndBoundedAbort|TestOptionalBlockValidationUsesSessionSQLModeAndPreparesShapes|TestUnixDatasourceUsesProcessIdentity|TestPendingUploadOwnershipAndRetainedRetry' \
  -count=1 -v
```

**All six tests passed**, including READ ONLY observed in performance_schema on the executing physical connection; SELECT succeeds, DML is rejected and a SELECT-hidden writing routine is blocked by MySQL without modifying data; clean connection reuse, timeout/row/payload discard; shared dynamic options; SQL quoting modes; Unix process identity; and two real manager users with fresh upload privacy, transactional submission denial/acceptance, tampered parameters, unrelated retained-upload denial and legitimate same-dataset retry.

The established **`make test-integration` was run** against the isolated MySQL 8.4.10 schema. It failed with **28 tests**. Running the identical command/environment on a clean archive of original HEAD produced the **same 28 failure names** (zero newly failing names). Failures include outdated schema/ingestion fixtures, unquoted MySQL reserved identifiers, JSON ordering/NULL scan assumptions, and two datasource fixtures relying on an empty root password. No unrelated baseline failures were patched. Test server changes (performance_schema access, test-only routine-creation setting and auth_socket accounts) applied only to the disposable server.

Failed test names in both baseline and changed suite:

- `TestAuthorTestsUsePersistedDatasource`
- `TestBulkScheduleStateAuditAndRollback`
- `TestBulkStopPreservesAttemptsAndUsesCanonicalCursorBehavior`
- `TestBusyDisabledFairnessAndConcurrentAttemptAllocation`
- `TestCancelledAndAbandonedAttemptsRemainUnresolved`
- `TestControlledBootstrapMatchesDWH2Topology`
- `TestCreateManyCreatesOrdinarySchedulesAndSkipsCurrentDuplicates`
- `TestCreateManySerializesIdenticalConcurrentRequestsAndUsesNormalRuntime`
- `TestDatasourceConnectionModePersistence`
- `TestDetailCleanupReclaimsRecoveredAndOrphanStagingWithoutRunFK`
- `TestDetailOutstandingMergedAndSplitPublicationIsAtomic`
- `TestDetailPublicationCrashBeforeSchedulerResolutionAdvancesOnce`
- `TestDetailStagingNoLongerLocksRunProgressRow`
- `TestDisableUpdateEnableFencesOldAttempt`
- `TestDurableQueueRunAllAndTerminalCAS`
- `TestDynamicOptionDefinitionPersistsCanonicalState`
- `TestExportAuthorizationClaimFencingAndDownloadRules`
- `TestExportOversightScopesHistoricalAccessAndAudit`
- `TestManualSubmissionsCreateOnlyUserAuditEvents`
- `TestMasterExecutorPublishesReferenceAndMarketingWithoutSnapshotDate`
- `TestMySQLCanonicalParametersAndAllRunAllChildren`
- `TestNoDateCoalescesAndAdvancesFromSuccessfulFinish`
- `TestRetryUntilSuccessAndChronologicalCursor`
- `TestSavingAccountStatementExactSetTextAndTimestamps`
- `TestScheduleManagementDerivesLiveSnapshotPolicy`
- `TestSchedulerParametersRoundTripMySQLForEveryCanonicalKind`
- `TestSourceStateCASPersistsActor`
- `TestStaleDetailWorkerCannotPublishAfterNewRunSucceeds`

## Existing controls preserved

Source review and passing ordinary/browser tests retain CrossOriginProtection, SameSite/HttpOnly cookies and production SESSION_SECURE validation, random 32-byte session tokens with only hashes persisted, Argon2id parameters and unknown-user dummy verification, reset/deactivation revocation, prepared binding and multiStatements=false, interactive bounds, export authorization/path containment, opaque dataset keys and size/UTF-8 validation, XLSX formula-injection behavior, production loopback/HTTPS/registration restrictions, and existing systemd hardening. Export/ingestion MySQL checks affected by the baseline failures above cannot be claimed to pass. FINCLOUD_INSECURE_SKIP_VERIFY remains explicitly out of scope and untouched.

## Exact changed files and purpose

All paths below are repository-relative; test entries verify the corresponding security boundary. No migration files changed.

| File | Security purpose |
| --- | --- |
| `.env.example` | Document limits/configuration, SELECT-only credentials, socket warning and deployment. |
| `README.md` | Document limits/configuration, SELECT-only credentials, socket warning and deployment. |
| `docs/PRODUCTION.md` | Document limits/configuration, SELECT-only credentials, socket warning and deployment. |
| `docs/REPORTING.md` | Document limits/configuration, SELECT-only credentials, socket warning and deployment. |
| `docs/SECURITY_HARDENING.md` | Audit, validation evidence, complete file list and deployment report. |
| `internal/app/app.go` | Wire authentication bounds/idle expiry and shared destination policy. |
| `internal/audit/audit.go` | Typed failed-login event with secret-free metadata. |
| `internal/browserauth/handler.go` | Bound password verification, throttle failures, audit safely and enforce idle expiry. |
| `internal/browserauth/http_test.go` | Verify authentication/idle/audit behavior. |
| `internal/browserauth/security.go` | Bound password verification, throttle failures, audit safely and enforce idle expiry. |
| `internal/browserauth/security_test.go` | Verify authentication/idle/audit behavior. |
| `internal/browserauth/service.go` | Bound password verification, throttle failures, audit safely and enforce idle expiry. |
| `internal/browserauth/service_test.go` | Verify authentication/idle/audit behavior. |
| `internal/config/config.go` | Parse/validate positive security settings and destination allowlists. |
| `internal/config/config_test.go` | Parse/validate positive security settings and destination allowlists. |
| `internal/customdataset/repository.go` | Effective-user fresh-upload lookup and transactional ownership/retry checks. |
| `internal/features/customdatasets/handler.go` | Effective-user fresh-upload lookup and transactional ownership/retry checks. |
| `internal/features/customdatasets/ownership_integration_test.go` | Real two-manager MySQL/HTTP ownership and retained retry regression coverage. |
| `internal/features/ingestion/testdata/browser/main.go` | CSP-enabled frontend fixture and behavior/template/browser regression checks. |
| `internal/features/ingestion/view_test.go` | CSP-enabled frontend fixture and behavior/template/browser regression checks. |
| `internal/features/reports/view_test.go` | CSP-enabled frontend fixture and behavior/template/browser regression checks. |
| `internal/features/sources/view_test.go` | CSP-enabled frontend fixture and behavior/template/browser regression checks. |
| `internal/platform/adminshell/shell_test.go` | CSP-enabled frontend fixture and behavior/template/browser regression checks. |
| `internal/reporting/destination.go` | CIDR/host/socket enforcement and validated-IP dialing. |
| `internal/reporting/destination_test.go` | Verify SQL policy, destination rules and pinned READ ONLY cleanup/discard semantics. |
| `internal/reporting/parameters.go` | Shared scanner-based read-only policy for report and dynamic option SQL. |
| `internal/reporting/pool.go` | Enforce destination policy on persistence and connection creation; preserve prepared/multi-statement settings. |
| `internal/reporting/query.go` | Pinned READ ONLY execution, bounded cleanup/discard and safe validation errors. |
| `internal/reporting/query_integration_test.go` | Verify SQL policy, destination rules and pinned READ ONLY cleanup/discard semantics. |
| `internal/reporting/query_test.go` | Verify SQL policy, destination rules and pinned READ ONLY cleanup/discard semantics. |
| `internal/reporting/readonly.go` | Shared scanner-based read-only policy for report and dynamic option SQL. |
| `internal/reporting/readonly_test.go` | Verify SQL policy, destination rules and pinned READ ONLY cleanup/discard semantics. |
| `internal/reporting/repository.go` | Enforce destination policy on persistence and connection creation; preserve prepared/multi-statement settings. |
| `internal/reporting/scanner.go` | Shared scanner-based read-only policy for report and dynamic option SQL. |
| `internal/server/middleware.go` | Full CSP and preserved response security headers; header regression tests. |
| `internal/server/middleware_test.go` | Full CSP and preserved response security headers; header regression tests. |
| `package-lock.json` | Use the official Alpine CSP frontend build. |
| `package.json` | Use the official Alpine CSP frontend build. |
| `tests/browser/csp.spec.js` | CSP-enabled frontend fixture and behavior/template/browser regression checks. |
| `web/src/css/app.css` | Replace eval/inline handlers with CSP-compatible components and self-hosted styles. |
| `web/src/js/app.js` | Replace eval/inline handlers with CSP-compatible components and self-hosted styles. |
| `web/static/css/app.css` | Self-hosted CSP-compatible scripts and rebuilt production assets. |
| `web/static/js/app.js` | Self-hosted CSP-compatible scripts and rebuilt production assets. |
| `web/static/js/head-state-init.js` | Self-hosted CSP-compatible scripts and rebuilt production assets. |
| `web/static/js/sidebar-init.js` | Self-hosted CSP-compatible scripts and rebuilt production assets. |
| `web/templates/components/toast.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/features/customdatasets/upload.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/features/datasources/form.html` | SELECT-only/socket privilege warning and CSP-safe controls. |
| `web/templates/features/ingestion/_runs_table.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/features/reports/index.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/features/reports/show.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/features/reporttemplates/form.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/features/schedules/bulk.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/features/schedules/index.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/features/sources/index.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/layouts/admin.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/partials/head-state-init.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |
| `web/templates/partials/sidebar.html` | Replace inline/eval-dependent frontend behavior with CSP-compatible markup. |


## Targeted follow-up on security-hardening HEAD

The follow-up applies exactly three corrections against `cd0b205`:

- At the 10,000-entry cap, untracked usernames still reach bounded password verification. Overflow failures do not allocate an entry; live tracked lockouts remain intact. Stale cleanup restores normal tracking.
- An internal throttle sentinel distinguishes skipped verification from real failed verification. Both render the identical generic HTTP response. Actual failed verifications, including the threshold attempt, are audited once; already locked requests and password-slot waiters that discover a lockout neither count another failure nor write another failed-login event.
- Exact hostname authorization for public resolved IPs is independent of configured CIDRs. Every address must satisfy explicit IP/CIDR authorization or the exact-hostname/public-address rule. Private/local addresses still require explicit IP/CIDR authorization. Dialing still uses validated IPs, preserving the original hostname for MySQL TLS identity verification.

Changed files: `internal/browserauth/security.go`, `internal/browserauth/service.go`, `internal/browserauth/handler.go`, `internal/browserauth/security_test.go`, `internal/browserauth/http_test.go`, `internal/reporting/destination.go`, `internal/reporting/destination_test.go`, `docs/REPORTING.md`, `docs/PRODUCTION.md`, and this report.

Follow-up validation passed: gofmt on the seven changed Go files; `go test ./internal/browserauth ./internal/reporting`; `go vet ./...`; `go test ./...`; `go build ./...`; and `make verify`. Go commands used `CGO_ENABLED=0 GOCACHE=/tmp/new-dwh-go-cache` to avoid host compiler caches. The focused integration-tagged reporting run passed nine top-level tests: four destination-policy tests and five real MySQL 8.4.10 tests (Unix socket identity, read-only policy/transaction, bounded abort/cancellation/discard, dynamic options and optional-block SQL modes). These were executed against the isolated disposable MySQL environment, which was stopped afterward. The full integration suite was not rerun for this follow-up; the 28 historical failures above are results from the preceding hardening validation, not new follow-up results.

No environment variable, schema migration, Fincloud TLS change, Argon2/session change, reporting SQL/execution change, pool-sizing change, dataset ownership change or CSP implementation change accompanies this follow-up. Remaining caveat: while failure tracking is saturated, overflow usernames have no per-username lockout until capacity returns; hashing remains bounded and per-IP protection remains a reverse-proxy responsibility. Audit writes remain best effort.
