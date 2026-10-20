-- Passkeys: sign in with a fingerprint, face or device PIN instead of a
-- password and code.

-- A random ID for each person that passkeys carry instead of their username,
-- made when they add their first passkey.
ALTER TABLE users ADD COLUMN passkey_handle TEXT NOT NULL DEFAULT '';

CREATE TABLE passkeys (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    cred_id      TEXT    NOT NULL UNIQUE, -- base64url
    public_key   BLOB    NOT NULL,        -- SubjectPublicKeyInfo
    alg          INTEGER NOT NULL,        -- COSE algorithm
    sign_count   INTEGER NOT NULL DEFAULT 0,
    backed_up    INTEGER NOT NULL DEFAULT 0, -- synced (iCloud Keychain, Google Password Manager…)
    name         TEXT    NOT NULL,           -- like "iPhone (Safari)"
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX passkeys_user ON passkeys (user_id);
