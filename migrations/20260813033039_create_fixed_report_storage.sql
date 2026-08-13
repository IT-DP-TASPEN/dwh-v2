-- +goose Up
CREATE TABLE fincloud_cif_opening_reports (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    load_id BIGINT UNSIGNED NOT NULL,
    row_ordinal BIGINT UNSIGNED NOT NULL,
    source_segment_index INT UNSIGNED NOT NULL,
    source_row_number BIGINT UNSIGNED NOT NULL,
    source_row_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_file_name VARCHAR(255) NULL,
    period_from DATE NOT NULL,
    period_to DATE NOT NULL,
    as_of_date DATE NOT NULL,
    cif_no TEXT NULL, cif_alt_no TEXT NULL, customer_name TEXT NULL, alias_name TEXT NULL,
    mobile_phone TEXT NULL, home_phone TEXT NULL, religion TEXT NULL, formal_education TEXT NULL,
    employee_id_retired_id TEXT NULL, age TEXT NULL, customer_type TEXT NULL, occupation TEXT NULL,
    company_name TEXT NULL, office_address TEXT NULL, total_monthly_income TEXT NULL, customer_status TEXT NULL,
    national_id_no TEXT NULL, tax_id TEXT NULL, emails TEXT NULL, birth_date TEXT NULL, birth_place TEXT NULL,
    home_address TEXT NULL, home_urban_village TEXT NULL, home_sub_distric TEXT NULL, home_city TEXT NULL,
    home_province TEXT NULL, home_postal_code TEXT NULL, mother_maiden_name TEXT NULL, gender TEXT NULL,
    marital_status TEXT NULL, customer_data_attachment TEXT NULL, branch_code TEXT NULL, officer_create TEXT NULL,
    register_date TEXT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_cif_opening_scope (period_from, period_to),
    KEY idx_cif_opening_load (load_id),
    CONSTRAINT fk_cif_opening_load FOREIGN KEY (load_id) REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE stg_fincloud_cif_opening_reports LIKE fincloud_cif_opening_reports;
ALTER TABLE stg_fincloud_cif_opening_reports
    DROP PRIMARY KEY, DROP COLUMN id,
    ADD member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL AFTER load_id,
    ADD PRIMARY KEY (load_id, member_key, row_ordinal),
    ADD UNIQUE KEY uq_stg_cif_opening_source_row (load_id, member_key, source_segment_index, source_row_number),
    ADD CONSTRAINT fk_stg_cif_opening_member FOREIGN KEY (load_id, member_key)
        REFERENCES fixed_report_load_members (load_id, member_key) ON DELETE CASCADE;

CREATE TABLE fincloud_journal_transaction_reports (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    load_id BIGINT UNSIGNED NOT NULL, row_ordinal BIGINT UNSIGNED NOT NULL,
    source_segment_index INT UNSIGNED NOT NULL, source_row_number BIGINT UNSIGNED NOT NULL,
    source_row_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_file_name VARCHAR(255) NULL, period_from DATE NOT NULL, period_to DATE NOT NULL, as_of_date DATE NOT NULL,
    branch TEXT NULL, journal_id TEXT NULL, transaction_date TEXT NULL, transaction_type TEXT NULL,
    reference_number TEXT NULL, description TEXT NULL, officer_create TEXT NULL, account_no TEXT NULL,
    customer_name TEXT NULL, customer_no TEXT NULL, account_alternate_no TEXT NULL, currency TEXT NULL,
    co_a_no TEXT NULL, co_a_name TEXT NULL, debit TEXT NULL, credit TEXT NULL, transaction_value TEXT NULL,
    transaction_code TEXT NULL, time TEXT NULL, create_date TEXT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id), KEY idx_journal_scope (period_from, period_to), KEY idx_journal_load (load_id),
    CONSTRAINT fk_journal_load FOREIGN KEY (load_id) REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE stg_fincloud_journal_transaction_reports LIKE fincloud_journal_transaction_reports;
ALTER TABLE stg_fincloud_journal_transaction_reports
    DROP PRIMARY KEY, DROP COLUMN id,
    ADD member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL AFTER load_id,
    ADD PRIMARY KEY (load_id, member_key, row_ordinal),
    ADD UNIQUE KEY uq_stg_journal_source_row (load_id, member_key, source_segment_index, source_row_number),
    ADD CONSTRAINT fk_stg_journal_member FOREIGN KEY (load_id, member_key)
        REFERENCES fixed_report_load_members (load_id, member_key) ON DELETE CASCADE;

CREATE TABLE fincloud_balance_sheet_reports (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    load_id BIGINT UNSIGNED NOT NULL, row_ordinal BIGINT UNSIGNED NOT NULL,
    source_segment_index INT UNSIGNED NOT NULL, source_row_number BIGINT UNSIGNED NOT NULL,
    source_row_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_file_name VARCHAR(255) NULL, period_from DATE NOT NULL, period_to DATE NOT NULL, as_of_date DATE NOT NULL,
    source_location_id VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    branch TEXT NULL, co_a_no TEXT NULL, chart_of_account TEXT NULL, beginning_balance TEXT NULL,
    debit_transaction TEXT NULL, credit_transaction TEXT NULL, last_balance TEXT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id), KEY idx_balance_sheet_scope (period_from, period_to), KEY idx_balance_sheet_load (load_id),
    CONSTRAINT fk_balance_sheet_load FOREIGN KEY (load_id) REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE stg_fincloud_balance_sheet_reports LIKE fincloud_balance_sheet_reports;
ALTER TABLE stg_fincloud_balance_sheet_reports
    DROP PRIMARY KEY, DROP COLUMN id,
    ADD member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL AFTER load_id,
    ADD PRIMARY KEY (load_id, member_key, row_ordinal),
    ADD UNIQUE KEY uq_stg_balance_sheet_source_row (load_id, member_key, source_segment_index, source_row_number),
    ADD CONSTRAINT fk_stg_balance_sheet_member FOREIGN KEY (load_id, member_key)
        REFERENCES fixed_report_load_members (load_id, member_key) ON DELETE CASCADE;

CREATE TABLE fincloud_profit_loss_statements (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    load_id BIGINT UNSIGNED NOT NULL, row_ordinal BIGINT UNSIGNED NOT NULL,
    source_segment_index INT UNSIGNED NOT NULL, source_row_number BIGINT UNSIGNED NOT NULL,
    source_row_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_file_name VARCHAR(255) NULL, period_from DATE NOT NULL, period_to DATE NOT NULL, as_of_date DATE NOT NULL,
    source_location_id VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    co_a_no TEXT NULL, chart_of_account TEXT NULL, beginning_balance TEXT NULL, debit TEXT NULL, credit TEXT NULL,
    last_balance TEXT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id), KEY idx_profit_loss_scope (period_from, period_to), KEY idx_profit_loss_load (load_id),
    CONSTRAINT fk_profit_loss_load FOREIGN KEY (load_id) REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE stg_fincloud_profit_loss_statements LIKE fincloud_profit_loss_statements;
ALTER TABLE stg_fincloud_profit_loss_statements
    DROP PRIMARY KEY, DROP COLUMN id,
    ADD member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL AFTER load_id,
    ADD PRIMARY KEY (load_id, member_key, row_ordinal),
    ADD UNIQUE KEY uq_stg_profit_loss_source_row (load_id, member_key, source_segment_index, source_row_number),
    ADD CONSTRAINT fk_stg_profit_loss_member FOREIGN KEY (load_id, member_key)
        REFERENCES fixed_report_load_members (load_id, member_key) ON DELETE CASCADE;

CREATE TABLE fincloud_coa_movement_reports (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    load_id BIGINT UNSIGNED NOT NULL, row_ordinal BIGINT UNSIGNED NOT NULL,
    source_segment_index INT UNSIGNED NOT NULL, source_row_number BIGINT UNSIGNED NOT NULL,
    source_row_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_file_name VARCHAR(255) NULL, period_from DATE NOT NULL, period_to DATE NOT NULL, as_of_date DATE NOT NULL,
    co_a_no TEXT NULL, branch TEXT NULL, `date` TEXT NULL, journal_id TEXT NULL, beginning_balance TEXT NULL,
    debit TEXT NULL, credit TEXT NULL, last_balance TEXT NULL, reference_no TEXT NULL, transaction_type TEXT NULL,
    description TEXT NULL, officer_create TEXT NULL, user_authorize TEXT NULL, create_date TEXT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id), KEY idx_coa_movement_scope (period_from, period_to), KEY idx_coa_movement_load (load_id),
    CONSTRAINT fk_coa_movement_load FOREIGN KEY (load_id) REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE stg_fincloud_coa_movement_reports LIKE fincloud_coa_movement_reports;
ALTER TABLE stg_fincloud_coa_movement_reports
    DROP PRIMARY KEY, DROP COLUMN id,
    ADD member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL AFTER load_id,
    ADD PRIMARY KEY (load_id, member_key, row_ordinal),
    ADD UNIQUE KEY uq_stg_coa_movement_source_row (load_id, member_key, source_segment_index, source_row_number),
    ADD CONSTRAINT fk_stg_coa_movement_member FOREIGN KEY (load_id, member_key)
        REFERENCES fixed_report_load_members (load_id, member_key) ON DELETE CASCADE;

CREATE TABLE fincloud_fund_distribution_reports (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    load_id BIGINT UNSIGNED NOT NULL, row_ordinal BIGINT UNSIGNED NOT NULL,
    source_segment_index INT UNSIGNED NOT NULL, source_row_number BIGINT UNSIGNED NOT NULL,
    source_row_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_file_name VARCHAR(255) NULL, period_from DATE NOT NULL, period_to DATE NOT NULL, as_of_date DATE NOT NULL,
    savings_alt_no TEXT NULL, journal_date TEXT NULL, fund_distribution_name TEXT NULL, transaction_no TEXT NULL,
    cif_no TEXT NULL, customer_name TEXT NULL, savings_no TEXT NULL, savings_product TEXT NULL,
    transaction_type TEXT NULL, transaction_amount TEXT NULL, branch TEXT NULL, description TEXT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id), KEY idx_fund_distribution_scope (period_from, period_to), KEY idx_fund_distribution_load (load_id),
    CONSTRAINT fk_fund_distribution_load FOREIGN KEY (load_id) REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE stg_fincloud_fund_distribution_reports LIKE fincloud_fund_distribution_reports;
ALTER TABLE stg_fincloud_fund_distribution_reports
    DROP PRIMARY KEY, DROP COLUMN id,
    ADD member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL AFTER load_id,
    ADD PRIMARY KEY (load_id, member_key, row_ordinal),
    ADD UNIQUE KEY uq_stg_fund_distribution_source_row (load_id, member_key, source_segment_index, source_row_number),
    ADD CONSTRAINT fk_stg_fund_distribution_member FOREIGN KEY (load_id, member_key)
        REFERENCES fixed_report_load_members (load_id, member_key) ON DELETE CASCADE;

CREATE TABLE fincloud_vault_mutation_reports (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    load_id BIGINT UNSIGNED NOT NULL, row_ordinal BIGINT UNSIGNED NOT NULL,
    source_segment_index INT UNSIGNED NOT NULL, source_row_number BIGINT UNSIGNED NOT NULL,
    source_row_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_file_name VARCHAR(255) NULL, period_from DATE NOT NULL, period_to DATE NOT NULL, as_of_date DATE NOT NULL,
    branch TEXT NULL, tellername TEXT NULL, transactiontype TEXT NULL, currency TEXT NULL,
    beginningbalance TEXT NULL, lastbalance TEXT NULL, debit TEXT NULL, credit TEXT NULL, officer TEXT NULL, datetime TEXT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id), KEY idx_vault_mutation_scope (period_from, period_to), KEY idx_vault_mutation_load (load_id),
    CONSTRAINT fk_vault_mutation_load FOREIGN KEY (load_id) REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE stg_fincloud_vault_mutation_reports LIKE fincloud_vault_mutation_reports;
ALTER TABLE stg_fincloud_vault_mutation_reports
    DROP PRIMARY KEY, DROP COLUMN id,
    ADD member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL AFTER load_id,
    ADD PRIMARY KEY (load_id, member_key, row_ordinal),
    ADD UNIQUE KEY uq_stg_vault_mutation_source_row (load_id, member_key, source_segment_index, source_row_number),
    ADD CONSTRAINT fk_stg_vault_mutation_member FOREIGN KEY (load_id, member_key)
        REFERENCES fixed_report_load_members (load_id, member_key) ON DELETE CASCADE;

CREATE TABLE fincloud_teller_mutation_reports (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    load_id BIGINT UNSIGNED NOT NULL, row_ordinal BIGINT UNSIGNED NOT NULL,
    source_segment_index INT UNSIGNED NOT NULL, source_row_number BIGINT UNSIGNED NOT NULL,
    source_row_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_file_name VARCHAR(255) NULL, period_from DATE NOT NULL, period_to DATE NOT NULL, as_of_date DATE NOT NULL,
    referencenumber TEXT NULL, accountnumber TEXT NULL, customername TEXT NULL, transactiontype TEXT NULL,
    beginningbalance TEXT NULL, debit TEXT NULL, credit TEXT NULL, lastbalance TEXT NULL, branch TEXT NULL,
    tellerid TEXT NULL, userauthorization TEXT NULL, useroverride TEXT NULL, transactiondate TEXT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id), KEY idx_teller_mutation_scope (period_from, period_to), KEY idx_teller_mutation_load (load_id),
    CONSTRAINT fk_teller_mutation_load FOREIGN KEY (load_id) REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE stg_fincloud_teller_mutation_reports LIKE fincloud_teller_mutation_reports;
ALTER TABLE stg_fincloud_teller_mutation_reports
    DROP PRIMARY KEY, DROP COLUMN id,
    ADD member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL AFTER load_id,
    ADD PRIMARY KEY (load_id, member_key, row_ordinal),
    ADD UNIQUE KEY uq_stg_teller_mutation_source_row (load_id, member_key, source_segment_index, source_row_number),
    ADD CONSTRAINT fk_stg_teller_mutation_member FOREIGN KEY (load_id, member_key)
        REFERENCES fixed_report_load_members (load_id, member_key) ON DELETE CASCADE;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: fixed report storage may contain business data';
