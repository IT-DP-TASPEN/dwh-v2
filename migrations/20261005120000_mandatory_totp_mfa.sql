-- +goose Up
-- Hard cutover: no password-only session survives this deployment.
DELETE FROM sessions;
ALTER TABLE sessions ADD COLUMN mfa_verified_at DATETIME(6) NOT NULL;
CREATE TABLE user_totp_enrollments (
    user_id BIGINT UNSIGNED NOT NULL,
    secret_ciphertext VARBINARY(512) NOT NULL,
    last_counter BIGINT NOT NULL,
    enrolled_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (user_id),
    CONSTRAINT fk_totp_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE user_mfa_recovery_codes (
    user_id BIGINT UNSIGNED NOT NULL,
    code_hash BINARY(32) NOT NULL,
    used_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (user_id, code_hash),
    CONSTRAINT fk_mfa_recovery_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE mfa_challenges (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    token_hash BINARY(32) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    session_id BIGINT UNSIGNED NULL,
    purpose VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    pending_secret VARBINARY(512) NULL,
    remember_me BOOLEAN NOT NULL DEFAULT FALSE,
    next_path VARCHAR(2048) NOT NULL,
    failures TINYINT UNSIGNED NOT NULL DEFAULT 0,
    expires_at DATETIME(6) NOT NULL,
    consumed_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_mfa_challenge_token (token_hash),
    KEY idx_mfa_challenge_user (user_id, purpose, consumed_at),
    KEY idx_mfa_challenge_expiry (expires_at),
    CONSTRAINT chk_mfa_challenge_failures CHECK (failures <= 5),
    CONSTRAINT chk_mfa_challenge_purpose CHECK (purpose IN ('login','enrollment','step_up','rotation','rotate_authorize','regenerate')),
    CONSTRAINT fk_mfa_challenge_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT fk_mfa_challenge_session FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
) ENGINE=InnoDB;

-- +goose Down
DROP TABLE mfa_challenges;
DROP TABLE user_mfa_recovery_codes;
DROP TABLE user_totp_enrollments;
DELETE FROM sessions;
ALTER TABLE sessions DROP COLUMN mfa_verified_at;
