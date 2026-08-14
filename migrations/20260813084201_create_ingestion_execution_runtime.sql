-- +goose Up
CREATE TABLE ingestion_runtime_settings (
    id TINYINT UNSIGNED NOT NULL,
    max_running_jobs INT UNSIGNED NOT NULL,
    fixed_member_concurrency INT UNSIGNED NOT NULL,
    detail_concurrency INT UNSIGNED NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    CONSTRAINT chk_ingestion_runtime_singleton CHECK (id = 1),
    CONSTRAINT chk_ingestion_runtime_limits CHECK (
        max_running_jobs BETWEEN 1 AND 64 AND
        fixed_member_concurrency BETWEEN 1 AND 64 AND
        detail_concurrency BETWEEN 1 AND 64
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO ingestion_runtime_settings
    (id, max_running_jobs, fixed_member_concurrency, detail_concurrency)
VALUES (1, 2, 4, 3);

CREATE TABLE ingestion_runs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    parent_run_id BIGINT UNSIGNED NULL,
    child_position SMALLINT UNSIGNED NULL,
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL,
    status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    parameter_kind VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    parameter_version SMALLINT UNSIGNED NOT NULL,
    parameters_json JSON NOT NULL,
    parameter_checksum BINARY(32) NOT NULL,
    trigger_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    trigger_reference VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL,
    requested_by_user_id BIGINT UNSIGNED NULL,
    skip_reason VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    cancel_requested_at DATETIME(6) NULL,
    cancel_reason VARCHAR(255) NULL,
    owner_id CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    claimed_at DATETIME(6) NULL,
    heartbeat_at DATETIME(6) NULL,
    snapshot_date DATE NULL,
    progress_total BIGINT UNSIGNED NOT NULL DEFAULT 0,
    progress_started BIGINT UNSIGNED NOT NULL DEFAULT 0,
    progress_succeeded BIGINT UNSIGNED NOT NULL DEFAULT 0,
    progress_failed BIGINT UNSIGNED NOT NULL DEFAULT 0,
    rows_processed BIGINT UNSIGNED NOT NULL DEFAULT 0,
    current_step VARCHAR(128) NULL,
    error_class VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    error_message VARCHAR(500) NULL,
    error_step VARCHAR(128) NULL,
    abandoned_previous_owner CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    abandoned_previous_heartbeat DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    started_at DATETIME(6) NULL,
    finished_at DATETIME(6) NULL,
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    active_job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin
        GENERATED ALWAYS AS (
            CASE
                WHEN kind IN ('job', 'run_all_child') AND status IN ('queued', 'running') THEN job_key
                ELSE NULL
            END
        ) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY uq_ingestion_runs_active_job (active_job_key),
    UNIQUE KEY uq_ingestion_runs_parent_position (parent_run_id, child_position),
    UNIQUE KEY uq_ingestion_runs_parent_job (parent_run_id, job_key),
    KEY idx_ingestion_runs_queue (kind, status, created_at, id),
    KEY idx_ingestion_runs_owner (owner_id, status, heartbeat_at),
    KEY idx_ingestion_runs_parent_status (parent_run_id, status, child_position),
    KEY idx_ingestion_runs_requested_by (requested_by_user_id),
    CONSTRAINT fk_ingestion_runs_parent
        FOREIGN KEY (parent_run_id) REFERENCES ingestion_runs (id) ON DELETE RESTRICT,
    CONSTRAINT fk_ingestion_runs_requested_by
        FOREIGN KEY (requested_by_user_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT chk_ingestion_runs_kind CHECK (kind IN ('job', 'run_all_parent', 'run_all_child')),
    CONSTRAINT chk_ingestion_runs_status CHECK (status IN (
        'planned', 'queued', 'running', 'succeeded', 'failed', 'skipped', 'cancelled',
        'abandoned', 'completed', 'completed_with_skips'
    )),
    CONSTRAINT chk_ingestion_runs_shape CHECK (
        (kind = 'job' AND parent_run_id IS NULL AND child_position IS NULL AND job_key IS NOT NULL) OR
        (kind = 'run_all_parent' AND parent_run_id IS NULL AND child_position IS NULL AND job_key IS NULL) OR
        (kind = 'run_all_child' AND parent_run_id IS NOT NULL AND child_position IS NOT NULL AND job_key IS NOT NULL)
    ),
    CONSTRAINT chk_ingestion_runs_progress CHECK (
        progress_started <= progress_total AND
        progress_succeeded + progress_failed <= progress_started
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE fixed_report_loads
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL AFTER id,
    ADD UNIQUE KEY uq_fixed_report_loads_ingestion_run (ingestion_run_id),
    ADD CONSTRAINT fk_fixed_report_loads_ingestion_run
        FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE RESTRICT;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: ingestion execution history may contain operational data';
