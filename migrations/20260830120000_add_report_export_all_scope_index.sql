-- +goose Up
ALTER TABLE report_export_jobs
    ADD KEY idx_report_export_jobs_created (created_at, id);

-- +goose Down
ALTER TABLE report_export_jobs
    DROP KEY idx_report_export_jobs_created;
