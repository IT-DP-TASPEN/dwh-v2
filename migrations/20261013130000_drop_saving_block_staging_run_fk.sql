-- +goose Up
-- Like the other Detail root stages (see 20260826150000), a direct run FK makes
-- every staged insert share-lock ingestion_runs and block progress/heartbeat
-- updates. Terminal cleanup already reclaims orphaned staging rows.
ALTER TABLE stg_fincloud_saving_block_transactions
    DROP FOREIGN KEY fk_stg_fincloud_saving_block_transactions_run;

-- +goose Down
ALTER TABLE stg_fincloud_saving_block_transactions
    ADD CONSTRAINT fk_stg_fincloud_saving_block_transactions_run FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE;
