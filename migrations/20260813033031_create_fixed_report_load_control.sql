-- +goose Up
CREATE TABLE fixed_report_loads (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    period_from DATE NOT NULL,
    period_to DATE NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    expected_member_count INT UNSIGNED NOT NULL,
    manifest_checksum BINARY(32) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    published_at DATETIME(6) NULL,
    PRIMARY KEY (id),
    KEY idx_fixed_report_loads_scope (job_key, period_from, period_to, id),
    KEY idx_fixed_report_loads_status (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fixed_report_load_members (
    load_id BIGINT UNSIGNED NOT NULL,
    member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    row_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
    member_checksum BINARY(32) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (load_id, member_key),
    CONSTRAINT fk_fixed_report_load_members_load
        FOREIGN KEY (load_id) REFERENCES fixed_report_loads (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE fixed_report_publications (
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    period_from DATE NOT NULL,
    period_to DATE NOT NULL,
    active_load_id BIGINT UNSIGNED NULL,
    published_at DATETIME(6) NULL,
    PRIMARY KEY (job_key, period_from, period_to),
    KEY idx_fixed_report_publications_active_load (active_load_id),
    CONSTRAINT fk_fixed_report_publications_active_load
        FOREIGN KEY (active_load_id) REFERENCES fixed_report_loads (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: fixed report loads may contain staged data';
