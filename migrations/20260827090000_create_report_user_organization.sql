-- +goose Up
CREATE TABLE report_user_folders (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NOT NULL,
    name VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_report_user_folders_user_name (user_id, name),
    UNIQUE KEY uq_report_user_folders_user_id (user_id, id),
    CONSTRAINT fk_report_user_folders_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT chk_report_user_folders_name CHECK (CHAR_LENGTH(TRIM(name)) BETWEEN 1 AND 100)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE report_user_preferences (
    user_id BIGINT UNSIGNED NOT NULL,
    report_id BIGINT UNSIGNED NOT NULL,
    folder_id BIGINT UNSIGNED NULL,
    starred BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id, report_id),
    KEY idx_report_user_preferences_folder (user_id, folder_id, report_id),
    KEY idx_report_user_preferences_starred (user_id, starred, report_id),
    CONSTRAINT fk_report_user_preferences_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT fk_report_user_preferences_report FOREIGN KEY (report_id) REFERENCES report_templates (id) ON DELETE CASCADE,
    CONSTRAINT fk_report_user_preferences_folder_owner FOREIGN KEY (user_id, folder_id) REFERENCES report_user_folders (user_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_report_user_preferences_starred CHECK (starred IN (FALSE, TRUE))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE report_user_preferences;
DROP TABLE report_user_folders;
