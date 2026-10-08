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

Loads freeze publication mode, source request mode, bounded chunk size, manifest version and exact Journal transaction-type IDs. Members preserve location/account dimensions, source endpoints and expected segment count alongside the stable member key.

`fixed_report_load_segments` retains actual source endpoints, `as_of_date`, request variant, row count and segment checksum. A zero-row segment is still recorded. Journal variants are the exact frozen transaction-type IDs. Segment metadata survives staging cleanup. Promotion validates frozen member and segment completeness, ordered row checksums and source-date integrity before modifying final rows.

## Schema and deployment

Fixed source data is rebuildable. The canonical migrations create the final shape directly: the seven date-addressable final and staging tables have `coverage_date DATE NOT NULL`, final tables carry `idx_fixed_coverage_date`, and P&L tables have no `coverage_date`. `fixed_report_publication_locks` (seeded with the eight Fixed jobs), `fixed_report_date_publications` and `fixed_report_load_segments` are created with the load-control tables. Runtime schema verification (`migrate up` and `/ready`) checks these columns, indexes, primary keys, mutex rows and provenance foreign keys.

There is no historical backfill, readiness flag or cutover layer. Fixed ingestion works immediately after migrating an empty database from zero. To populate history, run complete loads over the desired date range for each date-addressable report and every required exact P&L interval.

To spot-check that final rows agree with date authority:

```sql
SELECT f.coverage_date, COUNT(*) AS mismatched_rows
FROM fincloud_coa_movement_reports f
LEFT JOIN fixed_report_date_publications p
  ON p.job_key = 'coa_movement_report' AND p.coverage_date = f.coverage_date
WHERE f.coverage_date BETWEEN :from_date AND :to_date
  AND (p.active_load_id IS NULL OR p.active_load_id <> f.load_id)
GROUP BY f.coverage_date;
```

Expect no mismatches. A date may legitimately contain multiple source rows; source multiplicity is not an overlap defect.
