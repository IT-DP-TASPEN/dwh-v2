# Observability

The application emits operational logs as structured records on stdout. It does
not ship logs anywhere, does not depend on a log backend, and does not expose
metrics or traces. If a collector or backend is down, the application is
unaffected.

```
dwh (slog) -> stdout -> journald / container runtime -> collector (Vector, Filebeat, Fluent Bit, ...) -> Elasticsearch / OpenSearch / ...
```

Collector and backend configuration are deployment concerns and live outside
this repository.

## Three kinds of records

| Concern | Answers | Authoritative store |
|---|---|---|
| Audit log | Who changed what (users, roles, schedules, datasources, runtime settings) | `audit_logs` table |
| Technical diagnostics | Why an ingestion step failed (Fincloud response, parser, mapper, MySQL error) | `ingestion_run_errors` via the TechnicalEvent pipeline |
| Operational logs | What started, finished, how long it took, how much work, and the outcome | stdout (this document) |

Operational logs never replace the other two. Audited mutations are not
repeated as INFO logs, and diagnostic `Details` are not copied to stdout.

## Format

- `APP_ENV=development`: human-readable text at DEBUG.
- Any other environment: JSON at INFO.

Every record carries slog's `time`, `level`, `msg`, plus `service` (from
`APP_NAME`) and `environment` (from `APP_ENV`). Component loggers add
`component`. Lifecycle records add `event`.

`msg` is for humans reading journalctl. Searches and dashboards should filter on
`component` and `event`.

### Canonical fields

| Field | Meaning |
|---|---|
| `service`, `environment` | Process identity, set once on the base logger |
| `component` | `app`, `http`, `auth`, `ingestion`, `fincloud`, `scheduler`, `report_export`, `custom_dataset` |
| `event` | Stable, lowercase, dot-separated identity; never contains IDs or values |
| `status` | Domain status (`succeeded`, `failed`, `cancelled`, `abandoned`, `claim_lost`) |
| `duration_ms` | Elapsed milliseconds measured with Go's monotonic clock |
| `run_id`, `parent_run_id`, `schedule_id`, `occurrence_id`, `export_job_id`, `import_id`, `dataset_id`, `report_id`, `datasource_id`, `request_id` | Internal IDs for drill-down (high cardinality; do not use as dashboard dimensions) |
| `job_key`, `category`, `pool_kind`, `operation`, `error_class`, `route` | Bounded values suitable for filters and grouping |

## Events

| Component | Event | Level | Purpose |
|---|---|---|---|
| app | `app.started` | INFO | Process ready; includes `goose_version`, `address` |
| app | `app.database.connected` | INFO | DB destination (`network`, `host`, `port`, `socket`, `database`; never credentials) |
| app | `app.fincloud_tls_verification_disabled` | WARN | Explicit warning when Fincloud TLS verification is off |
| app | `app.stopping` / `app.stopped` | INFO | Graceful shutdown start / end (`duration_ms`); exceeding the deadline still fails the process with ERROR |
| http | `http.request.completed` | INFO | One per request: `request_id`, `method`, `route`, `status`, `duration_ms`, `response_bytes`, `protocol` |
| http | `http.request.error` | ERROR | Handler error via ErrorResponder: `request_id`, `method`, `route`, `operation`, `error` |
| http | `http.request.panic` | ERROR | Recovered panic with stack |
| ingestion | `ingestion.run.started` | INFO | A claimed run begins: `run_id`, `job_key`, `category`, `kind`, `trigger`, `parent_run_id` |
| ingestion | `ingestion.run.completed` | INFO / WARN / ERROR | Exactly one per executed attempt: `status`, `duration_ms`, `rows`, `total`, `succeeded`, `failed`; on failure `error_class`, `error_step`, `cause_type` |
| ingestion | `ingestion.pool.started` | INFO | One per worker pool: `pool_kind` (`fixed_member`, `detail_item`), `concurrency`, `work_items` |
| ingestion | `ingestion.technical_diagnostic` | per severity | Safe summary of a persisted diagnostic: `class`, `step`, `operation`, `error_type`, `attempt`, `terminal`, `recovered`, `http_status`, `table`, `mysql_error`, `sqlstate`, `tx_attempt` |
| fincloud | `fincloud.request.completed` | DEBUG | Successful request: `operation`, `method`, `http_status`, `duration_ms` |
| fincloud | `fincloud.session.reauthenticated` | INFO | A 401 on a stale session actually triggered a fresh login (`reason=unauthorized`) |
| scheduler | `scheduler.run.submitted` | INFO | Occurrence delivered to ingestion: `schedule_id`, `occurrence_id`, `run_id`, `job_key`, `attempt`, `scheduled_for`, `trigger`, `delivery_delay_ms` |
| report_export | `report_export.started` | INFO | `export_job_id`, `report_id`, `datasource_id`, `attempt` |
| report_export | `report_export.completed` | INFO / ERROR | `status`, `duration_ms`; success adds `rows`, `parts`, `artifact_size_bytes`; failure adds `stage`, `error_class` |
| custom_dataset | `custom_dataset.import.started` | INFO | `import_id`, `dataset_id`, `mode`, `attempt` |
| custom_dataset | `custom_dataset.import.completed` | INFO / ERROR | `status`, `duration_ms`, `rows`, `source_records`; failure adds `error_class` |

Ingestion completion level: `succeeded` and `cancelled` are INFO, `abandoned`
is WARN, `failed` is ERROR. The status is read from the persisted run row after
Finish, so publication-owned finishes, cancellation fallbacks, and stale
recovery report the canonical outcome. The read never influences Finish.

Remaining records without an `event` (cleanup sweeps, heartbeat warnings,
claim/sweep errors) are unchanged degraded-state or failure logs.

### Noise rules

- Successful access records are suppressed for `/health`, `/ready`,
  `/static/*`, and the HTMX status polls `/ingestion/summary`,
  `/runs/{id}/status`, `/runs/{id}/children`, `/runs/scheduler-wave`, and
  `/custom-datasets/{id}/status`. Responses with status >= 400 on those routes
  are still logged.
- Detail and Fixed pools log once per pool, never per account, CIF, or member.
- Fincloud request successes are DEBUG, so production (INFO) never sees one
  line per Detail item.
- Empty scheduler sweeps and in-flight checks are silent.
- No SQL statement or per-query logging exists.

## Level policy

- **INFO**: coarse lifecycle (start, success, scheduler delivery, pool start).
- **WARN**: degraded but continuing (retries, heartbeat trouble, abandoned runs, progress persistence degraded).
- **ERROR**: an operation failed and needs investigation. Each failure gets one terminal ERROR; lower layers return errors rather than logging them again.
- **DEBUG**: high-volume successful technical events (Fincloud request completions).

## Sensitive data

Operational logs never contain:

- passwords, authorization headers, cookies, API keys, TOTP secrets, recovery
  codes, Fincloud session IDs, or Fincloud usernames;
- raw request or response bodies, raw authentication payloads, raw source rows;
- query strings (HTTP records use the chi route pattern, e.g. `/runs/{id}`,
  or `unmatched`; Fincloud records use the static operation name, never the URL);
- report SQL text, report parameter values, workbook contents, or artifact paths;
- uploaded CSV content or cell values (validation errors stay in the import's
  database diagnostics);
- customer names, account numbers, CIF numbers, NIK, phone numbers, addresses,
  ingestion item identifiers, or Fixed member keys;
- ownership/fencing tokens.

MySQL server messages can quote offending values or SQL fragments. Report
export and custom dataset failures therefore log only `mysql_error` and
`sqlstate` for MySQL errors.

## Examples

All values are fake.

```json
{"time":"2026-10-09T01:00:00.123Z","level":"INFO","msg":"scheduled run submitted","service":"dwh","environment":"production","component":"scheduler","event":"scheduler.run.submitted","schedule_id":14,"occurrence_id":903,"run_id":812,"job_key":"coa_movement_report","attempt":1,"trigger":"scheduler","scheduled_for":"2026-10-09T01:00:00Z","delivery_delay_ms":123}
{"time":"2026-10-09T01:00:00.400Z","level":"INFO","msg":"ingestion run started","service":"dwh","environment":"production","component":"ingestion","run_id":812,"job_key":"coa_movement_report","event":"ingestion.run.started","category":"fixed","kind":"job","trigger":"scheduler"}
{"time":"2026-10-09T01:00:01.020Z","level":"INFO","msg":"ingestion pool started","service":"dwh","environment":"production","component":"ingestion","event":"ingestion.pool.started","run_id":812,"job_key":"coa_movement_report","pool_kind":"fixed_member","concurrency":4,"work_items":31}
{"time":"2026-10-09T01:01:12.533Z","level":"INFO","msg":"ingestion run completed","service":"dwh","environment":"production","component":"ingestion","run_id":812,"job_key":"coa_movement_report","event":"ingestion.run.completed","category":"fixed","status":"succeeded","duration_ms":72133,"rows":18210,"total":31,"succeeded":31,"failed":0}
{"time":"2026-10-09T08:15:02.004Z","level":"INFO","msg":"http request completed","service":"dwh","environment":"production","component":"http","event":"http.request.completed","request_id":"host/abc123-000042","method":"GET","route":"/runs/{id}","status":200,"duration_ms":18,"response_bytes":20431,"protocol":"HTTP/1.1"}
{"time":"2026-10-09T08:20:41.870Z","level":"ERROR","msg":"report export failed","service":"dwh","environment":"production","component":"report_export","export_job_id":77,"report_id":5,"datasource_id":2,"attempt":1,"event":"report_export.completed","status":"failed","duration_ms":412,"stage":"query","error_class":"query_failed","error_type":"*mysql.MySQLError","mysql_error":1146,"sqlstate":"42S02"}
```

## Not in this phase

- Metrics (`/metrics`, Prometheus) are not implemented.
- Tracing (OpenTelemetry, trace IDs, spans) is not implemented; correlate with
  `request_id` and domain IDs.
- Log shipping is not implemented in the application; configure a collector at
  deployment.
