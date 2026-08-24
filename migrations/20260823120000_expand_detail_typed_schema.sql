-- +goose Up
DROP PROCEDURE IF EXISTS assert_detail_typed_expansion_quiesced;
-- +goose StatementBegin
CREATE PROCEDURE assert_detail_typed_expansion_quiesced()
BEGIN
    IF EXISTS (
        SELECT 1 FROM ingestion_runs
        WHERE status = 'running'
          AND job_key IN ('cif_detail', 'saving_detail', 'time_deposit_detail', 'loan_detail')
        LIMIT 1
    ) THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'Detail typed expansion requires quiesced Detail jobs';
    END IF;
END;
-- +goose StatementEnd

CALL assert_detail_typed_expansion_quiesced();
DROP PROCEDURE assert_detail_typed_expansion_quiesced;

ALTER TABLE fincloud_cifs
    ADD COLUMN alt_no VARCHAR(32) NULL,
    ADD COLUMN document_status VARCHAR(32) NULL,
    ADD COLUMN location_name VARCHAR(64) NULL,
    ADD COLUMN record_created_by VARCHAR(64) NULL,
    ADD COLUMN record_created_location VARCHAR(32) NULL,
    ADD COLUMN record_updated_by VARCHAR(64) NULL,
    ADD COLUMN record_updated_location VARCHAR(32) NULL,
    ADD COLUMN record_updated_at DATETIME(6) NULL,
    ADD COLUMN record_timestamp DATETIME(6) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE stg_fincloud_cif_details
    ADD COLUMN alt_no VARCHAR(32) NULL,
    ADD COLUMN document_status VARCHAR(32) NULL,
    ADD COLUMN location_name VARCHAR(64) NULL,
    ADD COLUMN record_created_by VARCHAR(64) NULL,
    ADD COLUMN record_created_location VARCHAR(32) NULL,
    ADD COLUMN record_updated_by VARCHAR(64) NULL,
    ADD COLUMN record_updated_location VARCHAR(32) NULL,
    ADD COLUMN record_updated_at DATETIME(6) NULL,
    ADD COLUMN record_timestamp DATETIME(6) NULL,
    ROW_FORMAT=DYNAMIC;

CREATE TABLE fincloud_cif_personal_profiles (
    cif_no VARCHAR(50) NOT NULL,
    birth_place VARCHAR(64) NULL,
    gender VARCHAR(16) NULL,
    religion VARCHAR(32) NULL,
    marital_status VARCHAR(32) NULL,
    formal_education VARCHAR(32) NULL,
    mother_name VARCHAR(255) NULL,
    nationality VARCHAR(16) NULL,
    country_of_origin VARCHAR(16) NULL,
    email VARCHAR(255) NULL,
    title VARCHAR(32) NULL,
    member_type VARCHAR(16) NULL,
    dependent_count INT NULL,
    residence_years INT NULL,
    residence_months INT NULL,
    residence_status VARCHAR(32) NULL,
    ethnicity VARCHAR(32) NULL,
    marriage_date DATE NULL,
    npwp_no VARCHAR(32) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (cif_no),
    CONSTRAINT fk_fincloud_cif_personal_profiles_parent
        FOREIGN KEY (cif_no) REFERENCES fincloud_cifs (cif_no) ON DELETE CASCADE
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_cif_ktp (
    cif_no VARCHAR(50) NOT NULL,
    ktp_name VARCHAR(255) NULL,
    ktp_birth_date DATE NULL,
    ktp_religion VARCHAR(32) NULL,
    ktp_snapshot_address VARCHAR(255) NULL,
    ktp_valid_for_life VARCHAR(16) NULL,
    ktp_blood_type VARCHAR(16) NULL,
    ktp_gender VARCHAR(16) NULL,
    ktp_nationality VARCHAR(16) NULL,
    ktp_occupation VARCHAR(32) NULL,
    ktp_marital_status VARCHAR(32) NULL,
    ktp_birth_place VARCHAR(64) NULL,
    ktp_issue_place VARCHAR(64) NULL,
    ktp_issue_date DATE NULL,
    ktp_valid_until DATE NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (cif_no),
    CONSTRAINT fk_fincloud_cif_ktp_parent
        FOREIGN KEY (cif_no) REFERENCES fincloud_cifs (cif_no) ON DELETE CASCADE
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_cif_addresses (
    cif_no VARCHAR(50) NOT NULL,
    ktp_address_line_1 VARCHAR(255) NULL,
    ktp_address_line_2 VARCHAR(255) NULL,
    ktp_subdistrict VARCHAR(64) NULL,
    ktp_district VARCHAR(64) NULL,
    ktp_city VARCHAR(64) NULL,
    ktp_province VARCHAR(64) NULL,
    ktp_postal_code VARCHAR(16) NULL,
    ktp_rt VARCHAR(16) NULL,
    ktp_rw VARCHAR(16) NULL,
    home_address_line_1 VARCHAR(255) NULL,
    home_address_line_2 VARCHAR(255) NULL,
    home_subdistrict VARCHAR(64) NULL,
    home_district VARCHAR(64) NULL,
    home_city VARCHAR(64) NULL,
    home_province VARCHAR(64) NULL,
    home_postal_code VARCHAR(16) NULL,
    home_rt VARCHAR(16) NULL,
    home_rw VARCHAR(16) NULL,
    home_area_code VARCHAR(16) NULL,
    home_phone VARCHAR(32) NULL,
    home_mobile VARCHAR(32) NULL,
    home_fax VARCHAR(32) NULL,
    office_address_line_1 VARCHAR(255) NULL,
    office_address_line_2 VARCHAR(255) NULL,
    office_subdistrict VARCHAR(64) NULL,
    office_district VARCHAR(64) NULL,
    office_city VARCHAR(64) NULL,
    office_province VARCHAR(64) NULL,
    office_postal_code VARCHAR(16) NULL,
    office_rt VARCHAR(16) NULL,
    office_rw VARCHAR(16) NULL,
    office_area_code VARCHAR(16) NULL,
    office_phone VARCHAR(32) NULL,
    office_mobile VARCHAR(32) NULL,
    office_fax VARCHAR(32) NULL,
    home_same_as_ktp VARCHAR(16) NULL,
    office_same_as_deed VARCHAR(16) NULL,
    mailing_address_type VARCHAR(16) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (cif_no),
    CONSTRAINT fk_fincloud_cif_addresses_parent
        FOREIGN KEY (cif_no) REFERENCES fincloud_cifs (cif_no) ON DELETE CASCADE
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_cif_employment (
    cif_no VARCHAR(50) NOT NULL,
    work_type VARCHAR(32) NULL,
    job_title VARCHAR(64) NULL,
    business_field VARCHAR(32) NULL,
    company_name VARCHAR(255) NULL,
    previous_company_name VARCHAR(255) NULL,
    economic_sector VARCHAR(16) NULL,
    work_years INT NULL,
    work_months INT NULL,
    previous_work_years INT NULL,
    previous_work_months INT NULL,
    monthly_net_income DECIMAL(24,6) NULL,
    monthly_side_income DECIMAL(24,6) NULL,
    monthly_expense DECIMAL(24,6) NULL,
    side_business VARCHAR(128) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (cif_no),
    CONSTRAINT fk_fincloud_cif_employment_parent
        FOREIGN KEY (cif_no) REFERENCES fincloud_cifs (cif_no) ON DELETE CASCADE
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_cif_company (
    cif_no VARCHAR(50) NOT NULL,
    company_npwp_no VARCHAR(32) NULL,
    company_business_entity_type VARCHAR(16) NULL,
    company_initial_deed_no VARCHAR(64) NULL,
    company_latest_deed_no VARCHAR(64) NULL,
    company_initial_deed_place VARCHAR(64) NULL,
    company_latest_deed_place VARCHAR(64) NULL,
    company_initial_deed_date DATE NULL,
    company_latest_deed_date DATE NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (cif_no),
    CONSTRAINT fk_fincloud_cif_company_parent
        FOREIGN KEY (cif_no) REFERENCES fincloud_cifs (cif_no) ON DELETE CASCADE
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_cif_kyc (
    cif_no VARCHAR(50) NOT NULL,
    kyc_source_of_funds VARCHAR(64) NULL,
    kyc_income_source VARCHAR(64) NULL,
    kyc_fund_use_purpose VARCHAR(64) NULL,
    kyc_cash_deposit_limit DECIMAL(24,6) NULL,
    kyc_noncash_deposit_limit DECIMAL(24,6) NULL,
    kyc_cash_withdrawal_limit DECIMAL(24,6) NULL,
    kyc_noncash_withdrawal_limit DECIMAL(24,6) NULL,
    kyc_transaction_frequency_limit INT NULL,
    kyc_company_income DECIMAL(24,6) NULL,
    kyc_company_business_form VARCHAR(32) NULL,
    kyc_company_business_field VARCHAR(64) NULL,
    kyc_company_fund_use VARCHAR(32) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (cif_no),
    CONSTRAINT fk_fincloud_cif_kyc_parent
        FOREIGN KEY (cif_no) REFERENCES fincloud_cifs (cif_no) ON DELETE CASCADE
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_cif_regulatory (
    cif_no VARCHAR(50) NOT NULL,
    sid_alias_name VARCHAR(255) NULL,
    sid_debtor_group VARCHAR(32) NULL,
    sid_debtor_city VARCHAR(32) NULL,
    sid_status VARCHAR(32) NULL,
    sid_din VARCHAR(32) NULL,
    sid_related_party VARCHAR(16) NULL,
    sid_related_party_notes VARCHAR(64) NULL,
    sid_exceeds_bmpk VARCHAR(16) NULL,
    sid_violates_bmpk VARCHAR(16) NULL,
    labul_debtor_group VARCHAR(16) NULL,
    risk_identity VARCHAR(128) NULL,
    risk_business_location VARCHAR(128) NULL,
    risk_transaction_count VARCHAR(128) NULL,
    risk_business_activity VARCHAR(128) NULL,
    risk_ownership_structure VARCHAR(128) NULL,
    risk_product_service_network VARCHAR(128) NULL,
    risk_other_information VARCHAR(128) NULL,
    risk_final_summary VARCHAR(128) NULL,
    risk_profile VARCHAR(128) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (cif_no),
    CONSTRAINT fk_fincloud_cif_regulatory_parent
        FOREIGN KEY (cif_no) REFERENCES fincloud_cifs (cif_no) ON DELETE CASCADE
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE stg_fincloud_cif_personal_profiles LIKE fincloud_cif_personal_profiles;
ALTER TABLE stg_fincloud_cif_personal_profiles
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, cif_no),
    ADD CONSTRAINT fk_stg_fincloud_cif_personal_profiles_parent
        FOREIGN KEY (ingestion_run_id, cif_no)
        REFERENCES stg_fincloud_cif_details (ingestion_run_id, cif_no) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_cif_ktp LIKE fincloud_cif_ktp;
ALTER TABLE stg_fincloud_cif_ktp
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, cif_no),
    ADD CONSTRAINT fk_stg_fincloud_cif_ktp_parent
        FOREIGN KEY (ingestion_run_id, cif_no)
        REFERENCES stg_fincloud_cif_details (ingestion_run_id, cif_no) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_cif_addresses LIKE fincloud_cif_addresses;
ALTER TABLE stg_fincloud_cif_addresses
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, cif_no),
    ADD CONSTRAINT fk_stg_fincloud_cif_addresses_parent
        FOREIGN KEY (ingestion_run_id, cif_no)
        REFERENCES stg_fincloud_cif_details (ingestion_run_id, cif_no) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_cif_employment LIKE fincloud_cif_employment;
ALTER TABLE stg_fincloud_cif_employment
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, cif_no),
    ADD CONSTRAINT fk_stg_fincloud_cif_employment_parent
        FOREIGN KEY (ingestion_run_id, cif_no)
        REFERENCES stg_fincloud_cif_details (ingestion_run_id, cif_no) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_cif_company LIKE fincloud_cif_company;
ALTER TABLE stg_fincloud_cif_company
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, cif_no),
    ADD CONSTRAINT fk_stg_fincloud_cif_company_parent
        FOREIGN KEY (ingestion_run_id, cif_no)
        REFERENCES stg_fincloud_cif_details (ingestion_run_id, cif_no) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_cif_kyc LIKE fincloud_cif_kyc;
ALTER TABLE stg_fincloud_cif_kyc
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, cif_no),
    ADD CONSTRAINT fk_stg_fincloud_cif_kyc_parent
        FOREIGN KEY (ingestion_run_id, cif_no)
        REFERENCES stg_fincloud_cif_details (ingestion_run_id, cif_no) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_cif_regulatory LIKE fincloud_cif_regulatory;
ALTER TABLE stg_fincloud_cif_regulatory
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, cif_no),
    ADD CONSTRAINT fk_stg_fincloud_cif_regulatory_parent
        FOREIGN KEY (ingestion_run_id, cif_no)
        REFERENCES stg_fincloud_cif_details (ingestion_run_id, cif_no) ON DELETE CASCADE;

ALTER TABLE fincloud_saving_details
    ADD COLUMN alt_no VARCHAR(32) NULL,
    ADD COLUMN product_id VARCHAR(32) NULL,
    ADD COLUMN product_savings_type VARCHAR(32) NULL,
    ADD COLUMN savings_type VARCHAR(32) NULL,
    ADD COLUMN currency VARCHAR(8) NULL,
    ADD COLUMN document_status VARCHAR(32) NULL,
    ADD COLUMN created_date DATE NULL,
    ADD COLUMN product_credit_interest_rate DECIMAL(20,2) NULL,
    ADD COLUMN credit_interest_type VARCHAR(32) NULL,
    ADD COLUMN overdraft VARCHAR(16) NULL,
    ADD COLUMN joint_account VARCHAR(16) NULL,
    ADD COLUMN opening_purpose VARCHAR(128) NULL,
    ADD COLUMN source_of_fund VARCHAR(64) NULL,
    ADD COLUMN product_bnpl VARCHAR(16) NULL,
    ADD COLUMN auto_debit VARCHAR(16) NULL,
    ADD COLUMN print_bilyet VARCHAR(16) NULL,
    ADD COLUMN print_savings_book VARCHAR(16) NULL,
    ADD COLUMN block_reason VARCHAR(128) NULL,
    ADD COLUMN block_notes VARCHAR(128) NULL,
    ADD COLUMN unblock_reason VARCHAR(128) NULL,
    ADD COLUMN block_status VARCHAR(16) NULL,
    ADD COLUMN block_date DATE NULL,
    ADD COLUMN block_end_date DATE NULL,
    ADD COLUMN unblock_date DATE NULL,
    ADD COLUMN unblock_amount DECIMAL(24,6) NULL,
    ADD COLUMN accrued_balance DECIMAL(24,6) NULL,
    ADD COLUMN accrued_debit_balance DECIMAL(24,6) NULL,
    ADD COLUMN accrued_credit_interest_balance DECIMAL(24,6) NULL,
    ADD COLUMN active_standing_order_count INT NULL,
    ADD COLUMN fixed_debit_interest_payment_day INT NULL,
    ADD COLUMN fixed_credit_interest_payment_day INT NULL,
    ADD COLUMN marketing_code VARCHAR(32) NULL,
    ADD COLUMN marketing_notes VARCHAR(128) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE stg_fincloud_saving_details
    ADD COLUMN alt_no VARCHAR(32) NULL,
    ADD COLUMN product_id VARCHAR(32) NULL,
    ADD COLUMN product_savings_type VARCHAR(32) NULL,
    ADD COLUMN savings_type VARCHAR(32) NULL,
    ADD COLUMN currency VARCHAR(8) NULL,
    ADD COLUMN document_status VARCHAR(32) NULL,
    ADD COLUMN created_date DATE NULL,
    ADD COLUMN product_credit_interest_rate DECIMAL(20,2) NULL,
    ADD COLUMN credit_interest_type VARCHAR(32) NULL,
    ADD COLUMN overdraft VARCHAR(16) NULL,
    ADD COLUMN joint_account VARCHAR(16) NULL,
    ADD COLUMN opening_purpose VARCHAR(128) NULL,
    ADD COLUMN source_of_fund VARCHAR(64) NULL,
    ADD COLUMN product_bnpl VARCHAR(16) NULL,
    ADD COLUMN auto_debit VARCHAR(16) NULL,
    ADD COLUMN print_bilyet VARCHAR(16) NULL,
    ADD COLUMN print_savings_book VARCHAR(16) NULL,
    ADD COLUMN block_reason VARCHAR(128) NULL,
    ADD COLUMN block_notes VARCHAR(128) NULL,
    ADD COLUMN unblock_reason VARCHAR(128) NULL,
    ADD COLUMN block_status VARCHAR(16) NULL,
    ADD COLUMN block_date DATE NULL,
    ADD COLUMN block_end_date DATE NULL,
    ADD COLUMN unblock_date DATE NULL,
    ADD COLUMN unblock_amount DECIMAL(24,6) NULL,
    ADD COLUMN accrued_balance DECIMAL(24,6) NULL,
    ADD COLUMN accrued_debit_balance DECIMAL(24,6) NULL,
    ADD COLUMN accrued_credit_interest_balance DECIMAL(24,6) NULL,
    ADD COLUMN active_standing_order_count INT NULL,
    ADD COLUMN fixed_debit_interest_payment_day INT NULL,
    ADD COLUMN fixed_credit_interest_payment_day INT NULL,
    ADD COLUMN marketing_code VARCHAR(32) NULL,
    ADD COLUMN marketing_notes VARCHAR(128) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE fincloud_time_deposit_details
    ADD COLUMN account_name VARCHAR(255) NULL,
    ADD COLUMN certificate_no VARCHAR(32) NULL,
    ADD COLUMN product_id VARCHAR(16) NULL,
    ADD COLUMN product_deposit_type VARCHAR(32) NULL,
    ADD COLUMN currency VARCHAR(8) NULL,
    ADD COLUMN term VARCHAR(32) NULL,
    ADD COLUMN period VARCHAR(64) NULL,
    ADD COLUMN automatic_rollover VARCHAR(16) NULL,
    ADD COLUMN compound_interest VARCHAR(32) NULL,
    ADD COLUMN interest_rate_change VARCHAR(32) NULL,
    ADD COLUMN interest_payment_method VARCHAR(32) NULL,
    ADD COLUMN print_certificate VARCHAR(16) NULL,
    ADD COLUMN document_status VARCHAR(32) NULL,
    ADD COLUMN joint_account VARCHAR(16) NULL,
    ADD COLUMN joint_account_type VARCHAR(32) NULL,
    ADD COLUMN source_of_fund VARCHAR(128) NULL,
    ADD COLUMN opening_purpose VARCHAR(128) NULL,
    ADD COLUMN last_interest_payment_date DATE NULL,
    ADD COLUMN next_interest_payment_date DATE NULL,
    ADD COLUMN source_account_no VARCHAR(128) NULL,
    ADD COLUMN interest_destination_account VARCHAR(128) NULL,
    ADD COLUMN disbursement_account_no VARCHAR(128) NULL,
    ADD COLUMN created_date DATE NULL,
    ADD COLUMN description VARCHAR(128) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE stg_fincloud_time_deposit_details
    ADD COLUMN account_name VARCHAR(255) NULL,
    ADD COLUMN certificate_no VARCHAR(32) NULL,
    ADD COLUMN product_id VARCHAR(16) NULL,
    ADD COLUMN product_deposit_type VARCHAR(32) NULL,
    ADD COLUMN currency VARCHAR(8) NULL,
    ADD COLUMN term VARCHAR(32) NULL,
    ADD COLUMN period VARCHAR(64) NULL,
    ADD COLUMN automatic_rollover VARCHAR(16) NULL,
    ADD COLUMN compound_interest VARCHAR(32) NULL,
    ADD COLUMN interest_rate_change VARCHAR(32) NULL,
    ADD COLUMN interest_payment_method VARCHAR(32) NULL,
    ADD COLUMN print_certificate VARCHAR(16) NULL,
    ADD COLUMN document_status VARCHAR(32) NULL,
    ADD COLUMN joint_account VARCHAR(16) NULL,
    ADD COLUMN joint_account_type VARCHAR(32) NULL,
    ADD COLUMN source_of_fund VARCHAR(128) NULL,
    ADD COLUMN opening_purpose VARCHAR(128) NULL,
    ADD COLUMN last_interest_payment_date DATE NULL,
    ADD COLUMN next_interest_payment_date DATE NULL,
    ADD COLUMN source_account_no VARCHAR(128) NULL,
    ADD COLUMN interest_destination_account VARCHAR(128) NULL,
    ADD COLUMN disbursement_account_no VARCHAR(128) NULL,
    ADD COLUMN created_date DATE NULL,
    ADD COLUMN description VARCHAR(128) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE fincloud_time_deposit_mutations
    ADD COLUMN period VARCHAR(64) NULL,
    ADD COLUMN term VARCHAR(32) NULL,
    ADD COLUMN officer VARCHAR(64) NULL,
    ADD COLUMN description VARCHAR(128) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE stg_fincloud_time_deposit_mutations
    ADD COLUMN period VARCHAR(64) NULL,
    ADD COLUMN term VARCHAR(32) NULL,
    ADD COLUMN officer VARCHAR(64) NULL,
    ADD COLUMN description VARCHAR(128) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE fincloud_loan_details
    ADD COLUMN application_number VARCHAR(128) NULL,
    ADD COLUMN insurance_premium DECIMAL(24,6) NULL,
    ADD COLUMN insurance_company VARCHAR(255) NULL,
    ADD COLUMN collateral_policy_number VARCHAR(128) NULL,
    ADD COLUMN collateral_type VARCHAR(128) NULL,
    ADD COLUMN collateral_value DECIMAL(24,6) NULL,
    ADD COLUMN alt_no VARCHAR(32) NULL,
    ADD COLUMN pk_no VARCHAR(64) NULL,
    ADD COLUMN loan_agreement_no VARCHAR(64) NULL,
    ADD COLUMN product_id VARCHAR(64) NULL,
    ADD COLUMN product_code VARCHAR(16) NULL,
    ADD COLUMN product_loan_type VARCHAR(32) NULL,
    ADD COLUMN product_installment_type VARCHAR(32) NULL,
    ADD COLUMN account_status VARCHAR(32) NULL,
    ADD COLUMN document_status VARCHAR(32) NULL,
    ADD COLUMN currency VARCHAR(8) NULL,
    ADD COLUMN term VARCHAR(32) NULL,
    ADD COLUMN period VARCHAR(64) NULL,
    ADD COLUMN installment_day INT NULL,
    ADD COLUMN principal_amount DECIMAL(24,6) NULL,
    ADD COLUMN credit_limit DECIMAL(24,6) NULL,
    ADD COLUMN accrued_interest DECIMAL(24,6) NULL,
    ADD COLUMN flat_interest_rate DECIMAL(20,2) NULL,
    ADD COLUMN collectability_bpr INT NULL,
    ADD COLUMN collectability_update VARCHAR(32) NULL,
    ADD COLUMN arrears_start_date DATE NULL,
    ADD COLUMN last_due_date DATE NULL,
    ADD COLUMN next_due_date DATE NULL,
    ADD COLUMN last_principal_interest_payment_date DATE NULL,
    ADD COLUMN next_principal_interest_payment_date DATE NULL,
    ADD COLUMN close_date DATE NULL,
    ADD COLUMN restructure_final_agreement_no VARCHAR(64) NULL,
    ADD COLUMN restructure_final_agreement_date DATE NULL,
    ADD COLUMN restructure_method VARCHAR(32) NULL,
    ADD COLUMN restructure_frequency VARCHAR(32) NULL,
    ADD COLUMN sid_credit_nature VARCHAR(32) NULL,
    ADD COLUMN sid_credit_nature_2 VARCHAR(32) NULL,
    ADD COLUMN sid_usage_type VARCHAR(32) NULL,
    ADD COLUMN sid_repayment_source VARCHAR(32) NULL,
    ADD COLUMN sid_credit_group VARCHAR(32) NULL,
    ADD COLUMN sid_usage_orientation VARCHAR(32) NULL,
    ADD COLUMN sid_economic_sector VARCHAR(32) NULL,
    ADD COLUMN sid_economic_sector_2 VARCHAR(32) NULL,
    ADD COLUMN sid_business_type VARCHAR(32) NULL,
    ADD COLUMN disbursement_saving_account VARCHAR(128) NULL,
    ADD COLUMN disbursement_saving_account_2 VARCHAR(32) NULL,
    ADD COLUMN installment_payment_account VARCHAR(128) NULL,
    ADD COLUMN installment_payment_account_2 VARCHAR(32) NULL,
    ADD COLUMN loan_purpose VARCHAR(64) NULL,
    ADD COLUMN last_month_ppap DECIMAL(24,6) NULL,
    ADD COLUMN last_ppap_date DATE NULL,
    ADD COLUMN write_off_principal_balance DECIMAL(24,6) NULL,
    ADD COLUMN write_off_accrued_interest DECIMAL(24,6) NULL,
    ADD COLUMN write_off_interest_arrears DECIMAL(24,6) NULL,
    ADD COLUMN write_off_penalty_arrears DECIMAL(24,6) NULL,
    ADD COLUMN total_write_off DECIMAL(24,6) NULL,
    ADD COLUMN charge_off_principal DECIMAL(24,6) NULL,
    ADD COLUMN charge_off_interest DECIMAL(24,6) NULL,
    ADD COLUMN marketing VARCHAR(64) NULL,
    ADD COLUMN record_created_by VARCHAR(64) NULL,
    ADD COLUMN record_created_location VARCHAR(64) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE stg_fincloud_loan_details
    ADD COLUMN application_number VARCHAR(128) NULL,
    ADD COLUMN insurance_premium DECIMAL(24,6) NULL,
    ADD COLUMN insurance_company VARCHAR(255) NULL,
    ADD COLUMN collateral_policy_number VARCHAR(128) NULL,
    ADD COLUMN collateral_type VARCHAR(128) NULL,
    ADD COLUMN collateral_value DECIMAL(24,6) NULL,
    ADD COLUMN alt_no VARCHAR(32) NULL,
    ADD COLUMN pk_no VARCHAR(64) NULL,
    ADD COLUMN loan_agreement_no VARCHAR(64) NULL,
    ADD COLUMN product_id VARCHAR(64) NULL,
    ADD COLUMN product_code VARCHAR(16) NULL,
    ADD COLUMN product_loan_type VARCHAR(32) NULL,
    ADD COLUMN product_installment_type VARCHAR(32) NULL,
    ADD COLUMN account_status VARCHAR(32) NULL,
    ADD COLUMN document_status VARCHAR(32) NULL,
    ADD COLUMN currency VARCHAR(8) NULL,
    ADD COLUMN term VARCHAR(32) NULL,
    ADD COLUMN period VARCHAR(64) NULL,
    ADD COLUMN installment_day INT NULL,
    ADD COLUMN principal_amount DECIMAL(24,6) NULL,
    ADD COLUMN credit_limit DECIMAL(24,6) NULL,
    ADD COLUMN accrued_interest DECIMAL(24,6) NULL,
    ADD COLUMN flat_interest_rate DECIMAL(20,2) NULL,
    ADD COLUMN collectability_bpr INT NULL,
    ADD COLUMN collectability_update VARCHAR(32) NULL,
    ADD COLUMN arrears_start_date DATE NULL,
    ADD COLUMN last_due_date DATE NULL,
    ADD COLUMN next_due_date DATE NULL,
    ADD COLUMN last_principal_interest_payment_date DATE NULL,
    ADD COLUMN next_principal_interest_payment_date DATE NULL,
    ADD COLUMN close_date DATE NULL,
    ADD COLUMN restructure_final_agreement_no VARCHAR(64) NULL,
    ADD COLUMN restructure_final_agreement_date DATE NULL,
    ADD COLUMN restructure_method VARCHAR(32) NULL,
    ADD COLUMN restructure_frequency VARCHAR(32) NULL,
    ADD COLUMN sid_credit_nature VARCHAR(32) NULL,
    ADD COLUMN sid_credit_nature_2 VARCHAR(32) NULL,
    ADD COLUMN sid_usage_type VARCHAR(32) NULL,
    ADD COLUMN sid_repayment_source VARCHAR(32) NULL,
    ADD COLUMN sid_credit_group VARCHAR(32) NULL,
    ADD COLUMN sid_usage_orientation VARCHAR(32) NULL,
    ADD COLUMN sid_economic_sector VARCHAR(32) NULL,
    ADD COLUMN sid_economic_sector_2 VARCHAR(32) NULL,
    ADD COLUMN sid_business_type VARCHAR(32) NULL,
    ADD COLUMN disbursement_saving_account VARCHAR(128) NULL,
    ADD COLUMN disbursement_saving_account_2 VARCHAR(32) NULL,
    ADD COLUMN installment_payment_account VARCHAR(128) NULL,
    ADD COLUMN installment_payment_account_2 VARCHAR(32) NULL,
    ADD COLUMN loan_purpose VARCHAR(64) NULL,
    ADD COLUMN last_month_ppap DECIMAL(24,6) NULL,
    ADD COLUMN last_ppap_date DATE NULL,
    ADD COLUMN write_off_principal_balance DECIMAL(24,6) NULL,
    ADD COLUMN write_off_accrued_interest DECIMAL(24,6) NULL,
    ADD COLUMN write_off_interest_arrears DECIMAL(24,6) NULL,
    ADD COLUMN write_off_penalty_arrears DECIMAL(24,6) NULL,
    ADD COLUMN total_write_off DECIMAL(24,6) NULL,
    ADD COLUMN charge_off_principal DECIMAL(24,6) NULL,
    ADD COLUMN charge_off_interest DECIMAL(24,6) NULL,
    ADD COLUMN marketing VARCHAR(64) NULL,
    ADD COLUMN record_created_by VARCHAR(64) NULL,
    ADD COLUMN record_created_location VARCHAR(64) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE fincloud_loan_repayment_schedule
    ADD COLUMN flat_interest DECIMAL(24,6) NULL,
    ADD COLUMN flat_principal DECIMAL(24,6) NULL,
    ADD COLUMN flat_loan DECIMAL(24,6) NULL,
    ADD COLUMN payment_status VARCHAR(32) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE stg_fincloud_loan_repayment_schedule
    ADD COLUMN flat_interest DECIMAL(24,6) NULL,
    ADD COLUMN flat_principal DECIMAL(24,6) NULL,
    ADD COLUMN flat_loan DECIMAL(24,6) NULL,
    ADD COLUMN payment_status VARCHAR(32) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE fincloud_loan_payment_history
    ADD COLUMN paid_closing_penalty DECIMAL(24,6) NULL,
    ADD COLUMN dwp_nominal DECIMAL(24,6) NULL,
    ADD COLUMN description VARCHAR(128) NULL,
    ADD COLUMN officer VARCHAR(64) NULL,
    ADD COLUMN authorizer VARCHAR(64) NULL,
    ROW_FORMAT=DYNAMIC;

ALTER TABLE stg_fincloud_loan_payment_history
    ADD COLUMN paid_closing_penalty DECIMAL(24,6) NULL,
    ADD COLUMN dwp_nominal DECIMAL(24,6) NULL,
    ADD COLUMN description VARCHAR(128) NULL,
    ADD COLUMN officer VARCHAR(64) NULL,
    ADD COLUMN authorizer VARCHAR(64) NULL,
    ROW_FORMAT=DYNAMIC;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: Detail typed expansion may contain business data';
