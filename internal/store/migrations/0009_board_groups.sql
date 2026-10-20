-- taper:foreign-keys-off
-- The board role, files attached to chat messages, and group chats.
--
-- SQLite can't change a CHECK constraint in place, so users and files are
-- rebuilt (the runner turns foreign keys off for this, as SQLite's docs
-- describe, and checks them before committing).

CREATE TABLE users_new (
    id                   INTEGER PRIMARY KEY,
    username             TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    display_name         TEXT    NOT NULL,
    email                TEXT    NOT NULL DEFAULT '',
    role                 TEXT    NOT NULL CHECK (role IN ('admin', 'board', 'mentor', 'scholar')),
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

CREATE TABLE files_new (
    id           INTEGER PRIMARY KEY,
    owner_kind   TEXT    NOT NULL CHECK (owner_kind IN ('assignment', 'submission', 'message')),
    owner_id     INTEGER NOT NULL,
    name         TEXT    NOT NULL, -- as uploaded, cleaned
    size         INTEGER NOT NULL,
    content_type TEXT    NOT NULL,
    stored       TEXT    NOT NULL UNIQUE, -- path under files/
    uploaded_by  INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL
);
INSERT INTO files_new (id, owner_kind, owner_id, name, size, content_type, stored, uploaded_by, created_at)
SELECT id, owner_kind, owner_id, name, size, content_type, stored, uploaded_by, created_at FROM files;
DROP TABLE files;
ALTER TABLE files_new RENAME TO files;
CREATE INDEX files_owner ON files (owner_kind, owner_id);

-- Channels: the community channel, class channels, and group chats that
-- admins and the board make, with their own name and members.
ALTER TABLE channels ADD COLUMN kind TEXT NOT NULL DEFAULT 'class';
ALTER TABLE channels ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN created_by INTEGER REFERENCES users (id) ON DELETE SET NULL;
UPDATE channels SET kind = 'community' WHERE class_id IS NULL;

CREATE TABLE channel_members (
    channel_id INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    added_at   INTEGER NOT NULL,
    PRIMARY KEY (channel_id, user_id)
);
CREATE INDEX channel_members_user ON channel_members (user_id);
