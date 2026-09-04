-- +goose Up
DROP PROCEDURE IF EXISTS assert_saving_account_statement_quiesced;
-- +goose StatementBegin
CREATE PROCEDURE assert_saving_account_statement_quiesced()
BEGIN
    IF EXISTS (
        SELECT 1 FROM ingestion_runs
        WHERE status = 'running'
          AND (job_key = 'saving_detail' OR kind = 'run_all_parent')
        LIMIT 1
    ) THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'Savings Account Statement migration requires quiesced saving_detail and Run All jobs';
    END IF;
END;
-- +goose StatementEnd

CALL assert_saving_account_statement_quiesced();
DROP PROCEDURE assert_saving_account_statement_quiesced;

CREATE TABLE fincloud_saving_account_statements (
    account_no VARCHAR(50) NOT NULL,
    item_index INT UNSIGNED NOT NULL,
    transaction_date DATE NULL,
    transaction_time TIME NULL,
    opening_balance DECIMAL(24,6) NULL,
    debit DECIMAL(24,6) NULL,
    credit DECIMAL(24,6) NULL,
    closing_balance DECIMAL(24,6) NULL,
    closing_balance_equivalent DECIMAL(24,6) NULL,
    transaction_type TEXT NULL,
    description TEXT NULL,
    reference TEXT NULL,
    location TEXT NULL,
    journal_no TEXT NULL,
    created_by TEXT NULL,
    trx_rate DECIMAL(24,6) NULL,
    mid_rate_dc DECIMAL(24,6) NULL,
    raw_item_payload JSON NOT NULL,
    raw_item_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (account_no, item_index),
    CONSTRAINT fk_fincloud_saving_account_statements_parent
        FOREIGN KEY (account_no) REFERENCES fincloud_saving_details (account_no) ON DELETE CASCADE
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE stg_fincloud_saving_account_statements (
    ingestion_run_id BIGINT UNSIGNED NOT NULL,
    account_no VARCHAR(50) NOT NULL,
    item_index INT UNSIGNED NOT NULL,
    transaction_date DATE NULL,
    transaction_time TIME NULL,
    opening_balance DECIMAL(24,6) NULL,
    debit DECIMAL(24,6) NULL,
    credit DECIMAL(24,6) NULL,
    closing_balance DECIMAL(24,6) NULL,
    closing_balance_equivalent DECIMAL(24,6) NULL,
    transaction_type TEXT NULL,
    description TEXT NULL,
    reference TEXT NULL,
    location TEXT NULL,
    journal_no TEXT NULL,
    created_by TEXT NULL,
    trx_rate DECIMAL(24,6) NULL,
    mid_rate_dc DECIMAL(24,6) NULL,
    raw_item_payload JSON NOT NULL,
    raw_item_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    PRIMARY KEY (ingestion_run_id, account_no, item_index),
    CONSTRAINT fk_stg_fincloud_saving_account_statements_parent
        FOREIGN KEY (ingestion_run_id, account_no)
        REFERENCES stg_fincloud_saving_details (ingestion_run_id, account_no) ON DELETE CASCADE
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: Savings Account Statement tables may contain business data';
