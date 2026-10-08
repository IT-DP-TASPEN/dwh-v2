-- +goose NO TRANSACTION
-- +goose Up
-- Schema only: historical data is validated in bounded transactions by
-- fixed-coverage-backfill. INSTANT/INPLACE explicitly prohibit COPY fallback.
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

ALTER TABLE fixed_report_loads
    ADD COLUMN contract_version SMALLINT UNSIGNED NULL,
    ADD COLUMN publication_mode VARCHAR(32) NULL,
    ADD COLUMN source_request_mode VARCHAR(32) NULL,
    ADD COLUMN source_max_chunk_days SMALLINT UNSIGNED NULL,
    ADD COLUMN source_variants JSON NULL,
    ALGORITHM=INSTANT;
ALTER TABLE fixed_report_load_members
    ADD COLUMN source_location_id VARCHAR(191) NULL,
    ADD COLUMN account_code VARCHAR(191) NULL,
    ADD COLUMN source_period_from DATE NULL,
    ADD COLUMN source_period_to DATE NULL,
    ADD COLUMN expected_segment_count INT UNSIGNED NULL,
    ALGORITHM=INSTANT;

CREATE TABLE fixed_report_coverage_state (
    id TINYINT UNSIGNED NOT NULL,
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    ready_at DATETIME(6) NULL,
    PRIMARY KEY (id),
    CONSTRAINT chk_fixed_coverage_singleton CHECK (id = 1)
) ENGINE=InnoDB;
CREATE TABLE fixed_report_coverage_backfill (
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    last_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    complete BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (job_key)
) ENGINE=InnoDB;

ALTER TABLE fincloud_cif_opening_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE stg_fincloud_cif_opening_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE fincloud_cif_opening_reports ADD KEY idx_fixed_coverage_date (coverage_date), ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE fincloud_journal_transaction_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE stg_fincloud_journal_transaction_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE fincloud_journal_transaction_reports ADD KEY idx_fixed_coverage_date (coverage_date), ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE fincloud_balance_sheet_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE stg_fincloud_balance_sheet_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE fincloud_balance_sheet_reports ADD KEY idx_fixed_coverage_date (coverage_date), ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE fincloud_coa_movement_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE stg_fincloud_coa_movement_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE fincloud_coa_movement_reports ADD KEY idx_fixed_coverage_date (coverage_date), ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE fincloud_fund_distribution_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE stg_fincloud_fund_distribution_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE fincloud_fund_distribution_reports ADD KEY idx_fixed_coverage_date (coverage_date), ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE fincloud_vault_mutation_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE stg_fincloud_vault_mutation_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE fincloud_vault_mutation_reports ADD KEY idx_fixed_coverage_date (coverage_date), ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE fincloud_teller_mutation_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE stg_fincloud_teller_mutation_reports ADD COLUMN coverage_date DATE NULL, ALGORITHM=INSTANT;
ALTER TABLE fincloud_teller_mutation_reports ADD KEY idx_fixed_coverage_date (coverage_date), ALGORITHM=INPLACE, LOCK=NONE;

-- Empty installations are already prepared. Existing installations remain
-- blocked until the explicit backfill command exhaustively validates all rows.
INSERT INTO fixed_report_coverage_state (id, ready, ready_at)
SELECT 1, NOT (EXISTS (SELECT 1 FROM fincloud_cif_opening_reports LIMIT 1) OR
    EXISTS (SELECT 1 FROM fincloud_journal_transaction_reports LIMIT 1) OR
    EXISTS (SELECT 1 FROM fincloud_balance_sheet_reports LIMIT 1) OR
    EXISTS (SELECT 1 FROM fincloud_coa_movement_reports LIMIT 1) OR
    EXISTS (SELECT 1 FROM fincloud_fund_distribution_reports LIMIT 1) OR
    EXISTS (SELECT 1 FROM fincloud_vault_mutation_reports LIMIT 1) OR
    EXISTS (SELECT 1 FROM fincloud_teller_mutation_reports LIMIT 1)), NULL;
UPDATE fixed_report_coverage_state SET ready_at=CURRENT_TIMESTAMP(6) WHERE ready=TRUE;
INSERT INTO fixed_report_coverage_backfill (job_key, complete)
SELECT job_key, (SELECT ready FROM fixed_report_coverage_state WHERE id=1)
FROM fixed_report_publication_locks WHERE job_key <> 'profit_loss_statement';

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: Fixed coverage and source provenance retain publication history';
