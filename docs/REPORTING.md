# Report Template Engine V1

Reporting uses separately configured MySQL accounts. Keep those accounts read-only, deny `EXECUTE`, and leave `multiStatements=false`; these are the primary controls against stored-program and multiple-result-set execution. Datasource TLS is either verified with system roots (`required`) or explicitly disabled. Insecure certificate verification, private CAs, and client certificates are not supported in V1.

Set `APP_SECRET_ENCRYPTION_KEY` to the standard-base64 encoding of exactly 32 random bytes. New datasource passwords use a purpose- and datasource-ID-bound AES-256-GCM envelope. Existing reporting v1 ciphertext remains readable with the same key; changing only the environment-variable name does not require re-encryption. Losing or changing the key makes stored datasource credentials unreadable.

Interactive reads default to 10,000 rows, 8 MiB of encoded payload, 16 KiB per-cell previews, and 20 seconds. Crossing a row or payload bound cancels the query and discards its physical MySQL connection instead of draining unread rows. SQL is never rewritten and no `LIMIT` is injected. Full values remain available through background export, subject to XLSX format limits.

`single_option` and `multiple_option` parameters may use static options or a live dynamic query against the report's datasource. Dynamic queries return exactly `value,label`, may reference only earlier parameters, and use the same MySQL scanner/binder as report SQL. Runtime and export submission revalidate current membership in display order; export workers use the validated snapshot and do not rerun option queries.

Dynamic option reads default to 1,000 rows and 1 MiB of encoded option data, use the interactive timeout, and are never cached. Exceeding either bound fails the load and discards the physical MySQL connection; results are never silently truncated. Configure bounds with `REPORT_DYNAMIC_OPTION_MAX_ROWS` and `REPORT_DYNAMIC_OPTION_PAYLOAD_BYTES`.

Datetime parameters are timezone-naive SQL `DATETIME` wall-clock values. Entered components are preserved; the application does not convert them to UTC or Asia/Jakarta.

Exports use attempt-scoped workspaces and opaque final paths under `REPORT_EXPORT_DIR`. A fenced heartbeat controls active query and file generation; confirmed claim loss cancels both immediately. Cleanup expires referenced artifacts and removes unreferenced final artifacts after the orphan grace period.

V1 local-filesystem export storage assumes one application instance. Multiple instances require `REPORT_EXPORT_DIR` to be a shared filesystem with atomic rename semantics; otherwise a worker may publish an artifact that another instance cannot download or reconcile.

Runtime report stars and folders are personal presentation state owned by the effective user, including during impersonation. They never grant report access: every list, search, folder count, and starred count intersects the current explicit ACL with active report and datasource state. Preferences remain dormant while access is unavailable and reappear when access returns.

Folders are flat and each report has at most one folder per user. Deleting a folder transactionally clears that folder from every owned preference, including dormant memberships, without changing stars or deleting reports. Personal organization changes intentionally do not create security audit events.
