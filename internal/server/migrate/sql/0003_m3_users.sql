-- M3: dashboard users and PG-backed sessions.

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username      text NOT NULL UNIQUE,
    -- PHC-encoded argon2id ($argon2id$v=19$m=...,t=...,p=...$salt$hash).
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('admin', 'viewer')),
    disabled      boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- sha256 of the random bearer token; the cleartext exists only in the
    -- browser cookie (same pattern as join_tokens.secret_hash — a DB dump
    -- never yields usable sessions).
    token_hash   bytea NOT NULL UNIQUE,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Per-session CSRF token, returned to the SPA by login and /auth/me.
    csrf_token   text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Absolute expiry; no sliding idle window (MVP).
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);
