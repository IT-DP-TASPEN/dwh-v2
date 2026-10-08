# Fixed Report publication

Fixed definitions explicitly separate publication authority from source request chunking. The canonical catalog still contains 41 jobs. Scheduling remains the previous Jakarta calendar day, with existing Run All hierarchy, admission, retries and head-of-line behavior.

| Job | Publication authority | Source requests |
| --- | --- | --- |
| `cif_opening_report` | Register Date | Range chunks of at most 30 days |
| `journal_transaction_report` | Transaction Date | Range chunks of at most 30 days × frozen transaction types |
| `balance_sheet_report` | Requested snapshot date | One date × frozen locations |
| `coa_movement_report` | Date | Range chunks of at most 30 days × frozen account codes |
| `fund_distribution_report` | Journal Date | Range chunks of at most 30 days |
| `vault_mutation_report` | Source datetime calendar date | Range chunks of at most 30 days |
| `teller_mutation_report` | Source transactiondate calendar date | Range chunks of at most 30 days |
| `profit_loss_statement` | Exact requested interval × complete frozen locations | One exact interval request per location |

## Date authority

A successful candidate replaces the complete final row collection for every calendar date in its inclusive parent interval. For example, Feb–Jul replaces Feb–Jul and leaves January alone, regardless of the older rows' parent interval or load ID. `coverage_date DATE` is normalized publication metadata; raw source columns, parent periods, `as_of_date`, source segment indices, row numbers and checksums remain provenance.

CIF authority follows registration date, including mutable customer attributes returned by a later complete source request. No report deduplicates by checksum or an invented business key. Legitimate repeated source rows remain repeated.

The six source date fields are strict: CIF/Journal/CoA/Fund use `YYYY-MM-DD`; Vault uses `YYYY -MM-DDHH:MM:SS`; Teller uses `YYYY-MM-DD HH:MM:SS`. Vault and Teller retain the validated source calendar date without timezone conversion. Balance Sheet uses the requested snapshot date and does not parse a fabricated CSV date. Every date-bearing row must fall inside its actual source segment, not merely the parent interval. Blank, malformed, unsupported or out-of-segment dates fail the entire load as `source_contract`. A failed load never promotes its partial staging.

`fixed_report_date_publications(job_key, coverage_date)` records authority even for dates with no rows. A successful header-only response deletes previous rows for covered dates and records the new load; it does not create fake final rows.

Publication locks `fixed_report_publication_locks` for the job inside its transaction, then checks date authority. A larger `fixed_report_loads.id` wins. If any covered date has a larger active load ID, the entire candidate is stale, including otherwise unclaimed dates. Different jobs have separate mutex rows. Replacement, date metadata, published load status and `FinishSucceededInTx` commit together. Existing `ingestion_runs.active_job_key` admission and run-owner fencing remain intact.

## Profit & Loss and consumer SQL

P&L is an interval result. Each frozen location receives exactly one source call `[location, period_from, period_to]`, even for intervals longer than 30 days. There is no chunk fallback or consolidation formula. Rejection of the exact source request fails the run.

Its segment endpoints equal the parent interval. `as_of_date = period_to` is request provenance, not a business date; P&L has no `coverage_date`. `fixed_report_publications` remains the exact-interval authority for P&L. An exact rerun physically replaces that interval; different overlapping intervals may coexist intentionally.

A report meaning “P&L for X..Y” must select exact endpoints, for example:

```sql
SELECT source_location_id, co_a_no, beginning_balance, debit, credit, last_balance
FROM fincloud_profit_loss_statements
WHERE period_from = :period_from AND period_to = :period_to;
```

Repository search found no shipped/default P&L SQL template to rewrite. User-authored SQL must be reviewed by its owners; filtering P&L by an overlapping date or `as_of_date` does not identify an exact interval result. Date-report consumers can continue their source-date filters without joining publication metadata. In particular the deployed OJK CoA Movement date query benefits from physical replacement once its dates are refreshed.

## Persistent provenance

New loads freeze publication mode, source request mode, bounded chunk size, manifest version and exact Journal transaction-type IDs. New members preserve location/account dimensions, source endpoints and expected segment count alongside the stable member key.

`fixed_report_load_segments` retains actual source endpoints, `as_of_date`, request variant, row count and segment checksum. A zero-row segment is still recorded. Journal variants are the exact frozen transaction-type IDs. Segment metadata survives staging cleanup. Promotion validates frozen member and segment completeness, ordered row checksums and source-date integrity before modifying final rows. Existing historical run/load/member metadata and old exact-range publication evidence are retained.

## Migration and historical preparation

Migration `20261008120000_fixed_report_date_publication.sql` is schema-only and nontransactional because MySQL DDL commits independently. It adds nullable coverage columns with explicit `ALGORITHM=INSTANT` and final-table coverage indexes with `ALGORITHM=INPLACE, LOCK=NONE`, preventing silent COPY fallback. Existing Journal/CoA query indexes remain. DDL still needs brief metadata locks; index construction reads large tables and consumes I/O, temporary disk and replication capacity. Measure capacity and schedule a maintenance window. Never run this migration through application startup.

The inspected production MySQL version was 8.4.11; the seven-table footprint was approximately 29 GB of data plus 9 GB of indexes, with roughly 80 million estimated rows. All seven reported Dynamic row format. The configured read-only inspection account cannot read InnoDB instant-column version counters because it lacks PROCESS; an operator should check those limits and capacity before DDL. The migration was executed successfully on disposable MySQL 8.4.10, not on production. Requiring NOT NULL immediately or backfilling all rows in one migration transaction would impose unnecessary rebuild/transaction risk. New writes and promotion enforce non-null valid dates in application code. Nullable legacy schema is intentional during preparation; runtime schema verification checks the migration-owned columns/indexes/tables.

A populated installation starts with durable `fixed_report_coverage_state.ready = FALSE`; Fixed execution, load creation and promotion fail closed. Other application features may remain available. Fresh empty installations are immediately ready. Old binaries must remain stopped throughout cutover because they do not understand this guard.

The explicit operator command validates the configured/selected database and requires its exact name. It refuses `dwh2`. It performs bounded transactions (default 1,000 rows, maximum 5,000), scans each final table by primary key, strictly validates every source date including already-filled rows, and commits coverage updates with a durable watermark in `fixed_report_coverage_backfill`. Invalid rows or mismatched prefilled coverage roll back the entire batch without advancing progress. Earlier committed batches remain resumable. Fix an invalid legacy date only through a separately authorized source-evidence remediation; the command never guesses or skips it.

The command refuses queued/running Fixed runs. Stop workers and terminalize outstanding runs through the established cancellation/recovery procedures first. After every table is exhausted it verifies no null coverage remains and atomically activates the model. It is idempotent after readiness. Do not manually advance watermarks or set readiness true, and do not allow external writers during preparation.

Backfill does not create historical date authority or reinterpret overlaps. Existing identical/overlapping historical rows remain until authoritative re-ingestion replaces those dates. Historical staging is not resumed under the new manifest version. Old run/load/member and interval-publication history is not deleted.

## Deployment sequence

1. Back up `dwh`, including schema and retained ingestion audit tables; verify restoration procedures and disk/replication capacity. `dwh2` stays a read-only parity reference and is never a runtime dependency.
2. Disable submissions/scheduling and stop every ingestion worker/old application instance. Resolve queued/running Fixed runs using existing operational procedures. Keep writers stopped until readiness is verified.
3. Build/deploy the new binary, migration runner and operator command together (`make build` creates all three binaries). Run `bin/migrate up` with the migration account and configuration targeting `dwh`. Do not start an old binary against the new schema.
4. With the same selected database configuration and an account allowed to update the preparation tables/final rows, run:

   ```sh
   bin/fixed-coverage-backfill --confirm-database dwh --batch-size 1000
   ```

   Monitor I/O, binlogs, replication lag, disk and batch progress. Interrupting the command rolls back its current batch; rerun the same command to resume. Smaller batches reduce transaction size. Index DDL is not automatically resumable: on migration interruption inspect completed DDL and Goose state before an operator-approved recovery; do not blindly rerun partial nontransactional DDL.
5. Verify `bin/migrate status`, runtime schema verification, `SELECT ready, ready_at FROM fixed_report_coverage_state WHERE id=1`, seven completed backfill rows, and zero null coverage in every date final table. A malformed legacy date blocks activation until correctly remediated. No production preparation commands were executed as part of implementation.
6. Start only the new application/workers and re-enable scheduling/submissions.
7. Run complete authoritative loads over the desired retained date history for CIF, Journal, Balance Sheet date series, CoA Movement, Fund Distribution, Vault Mutation and Teller Mutation. All frozen locations/accounts/types must complete.
8. Rerun every required exact P&L interval to replace previously chunk-concatenated results. Overlapping intervals remain distinct.
9. Validate representative consumer queries, including OJK CoA Movement and exact-interval P&L. Historical overlaps outside refreshed dates may still be present.
10. For each refreshed report verify complete date-authority metadata (including empty dates), and check final rows agree with it:

    ```sql
    SELECT f.coverage_date, COUNT(*) AS mismatched_rows
    FROM fincloud_coa_movement_reports f
    LEFT JOIN fixed_report_date_publications p
      ON p.job_key = 'coa_movement_report' AND p.coverage_date = f.coverage_date
    WHERE f.coverage_date BETWEEN :from_date AND :to_date
      AND (p.active_load_id IS NULL OR p.active_load_id <> f.load_id)
    GROUP BY f.coverage_date;
    ```

    Expect no mismatches. A retained date may legitimately contain multiple source rows; their multiplicity is not an overlap defect. A nonrefreshed historical date may still contain multiple old loads. Do not delete history or deduplicate by checksum to conceal this distinction.

This migration has no destructive automatic Down path. Application rollback after cutover requires a coordinated restoration plan; an old binary can reintroduce overlap and incorrect P&L chunking.
