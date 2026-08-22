-- +goose Up
DROP PROCEDURE IF EXISTS assert_detail_current_state_migration_empty;
-- +goose StatementBegin
CREATE PROCEDURE assert_detail_current_state_migration_empty()
BEGIN
    IF EXISTS (SELECT 1 FROM fincloud_cifs LIMIT 1)
        OR EXISTS (SELECT 1 FROM fincloud_saving_details LIMIT 1)
        OR EXISTS (SELECT 1 FROM fincloud_time_deposit_details LIMIT 1)
        OR EXISTS (SELECT 1 FROM fincloud_time_deposit_mutations LIMIT 1)
        OR EXISTS (SELECT 1 FROM fincloud_loan_details LIMIT 1)
        OR EXISTS (SELECT 1 FROM fincloud_loan_disbursement_fees LIMIT 1)
        OR EXISTS (SELECT 1 FROM fincloud_loan_repayment_schedule LIMIT 1)
        OR EXISTS (SELECT 1 FROM fincloud_loan_payment_history LIMIT 1)
    THEN
        SIGNAL SQLSTATE '45000'
            SET MESSAGE_TEXT = 'Detail current-state migration requires owner-approved reset and empty Detail tables';
    END IF;
END;
-- +goose StatementEnd

CALL assert_detail_current_state_migration_empty();
DROP PROCEDURE assert_detail_current_state_migration_empty;

ALTER TABLE fincloud_time_deposit_mutations
    DROP FOREIGN KEY fk_fincloud_time_deposit_mutations_parent,
    DROP PRIMARY KEY,
    DROP COLUMN as_of_date,
    ADD PRIMARY KEY (account_no, item_index);

ALTER TABLE fincloud_loan_disbursement_fees
    DROP FOREIGN KEY fk_fincloud_loan_fees_parent,
    DROP PRIMARY KEY,
    DROP COLUMN as_of_date,
    ADD PRIMARY KEY (account_no, item_index);

ALTER TABLE fincloud_loan_repayment_schedule
    DROP FOREIGN KEY fk_fincloud_loan_schedule_parent,
    DROP PRIMARY KEY,
    DROP COLUMN as_of_date,
    ADD PRIMARY KEY (account_no, item_index);

ALTER TABLE fincloud_loan_payment_history
    DROP FOREIGN KEY fk_fincloud_loan_history_parent,
    DROP PRIMARY KEY,
    DROP COLUMN as_of_date,
    ADD PRIMARY KEY (account_no, item_index);

ALTER TABLE fincloud_cifs
    DROP INDEX idx_fincloud_cifs_cif_no,
    DROP PRIMARY KEY,
    DROP COLUMN as_of_date,
    ADD PRIMARY KEY (cif_no);

ALTER TABLE fincloud_saving_details
    DROP INDEX idx_fincloud_saving_details_account,
    DROP INDEX idx_fincloud_saving_details_cif,
    DROP PRIMARY KEY,
    DROP COLUMN as_of_date,
    ADD PRIMARY KEY (account_no),
    ADD KEY idx_fincloud_saving_details_cif (cif_no);

ALTER TABLE fincloud_time_deposit_details
    DROP INDEX idx_fincloud_time_deposit_account,
    DROP INDEX idx_fincloud_time_deposit_cif,
    DROP PRIMARY KEY,
    DROP COLUMN as_of_date,
    ADD PRIMARY KEY (account_no),
    ADD KEY idx_fincloud_time_deposit_cif (cif_no);

ALTER TABLE fincloud_loan_details
    DROP INDEX idx_fincloud_loan_account,
    DROP INDEX idx_fincloud_loan_cif,
    DROP PRIMARY KEY,
    DROP COLUMN as_of_date,
    ADD PRIMARY KEY (account_no),
    ADD KEY idx_fincloud_loan_cif (cif_no);

ALTER TABLE fincloud_time_deposit_mutations
    ADD CONSTRAINT fk_fincloud_time_deposit_mutations_parent
        FOREIGN KEY (account_no) REFERENCES fincloud_time_deposit_details (account_no) ON DELETE CASCADE;

ALTER TABLE fincloud_loan_disbursement_fees
    ADD CONSTRAINT fk_fincloud_loan_fees_parent
        FOREIGN KEY (account_no) REFERENCES fincloud_loan_details (account_no) ON DELETE CASCADE;

ALTER TABLE fincloud_loan_repayment_schedule
    ADD CONSTRAINT fk_fincloud_loan_schedule_parent
        FOREIGN KEY (account_no) REFERENCES fincloud_loan_details (account_no) ON DELETE CASCADE;

ALTER TABLE fincloud_loan_payment_history
    ADD CONSTRAINT fk_fincloud_loan_history_parent
        FOREIGN KEY (account_no) REFERENCES fincloud_loan_details (account_no) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_cif_details LIKE fincloud_cifs;
ALTER TABLE stg_fincloud_cif_details
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, cif_no),
    ADD CONSTRAINT fk_stg_fincloud_cif_details_run
        FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_saving_details LIKE fincloud_saving_details;
ALTER TABLE stg_fincloud_saving_details
    DROP INDEX idx_fincloud_saving_details_cif,
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, account_no),
    ADD CONSTRAINT fk_stg_fincloud_saving_details_run
        FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_time_deposit_details LIKE fincloud_time_deposit_details;
ALTER TABLE stg_fincloud_time_deposit_details
    DROP INDEX idx_fincloud_time_deposit_cif,
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, account_no),
    ADD CONSTRAINT fk_stg_fincloud_time_deposit_details_run
        FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_time_deposit_mutations LIKE fincloud_time_deposit_mutations;
ALTER TABLE stg_fincloud_time_deposit_mutations
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, account_no, item_index),
    ADD CONSTRAINT fk_stg_fincloud_time_deposit_mutations_parent
        FOREIGN KEY (ingestion_run_id, account_no)
        REFERENCES stg_fincloud_time_deposit_details (ingestion_run_id, account_no) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_loan_details LIKE fincloud_loan_details;
ALTER TABLE stg_fincloud_loan_details
    DROP INDEX idx_fincloud_loan_cif,
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, account_no),
    ADD CONSTRAINT fk_stg_fincloud_loan_details_run
        FOREIGN KEY (ingestion_run_id) REFERENCES ingestion_runs (id) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_loan_disbursement_fees LIKE fincloud_loan_disbursement_fees;
ALTER TABLE stg_fincloud_loan_disbursement_fees
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, account_no, item_index),
    ADD CONSTRAINT fk_stg_fincloud_loan_fees_parent
        FOREIGN KEY (ingestion_run_id, account_no)
        REFERENCES stg_fincloud_loan_details (ingestion_run_id, account_no) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_loan_repayment_schedule LIKE fincloud_loan_repayment_schedule;
ALTER TABLE stg_fincloud_loan_repayment_schedule
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, account_no, item_index),
    ADD CONSTRAINT fk_stg_fincloud_loan_schedule_parent
        FOREIGN KEY (ingestion_run_id, account_no)
        REFERENCES stg_fincloud_loan_details (ingestion_run_id, account_no) ON DELETE CASCADE;

CREATE TABLE stg_fincloud_loan_payment_history LIKE fincloud_loan_payment_history;
ALTER TABLE stg_fincloud_loan_payment_history
    DROP PRIMARY KEY,
    DROP COLUMN created_at,
    DROP COLUMN updated_at,
    ADD COLUMN ingestion_run_id BIGINT UNSIGNED NOT NULL FIRST,
    ADD PRIMARY KEY (ingestion_run_id, account_no, item_index),
    ADD CONSTRAINT fk_stg_fincloud_loan_history_parent
        FOREIGN KEY (ingestion_run_id, account_no)
        REFERENCES stg_fincloud_loan_details (ingestion_run_id, account_no) ON DELETE CASCADE;

-- +goose Down
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'irreversible: Detail current-state migration discards dated identity';
