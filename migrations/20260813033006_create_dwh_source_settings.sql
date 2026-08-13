-- +goose Up
CREATE TABLE IF NOT EXISTS source_settings (
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

DROP PROCEDURE IF EXISTS phase3_validate_source_settings;
-- +goose StatementBegin
CREATE PROCEDURE phase3_validate_source_settings()
BEGIN
    DECLARE compatible_columns INT DEFAULT 0;
    DECLARE primary_columns TEXT;
    SELECT COUNT(*) INTO compatible_columns
    FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'source_settings'
      AND (
        (COLUMN_NAME = 'source_id' AND COLUMN_TYPE = 'varchar(128)' AND IS_NULLABLE = 'NO'
          AND COLLATION_NAME IN ('utf8mb4_unicode_ci', 'utf8mb4_0900_bin')) OR
        (COLUMN_NAME = 'enabled' AND COLUMN_TYPE = 'tinyint(1)' AND IS_NULLABLE = 'NO') OR
        (COLUMN_NAME = 'updated_by_user_id' AND COLUMN_TYPE = 'bigint unsigned' AND IS_NULLABLE = 'YES') OR
        (COLUMN_NAME = 'created_at' AND COLUMN_TYPE = 'datetime(6)' AND IS_NULLABLE = 'NO') OR
        (COLUMN_NAME = 'updated_at' AND COLUMN_TYPE = 'datetime(6)' AND IS_NULLABLE = 'NO')
      );
    IF compatible_columns <> 5 OR
       (SELECT COUNT(*) FROM information_schema.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'source_settings') <> 5 THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'source_settings schema is incompatible with the canonical target';
    END IF;
    SELECT GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)
      INTO primary_columns
      FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'source_settings' AND INDEX_NAME = 'PRIMARY';
    IF primary_columns <> 'source_id' THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'source_settings primary key is incompatible with the canonical target';
    END IF;
END;
-- +goose StatementEnd
CALL phase3_validate_source_settings();
DROP PROCEDURE phase3_validate_source_settings;

ALTER TABLE source_settings
    MODIFY source_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL;

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
    ('loan_detail', TRUE, NULL)
ON DUPLICATE KEY UPDATE source_id = VALUES(source_id);

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: source_settings may contain operator state';
