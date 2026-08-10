-- +goose Up
ALTER TABLE sessions
    ADD COLUMN impersonated_user_id BIGINT UNSIGNED NULL AFTER user_id,
    ADD KEY idx_sessions_impersonated_user_id (impersonated_user_id),
    ADD CONSTRAINT fk_sessions_impersonated_user
        FOREIGN KEY (impersonated_user_id) REFERENCES users (id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE sessions
    DROP FOREIGN KEY fk_sessions_impersonated_user,
    DROP KEY idx_sessions_impersonated_user_id,
    DROP COLUMN impersonated_user_id;
