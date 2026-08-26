-- +goose Up
-- Detail staging is explicitly reclaimed after terminal/recovered runs. These
-- direct FKs made every staged insert take a shared lock on ingestion_runs,
-- blocking independent progress and heartbeat updates.
UPDATE ingestion_runs
SET status='abandoned', error_class='abandoned',
    error_message='Legacy execution had no recoverable owner lease.',
    error_step='ownership_lease', finished_at=UTC_TIMESTAMP(6)
WHERE status='running' AND (owner_id IS NULL OR heartbeat_at IS NULL);

UPDATE ingestion_runs child
JOIN ingestion_runs parent ON parent.id=child.parent_run_id
SET child.status='cancelled', child.error_class='abandoned',
    child.error_message='Run All parent had no recoverable owner lease.',
    child.error_step='ownership_lease', child.finished_at=UTC_TIMESTAMP(6)
WHERE parent.kind='run_all_parent' AND parent.status='abandoned'
  AND parent.error_step='ownership_lease' AND child.status IN ('planned','queued');

ALTER TABLE ingestion_runs
    ADD KEY idx_ingestion_runs_stale (status, heartbeat_at, id);

ALTER TABLE stg_fincloud_cif_details
    DROP FOREIGN KEY fk_stg_fincloud_cif_details_run;
ALTER TABLE stg_fincloud_saving_details
    DROP FOREIGN KEY fk_stg_fincloud_saving_details_run;
ALTER TABLE stg_fincloud_time_deposit_details
    DROP FOREIGN KEY fk_stg_fincloud_time_deposit_details_run;
ALTER TABLE stg_fincloud_loan_details
    DROP FOREIGN KEY fk_stg_fincloud_loan_details_run;

-- +goose Down
ALTER TABLE ingestion_runs DROP KEY idx_ingestion_runs_stale;

ALTER TABLE stg_fincloud_cif_details
    ADD CONSTRAINT fk_stg_fincloud_cif_details_run FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE;
ALTER TABLE stg_fincloud_saving_details
    ADD CONSTRAINT fk_stg_fincloud_saving_details_run FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE;
ALTER TABLE stg_fincloud_time_deposit_details
    ADD CONSTRAINT fk_stg_fincloud_time_deposit_details_run FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE;
ALTER TABLE stg_fincloud_loan_details
    ADD CONSTRAINT fk_stg_fincloud_loan_details_run FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE;
