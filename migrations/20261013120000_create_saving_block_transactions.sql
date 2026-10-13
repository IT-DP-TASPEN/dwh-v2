-- +goose Up
-- Current saving-account block items (datablockopen). Parentless by design:
-- consumers join fincloud_saving_details logically on account_no, with no FK,
-- because saving_detail and saving_block_detail publish independently.
CREATE TABLE fincloud_saving_block_transactions (
    account_no VARCHAR(50) NOT NULL,
    item_index INT UNSIGNED NOT NULL,
    transaction_id VARCHAR(128) NULL,
    transaction_date DATE NULL,
    customer_name VARCHAR(255) NULL,
    block_date DATE NULL,
    block_end_date DATE NULL,
    currency VARCHAR(8) NULL,
    description TEXT NULL,
    block_reason TEXT NULL,
    transaction_type VARCHAR(128) NULL,
    blocked_amount DECIMAL(24,6) NULL,
    officer VARCHAR(255) NULL,
    not_batch BOOLEAN NULL,
    is_open BOOLEAN NULL,
    raw_item_payload JSON NOT NULL,
    raw_item_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (account_no, item_index),
    KEY idx_fincloud_saving_block_transactions_transaction (transaction_id)
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE stg_fincloud_saving_block_transactions (
    ingestion_run_id BIGINT UNSIGNED NOT NULL,
    account_no VARCHAR(50) NOT NULL,
    item_index INT UNSIGNED NOT NULL,
    transaction_id VARCHAR(128) NULL,
    transaction_date DATE NULL,
    customer_name VARCHAR(255) NULL,
    block_date DATE NULL,
    block_end_date DATE NULL,
    currency VARCHAR(8) NULL,
    description TEXT NULL,
    block_reason TEXT NULL,
    transaction_type VARCHAR(128) NULL,
    blocked_amount DECIMAL(24,6) NULL,
    officer VARCHAR(255) NULL,
    not_batch BOOLEAN NULL,
    is_open BOOLEAN NULL,
    raw_item_payload JSON NOT NULL,
    raw_item_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    PRIMARY KEY (ingestion_run_id, account_no, item_index),
    CONSTRAINT fk_stg_fincloud_saving_block_transactions_run
        FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO source_settings (source_id, enabled, updated_by_user_id) VALUES
    ('saving_block_detail', TRUE, NULL)
ON DUPLICATE KEY UPDATE source_id=VALUES(source_id);

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: Saving block transactions may contain business data';
