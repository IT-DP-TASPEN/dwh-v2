-- +goose Up
CREATE TABLE schedules (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name VARCHAR(128) NOT NULL,
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    cron_expression VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    timezone VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    policy_kind VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    policy_version SMALLINT UNSIGNED NOT NULL,
    policy_json JSON NOT NULL,
    policy_checksum BINARY(32) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    next_run_at DATETIME(6) NULL,
    revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
    scheduler_not_before DATETIME(6) NULL,
    delivery_block_reason VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    delivery_blocked_at DATETIME(6) NULL,
    validation_error_class VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    validation_error_message VARCHAR(500) NULL,
    validation_error_at DATETIME(6) NULL,
    created_by_user_id BIGINT UNSIGNED NULL,
    updated_by_user_id BIGINT UNSIGNED NULL,
    archived_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_schedules_due (enabled, scheduler_not_before, next_run_at, id),
    KEY idx_schedules_job (job_key),
    KEY idx_schedules_created_by (created_by_user_id),
    KEY idx_schedules_updated_by (updated_by_user_id),
    CONSTRAINT fk_schedules_created_by_user FOREIGN KEY (created_by_user_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT fk_schedules_updated_by_user FOREIGN KEY (updated_by_user_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT chk_schedules_revision CHECK (revision > 0),
    CONSTRAINT chk_schedules_cursor CHECK (
        (enabled = TRUE AND archived_at IS NULL AND next_run_at IS NOT NULL) OR
        (enabled = FALSE AND next_run_at IS NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE schedule_occurrences (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    schedule_id BIGINT UNSIGNED NOT NULL,
    scheduled_for DATETIME(6) NOT NULL,
    identity_source VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    resolution_mode VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    schedule_revision BIGINT UNSIGNED NOT NULL,
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    cron_expression VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    timezone VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    policy_kind VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    policy_version SMALLINT UNSIGNED NOT NULL,
    policy_json JSON NOT NULL,
    policy_checksum BINARY(32) NOT NULL,
    attempt_count INT UNSIGNED NOT NULL DEFAULT 0,
    retry_not_before DATETIME(6) NULL,
    closed_at DATETIME(6) NULL,
    closed_by_user_id BIGINT UNSIGNED NULL,
    rejection_class VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    rejection_message VARCHAR(500) NULL,
    rejection_revision BIGINT UNSIGNED NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    active_schedule_id BIGINT UNSIGNED
        GENERATED ALWAYS AS (CASE WHEN status = 'unresolved' THEN schedule_id ELSE NULL END) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY uq_schedule_occurrences_identity (schedule_id, scheduled_for),
    UNIQUE KEY uq_schedule_occurrences_active (active_schedule_id),
    KEY idx_schedule_occurrences_retry (status, retry_not_before),
    KEY idx_schedule_occurrences_closed_by (closed_by_user_id),
    CONSTRAINT fk_schedule_occurrences_schedule FOREIGN KEY (schedule_id) REFERENCES schedules (id) ON DELETE RESTRICT,
    CONSTRAINT fk_schedule_occurrences_closed_by FOREIGN KEY (closed_by_user_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT chk_schedule_occurrences_identity_source CHECK (identity_source IN ('validated_cron', 'persisted_cursor_fallback')),
    CONSTRAINT chk_schedule_occurrences_resolution CHECK (resolution_mode IN ('historical', 'live_coalesced', 'invalid')),
    CONSTRAINT chk_schedule_occurrences_status CHECK (status IN ('unresolved', 'resolved', 'discarded', 'rejected_invalid'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE schedule_attempts (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    occurrence_id BIGINT UNSIGNED NOT NULL,
    attempt_no INT UNSIGNED NOT NULL,
    ingestion_run_id BIGINT UNSIGNED NOT NULL,
    trigger_reference VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    submitted_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_schedule_attempts_number (occurrence_id, attempt_no),
    UNIQUE KEY uq_schedule_attempts_run (ingestion_run_id),
    UNIQUE KEY uq_schedule_attempts_reference (trigger_reference),
    CONSTRAINT fk_schedule_attempts_occurrence FOREIGN KEY (occurrence_id) REFERENCES schedule_occurrences (id) ON DELETE RESTRICT,
    CONSTRAINT fk_schedule_attempts_run FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE RESTRICT,
    CONSTRAINT chk_schedule_attempts_number CHECK (attempt_no > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE ingestion_runs
    ADD COLUMN scheduler_trigger_reference VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin
        GENERATED ALWAYS AS (
            CASE WHEN kind = 'job' AND trigger_type = 'scheduler' THEN trigger_reference ELSE NULL END
        ) STORED,
    ADD UNIQUE KEY uq_ingestion_runs_scheduler_trigger (scheduler_trigger_reference);

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: scheduler history may contain operational data';
