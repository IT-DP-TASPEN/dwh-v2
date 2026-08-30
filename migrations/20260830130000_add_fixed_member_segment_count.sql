-- +goose Up
ALTER TABLE fixed_report_load_members
    ADD COLUMN staged_segment_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER status;

-- +goose Down
ALTER TABLE fixed_report_load_members
    DROP COLUMN staged_segment_count;
