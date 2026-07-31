-- M1: identity and enrollment tables.

CREATE TABLE sites (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL UNIQUE,
    display_name text NOT NULL DEFAULT '',
    location     text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE agents (
    id                  uuid PRIMARY KEY,
    site_id             uuid NOT NULL REFERENCES sites(id),
    hostname            text NOT NULL,
    -- Address peers should use to probe this agent (NAT-safe; defaults to
    -- the source address observed at enrollment).
    probe_address       text NOT NULL DEFAULT '',
    version             text NOT NULL DEFAULT '',
    last_seen_at        timestamptz,
    current_config_hash text NOT NULL DEFAULT '',
    -- Running total of spooled results the agent reported losing to spool
    -- bounds enforcement (dropped_since_last_push deltas; the agent clears
    -- the counter only after an acknowledged push, so retried pushes may
    -- overcount — this is an operator alarm signal, not accounting).
    dropped_results     bigint NOT NULL DEFAULT 0,
    last_dropped_at     timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agents_site_idx ON agents (site_id);

CREATE TABLE join_tokens (
    id            uuid PRIMARY KEY,
    -- sha256 of the secret half of "<id>.<secret>"; the cleartext is
    -- printed exactly once at creation.
    secret_hash   bytea NOT NULL,
    site_id       uuid NOT NULL REFERENCES sites(id),
    created_by    text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    used_at       timestamptz,
    used_by_agent uuid REFERENCES agents(id),
    -- sha256 of the CSR that consumed this token. A retry presenting the
    -- SAME token and SAME CSR is an idempotent replay (the enroll response
    -- was lost in transit), not a token reuse.
    used_csr_hash bytea
);

CREATE TABLE certificates (
    serial     numeric PRIMARY KEY,
    agent_id   uuid NOT NULL REFERENCES agents(id),
    not_before timestamptz NOT NULL,
    not_after  timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);
CREATE INDEX certificates_agent_idx ON certificates (agent_id);
