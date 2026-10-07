-- Classes and who's in them.

CREATE TABLE classes (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '', -- Markdown
    term        TEXT    NOT NULL DEFAULT '', -- free text, like "2026–27" or "Fall 2026"
    meets       TEXT    NOT NULL DEFAULT '', -- free text, like "Tuesdays 10–11:30"
    color       TEXT    NOT NULL DEFAULT 'blue',
    archived    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE class_members (
    class_id INTEGER NOT NULL REFERENCES classes (id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role     TEXT    NOT NULL CHECK (role IN ('mentor', 'scholar')),
    added_at INTEGER NOT NULL,
    PRIMARY KEY (class_id, user_id)
);

CREATE INDEX class_members_user ON class_members (user_id);
