-- +goose Up
CREATE TABLE fincloud_reference_categories (
    domain VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    category_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    source_shape VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_item_count BIGINT UNSIGNED NOT NULL,
    item_count BIGINT UNSIGNED NOT NULL,
    discarded_blank_count BIGINT UNSIGNED NOT NULL,
    category_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (domain, category_key),
    CONSTRAINT chk_fincloud_reference_category_shape CHECK (source_shape IN ('id_description_objects','string_array','empty_array')),
    CONSTRAINT chk_fincloud_reference_category_counts CHECK (source_item_count = item_count + discarded_blank_count)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_reference_items (
    domain VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    category_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    source_ordinal BIGINT UNSIGNED NOT NULL,
    code VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    description TEXT NOT NULL,
    raw_item_payload JSON NOT NULL,
    item_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (domain, category_key, source_ordinal),
    KEY idx_fincloud_reference_items_code (domain, category_key, code),
    CONSTRAINT fk_fincloud_reference_items_category FOREIGN KEY (domain, category_key)
        REFERENCES fincloud_reference_categories (domain, category_key) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE stg_fincloud_reference_categories (
    ingestion_run_id BIGINT UNSIGNED NOT NULL,
    domain VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    category_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    source_shape VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    source_item_count BIGINT UNSIGNED NOT NULL,
    item_count BIGINT UNSIGNED NOT NULL,
    discarded_blank_count BIGINT UNSIGNED NOT NULL,
    category_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    PRIMARY KEY (ingestion_run_id, domain, category_key),
    CONSTRAINT chk_stg_fincloud_reference_category_counts CHECK (source_item_count = item_count + discarded_blank_count)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE stg_fincloud_reference_items (
    ingestion_run_id BIGINT UNSIGNED NOT NULL,
    domain VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    category_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    source_ordinal BIGINT UNSIGNED NOT NULL,
    code VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    description TEXT NOT NULL,
    raw_item_payload JSON NOT NULL,
    item_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    PRIMARY KEY (ingestion_run_id, domain, category_key, source_ordinal)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fincloud_marketing_master (
    marketing_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    marketing_name VARCHAR(255) NOT NULL,
    location_name VARCHAR(255) NOT NULL,
    active_status VARCHAR(64) NOT NULL,
    document_status VARCHAR(64) NOT NULL,
    source_transaction_at VARCHAR(64) NOT NULL,
    raw_payload JSON NOT NULL,
    raw_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (marketing_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE stg_fincloud_marketing_master (
    ingestion_run_id BIGINT UNSIGNED NOT NULL,
    marketing_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    marketing_name VARCHAR(255) NOT NULL,
    location_name VARCHAR(255) NOT NULL,
    active_status VARCHAR(64) NOT NULL,
    document_status VARCHAR(64) NOT NULL,
    source_transaction_at VARCHAR(64) NOT NULL,
    raw_payload JSON NOT NULL,
    raw_checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    last_fetched_at DATETIME(6) NOT NULL,
    PRIMARY KEY (ingestion_run_id, marketing_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO source_settings (source_id, enabled, updated_by_user_id) VALUES
    ('cif_reference_master', TRUE, NULL),
    ('saving_reference_master', TRUE, NULL),
    ('time_deposit_reference_master', TRUE, NULL),
    ('loan_reference_master', TRUE, NULL),
    ('marketing_master', TRUE, NULL)
ON DUPLICATE KEY UPDATE source_id=VALUES(source_id);

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='irreversible: Master/reference current state may contain operational data';
