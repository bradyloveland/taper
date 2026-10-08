-- Chat: a community channel for the whole school and one for each class.

CREATE TABLE channels (
    id         INTEGER PRIMARY KEY,
    class_id   INTEGER UNIQUE REFERENCES classes (id) ON DELETE CASCADE, -- NULL: the community channel
    enabled    INTEGER NOT NULL DEFAULT 1,  -- admins can turn a channel off
    created_at INTEGER NOT NULL
);

-- The community channel; class channels are made when first needed.
INSERT INTO channels (class_id, enabled, created_at) VALUES (NULL, 1, CAST(strftime('%s', 'now') AS INTEGER));

CREATE TABLE messages (
    id         INTEGER PRIMARY KEY,
    channel_id INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id    INTEGER REFERENCES users (id) ON DELETE SET NULL,
    body       TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    deleted_at INTEGER NOT NULL DEFAULT 0, -- removed by its author or a moderator
    deleted_by INTEGER REFERENCES users (id) ON DELETE SET NULL
);

CREATE INDEX messages_channel ON messages (channel_id, id);

-- How far each person has read in each channel.
CREATE TABLE channel_reads (
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    last_read  INTEGER NOT NULL DEFAULT 0, -- a message id
    PRIMARY KEY (user_id, channel_id)
);

-- People who can't post in a channel for a while.
CREATE TABLE chat_mutes (
    channel_id INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    until      INTEGER NOT NULL DEFAULT 0, -- 0: until someone unmutes them
    by_user    INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (channel_id, user_id)
);
