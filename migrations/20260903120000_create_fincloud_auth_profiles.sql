-- +goose Up
CREATE TABLE fincloud_auth_profiles (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name VARCHAR(128) NOT NULL,
    username VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    role_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    location_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    password_ciphertext BLOB NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'disabled',
    revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
    created_by_user_id BIGINT UNSIGNED NOT NULL,
    updated_by_user_id BIGINT UNSIGNED NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_fincloud_auth_profiles_name (name),
    KEY idx_fincloud_auth_profiles_status (status, name, id),
    KEY idx_fincloud_auth_profiles_username (username, status, id),
    CONSTRAINT fk_fincloud_auth_profiles_created_by FOREIGN KEY (created_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT fk_fincloud_auth_profiles_updated_by FOREIGN KEY (updated_by_user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT chk_fincloud_auth_profiles_status CHECK (status IN ('active','disabled','archived')),
    CONSTRAINT chk_fincloud_auth_profiles_revision CHECK (revision > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE source_settings
    ADD COLUMN fincloud_auth_profile_id BIGINT UNSIGNED NULL AFTER enabled,
    ADD KEY idx_source_settings_auth_profile (fincloud_auth_profile_id),
    ADD CONSTRAINT fk_source_settings_auth_profile
        FOREIGN KEY (fincloud_auth_profile_id) REFERENCES fincloud_auth_profiles (id) ON DELETE RESTRICT;

ALTER TABLE ingestion_runs
    ADD COLUMN fincloud_auth_profile_id BIGINT UNSIGNED NULL AFTER snapshot_date,
    ADD COLUMN fincloud_auth_profile_revision BIGINT UNSIGNED NULL AFTER fincloud_auth_profile_id,
    ADD COLUMN fincloud_auth_profile_name VARCHAR(128) NULL AFTER fincloud_auth_profile_revision,
    ADD COLUMN fincloud_auth_username VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL AFTER fincloud_auth_profile_name,
    ADD COLUMN fincloud_auth_role_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL AFTER fincloud_auth_username,
    ADD COLUMN fincloud_auth_location_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL AFTER fincloud_auth_role_id,
    ADD KEY idx_ingestion_runs_auth_profile (fincloud_auth_profile_id),
    ADD CONSTRAINT fk_ingestion_runs_auth_profile
        FOREIGN KEY (fincloud_auth_profile_id) REFERENCES fincloud_auth_profiles (id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_ingestion_runs_auth_snapshot CHECK (
        (fincloud_auth_profile_id IS NULL AND fincloud_auth_profile_revision IS NULL
            AND fincloud_auth_profile_name IS NULL AND fincloud_auth_username IS NULL
            AND fincloud_auth_role_id IS NULL AND fincloud_auth_location_id IS NULL)
        OR
        (fincloud_auth_profile_id IS NOT NULL AND fincloud_auth_profile_revision IS NOT NULL
            AND fincloud_auth_profile_name IS NOT NULL AND fincloud_auth_username IS NOT NULL
            AND fincloud_auth_role_id IS NOT NULL AND fincloud_auth_location_id IS NOT NULL)
    );

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: Fincloud auth profiles and frozen run identity may contain operational history';
