# Report Template Engine V1

Production reporting credentials MUST be SELECT-only/read-only. Deny INSERT/UPDATE/DELETE, CREATE/ALTER/DROP, FILE, and privilege to invoke dangerous stored routines (normally deny EXECUTE). Use separately configured reporting accounts, not application runtime or migration credentials. Do not infer safety by parsing SHOW GRANTS: inherited roles and stored programs make effective privileges difficult to prove.

The application accepts only lexical SELECT or WITH ... SELECT query forms. The shared MySQL-mode-aware scanner ignores strings, quoted identifiers, and ordinary comments; executable comments, multiple statements, output-file clauses, locking reads, assignments, and session named-lock functions are rejected without rewriting SQL. The policy applies to report and dynamic option templates, activation, active updates, test queries/options, interactive execution, and background exports.

Prepared execution occurs on one pinned physical connection inside START TRANSACTION READ ONLY. Successful EOF closes statements and rolls back before reuse. Cancellation, bounded abort, protocol errors, or uncertain cleanup discard the physical connection. Establishing a read-only transaction is mandatory; there is no writable fallback. Keep `multiStatements=false`. Application SQL validation and READ ONLY transactions are defense in depth, not replacements for database least privilege; SELECT can invoke stored programs or other side-effecting functions, and MySQL permits some temporary-table operations in read-only transactions. Datasource TLS is either verified with system roots (`required`) or explicitly disabled. Insecure certificate verification, private CAs, and client certificates are not supported in V1.

Set `APP_SECRET_ENCRYPTION_KEY` to the standard-base64 encoding of exactly 32 random bytes. New datasource passwords use a purpose- and datasource-ID-bound AES-256-GCM envelope. Existing reporting v1 ciphertext remains readable with the same key; changing only the environment-variable name does not require re-encryption. Losing or changing the key makes stored datasource credentials unreadable.

Interactive reads default to 10,000 rows, 8 MiB of encoded payload, 16 KiB per-cell previews, and 20 seconds. Crossing a row or payload bound cancels the query and discards its physical MySQL connection instead of draining unread rows. Optional clauses wrapped in `[[ ... ]]` are included only when every optional parameter they reference is provided; block-free SQL is not rewritten, and no `LIMIT` is injected. Full values remain available through background export, subject to XLSX format limits.

`single_option` and `multiple_option` parameters may use static options or a live dynamic query against the report's datasource. Dynamic queries return exactly `value,label`, may reference only earlier parameters, and use the same MySQL scanner/binder as report SQL. Runtime and export submission revalidate current membership in display order; export workers use the validated snapshot and do not rerun option queries.

Dynamic option reads default to 1,000 rows and 1 MiB of encoded option data, use the interactive timeout, and are never cached. Exceeding either bound fails the load and discards the physical MySQL connection; results are never silently truncated. Configure bounds with `REPORT_DYNAMIC_OPTION_MAX_ROWS` and `REPORT_DYNAMIC_OPTION_PAYLOAD_BYTES`.

Datetime parameters are timezone-naive SQL `DATETIME` wall-clock values. Entered components are preserved; the application does not convert them to UTC or Asia/Jakarta.

Exports use attempt-scoped workspaces and opaque final paths under `REPORT_EXPORT_DIR`. A fenced heartbeat controls active query and file generation; confirmed claim loss cancels both immediately. Cleanup expires referenced artifacts and removes unreferenced final artifacts after the orphan grace period.

V1 local-filesystem export storage assumes one application instance. Multiple instances require `REPORT_EXPORT_DIR` to be a shared filesystem with atomic rename semantics; otherwise a worker may publish an artifact that another instance cannot download or reconcile.

Runtime report stars and folders are personal presentation state owned by the effective user, including during impersonation. They never grant report access: every list, search, folder count, and starred count intersects the current explicit ACL with active report and datasource state. Preferences remain dormant while access is unavailable and reappear when access returns.

Folders are flat and each report has at most one folder per user. Deleting a folder transactionally clears that folder from every owned preference, including dormant memberships, without changing stars or deleting reports. Personal organization changes intentionally do not create security audit events.

## Destination allowlists

Configure comma-separated `REPORT_DATASOURCE_ALLOWED_TCP_CIDRS`, `REPORT_DATASOURCE_ALLOWED_HOSTS`, and `REPORT_DATASOURCE_ALLOWED_UNIX_SOCKETS`. Production has no implicit allowed destination. Development/test defaults to loopback TCP CIDRs only when all lists are empty. CIDRs and exact literal IPs authorize addresses; exact hostnames alone authorize public resolved addresses. When CIDRs are present, all resolved addresses must match a CIDR or exact literal IP. Private, localhost, and link-local addresses need explicit IP/CIDR permission even for an allowlisted hostname. Every resolved address is checked on saves/tests and on every new physical pool connection. Dialing uses the validated IP directly to prevent a second DNS lookup. A policy change requires process restart; stored datasources cannot bypass the new policy.

Unix sockets remain supported. Require an exact cleaned absolute socket path; alternate spellings and traversal are rejected. Socket authentication may inherit strong local MySQL privileges depending on server configuration. Its database account MUST also be SELECT-only; protect the socket path and parent directories from untrusted local changes. These checks authorize destinations, not database privileges.

Security integration tests use a disposable MySQL 8.4 schema. The account needs schema migration privileges plus SELECT on performance_schema to inspect current transaction access mode. The hidden-write probe needs CREATE ROUTINE and, when binary logging requires it, an appropriately administered test server with log_bin_trust_function_creators enabled. Never enable this setting on production just to run tests.
