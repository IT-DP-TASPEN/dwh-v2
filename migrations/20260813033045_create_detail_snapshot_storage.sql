-- +goose Up
CREATE TABLE fincloud_cifs (
    as_of_date DATE NOT NULL,
    cif_no VARCHAR(50) NOT NULL,
    customer_name VARCHAR(255) NOT NULL,
    customer_type VARCHAR(128) NULL,
    identity_type VARCHAR(128) NULL,
    ktp_no VARCHAR(128) NULL,
    birth_date DATE NULL,
    cif_open_date DATE NULL,
    record_created_at DATETIME(6) NULL,
    raw_payload JSON NOT NULL,
    raw_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (as_of_date, cif_no),
    KEY idx_fincloud_cifs_cif_no (cif_no, as_of_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_saving_details (
    as_of_date DATE NOT NULL,
    account_no VARCHAR(50) NOT NULL,
    cif_no VARCHAR(50) NOT NULL,
    account_name VARCHAR(255) NULL,
    location_id VARCHAR(32) NULL,
    beginning_balance DECIMAL(24,6) NOT NULL,
    balance DECIMAL(24,6) NOT NULL,
    blocked_balance DECIMAL(24,6) NULL,
    debit_mutation DECIMAL(24,6) NULL,
    credit_mutation DECIMAL(24,6) NULL,
    open_date DATE NULL,
    closed_date DATE NULL,
    raw_payload JSON NOT NULL,
    raw_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (as_of_date, account_no),
    KEY idx_fincloud_saving_details_account (account_no, as_of_date),
    KEY idx_fincloud_saving_details_cif (cif_no, as_of_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_time_deposit_details (
    as_of_date DATE NOT NULL,
    account_no VARCHAR(50) NOT NULL,
    cif_no VARCHAR(50) NOT NULL,
    nominal DECIMAL(24,6) NOT NULL,
    accrued_interest DECIMAL(24,6) NULL,
    product_interest_rate DECIMAL(20,2) NULL,
    open_date DATE NULL,
    maturity_date DATE NULL,
    location_id VARCHAR(32) NULL,
    raw_payload JSON NOT NULL,
    raw_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (as_of_date, account_no),
    KEY idx_fincloud_time_deposit_account (account_no, as_of_date),
    KEY idx_fincloud_time_deposit_cif (cif_no, as_of_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_time_deposit_mutations (
    as_of_date DATE NOT NULL,
    account_no VARCHAR(50) NOT NULL,
    item_index INT UNSIGNED NOT NULL,
    transaction_date DATE NULL,
    transaction_type VARCHAR(128) NULL,
    currency VARCHAR(8) NULL,
    nominal DECIMAL(24,6) NULL,
    interest_rate DECIMAL(20,2) NULL,
    reference VARCHAR(255) NULL,
    branch VARCHAR(255) NULL,
    journal_no VARCHAR(128) NULL,
    raw_item_payload JSON NOT NULL,
    raw_item_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (as_of_date, account_no, item_index),
    CONSTRAINT fk_fincloud_time_deposit_mutations_parent
        FOREIGN KEY (as_of_date, account_no)
        REFERENCES fincloud_time_deposit_details (as_of_date, account_no) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_loan_details (
    as_of_date DATE NOT NULL,
    account_no VARCHAR(50) NOT NULL,
    cif_no VARCHAR(50) NOT NULL,
    location_id VARCHAR(32) NULL,
    disbursement_date DATE NULL,
    outstanding_principal DECIMAL(24,6) NULL,
    principal_arrears DECIMAL(24,6) NULL,
    interest_arrears DECIMAL(24,6) NULL,
    penalty_arrears DECIMAL(24,6) NULL,
    dpd INT NULL,
    collectability_bi INT NULL,
    product_interest_rate DECIMAL(20,2) NULL,
    write_off_date DATE NULL,
    raw_payload JSON NOT NULL,
    raw_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (as_of_date, account_no),
    KEY idx_fincloud_loan_account (account_no, as_of_date),
    KEY idx_fincloud_loan_cif (cif_no, as_of_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_loan_disbursement_fees (
    as_of_date DATE NOT NULL,
    account_no VARCHAR(50) NOT NULL,
    item_index INT UNSIGNED NOT NULL,
    fee_name VARCHAR(128) NULL,
    fee_amount DECIMAL(24,6) NULL,
    calculate_dwp VARCHAR(32) NULL,
    raw_item_payload JSON NOT NULL,
    raw_item_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (as_of_date, account_no, item_index),
    CONSTRAINT fk_fincloud_loan_fees_parent
        FOREIGN KEY (as_of_date, account_no)
        REFERENCES fincloud_loan_details (as_of_date, account_no) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_loan_repayment_schedule (
    as_of_date DATE NOT NULL,
    account_no VARCHAR(50) NOT NULL,
    item_index INT UNSIGNED NOT NULL,
    schedule_date DATE NULL,
    installment_amount DECIMAL(24,6) NULL,
    interest_amount DECIMAL(24,6) NULL,
    principal_amount DECIMAL(24,6) NULL,
    penalty_amount DECIMAL(24,6) NULL,
    paid_principal DECIMAL(24,6) NULL,
    paid_interest DECIMAL(24,6) NULL,
    paid_penalty DECIMAL(24,6) NULL,
    remaining_loan DECIMAL(24,6) NULL,
    installment_no INT NULL,
    raw_item_payload JSON NOT NULL,
    raw_item_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (as_of_date, account_no, item_index),
    CONSTRAINT fk_fincloud_loan_schedule_parent
        FOREIGN KEY (as_of_date, account_no)
        REFERENCES fincloud_loan_details (as_of_date, account_no) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_loan_payment_history (
    as_of_date DATE NOT NULL,
    account_no VARCHAR(50) NOT NULL,
    item_index INT UNSIGNED NOT NULL,
    transaction_date DATE NULL,
    installment_no INT NULL,
    payment_date DATE NULL,
    currency VARCHAR(8) NULL,
    due_date DATE NULL,
    total_paid DECIMAL(24,6) NULL,
    paid_principal DECIMAL(24,6) NULL,
    paid_interest DECIMAL(24,6) NULL,
    paid_penalty DECIMAL(24,6) NULL,
    journal_no VARCHAR(128) NULL,
    branch VARCHAR(255) NULL,
    raw_item_payload JSON NOT NULL,
    raw_item_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (as_of_date, account_no, item_index),
    CONSTRAINT fk_fincloud_loan_history_parent
        FOREIGN KEY (as_of_date, account_no)
        REFERENCES fincloud_loan_details (as_of_date, account_no) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: detail snapshot tables may contain business data';
