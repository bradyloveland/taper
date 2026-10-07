-- Problems reported from inside Taper, and the GitHub issues they became.

CREATE TABLE bug_reports (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER REFERENCES users (id) ON DELETE SET NULL,
    title        TEXT    NOT NULL,
    body         TEXT    NOT NULL,
    page         TEXT    NOT NULL DEFAULT '',
    issue_url    TEXT    NOT NULL DEFAULT '',
    issue_number INTEGER NOT NULL DEFAULT 0,
    error        TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);

CREATE INDEX bug_reports_created ON bug_reports (created_at);
