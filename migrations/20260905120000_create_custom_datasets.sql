-- +goose Up
CREATE TABLE custom_dataset_uploads (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    storage_key VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    original_filename VARCHAR(255) NOT NULL,
    byte_size BIGINT UNSIGNED NOT NULL,
    sha256 BINARY(32) NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'uploaded',
    revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
    created_by_user_id BIGINT UNSIGNED NOT NULL,
    retained_at DATETIME(6) NULL,
    expires_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_custom_dataset_uploads_storage_key (storage_key),
    KEY idx_custom_dataset_uploads_expiry (status,expires_at,id),
    CONSTRAINT fk_custom_dataset_uploads_created_by FOREIGN KEY (created_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT chk_custom_dataset_uploads_status CHECK (status IN ('uploaded','retained','expired')),
    CONSTRAINT chk_custom_dataset_uploads_revision CHECK (revision > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE custom_datasets (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name VARCHAR(128) NOT NULL,
    description VARCHAR(1000) NOT NULL DEFAULT '',
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'provisioning',
    revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
    schema_revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
    current_generation_id BIGINT UNSIGNED NULL,
    row_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
    created_by_user_id BIGINT UNSIGNED NOT NULL,
    updated_by_user_id BIGINT UNSIGNED NOT NULL,
    activated_at DATETIME(6) NULL,
    archived_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_custom_datasets_status (status,name,id),
    CONSTRAINT fk_custom_datasets_created_by FOREIGN KEY (created_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT fk_custom_datasets_updated_by FOREIGN KEY (updated_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT chk_custom_datasets_status CHECK (status IN ('provisioning','active','archived')),
    CONSTRAINT chk_custom_datasets_revision CHECK (revision > 0),
    CONSTRAINT chk_custom_datasets_schema_revision CHECK (schema_revision > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE custom_dataset_columns (
    dataset_id BIGINT UNSIGNED NOT NULL,
    ordinal SMALLINT UNSIGNED NOT NULL,
    display_name LONGTEXT NOT NULL,
    query_name VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    physical_name CHAR(4) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    logical_type VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    date_format VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
    PRIMARY KEY (dataset_id,ordinal),
    UNIQUE KEY uq_custom_dataset_columns_query (dataset_id,query_name),
    UNIQUE KEY uq_custom_dataset_columns_physical (dataset_id,physical_name),
    CONSTRAINT fk_custom_dataset_columns_dataset FOREIGN KEY (dataset_id) REFERENCES custom_datasets (id) ON DELETE RESTRICT,
    CONSTRAINT chk_custom_dataset_columns_ordinal CHECK (ordinal BETWEEN 1 AND 50),
    CONSTRAINT chk_custom_dataset_columns_type CHECK (logical_type IN ('text','integer','decimal','date','datetime','boolean')),
    CONSTRAINT chk_custom_dataset_columns_date_format CHECK (
        (logical_type IN ('date','datetime') AND date_format IS NOT NULL) OR
        (logical_type NOT IN ('date','datetime') AND date_format IS NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE custom_dataset_imports (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    dataset_id BIGINT UNSIGNED NOT NULL,
    upload_id BIGINT UNSIGNED NOT NULL,
    delimiter VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    header_record_number BIGINT UNSIGNED NOT NULL,
    mode VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    schema_revision BIGINT UNSIGNED NOT NULL,
    generation_id BIGINT UNSIGNED NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'queued',
    phase VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'queued',
    attempt INT UNSIGNED NOT NULL DEFAULT 0,
    published_attempt INT UNSIGNED NULL,
    owner_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    claimed_at DATETIME(6) NULL,
    heartbeat_at DATETIME(6) NULL,
    source_records BIGINT UNSIGNED NOT NULL DEFAULT 0,
    staged_rows BIGINT UNSIGNED NOT NULL DEFAULT 0,
    failure_class VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    failure_message VARCHAR(500) NULL,
    diagnostics JSON NULL,
    diagnostics_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    submitted_by_user_id BIGINT UNSIGNED NOT NULL,
    started_at DATETIME(6) NULL,
    finished_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    active_dataset_id BIGINT UNSIGNED GENERATED ALWAYS AS (CASE WHEN status IN ('queued','running') THEN dataset_id ELSE NULL END) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY uq_custom_dataset_imports_active (active_dataset_id),
    KEY idx_custom_dataset_imports_claim (status,created_at,id),
    KEY idx_custom_dataset_imports_stale (status,heartbeat_at,id),
    KEY idx_custom_dataset_imports_history (dataset_id,created_at,id),
    KEY idx_custom_dataset_imports_publish (dataset_id,generation_id,status,id,published_attempt),
    KEY idx_custom_dataset_imports_upload (upload_id,id),
    CONSTRAINT fk_custom_dataset_imports_dataset FOREIGN KEY (dataset_id) REFERENCES custom_datasets (id) ON DELETE RESTRICT,
    CONSTRAINT fk_custom_dataset_imports_upload FOREIGN KEY (upload_id) REFERENCES custom_dataset_uploads (id) ON DELETE RESTRICT,
    CONSTRAINT fk_custom_dataset_imports_submitted_by FOREIGN KEY (submitted_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT chk_custom_dataset_imports_delimiter CHECK (delimiter IN ('comma','semicolon','tab')),
    CONSTRAINT chk_custom_dataset_imports_header CHECK (header_record_number > 0),
    CONSTRAINT chk_custom_dataset_imports_mode CHECK (mode IN ('replace','append')),
    CONSTRAINT chk_custom_dataset_imports_status CHECK (status IN ('queued','running','succeeded','failed')),
    CONSTRAINT chk_custom_dataset_imports_phase CHECK (phase IN ('queued','validating','staging','publishing','complete')),
    CONSTRAINT chk_custom_dataset_imports_attempt CHECK (attempt >= 0),
    CONSTRAINT chk_custom_dataset_imports_schema_revision CHECK (schema_revision > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE custom_datasets
    ADD CONSTRAINT fk_custom_datasets_current_generation FOREIGN KEY (current_generation_id) REFERENCES custom_dataset_imports (id) ON DELETE RESTRICT;

ALTER TABLE custom_dataset_imports
    ADD CONSTRAINT fk_custom_dataset_imports_generation FOREIGN KEY (generation_id) REFERENCES custom_dataset_imports (id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE custom_datasets DROP FOREIGN KEY fk_custom_datasets_current_generation;
DROP TABLE custom_dataset_columns;
DROP TABLE custom_dataset_imports;
DROP TABLE custom_datasets;
DROP TABLE custom_dataset_uploads;
