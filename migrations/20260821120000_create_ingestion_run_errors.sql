-- +goose Up
CREATE TABLE ingestion_run_errors (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    run_id BIGINT UNSIGNED NOT NULL,
    occurred_at DATETIME(6) NOT NULL,
    last_occurred_at DATETIME(6) NOT NULL,
    severity VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    event_kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    terminal BOOLEAN NOT NULL DEFAULT FALSE,
    recovered BOOLEAN NULL,
    class VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    step VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    operation VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    job_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    item_identifier VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL,
    member_key VARCHAR(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL,
    attempt SMALLINT UNSIGNED NULL,
    error_type VARCHAR(255) NULL,
    error_message VARCHAR(2048) NULL,
    aggregation_scope VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
    aggregation_key BINARY(32) NULL,
    occurrence_count BIGINT UNSIGNED NOT NULL DEFAULT 1,
    sample_items JSON NULL,
    technical_details JSON NOT NULL,
    PRIMARY KEY (id),
    KEY idx_ingestion_run_errors_run_time (run_id, occurred_at, id),
    UNIQUE KEY uq_ingestion_run_errors_aggregate (run_id, aggregation_scope, aggregation_key),
    CONSTRAINT fk_ingestion_run_errors_run
        FOREIGN KEY (run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE,
    CONSTRAINT chk_ingestion_run_errors_severity CHECK (severity IN ('info', 'warning', 'error')),
    CONSTRAINT chk_ingestion_run_errors_kind CHECK (event_kind IN ('failure', 'retry', 'recovery', 'overflow')),
    CONSTRAINT chk_ingestion_run_errors_occurrences CHECK (occurrence_count >= 1),
    CONSTRAINT chk_ingestion_run_errors_aggregation CHECK (
        (aggregation_scope IS NULL AND aggregation_key IS NULL) OR
        (aggregation_scope IS NOT NULL AND aggregation_key IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: ingestion technical diagnostics may contain operational evidence';
