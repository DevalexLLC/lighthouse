-- M2: probe configuration and the probe_results hypertable.

-- Everything probeable, mesh peers included. kind='agent' rows are created
-- automatically at enrollment; kind='external' rows come from the admin CLI.
CREATE TABLE targets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind       text NOT NULL CHECK (kind IN ('agent', 'external')),
    -- Admin CLI handle for external targets; synthesized for agent targets.
    name       text NOT NULL UNIQUE,
    agent_id   uuid REFERENCES agents(id),
    address    text NOT NULL DEFAULT '',
    port       int  NOT NULL DEFAULT 0,
    -- For HTTP probes: full URL.
    url        text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'agent') = (agent_id IS NOT NULL))
);
CREATE UNIQUE INDEX targets_agent_uidx ON targets (agent_id) WHERE agent_id IS NOT NULL;

-- Backfill one agent-kind target per already-enrolled agent (new enrollments
-- insert theirs inside the EnrollAgent transaction).
INSERT INTO targets (kind, name, agent_id)
SELECT 'agent', 'agent:' || a.id, a.id FROM agents a;

CREATE TABLE mesh_groups (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mesh_members (
    mesh_id uuid NOT NULL REFERENCES mesh_groups(id) ON DELETE CASCADE,
    site_id uuid NOT NULL REFERENCES sites(id),
    PRIMARY KEY (mesh_id, site_id)
);

-- A row is EITHER a direct probe (site_id + target_id: every agent at the
-- site runs it) OR a mesh template (mesh_id: meshexpand expands it over
-- ordered site pairs into per-agent specs).
CREATE TABLE probe_configs (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id          uuid REFERENCES sites(id),
    target_id        uuid REFERENCES targets(id),
    mesh_id          uuid REFERENCES mesh_groups(id) ON DELETE CASCADE,
    -- lighthouse.v1.ProbeType numeric value.
    probe_type       smallint NOT NULL CHECK (probe_type > 0),
    interval_ms      int NOT NULL CHECK (interval_ms > 0),
    timeout_ms       int NOT NULL CHECK (timeout_ms > 0),
    train_count      int NOT NULL DEFAULT 0,
    train_spacing_ms int NOT NULL DEFAULT 0,
    params           jsonb NOT NULL DEFAULT '{}',
    enabled          boolean NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK ((mesh_id IS NOT NULL AND site_id IS NULL AND target_id IS NULL)
        OR (mesh_id IS NULL AND site_id IS NOT NULL AND target_id IS NOT NULL))
);

-- Raw measurement hypertable. agent_id always comes from the mTLS identity
-- server-side, never from message fields. Timing columns are int
-- microseconds (NULL = not measured, wire -1); int caps at ~35 minutes,
-- far beyond any probe timeout. No FKs: ingest hot path.
CREATE TABLE probe_results (
    time       timestamptz NOT NULL,
    agent_id   uuid NOT NULL,
    target_id  uuid NOT NULL,
    probe_id   uuid NOT NULL,
    -- lighthouse.v1.ProbeType / ProbeStatus numeric values.
    probe_type smallint NOT NULL,
    status     smallint NOT NULL,
    sent       int NOT NULL DEFAULT 0,
    received   int NOT NULL DEFAULT 0,
    loss_pct   real,
    rtt_min_us    int,
    rtt_avg_us    int,
    rtt_max_us    int,
    rtt_stddev_us int,
    jitter_us     int,
    dns_us           int,
    tcp_connect_us   int,
    tls_handshake_us int,
    ttfb_us          int,
    total_us         int,
    -- Truncated human-readable failure reason; NULL on OK.
    error      text
);

SELECT create_hypertable('probe_results', 'time', chunk_time_interval => interval '1 day');

CREATE INDEX probe_results_series_idx
    ON probe_results (agent_id, target_id, probe_type, time DESC);

-- Spool replay is at-least-once; duplicates are dropped on insert
-- (ON CONFLICT DO NOTHING against this index).
CREATE UNIQUE INDEX probe_results_dedupe_uidx
    ON probe_results (agent_id, probe_id, time);
