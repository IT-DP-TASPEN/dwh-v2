-- +goose Up
ALTER TABLE ingestion_runs
    ADD COLUMN mapper_diagnostics JSON NULL AFTER error_step;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: mapper diagnostics may contain operational evidence';
