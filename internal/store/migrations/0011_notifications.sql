-- Notifications: in the app, and by email for people who choose it.

CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL, -- assignment, work, chat, due, calendar
    ref        TEXT    NOT NULL DEFAULT '', -- what it's about, like "chat:3"; unread ones with the same ref are combined
    title      TEXT    NOT NULL,
    link       TEXT    NOT NULL DEFAULT '',
    count      INTEGER NOT NULL DEFAULT 1, -- how many things it stands for, when combined
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    read_at    INTEGER,
    emailed_at INTEGER -- sent by email, or no longer waiting to be
);
CREATE INDEX notifications_user ON notifications (user_id, updated_at);
CREATE INDEX notifications_unread ON notifications (user_id, ref) WHERE read_at IS NULL;
CREATE INDEX notifications_unsent ON notifications (user_id) WHERE emailed_at IS NULL;

-- Each person's choices. No row means the defaults: every kind on, no email.
CREATE TABLE notify_prefs (
    user_id     INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    email       TEXT    NOT NULL DEFAULT 'off' CHECK (email IN ('off', 'each', 'daily')),
    off_kinds   TEXT    NOT NULL DEFAULT '', -- comma-separated kinds turned off
    digest_at   INTEGER NOT NULL DEFAULT 0   -- when the last daily summary went
);

-- Reminders already sent, so each goes once.
CREATE TABLE notify_sent (
    key        TEXT PRIMARY KEY, -- like "due:12:34" (assignment, scholar)
    created_at INTEGER NOT NULL
);

-- Assignments people have been told about. Those already published when this
-- version is installed count as told.
ALTER TABLE assignments ADD COLUMN announced INTEGER NOT NULL DEFAULT 0;
UPDATE assignments SET announced = 1 WHERE publish_at > 0 AND publish_at <= CAST(strftime('%s', 'now') AS INTEGER);
