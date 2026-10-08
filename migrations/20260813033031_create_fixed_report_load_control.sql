-- +goose Up
CREATE TABLE fixed_report_loads (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    period_from DATE NOT NULL,
    period_to DATE NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    expected_member_count INT UNSIGNED NOT NULL,
    manifest_checksum BINARY(32) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    published_at DATETIME(6) NULL,
    contract_version SMALLINT UNSIGNED NULL,
    publication_mode VARCHAR(32) NULL,
    source_request_mode VARCHAR(32) NULL,
    source_max_chunk_days SMALLINT UNSIGNED NULL,
    source_variants JSON NULL,
    PRIMARY KEY (id),
    KEY idx_fixed_report_loads_scope (job_key, period_from, period_to, id),
    KEY idx_fixed_report_loads_status (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fixed_report_load_members (
    load_id BIGINT UNSIGNED NOT NULL,
    member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    staged_segment_count INT UNSIGNED NOT NULL DEFAULT 0,
    row_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
    member_checksum BINARY(32) NULL,
    source_location_id VARCHAR(191) NULL,
    account_code VARCHAR(191) NULL,
    source_period_from DATE NULL,
    source_period_to DATE NULL,
    expected_segment_count INT UNSIGNED NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (load_id, member_key),
    CONSTRAINT fk_fixed_report_load_members_load
        FOREIGN KEY (load_id) REFERENCES fixed_report_loads (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fixed_report_publications (
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    period_from DATE NOT NULL,
    period_to DATE NOT NULL,
    active_load_id BIGINT UNSIGNED NULL,
    published_at DATETIME(6) NULL,
    PRIMARY KEY (job_key, period_from, period_to),
    KEY idx_fixed_report_publications_active_load (active_load_id),
    CONSTRAINT fk_fixed_report_publications_active_load
        FOREIGN KEY (active_load_id) REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- One row per Fixed job: the transactional publication mutex.
CREATE TABLE fixed_report_publication_locks (
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    PRIMARY KEY (job_key)
) ENGINE=InnoDB;
INSERT INTO fixed_report_publication_locks (job_key) VALUES
    ('cif_opening_report'),
    ('journal_transaction_report'),
    ('balance_sheet_report'),
    ('profit_loss_statement'),
    ('coa_movement_report'),
    ('fund_distribution_report'),
    ('vault_mutation_report'),
    ('teller_mutation_report');

-- Authoritative load per (job, calendar date) for date-addressable reports.
CREATE TABLE fixed_report_date_publications (
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    coverage_date DATE NOT NULL,
    active_load_id BIGINT UNSIGNED NOT NULL,
    published_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (job_key, coverage_date),
    KEY idx_fixed_date_publication_load (active_load_id),
    CONSTRAINT fk_fixed_date_publication_load FOREIGN KEY (active_load_id)
        REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB;

-- Actual source request boundaries per member, including zero-row segments.
CREATE TABLE fixed_report_load_segments (
    load_id BIGINT UNSIGNED NOT NULL,
    member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    segment_index INT UNSIGNED NOT NULL,
    source_period_from DATE NOT NULL,
    source_period_to DATE NOT NULL,
    as_of_date DATE NOT NULL,
    row_count BIGINT UNSIGNED NOT NULL,
    request_variant VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL,
    segment_checksum BINARY(32) NOT NULL,
    PRIMARY KEY (load_id, member_key, segment_index),
    CONSTRAINT fk_fixed_segment_member FOREIGN KEY (load_id, member_key)
        REFERENCES fixed_report_load_members (load_id, member_key) ON DELETE RESTRICT
) ENGINE=InnoDB;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: fixed report loads may contain staged data';
