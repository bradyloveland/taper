-- Web push: devices that get notifications on their lock screen.
--
-- Each subscription belongs to the session that turned it on, so signing out
-- of a device (or the session ending) stops its notifications.

CREATE TABLE push_subs (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    session_hash TEXT    NOT NULL REFERENCES sessions (token_hash) ON DELETE CASCADE,
    endpoint     TEXT    NOT NULL UNIQUE,
    p256dh       TEXT    NOT NULL, -- base64url
    auth         TEXT    NOT NULL, -- base64url
    user_agent   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_ok_at   INTEGER NOT NULL DEFAULT 0,
    failures     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX push_subs_user ON push_subs (user_id);
CREATE INDEX push_subs_session ON push_subs (session_hash);
