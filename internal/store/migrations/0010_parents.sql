-- taper:foreign-keys-off
-- The parent role, and which scholars each person is a parent of.
--
-- users is rebuilt to widen its role CHECK (see 0009). Anyone who isn't a
-- scholar can be a parent: mentors and board members often are.

CREATE TABLE users_new (
    id                   INTEGER PRIMARY KEY,
    username             TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    display_name         TEXT    NOT NULL,
    email                TEXT    NOT NULL DEFAULT '',
    role                 TEXT    NOT NULL CHECK (role IN ('admin', 'board', 'mentor', 'parent', 'scholar')),
    password_hash        TEXT    NOT NULL,
    must_change_password INTEGER NOT NULL DEFAULT 0,
    active               INTEGER NOT NULL DEFAULT 1,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    last_login_at        INTEGER,
    totp_secret          TEXT    NOT NULL DEFAULT '',
    totp_enabled         INTEGER NOT NULL DEFAULT 0,
    totp_last_step       INTEGER NOT NULL DEFAULT 0
);
INSERT INTO users_new (id, username, display_name, email, role, password_hash, must_change_password, active, created_at,
    updated_at, last_login_at, totp_secret, totp_enabled, totp_last_step)
SELECT id, username, display_name, email, role, password_hash, must_change_password, active, created_at,
    updated_at, last_login_at, totp_secret, totp_enabled, totp_last_step FROM users;
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;

CREATE TABLE parents (
    parent_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    scholar_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    added_at   INTEGER NOT NULL,
    PRIMARY KEY (parent_id, scholar_id)
);
CREATE INDEX parents_scholar ON parents (scholar_id);
