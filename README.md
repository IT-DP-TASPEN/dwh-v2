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

## Database lineage

Production starts from a clean application database and applies the canonical migrations from zero. `dwh2` is legacy/reference-only and the migration command refuses to mutate it. Migrations never run from web startup.

## Configuration

`APP_ENV` accepts `development`, `production`, or `test`. `APP_NAME` controls runtime branding and is independent of the Go module name. `APP_BIND_HOST` defaults to `127.0.0.1`; production requires a loopback IP. `APP_SHUTDOWN_TIMEOUT` defaults to 45 seconds.

Production also requires an HTTPS `APP_URL`, `SESSION_SECURE=true`, `ALLOW_REGISTRATION=false`, and a nonempty database password.

`ALLOW_REGISTRATION=false` removes both registration routes. When enabled, public registration always creates an active user with the protected `user` role; submitted role fields are ignored because no role choice exists.

Session settings:

- `SESSION_LIFETIME` controls normal fixed absolute expiry; default 24 hours.
- `SESSION_REMEMBER_LIFETIME` controls Remember Me fixed absolute expiry; default 30 days.
- `SESSION_SECURE=true` is required when serving through HTTPS.
- Activity updates `last_seen_at` but never extends expiry.
- Only token SHA-256 digests are persisted; raw tokens exist only in cookies.
- Expired rows are deleted immediately at server startup and approximately hourly afterward.

Authentication cookies are `HttpOnly`, `SameSite=Lax`, path `/`, and use the configured secure flag.

The web runtime also requires Fincloud source configuration: `FINCLOUD_BASE_URL`, `FINCLOUD_USERNAME`, `FINCLOUD_PASSWORD`, `FINCLOUD_LOCATION_ID`, and `FINCLOUD_ROLE_ID`. The base URL must be absolute HTTPS without embedded credentials. `FINCLOUD_HTTP_TIMEOUT` defaults to `30s`; TLS verification is enabled unless `FINCLOUD_INSECURE_SKIP_VERIFY=true` is explicitly set. These values are validated only by the web runtime: migrations and `app admin create` do not use or require Fincloud. Client construction performs no login or connectivity probe, so temporary Fincloud unavailability does not prevent a correctly configured web process from starting.

`FINCLOUD_LOCATION_ID` is login/session context only. It is never substituted for fixed-report data scope; complete report location/account-code dimensions are source-enumerated internally.

## Permissions and management

The 25 canonical permission keys are aggregated from features and synchronized additively at server and CLI bootstrap. Unknown database permissions and assignments are preserved. Migrations never run automatically at application startup. `audit.view` may be assigned to user or custom roles; administrators receive it through the normal superuser bypass.

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

## Adding a feature

Run `make feature name=customers`, then wire the generated feature in the single composition file. The scaffolder creates only a compiling route, handler, permission, navigation leaf, and template; add model/form/repository/service files only when the domain needs them.

See [Adding a Feature](docs/ADDING_A_FEATURE.md) for the complete migration, SQL, service, audit, route, permission, navigation, template, test, and composition workflow.

## HTTP and frontend behavior

All state changes are POST-only and protected by `http.CrossOriginProtection`. Missing application routes return a themed real `404`; unexpected failures return a generic `500` with a request ID. Panic values and stacks are logged, never rendered. Authenticated `403` pages retain the impersonation banner and Return to Admin action.

Sensitive HTML and HTMX fragments use `Cache-Control: no-store`; static assets do not. Baseline headers deny framing/sniffing and restrict referrers, browser capabilities, form targets, base URIs, and objects. The CSP intentionally omits `script-src` so the current Alpine build remains compatible.

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

The starter deliberately has no in-process login limiter: it cannot safely infer client identity behind arbitrary proxies, and an attacker-keyed in-memory map would be unsafe across replicas.

## Testing

```sh
make test
go test -race ./...
make verify
```

Real MySQL tests are opt-in:

```sh
cp .env.test.example .env.test.local
set -a; . ./.env.test.local; set +a
make test-integration
```

All five `TEST_DB_*` variables must be explicitly present. The password may be empty. The complete test host/port/database selection must not match the normal runtime connection, and the database may never be `dwh2` or `dwh3`. A disposable database may legitimately be named `dwh` when it is isolated from the runtime connection. Tests apply real migrations and truncate application tables there; they never fall back to runtime credentials.

Integration coverage also exercises the real Goose adoption topology, snapshot transactions, member-complete fixed-report promotion, runtime additive DDL, schema-lock races, and physical disposal of a connection with uncertain named-lock release.

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
cmd/adopt-dwh2/       explicit one-time dwh2 adoption administration
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
internal/adoption/    fail-closed dwh2 preflight and adoption engine
internal/dwhschema/   canonical DWH schema/adoption metadata
internal/render/      templates, notices, and safe error responses
internal/server/      Chi routes, middleware, and graceful server
internal/testutil/    shared test-only infrastructure
migrations/           immutable Goose migration history
web/                  templates, frontend source, generated assets
```

Dependencies are wired explicitly at startup. Feature-local repositories use parameterized SQL; templates never query the database. The result remains reusable infrastructure plus conventional application code—not a generic resource/form/table DSL.
