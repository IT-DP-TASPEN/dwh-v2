-- +goose Up
CREATE TABLE source_settings (
    source_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    updated_by_user_id BIGINT UNSIGNED NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (source_id),
    KEY idx_source_settings_updated_by_user_id (updated_by_user_id),
    CONSTRAINT fk_source_settings_updated_by_user
        FOREIGN KEY (updated_by_user_id) REFERENCES users (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO source_settings (source_id, enabled, updated_by_user_id) VALUES
    ('cif_opening_report', TRUE, NULL),
    ('journal_transaction_report', TRUE, NULL),
    ('balance_sheet_report', TRUE, NULL),
    ('profit_loss_statement', TRUE, NULL),
    ('coa_movement_report', TRUE, NULL),
    ('fund_distribution_report', TRUE, NULL),
    ('vault_mutation_report', TRUE, NULL),
    ('teller_mutation_report', TRUE, NULL),
    ('eod_cif_opening_report_full', TRUE, NULL),
    ('eod_detail_outstanding_rekening_pinjaman', TRUE, NULL),
    ('eod_laporan_pelunasan_pinjaman_sebelum_jt', TRUE, NULL),
    ('eod_laporan_pembayaran_angsuran', TRUE, NULL),
    ('eod_laporan_pencairan_pinjaman', TRUE, NULL),
    ('eod_laporan_pinjaman_akan_jatuh_tempo', TRUE, NULL),
    ('eod_loan_write_off_report', TRUE, NULL),
    ('eod_savings_account_api_transaction', TRUE, NULL),
    ('eod_savings_account_closing_report', TRUE, NULL),
    ('eod_savings_account_opening_report', TRUE, NULL),
    ('eod_savings_account_balance_report', TRUE, NULL),
    ('eod_loan_will_due_report', TRUE, NULL),
    ('eod_savings_balance_details_report', TRUE, NULL),
    ('eod_time_deposit_account_balance_details', TRUE, NULL),
    ('eod_time_deposit_closing_report', TRUE, NULL),
    ('eod_time_deposit_placement_report', TRUE, NULL),
    ('eod_savings_balance_details_report_rak', TRUE, NULL),
    ('cbr_balance_sheet', TRUE, NULL),
    ('cbr_arrears', TRUE, NULL),
    ('cbr_collateral', TRUE, NULL),
    ('cbr_customer', TRUE, NULL),
    ('cbr_loan', TRUE, NULL),
    ('cbr_savings', TRUE, NULL),
    ('cbr_time_deposit', TRUE, NULL),
    ('cif_detail', TRUE, NULL),
    ('saving_detail', TRUE, NULL),
    ('time_deposit_detail', TRUE, NULL),
    ('loan_detail', TRUE, NULL);

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: source_settings may contain operator state';
