-- Assignments, scholars' work on them, and uploaded files.

CREATE TABLE assignments (
    id           INTEGER PRIMARY KEY,
    class_id     INTEGER NOT NULL REFERENCES classes (id) ON DELETE CASCADE,
    title        TEXT    NOT NULL,
    instructions TEXT    NOT NULL DEFAULT '', -- Markdown
    due_date     TEXT    NOT NULL DEFAULT '', -- YYYY-MM-DD, school time; '' for no due date
    due_time     TEXT    NOT NULL DEFAULT '', -- HH:MM; '' for the end of the day
    publish_at   INTEGER NOT NULL DEFAULT 0,  -- when scholars can see it; 0 for a draft
    work_online  INTEGER NOT NULL DEFAULT 1,  -- scholars can write their work in Taper
    allow_files  INTEGER NOT NULL DEFAULT 1,  -- scholars can upload files
    created_by   INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE INDEX assignments_class ON assignments (class_id);

-- One per scholar per assignment, made when they first save or turn in work.
CREATE TABLE submissions (
    id            INTEGER PRIMARY KEY,
    assignment_id INTEGER NOT NULL REFERENCES assignments (id) ON DELETE CASCADE,
    scholar_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    body          TEXT    NOT NULL DEFAULT '', -- work written in Taper
    status        TEXT    NOT NULL DEFAULT 'draft'
                  CHECK (status IN ('draft', 'turned_in', 'needs_work', 'complete')),
    turned_in_at  INTEGER NOT NULL DEFAULT 0,  -- the last time it was turned in
    feedback      TEXT    NOT NULL DEFAULT '', -- the mentor's latest feedback (Markdown)
    feedback_by   INTEGER REFERENCES users (id) ON DELETE SET NULL,
    feedback_at   INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    UNIQUE (assignment_id, scholar_id)
);

-- What happened to a piece of work: turned in, taken back, feedback given.
CREATE TABLE submission_history (
    id            INTEGER PRIMARY KEY,
    submission_id INTEGER NOT NULL REFERENCES submissions (id) ON DELETE CASCADE,
    kind          TEXT    NOT NULL, -- turned_in, taken_back, needs_work, complete
    by_user       INTEGER REFERENCES users (id) ON DELETE SET NULL,
    note          TEXT    NOT NULL DEFAULT '', -- feedback given at the time
    at            INTEGER NOT NULL
);

CREATE INDEX submission_history_submission ON submission_history (submission_id);

-- Uploaded files, stored under files/ in the data folder.
CREATE TABLE files (
    id           INTEGER PRIMARY KEY,
    owner_kind   TEXT    NOT NULL CHECK (owner_kind IN ('assignment', 'submission')),
    owner_id     INTEGER NOT NULL,
    name         TEXT    NOT NULL, -- as uploaded, cleaned
    size         INTEGER NOT NULL,
    content_type TEXT    NOT NULL,
    stored       TEXT    NOT NULL UNIQUE, -- path under files/
    uploaded_by  INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL
);

CREATE INDEX files_owner ON files (owner_kind, owner_id);
