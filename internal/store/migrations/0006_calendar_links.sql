-- One private calendar link per person per calendar, so each can be reset
-- on its own. Replaces calendar_feeds, which had one token per person.

DROP TABLE calendar_feeds;

CREATE TABLE calendar_links (
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    scope        TEXT    NOT NULL, -- "mine", "school" or "class:ID"
    token_hash   TEXT    NOT NULL UNIQUE,
    token_sealed TEXT    NOT NULL, -- shown to its owner, so kept encrypted
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (user_id, scope)
);
