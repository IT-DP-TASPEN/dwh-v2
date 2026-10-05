# Production operation

The production application is one long-running Go process connected to a clean MySQL database. It serves HTTP and runs the database-backed coordinator and scheduler internally. No cron job or systemd timer is used.

Infrastructure prerequisites are an HTTPS reverse proxy forwarding to the configured loopback address, MySQL 8.4+, and a separate access-controlled backup destination. Provisioning those systems is outside this repository.

## Configuration

```dotenv
APP_NAME="New DWH"
APP_ENV=production
APP_URL=https://dwh.example.internal
APP_BIND_HOST=127.0.0.1
APP_PORT=8080
APP_SHUTDOWN_TIMEOUT=45s
ALLOW_REGISTRATION=false

SESSION_COOKIE_NAME=admin_session
SESSION_LIFETIME=24h
SESSION_REMEMBER_LIFETIME=720h
SESSION_IDLE_TIMEOUT=2h
AUTH_MAX_CONCURRENT_PASSWORD_HASHES=4
AUTH_LOGIN_FAILURE_WINDOW=15m
AUTH_LOGIN_MAX_FAILURES=5
AUTH_LOGIN_LOCKOUT=15m
SESSION_SECURE=true

DB_NETWORK=tcp
DB_HOST=127.0.0.1
DB_PORT=3306
DB_NAME=dwh
DB_USER=new_dwh_runtime
DB_PASSWORD=replace-me

FINCLOUD_BASE_URL=https://fincloud.example/fincloud
FINCLOUD_HTTP_TIMEOUT=30s
FINCLOUD_INSECURE_SKIP_VERIFY=true

APP_SECRET_ENCRYPTION_KEY=replace-with-standard-base64-32-byte-key
REPORT_EXPORT_DIR=/var/lib/new-dwh/report-exports
# Replace examples with the actual reporting destinations. Narrow CIDRs preferred.
REPORT_DATASOURCE_ALLOWED_TCP_CIDRS=10.20.30.40/32
REPORT_DATASOURCE_ALLOWED_HOSTS=
REPORT_DATASOURCE_ALLOWED_UNIX_SOCKETS=/var/run/mysqld/mysqld.sock
CUSTOM_DATASET_DIR=/var/lib/new-dwh/custom-datasets
```

Production rejects HTTP `APP_URL`, registration, insecure session cookies, empty TCP database passwords, and non-loopback bind addresses. Fincloud TLS verification remains enabled by default. The accepted temporary exception requires the explicit Fincloud-only opt-out above and emits a warning without credentials or endpoint details.

### Local MySQL Unix socket

For MySQL on the same Linux host, replace the entire TCP database block above with:

```dotenv
DB_NETWORK=unix
DB_SOCKET=/var/run/mysqld/mysqld.sock
DB_NAME=dwh
DB_USER=dwhadmin
DB_PASSWORD=
```

Keep the other production settings above, including HTTPS, secure sessions, the encryption key, and durable storage directories. `DB_NETWORK` defaults to `tcp` when omitted; unknown or empty values are rejected. TCP retains its host/port defaults and production password requirement. Unix mode requires the socket, database name, and database user; it does not require or validate `DB_HOST`/`DB_PORT`. It never falls back to TCP. A missing socket or rejected authentication fails startup with the socket address and the driver error.

Run the application, migrations, and administrator CLI as Linux user `dwhadmin` with the same exported configuration. systemd's `EnvironmentFile` is loaded only for the service, not for interactive CLI commands. MySQL authenticates the connecting OS user through its server-side `auth_socket` plugin; the application does not implement authentication plugins, change users, invoke sudo, or supply a fake password. See the [MySQL socket authentication documentation](https://dev.mysql.com/doc/refman/8.4/en/socket-pluggable-authentication.html).

After the operator verifies that `auth_socket` is installed and active, provision a new account using an authorized MySQL administration session:

```sql
CREATE USER 'dwhadmin'@'localhost' IDENTIFIED WITH auth_socket;
GRANT ALL PRIVILEGES ON dwh.* TO 'dwhadmin'@'localhost';
```

If the account already exists, inspect its authentication plugin and grants before changing it. Keep privileges scoped to `dwh.*`; no global grants or `GRANT OPTION` are needed. This account covers runtime DDL and migrations. Separate migration and runtime accounts remain an option for TCP deployments or separately provisioned socket identities.

From a shell already running as `dwhadmin`, verify the actual socket and account without a password:

```sh
id -un
mysql --protocol=SOCKET --socket=/var/run/mysqld/mysqld.sock \
  --user=dwhadmin dwh --execute='SELECT USER(), CURRENT_USER(), DATABASE();'
```

Confirm `CURRENT_USER()` is `dwhadmin@localhost` and the selected database is `dwh`. Then run the application and check `/ready` after migrations. DSN unit tests cannot validate the host's MySQL plugin, grants, socket permissions, or OS identity.

### Report datasource Unix sockets

After migration `20260929120000_add_report_datasource_unix_socket.sql`, an administrator can create a report datasource with Connection **Unix socket**, Socket path `/var/run/mysqld/mysqld.sock`, Database `dwh`, and Username `dwhreport`. The form hides and disables TCP host, port, password, and TLS controls. Server validation requires an absolute socket path, database, and username. Unix connections store no password ciphertext and never decrypt an existing TCP credential. Password input or an enabled TLS policy is rejected; omitted TLS means disabled. No TCP fallback occurs.

Every datasource connection runs as the same application process. Do not reuse the primary `dwhadmin` runtime/migration account: its write and schema privileges are inappropriate for reporting. Provision a dedicated SELECT-only database account mapped by the server's `auth_socket` configuration to the application's OS identity:

```sql
CREATE USER 'dwhreport'@'localhost' IDENTIFIED WITH auth_socket AS 'dwhadmin';
GRANT SELECT ON dwh.* TO 'dwhreport'@'localhost';
```

Verify this mapping on the deployed MySQL server from a shell running as `dwhadmin`, using `--user=dwhreport`, before activation. Do not grant write, schema, FILE, or dangerous routine privileges. Socket authentication can otherwise inherit strong local privileges depending on server configuration. If the configured plugin does not support the required read-only identity mapping, use a separately provisioned SELECT-only TCP account. A socket account whose configured OS identity does not match must fail normally. The application never uses sudo, setuid, shell commands, or per-datasource OS impersonation.

Use **Test connection** before activating the datasource. This action opens the actual configured socket and executes `SELECT 1` through the report pool. User-facing failures remain generic and contain no credentials or DSN. Test audits record only the outcome. Updates record changed connection field names and credential/connection change flags without secret values. Existing TCP TLS policy, encrypted passwords, connection bounds, and pool limits remain unchanged. Leaving a TCP edit password blank preserves it. Switching TCP to Unix removes it; switching Unix to TCP requires a new password. Each update increments the revision, so cached pools refresh even across application instances.

The migration defaults existing rows to `tcp` and leaves ciphertext unchanged. Stop the old process before applying this migration, then start the new binary; mixed versions are unsupported because older datasource readers use `SELECT *` and do not know the new fields. Migrations remain operator-controlled. Rollback refuses while any Unix datasource remains: convert those records to valid TCP connections with real passwords before a reviewed non-production rollback. Production `migrate down` remains disabled. When rolling back to a binary without primary Unix support, restore a valid TCP application configuration too.

## Build and clean installation

From a reviewed commit:

```sh
npm ci
make build
sha256sum bin/app bin/migrate migrations/*.sql
```

Copy `bin/app`, `bin/migrate`, the immutable `migrations/` directory, the checksum manifest, and `deploy/new-dwh.service` into the release artifact. Do not include `.env`, frontend source maps, test databases, or the adoption command.

Create an empty database and use a migration account:

```sh
APP_ENV=production ./bin/migrate up --confirm-database dwh
./bin/app admin create --username admin --name Administrator
```

The migration command verifies the selected database name, refuses `dwh2`, rejects unknown Goose history, and accepts only an empty first installation or a canonical migration prefix. It never enables `AllowMissing` or stamps versions. Web startup never runs migrations.

The Detail current-state migration aborts before schema changes when any dated Detail parent or child table contains rows. Existing dated rows cannot be collapsed safely with `MAX(as_of_date)` because they have no durable complete-run identity. A populated deployment requires a separately approved backup/reset, the migration, then one fresh authoritative Detail synchronization.

Start the application only after `GET /ready` returns `200`. `/health` is process liveness; `/ready` checks MySQL and the current application schema without contacting Fincloud.

## Database privileges

Use separate migration and runtime accounts when provisioning distinct identities; the local socket example above uses the database-scoped `dwhadmin` account for both. Canonical migrations require DML plus `CREATE`, `ALTER`, `DROP`, `INDEX`, `REFERENCES`, `CREATE ROUTINE`, `ALTER ROUTINE`, and `EXECUTE` on the application database. The routine privileges support the adoption-aware validation procedure inside the canonical source-settings migration; no routine remains after a successful migration. Runtime requires:

```text
SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, CREATE VIEW, SHOW VIEW, DROP
```

`CREATE` and `ALTER` are mandatory for DynamicAdditive maintenance tables and custom dataset physical tables. `CREATE VIEW` and `SHOW VIEW` support the stable custom dataset query contract. `DROP` is used only by the guarded, never-published provisioning reset path; active dataset objects are never altered or dropped. Granting and account management remain operator responsibilities.

Set `CUSTOM_DATASET_DIR` to durable private storage shared by every application instance. Retained uploads are immutable provenance and must be included in backup/restore. Configure MySQL `max_allowed_packet` to at least `256M` as a conservative production baseline. The importer discovers the actual server value and rejects any single-row batch it cannot conservatively prove will fit; the baseline is a recommendation, not a guarantee for every legal near-150MB record.

## systemd template and shutdown

`deploy/new-dwh.service` is a template only. It now uses `User=dwhadmin` and `Group=dwhadmin` for the local `auth_socket` deployment. Provision that Linux user and group before starting the service. TCP deployments can retain their existing dedicated service identity. Adjust release paths before installation. Its `TimeoutStopSec=60s` exceeds the default application shutdown budget of 45 seconds. If `APP_SHUTDOWN_TIMEOUT` increases, increase `TimeoutStopSec` too.

The unit uses `StateDirectory=new-dwh` with mode `0700`; systemd creates `/var/lib/new-dwh` owned by the service user and makes it writable under `ProtectSystem=strict`. Set `REPORT_EXPORT_DIR=/var/lib/new-dwh/report-exports` and `CUSTOM_DATASET_DIR=/var/lib/new-dwh/custom-datasets` as above. The application creates private storage subdirectories with mode `0700` and files with mode `0600`. For CLI-first installation, an operator must create the state directory before starting the service:

```sh
install -d -o dwhadmin -g dwhadmin -m 0700 /var/lib/new-dwh
```

Before switching an existing service from `new-dwh` to `dwhadmin`, stop writers and back up retained uploads and exports. Inspect the actual configured storage paths and transfer ownership of those two storage trees, including existing files, to `dwhadmin:dwhadmin`. Preserve their private modes and stored paths. Do not recursively change ownership of the release, MySQL data, or unrelated directories. If using custom storage paths, add only those exact paths to `ReadWritePaths=` in a systemd override and ensure the service user owns them; `/home` is inaccessible with `ProtectHome=true`.

Keep `/opt/new-dwh` and its binaries and migrations operator-owned, readable/traversable by `dwhadmin`, with executable binaries. Keep `/etc/new-dwh/new-dwh.env` root-owned with mode `0600`; the system systemd manager reads it before dropping privileges. Do not deploy a second `.env` in the release directory. Ensure `dwhadmin` can traverse the socket's parent directories and connect to the MySQL-managed socket. Adjust only required access; do not change the socket's owner to the application user. `PrivateTmp=true` supports the example socket under `/var/run/mysqld`; a socket under `/tmp` requires a reviewed unit override or a socket location outside the private temporary namespace.

SIGTERM stops scheduler delivery, begins graceful HTTP shutdown, cancels owned ingestion work, waits for component cleanup, and force-closes only after the application deadline.

## Backup and restore

Store MySQL client credentials in a permission-restricted option file rather than command arguments:

```sh
mysqldump --defaults-extra-file=/secure/mysql-client.cnf \
  --single-transaction --skip-add-locks --no-tablespaces --set-gtid-purged=OFF dwh > dwh.sql
sha256sum dwh.sql > dwh.sql.sha256
sha256sum --check dwh.sql.sha256
mysql --defaults-extra-file=/secure/mysql-client.cnf restored_dwh < dwh.sql
```

After restore, run `migrate status`, start the application against the restored database, check `/ready`, and verify administrator login. The production backup destination, encryption, retention, and access policy belong to infrastructure operations; a restore must be proven before launch.

## Controlled launch

1. Migrate a clean `dwh` database.
2. Verify readiness and bootstrap the administrator.
3. Start `new-dwh.service` behind HTTPS.
4. Verify login, RBAC, sources, empty schedules, and run-history pages.
5. At a separately approved live-source gate, select `${SMOKE_DATE}` and jobs using current evidence about request/member volume, duration, Fincloud load, output rows, database growth, and failure risk.
6. Validate all 41 contracts using [Production validation](PRODUCTION_VALIDATION.md).
7. Create schedules disabled, review cron/timezone/policy, then enable only approved schedules.

For the 36→41 deployment, quiescence is mandatory: block new Run All submissions, wait for current Run All work, stop the old scheduler/coordinator, and require this query to return zero both before and after stopping writers:

```sql
SELECT COUNT(*)
FROM ingestion_runs
WHERE kind='run_all_parent'
  AND status='running';
```

Do not deploy the 41-job binary while this count is nonzero. Apply `20260903090000_rename_live_snapshot.sql` before `20260903091000_create_master_reference_ingestion.sql`, verify no executable legacy live-snapshot rows remain and exactly 41 source settings exist, then start only the new binary.

The candidate smoke order—Vault, Balance Sheet, representative EOD, representative CBR, detail, then CoA—is not proven or mandatory. Live evidence decides the final selection and order.

## Fincloud Auth Profile rollout

This migration requires a quiesced cutover. Do not disable sources individually. Record the IDs of enabled schedules, use the existing bulk schedule action to disable them, and configure the reverse proxy to reject these ingestion-producing requests while retaining read and Auth Profile administration access:

```text
POST /sources/{jobKey}/runs
POST /runs/run-all
POST /runs/{id}/recover-abandoned
```

Drain the old process and require this query to return zero:

```sql
SELECT COUNT(*)
FROM ingestion_runs
WHERE status IN ('planned', 'queued', 'running');
```

Then stop the old application writers and run the query again. Only after the old binary is stopped, remove its legacy reporting-key variable and set `APP_SECRET_ENCRYPTION_KEY` using the exact same decoded 32-byte key. The new binary recognizes only `APP_SECRET_ENCRYPTION_KEY`. Existing reporting-datasource v1 ciphertext does not need re-encryption.

Apply `20260903120000_create_fincloud_auth_profiles.sql`, deploy the new binary with schedules still disabled and the ingress block still active, then create, Test, and Activate Auth Profiles. Explicitly bind every enabled source. This readiness query must return zero before ingestion resumes:

```sql
SELECT COUNT(*)
FROM source_settings s
LEFT JOIN fincloud_auth_profiles p ON p.id = s.fincloud_auth_profile_id
WHERE s.enabled = TRUE
  AND (p.id IS NULL OR p.status <> 'active');
```

After the UI and query both show zero configuration-required enabled sources, bulk-enable exactly the schedules recorded before the cutover, remove the ingress block, and confirm scheduler, direct, Run All, and reporting-v1 access.

## Reporting and authentication security

Production report datasource destinations fail closed when all destination allowlists are empty. Configure the three `REPORT_DATASOURCE_ALLOWED_*` variables before restarting. Existing stored datasources are validated again, and every new physical connection resolves and checks its destination before dialing an already validated IP. Every resolved address must be authorized either by an allowed CIDR/exact literal IP, or by an exact allowlisted hostname resolving to a public destination. These rules are independent even when CIDRs are configured. Private, loopback, and link-local destinations still require an explicitly permitted CIDR or literal IP; any disallowed DNS answer rejects the entire hostname. Socket paths must be absolute, already cleaned, and exactly allowlisted; traversal and equivalent alternate spelling are rejected. Use narrowly scoped CIDRs and protect allowlisted socket paths against replacement or symlinks by untrusted local users.

Every production reporting account must be SELECT-only, with no INSERT/UPDATE/DELETE, CREATE/ALTER/DROP, FILE, or privilege to invoke dangerous stored routines. Use a separate reporting account from the application runtime/migration accounts. Unix socket authentication can inherit strong local privileges depending on MySQL configuration; the socket's database account must also be read-only. SQL validation and READ ONLY transactions are defense in depth, not substitutes for these grants. See [Reporting](REPORTING.md).

`SESSION_IDLE_TIMEOUT` defaults to `2h`; absolute session lifetimes still apply, including remember-me sessions. Session activity is touched periodically, so expiry is measured from the last persisted activity (up to five minutes before the last request with default settings). Idle sessions are revoked when encountered. Password reset and deactivation still revoke sessions.

`AUTH_MAX_CONCURRENT_PASSWORD_HASHES` defaults to `4`. Requests wait within their context; synchronous Argon2 work keeps its slot until completion even after cancellation. Argon2 parameters and unknown-user dummy verification remain unchanged. Failed usernames are normalized and locked after `AUTH_LOGIN_MAX_FAILURES` (default `5`) within `AUTH_LOGIN_FAILURE_WINDOW` (default `15m`) for `AUTH_LOGIN_LOCKOUT` (default `15m`). Success resets the state. Actual failed credential verifications (including dummy verification and the attempt that starts lockout) each create one failed-login audit event. Requests rejected by an already active lockout skip verification, failure-count updates and failed-login audit writes, including when lockout is discovered after waiting for a password slot. Both outcomes render the same generic login response. Failed-login audit records contain only the attempted normalized username and event time, never account existence, passwords, hashes, or tokens.

Throttling is process-local and resets on restart. Its map holds at most 10,000 usernames; saturation leaves new usernames untracked until cleanup frees entries, but does not block their bounded password verification or evict existing live lockouts. Continue applying per-IP limits at a trusted reverse proxy and aggregate security events across replicas. The application does not trust X-Forwarded-For for security decisions. All new durations and integer bounds must be positive.

Rebuild frontend assets (`npm ci && npm run build`) and deploy the updated binary and static assets together. CSP requires the official Alpine CSP build; inline scripts and eval are disabled. The narrow `style-src-attr 'unsafe-inline'` exception supports Alpine visibility/transitions and context-menu positioning; stylesheet elements remain self-hosted. No schema migration is required for these changes.

## Mandatory MFA deployment

The mandatory TOTP MFA migration revokes all existing browser sessions. Back up
the database, preserve `APP_SECRET_ENCRYPTION_KEY`, deploy the new migration,
binary, and assets together, apply migrations through the normal migration
command, then restart. There are no automatic startup migrations. Users without
an enrollment must verify their authenticator and save the 10 recovery codes
before entering the application. Ensure server NTP synchronization.

Challenges last five minutes with five failed factor attempts. Sensitive actions
require MFA within 10 minutes. Rotation, recovery regeneration, administrative
reset, and emergency operator reset revoke sessions. No remembered-device or
administrator MFA bypass exists. See [MFA operations](MFA.md) for the full rollout
sequence, protected operations, and `app user mfa-reset --username USER` recovery.
