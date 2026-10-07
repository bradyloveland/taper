-- Two-step sign-in, recovery codes, and password reset links.

-- totp_secret is encrypted (internal/secret); totp_last_step stops a code
-- being used twice.
ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;

CREATE TABLE recovery_codes (
    user_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash TEXT    NOT NULL,
    used_at   INTEGER,
    PRIMARY KEY (user_id, code_hash)
);

-- A session waiting for the second step of signing in.
ALTER TABLE sessions ADD COLUMN mfa_pending INTEGER NOT NULL DEFAULT 0;

CREATE TABLE password_resets (
    token_hash TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
);

CREATE INDEX password_resets_user ON password_resets (user_id);
