# Custom datasets

Custom datasets publish immutable UTF-8 CSV uploads into the same MySQL schema as the application. The public SQL contract is `custom_dataset_view_<id>`; physical `cNNN` columns and technical provenance columns are internal.

The importer supports comma, semicolon, and tab delimiters, strict RFC-style CSV quoting, a configurable logical header record, 50 columns, 1,000,000 published rows, and files up to exactly 150,000,000 bytes. Empty strings become SQL `NULL`; nonempty text is preserved exactly. Typed values use strict integer, fixed-point decimal, date/date-time, and TRUE/FALSE syntax.

Every upload and import is immutable provenance. A never-published provisioning dataset may retry its retained file with a revised schema or upload a corrected file, which creates a new upload/import while retaining the failed pair. The first successful publication freezes the schema. Active datasets accept only Replace or Append imports. Archive is terminal but deliberately does not remove the SQL view from reporting.

Replace stages a hidden generation and switches the dataset pointer in one transaction. Append stages hidden attempt rows and publishes the import in one transaction. Cleanup removes failed attempts and all imports in retired generations in bounded batches while keeping upload/import metadata and diagnostics.

Runtime requires same-schema `CREATE`, `ALTER`, `CREATE VIEW`, `SHOW VIEW`, and narrowly scoped `DROP` privileges. Set `CUSTOM_DATASET_DIR` in production. A conservative MySQL baseline is `max_allowed_packet >=256M`; the worker discovers the actual value and explicitly fails before executing a single-row batch it cannot prove will fit.
