-- +goose Up
CREATE TABLE dynamic_csv_sources (
    source_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    source_kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    table_name VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    identity_mode VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    schema_mode VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    first_seen_at DATETIME(6) NOT NULL,
    last_seen_at DATETIME(6) NOT NULL,
    first_seen_filename VARCHAR(255) NOT NULL,
    last_seen_filename VARCHAR(255) NOT NULL,
    first_seen_as_of_date DATE NOT NULL,
    last_seen_as_of_date DATE NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (source_id),
    UNIQUE KEY uq_dynamic_csv_sources_table (table_name),
    KEY idx_dynamic_csv_sources_kind (source_kind),
    KEY idx_dynamic_csv_sources_last_seen (last_seen_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE dynamic_csv_source_columns (
    source_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    original_header TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    physical_column VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    ordinal_position INT UNSIGNED NOT NULL,
    first_seen_at DATETIME(6) NOT NULL,
    last_seen_at DATETIME(6) NOT NULL,
    first_seen_filename VARCHAR(255) NOT NULL,
    last_seen_filename VARCHAR(255) NOT NULL,
    first_seen_as_of_date DATE NOT NULL,
    last_seen_as_of_date DATE NOT NULL,
    seen_count BIGINT UNSIGNED NOT NULL DEFAULT 1,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (source_id, physical_column),
    CONSTRAINT fk_dynamic_csv_source_columns_source
        FOREIGN KEY (source_id) REFERENCES dynamic_csv_sources (source_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: dynamic CSV registry describes runtime tables';
