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
SESSION_SECURE=true

DB_HOST=127.0.0.1
DB_PORT=3306
DB_NAME=dwh
DB_USER=new_dwh_runtime
DB_PASSWORD=replace-me

FINCLOUD_BASE_URL=https://fincloud.example/fincloud
FINCLOUD_USERNAME=replace-me
FINCLOUD_PASSWORD=replace-me
FINCLOUD_LOCATION_ID=replace-me
FINCLOUD_ROLE_ID=replace-me
FINCLOUD_HTTP_TIMEOUT=30s
FINCLOUD_INSECURE_SKIP_VERIFY=true
```

Production rejects HTTP `APP_URL`, registration, insecure session cookies, empty database passwords, and non-loopback bind addresses. Fincloud TLS verification remains enabled by default. The accepted temporary exception requires the explicit Fincloud-only opt-out above and emits a warning without credentials or endpoint details.

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

Use separate migration and runtime accounts. Canonical migrations require DML plus `CREATE`, `ALTER`, `DROP`, `INDEX`, `REFERENCES`, `CREATE ROUTINE`, `ALTER ROUTINE`, and `EXECUTE` on the application database. The routine privileges support the adoption-aware validation procedure inside the canonical source-settings migration; no routine remains after a successful migration. Runtime requires:

```text
SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER
```

`CREATE` and `ALTER` are mandatory for DynamicAdditive maintenance tables. Runtime does not require `DROP`. Granting and account management remain operator responsibilities.

## systemd template and shutdown

`deploy/new-dwh.service` is a template only. Install it as a non-root dedicated user after adjusting paths. Its `TimeoutStopSec=60s` exceeds the default application shutdown budget of 45 seconds. If `APP_SHUTDOWN_TIMEOUT` increases, increase `TimeoutStopSec` too.

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
6. Validate all 36 contracts using [Production validation](PRODUCTION_VALIDATION.md).
7. Create schedules disabled, review cron/timezone/policy, then enable only approved schedules.

The candidate smoke order—Vault, Balance Sheet, representative EOD, representative CBR, detail, then CoA—is not proven or mandatory. Live evidence decides the final selection and order.
