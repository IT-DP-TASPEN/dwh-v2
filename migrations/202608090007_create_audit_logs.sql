-- +goose Up
CREATE TABLE audit_logs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    actor_user_id BIGINT UNSIGNED NULL,
    actor_username VARCHAR(191) NULL,
    effective_user_id BIGINT UNSIGNED NULL,
    effective_username VARCHAR(191) NULL,
    action VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    resource_type VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
    resource_id BIGINT UNSIGNED NULL,
    metadata JSON NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_audit_logs_created_at (created_at),
    KEY idx_audit_logs_actor_user_id (actor_user_id),
    KEY idx_audit_logs_effective_user_id (effective_user_id),
    KEY idx_audit_logs_action (action),
    KEY idx_audit_logs_resource (resource_type, resource_id),
    CONSTRAINT fk_audit_logs_actor_user
        FOREIGN KEY (actor_user_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT fk_audit_logs_effective_user
        FOREIGN KEY (effective_user_id) REFERENCES users (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE audit_logs;
