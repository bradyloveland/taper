-- School and class calendars, and private calendar subscription links.

-- Dates and times are the school's local wall-clock time (the time zone is a
-- setting), so a weekly 10:00 class stays at 10:00 across daylight saving.
CREATE TABLE events (
    id           INTEGER PRIMARY KEY,
    class_id     INTEGER REFERENCES classes (id) ON DELETE CASCADE, -- NULL: the school calendar
    title        TEXT    NOT NULL,
    description  TEXT    NOT NULL DEFAULT '',
    location     TEXT    NOT NULL DEFAULT '',
    closed       INTEGER NOT NULL DEFAULT 0,  -- a day without school (holidays, breaks)
    start_date   TEXT    NOT NULL,            -- YYYY-MM-DD
    start_time   TEXT    NOT NULL DEFAULT '', -- HH:MM, or '' for all day
    end_date     TEXT    NOT NULL,            -- YYYY-MM-DD, the last day (inclusive)
    end_time     TEXT    NOT NULL DEFAULT '', -- HH:MM, or '' for all day
    repeat       TEXT    NOT NULL DEFAULT '' CHECK (repeat IN ('', 'daily', 'weekly', 'monthly', 'yearly')),
    repeat_days  TEXT    NOT NULL DEFAULT '', -- weekly: weekdays 0-6 (Sunday 0), like "2,4"
    repeat_until TEXT    NOT NULL DEFAULT '', -- YYYY-MM-DD, or '' for no end
    uid          TEXT    NOT NULL UNIQUE,     -- for calendar apps
    sequence     INTEGER NOT NULL DEFAULT 0,  -- bumped on each change, for calendar apps
    created_by   INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE INDEX events_class ON events (class_id);
CREATE INDEX events_dates ON events (start_date, end_date, repeat_until);

-- Single dates left out of a repeating event.
CREATE TABLE event_skips (
    event_id INTEGER NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    date     TEXT    NOT NULL,
    PRIMARY KEY (event_id, date)
);

-- Each person's private calendar subscription link. The token is shown to
-- its owner, so it's kept (encrypted) as well as hashed for lookups.
CREATE TABLE calendar_feeds (
    user_id      INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    token_hash   TEXT    NOT NULL UNIQUE,
    token_sealed TEXT    NOT NULL,
    created_at   INTEGER NOT NULL
);
