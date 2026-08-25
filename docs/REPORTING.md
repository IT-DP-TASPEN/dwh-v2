# Report Template Engine V1

Reporting uses separately configured MySQL accounts. Keep those accounts read-only, deny `EXECUTE`, and leave `multiStatements=false`; these are the primary controls against stored-program and multiple-result-set execution. Datasource TLS is either verified with system roots (`required`) or explicitly disabled. Insecure certificate verification, private CAs, and client certificates are not supported in V1.

Set `REPORT_DATASOURCE_MASTER_KEY` to the standard-base64 encoding of exactly 32 random bytes. Datasource passwords are encrypted with AES-256-GCM and datasource-ID additional authenticated data. Losing or changing this key makes stored datasource credentials unreadable.

Interactive reads default to 10,000 rows, 8 MiB of encoded payload, 16 KiB per-cell previews, and 20 seconds. Crossing a row or payload bound cancels the query and discards its physical MySQL connection instead of draining unread rows. SQL is never rewritten and no `LIMIT` is injected. Full values remain available through background export, subject to XLSX format limits.

Datetime parameters are timezone-naive SQL `DATETIME` wall-clock values. Entered components are preserved; the application does not convert them to UTC or Asia/Jakarta.

Exports use attempt-scoped workspaces and opaque final paths under `REPORT_EXPORT_DIR`. A fenced heartbeat controls active query and file generation; confirmed claim loss cancels both immediately. Cleanup expires referenced artifacts and removes unreferenced final artifacts after the orphan grace period.

V1 local-filesystem export storage assumes one application instance. Multiple instances require `REPORT_EXPORT_DIR` to be a shared filesystem with atomic rename semantics; otherwise a worker may publish an artifact that another instance cannot download or reconcile.
