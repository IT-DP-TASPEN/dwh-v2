# Production ingestion validation

This matrix is a runbook, not evidence that live Fincloud behavior has been re-tested. For every approved smoke execution record the selected date, frozen member count, request count, duration, row count, database growth, safe error classification, and final publication/snapshot status. Never record credentials, sessions, customer payloads, or sensitive row values.

| Job key | Category | Contract | Validation focus |
| --- | --- | --- | --- |
| `cif_opening_report` | Fixed | Range; one all-location-empty member | chunks, rows, atomic promotion |
| `journal_transaction_report` | Fixed | Range; one all-location-empty member | chunks, rows, atomic promotion |
| `balance_sheet_report` | Fixed | Date series × frozen locations | location provenance, all-member promotion |
| `profit_loss_statement` | Fixed | Range × frozen locations | location provenance, all-member promotion |
| `coa_movement_report` | Fixed | Range × frozen account codes | explicit empty location, zero-row members, load |
| `fund_distribution_report` | Fixed | Range; one all-location-empty member | chunks, rows, atomic promotion |
| `vault_mutation_report` | Fixed | Range; one all-location-empty member | chunks, rows, atomic promotion |
| `teller_mutation_report` | Fixed | Range; one all-location-empty member | chunks, rows, atomic promotion |
| `eod_cif_opening_report_full` | EOD | Date series; lookback 3 | matched date, dynamic columns, snapshot |
| `eod_detail_outstanding_rekening_pinjaman` | EOD | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `eod_laporan_pelunasan_pinjaman_sebelum_jt` | EOD | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `eod_laporan_pembayaran_angsuran` | EOD | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `eod_laporan_pencairan_pinjaman` | EOD | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `eod_laporan_pinjaman_akan_jatuh_tempo` | EOD | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `eod_loan_write_off_report` | EOD | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `eod_savings_account_api_transaction` | EOD | Date series; lookback 3 | row-number identity, snapshot |
| `eod_savings_account_closing_report` | EOD | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `eod_savings_account_opening_report` | EOD | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `eod_savings_account_balance_report` | EOD | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `eod_loan_will_due_report` | EOD | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `eod_savings_balance_details_report` | EOD | Date series; lookback 3 | intentional fixture gap; contract validation |
| `eod_time_deposit_account_balance_details` | EOD | Date series; lookback 3 | intentional fixture gap; contract validation |
| `eod_time_deposit_closing_report` | EOD | Date series; lookback 3 | intentional fixture gap; contract validation |
| `eod_time_deposit_placement_report` | EOD | Date series; lookback 3 | intentional fixture gap; contract validation |
| `eod_savings_balance_details_report_rak` | EOD | Date series; lookback 3 | intentional fixture gap; contract validation |
| `cbr_balance_sheet` | CBR | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `cbr_arrears` | CBR | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `cbr_collateral` | CBR | Date series; lookback 3 | row-number identity, snapshot |
| `cbr_customer` | CBR | Date series; lookback 3 | row-number identity, snapshot |
| `cbr_loan` | CBR | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `cbr_savings` | CBR | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `cbr_time_deposit` | CBR | Date series; lookback 3 | identity, dynamic columns, snapshot |
| `cif_detail` | Detail | Current live snapshot | frozen Jakarta date, identifiers, partial progress |
| `saving_detail` | Detail | Current live snapshot | frozen Jakarta date, identifiers, partial progress |
| `time_deposit_detail` | Detail | Current live snapshot | frozen Jakarta date, parents/children, partial progress |
| `loan_detail` | Detail | Current live snapshot | frozen Jakarta date, parents/children, partial progress |

## Acceptance per job

- Canonical parameters and internal member set match the catalog contract.
- Source completion is proven without exposing payloads.
- Persisted row counts/checksums and snapshot or publication scope are correct.
- Cancellation/failure leaves previously committed data according to the job’s Phase 3/4 atomicity contract.
- Runtime growth and duration remain acceptable before schedules are enabled.

Create production schedules only after the corresponding job is accepted. Use strict five-field cron and an IANA timezone, create disabled, review the first future occurrence, and then enable. Detail schedules represent live coalesced snapshots; missed historical detail states cannot be reconstructed.
