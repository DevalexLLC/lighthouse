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

-- Shared dashboard settings: exactly one row (id is an always-true bool PK).
-- Thresholds drive map dot/line severity; edited from the SPA by admins.
-- The CHECKs mirror httpapi validation — hitting one from the API means a
-- handler bug, and it should be loud.
CREATE TABLE dashboard_settings (
    id              boolean PRIMARY KEY DEFAULT true CHECK (id),
    latency_warn_us bigint NOT NULL DEFAULT 100000,
    latency_crit_us bigint NOT NULL DEFAULT 250000,
    loss_warn_pct   double precision NOT NULL DEFAULT 1,
    loss_crit_pct   double precision NOT NULL DEFAULT 5,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      text NOT NULL DEFAULT '',
    CHECK (latency_warn_us > 0 AND latency_crit_us > latency_warn_us),
    CHECK (loss_warn_pct >= 0 AND loss_crit_pct > loss_warn_pct AND loss_crit_pct <= 100)
);
-- Seeded here so GET never needs a missing-row branch and UPDATE always hits.
INSERT INTO dashboard_settings DEFAULT VALUES;
