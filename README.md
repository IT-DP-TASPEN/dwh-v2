# Go Admin Starter

A conventional server-rendered Go starter for authenticated administration applications. It provides reusable infrastructure without introducing a CRUD framework or application-specific features.

## Stack

- Go, Chi, `html/template`
- sqlx, MySQL 8+, Goose
- HTMX, Alpine.js, Tailwind CSS v4, Lucide
- Argon2id passwords and server-side database sessions

Production builds embed templates and generated assets in one Go binary. Development reads them from `web/` so template changes refresh without recompiling.

## Included

- Username/password login, Remember Me, optional public registration
- Code-defined granular RBAC with a protected administrator superuser role
- User and role management with race-safe last-active-admin protection
- Administrator-only impersonation with real actor/effective user separation
- Responsive nested navigation and Light/Dark/System theme
- Standard-library cross-origin protection, security headers, safe errors, and no-store HTML
- Append-only actor/effective audit logging
- Read-only, permission-gated audit log viewer
- Hourly expired-session cleanup
- Unit, race, and opt-in real-MySQL integration tests
- Immutable UTF-8 CSV custom datasets with typed schemas, atomic Replace/Append publication, stable SQL views, and retained import history

## Quick start

Requirements: Go 1.26.5+, Node.js 24+, npm, and MySQL 8+.

```sh
cp .env.example .env
npm install
make migrate
make admin
make dev
```

Edit `.env` before migrating. `DB_NAME` and `DB_USER` are required. Environment variables override `.env`; never commit `.env`. Development commands run from the repository root.

Open <http://localhost:8080> after creating the initial administrator.

## Commands

| Command | Purpose |
| --- | --- |
| `make dev` | Run Air, Tailwind, and esbuild watchers. |
| `make build` | Build production frontend assets plus `bin/app` and `bin/migrate`. |
| `make test` | Run the normal database-free test suite. |
| `make test-integration` | Run tagged tests against an explicit disposable MySQL database. |
| `make fmt` | Format Go packages. |
| `make lint` | Run `go vet ./...`. |
| `make verify` | Build frontend, vet, test, and compile without migrations or integration DB. |
| `make migrate` | Apply pending migrations. |
| `make migrate-down` | Roll back the latest migration. |
| `make migrate-status` | Display migration state. |
| `make migrate-create name=create_example` | Create a Goose SQL migration. |
| `make admin` | Interactively create an administrator. |
| `make rename-module module=github.com/example/project` | Safely rename the Go module and exact imports/docs. |
| `make feature name=customers` | Scaffold a minimal feature without wiring or overwriting files. |

Frontend source under `web/src/` is authoritative. Generated `web/static/css/app.css` and `web/static/js/app.js` are committed; do not edit them manually.

## Database

The application database is created from canonical Goose migrations. It is disposable: recreate an empty database, run `migrate up` from zero, bootstrap the administrator, and re-ingest source data. There is no in-place upgrade path for older schemas. The migration command operates only on the configured `DB_NAME`, verifies that the selected database matches it, and in production requires `--confirm-database <DB_NAME>`. Migrations never run from web startup.

Fixed source data is rebuildable. Seven Fixed reports publish authoritative calendar-date slices, so overlapping date ranges replace the overlap; P&L is an exact interval result. No historical backfill or cutover layer exists. See [Fixed Report publication](docs/FIXED_REPORT_PUBLICATION.md).

## Configuration

`APP_ENV` accepts `development`, `production`, or `test`. `APP_NAME` controls runtime branding and is independent of the Go module name. `APP_BIND_HOST` defaults to `127.0.0.1`; production requires a loopback IP. `APP_SHUTDOWN_TIMEOUT` defaults to 45 seconds.

`DB_NETWORK` accepts `tcp` (the default when omitted) or `unix`. TCP uses `DB_HOST` (default `127.0.0.1`) and `DB_PORT` (default `3306`). Unix mode requires `DB_SOCKET` and ignores `DB_HOST`/`DB_PORT`, with no TCP fallback. Both modes require `DB_NAME` and `DB_USER`. This configures the application's own database, including migrations and administrator commands. Report datasources select their connection mode independently in the datasource form.

Production also requires an HTTPS `APP_URL`, `SESSION_SECURE=true`, and `ALLOW_REGISTRATION=false`. TCP requires a nonempty `DB_PASSWORD` in production; Unix mode allows an empty password. For local MySQL `auth_socket`, set `DB_NETWORK=unix`, `DB_SOCKET=/var/run/mysqld/mysqld.sock`, `DB_NAME=dwh`, `DB_USER=dwhadmin`, and `DB_PASSWORD=`, then run the process as Linux user `dwhadmin`. See [Production operation](docs/PRODUCTION.md) for account, systemd, and storage permissions.

Report datasources support TCP with host, port, encrypted password, and the existing TLS policy, or Unix socket with an absolute socket path and no password or TLS. Existing rows migrate to TCP without changing their encrypted passwords. Switching to Unix clears the stored credential; switching back requires a new TCP password. All Unix datasources use the application's single Linux process identity. An `auth_socket` account for a different OS user cannot be impersonated. The datasource Test action uses the same pool and connection builder as report execution.

`ALLOW_REGISTRATION=false` removes both registration routes. When enabled, public registration always creates an active user with the protected `user` role; submitted role fields are ignored because no role choice exists.

Session settings:

- `SESSION_LIFETIME` controls normal fixed absolute expiry; default 24 hours.
- `SESSION_REMEMBER_LIFETIME` controls Remember Me fixed absolute expiry; default 30 days.
- `SESSION_SECURE=true` is required when serving through HTTPS.
- Activity updates `last_seen_at` but never extends expiry.
- Only token SHA-256 digests are persisted; raw tokens exist only in cookies.
- Expired rows are deleted immediately at server startup and approximately hourly afterward.

Authentication cookies are `HttpOnly`, `SameSite=Lax`, path `/`, and use the configured secure flag.

The web runtime requires `FINCLOUD_BASE_URL`, `APP_SECRET_ENCRYPTION_KEY`, and the Fincloud HTTP/TLS settings. The base URL must be absolute HTTPS without embedded credentials. `FINCLOUD_HTTP_TIMEOUT` defaults to `30s`; TLS verification is enabled unless `FINCLOUD_INSECURE_SKIP_VERIFY=true` is explicitly set. Fincloud usernames, passwords, roles, and locations are stored in managed Auth Profiles and bound explicitly to sources. Process startup performs no Fincloud login or connectivity probe.

An Auth Profile location ID is login/session context only. It is never substituted for fixed-report data scope; complete report location/account-code dimensions are source-enumerated internally.

## Permissions and management

The 42 canonical permission keys are aggregated from features and synchronized additively at server and CLI bootstrap. Unknown database permissions and assignments are preserved. Migrations never run automatically at application startup. `audit.view` may be assigned to user or custom roles; administrators receive it through the normal superuser bypass.

Each user has exactly one role. The `admin` role is an immutable superuser; non-admin roles use current database permission assignments. Permissions are loaded for every authenticated request, so changes apply on the next request without cache invalidation.

User profile, role, status, and password operations are separate POST mutations. Users are never hard-deleted. Password reset revokes sessions owned by the target. Deactivation revokes sessions owned by or impersonating the target. Database locks prevent concurrent changes from removing the final active administrator.

## Impersonation

Impersonation is an administrator-only lifecycle capability, not a granular permission.

- `sessions.user_id` remains the original authenticated administrator.
- `sessions.impersonated_user_id` becomes the temporary effective user.
- Authorization, navigation, and management rules use target permissions only; administrator bypass does not leak.
- Inactive, administrator, self, and nested targets are rejected.
- Start and stop rotate the session token while preserving Remember Me and absolute expiry.
- Logout terminates the entire underlying administrator-owned session.
- Invalid target state revokes the session instead of falling back to administrator access.

## Audit logging

`audit_logs` is append-only from application code. It stores nullable user IDs plus username snapshots for two distinct identities:

- Normal request: actor and effective user are the same.
- Impersonated request: actor is the administrator; effective user is the target.
- Registration and CLI administrator bootstrap have null actor/effective attribution and identify the created user as the resource.

User/role management changes and impersonation token transitions append their audit record inside the same database transaction. An audit failure rolls back the mutation. Successful login, logout, and registration use deliberate best-effort auditing: failure is logged but never invalidates a login, resurrects a logout, or removes a registered user.

Audit metadata is typed and allowlisted. Passwords, hashes, raw tokens, cookies, credentials, authorization headers, and request bodies are never recorded. The read-only `/audit-logs` viewer requires `audit.view`, uses stored identity snapshots, supports exact-action filtering and 50-row pages, and exposes no mutation route.

## Operational logging

The application writes structured logs to stdout only (JSON in production, text in development). Records carry stable `service`, `environment`, `component`, and `event` fields for later collection; audit logs and ingestion diagnostics stay in the database. See [Observability](docs/OBSERVABILITY.md).

## Adding a feature

Run `make feature name=customers`, then wire the generated feature in the single composition file. The scaffolder creates only a compiling route, handler, permission, navigation leaf, and template; add model/form/repository/service files only when the domain needs them.

See [Adding a Feature](docs/ADDING_A_FEATURE.md) for the complete migration, SQL, service, audit, route, permission, navigation, template, test, and composition workflow.

## HTTP and frontend behavior

All state changes are POST-only and protected by `http.CrossOriginProtection`. Missing application routes return a themed real `404`; unexpected failures return a generic `500` with a request ID. Panic values and stacks are logged, never rendered. Authenticated `403` pages retain the impersonation banner and Return to Admin action.

Sensitive HTML and HTMX fragments use `Cache-Control: no-store`; static assets do not. Headers deny framing/sniffing and restrict referrers and browser capabilities. CSP restricts default, script, stylesheet, image, font, and connection sources to self-hosted assets (data images are allowed). Inline scripts and eval are disabled using Alpine's CSP build and delegated HTMX handlers. Only style attributes have an inline exception for visibility/transitions and positioned menus.

HTMX is progressive enhancement: ordinary links/forms and server-rendered POST/redirect flows remain authoritative. Alpine handles theme/sidebar state, toasts, and the accessible destructive confirmation dialog. Theme and desktop sidebar preferences use `localStorage`; mobile drawer state is transient.

## Production deployment

```sh
make build
./bin/migrate up --confirm-database dwh
./bin/app serve
```

See [Production operation](docs/PRODUCTION.md) and [Production ingestion validation](docs/PRODUCTION_VALIDATION.md). Production operators should:

- terminate HTTPS correctly and set `SESSION_SECURE=true`;
- configure HSTS at the trusted TLS edge when appropriate;
- rate-limit `POST /login` at a trusted reverse proxy, load balancer, WAF, or edge;
- define and enforce trusted-proxy semantics before using `X-Forwarded-For` or `X-Real-IP`;
- protect environment secrets and database credentials;
- operate database backups and migrations explicitly.

The application bounds Argon2 verification concurrency and keeps a bounded, process-local failed-login throttle by normalized username. Both existing and unknown usernames return generic failures; unknown users still perform dummy verification outside lockout. Configure `AUTH_MAX_CONCURRENT_PASSWORD_HASHES`, `AUTH_LOGIN_FAILURE_WINDOW`, `AUTH_LOGIN_MAX_FAILURES`, and `AUTH_LOGIN_LOCKOUT`. Per-IP limits remain an edge responsibility. `SESSION_IDLE_TIMEOUT` defaults to `2h` and applies to remember-me sessions alongside absolute expiry. See [Production operation](docs/PRODUCTION.md) for deployment settings and limitations.

## Testing

```sh
make test
go test -race ./...
make verify
```

Run the permanent browser regression suite after installing its Chromium build:

```sh
npx playwright install chromium
npm run test:browser
```

Real MySQL tests are opt-in:

```sh
cp .env.test.example .env.test.local
set -a; . ./.env.test.local; set +a
make test-integration
```

All five `TEST_DB_*` variables must be explicitly present. The password may be empty. The complete test host/port/database selection must not match the normal runtime connection,. With a Unix runtime connection, tests must use a different database name: a TCP endpoint may reach the same MySQL server. A disposable database may legitimately be named `dwh` when it is isolated from a TCP runtime connection. Tests apply real migrations and truncate application tables there; they never fall back to runtime credentials.

Integration coverage also exercises migration from zero, snapshot transactions, member-complete fixed-report promotion, runtime additive DDL, schema-lock races, and physical disposal of a connection with uncertain named-lock release.

Datasource integration tests cover migration defaults, credential preservation, mode switching, audit metadata, and guarded rollback. Optional `TEST_DB_SOCKET` enables a real passwordless `auth_socket` check for both the production primary connection and a report datasource; provision the current test process's OS username as a MySQL socket account on the disposable database. `TEST_DB_SOCKET_MISMATCH_USER` additionally checks that a separately provisioned socket account with a different OS identity fails authentication. Never point these variables at production.

## Renaming the starter

Run before adding project-specific imports:

```sh
make rename-module module=github.com/example/project
go mod tidy
make test
```

The stdlib-only tool validates a conservative module path, edits the exact `go.mod` module directive, rewrites parsed Go import literals, updates exact documentation references, and reports changed files. It never renames directories and skips `.git`, dependencies, build output, temporary output, generated static assets, binaries, and unrelated similar text.

## Architecture

```text
cmd/app/              server and administrator CLI
cmd/migrate/          operator-controlled Goose wrapper
cmd/rename-module/    safe starter module renaming
cmd/feature/          minimal feature scaffolder
internal/access/      roles, permissions, synchronization, authorization
internal/audit/       append-only typed audit events
internal/auth/        passwords, sessions, tokens, and cleanup primitives
internal/browserauth/ login, registration, logout, and principal resolution
internal/securityctx/ neutral actor/effective request snapshots
internal/platform/    admin shell, navigation, pagination, and web helpers
internal/features/    dashboard, users, roles, impersonation, and audit viewer
internal/fincloud/    lazy authenticated Fincloud source client and active DTOs
internal/ingestion/   DWH source contracts, catalog, planning, and parsers
internal/ingestionstore/ fixed/detail/maintenance persistence and dynamic DDL
internal/dwhschema/   canonical migration list and runtime schema verification
internal/render/      templates, notices, and safe error responses
internal/server/      Chi routes, middleware, and graceful server
internal/reporting/   report templates, parameters, ACL, pools, and bounded MySQL execution
internal/reportexport/ durable export queue, XLSX/ZIP generation, fencing, and artifact storage
internal/testutil/    shared test-only infrastructure
migrations/           immutable Goose migration history
web/                  templates, frontend source, generated assets
```

Dependencies are wired explicitly at startup. Feature-local repositories use parameterized SQL; templates never query the database. The result remains reusable infrastructure plus conventional application code—not a generic resource/form/table DSL.

## Mandatory MFA

Every user, including administrators, must complete TOTP MFA before receiving a
browser session. First password login requires enrollment. Remember Me changes
session lifetime only. Sensitive datasource and report template changes require
MFA verified within 10 minutes. Recovery codes appear once.

**Deploying migration `20261005120000_mandatory_totp_mfa.sql` revokes every existing
browser session.** Deploy migrations, binary, and assets together; users must sign
in and enroll again where necessary. See [MFA operations and rollout](docs/MFA.md)
for recovery, encryption-key requirements, protected actions, and test commands.
