-- +goose Up
ALTER TABLE ingestion_runs
    ADD KEY idx_ingestion_runs_admin_job (job_key, id),
    ADD KEY idx_ingestion_runs_admin_status (status, id),
    ADD KEY idx_ingestion_runs_admin_trigger (trigger_type, id);

-- +goose Down
ALTER TABLE ingestion_runs
    DROP KEY idx_ingestion_runs_admin_job,
    DROP KEY idx_ingestion_runs_admin_status,
    DROP KEY idx_ingestion_runs_admin_trigger;
