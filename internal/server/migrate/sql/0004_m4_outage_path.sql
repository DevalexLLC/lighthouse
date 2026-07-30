-- M4: outage detection (per-series hysteresis + agent silence) and
-- traceroute path watching.
--
-- A series is (agent_id, probe_id): the probe_id already pins target and
-- type (mesh probe IDs are deterministic UUIDv5), matching the dedupe index
-- on probe_results. target_id/probe_type are denormalized copies for
-- display. Like probe_results, these tables sit on the ingest hot path and
-- carry no FKs to agents/targets — mesh probe_ids have no backing row
-- anywhere. The single FK (series_state → outage_events) is load-bearing
-- for "exactly one open event per series".

CREATE TABLE outage_events (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind       text NOT NULL CHECK (kind IN ('probe_failing', 'agent_offline')),
    agent_id   uuid NOT NULL,
    probe_id   uuid,               -- NULL for agent_offline
    target_id  uuid,               -- NULL for agent_offline
    probe_type smallint,           -- NULL for agent_offline
    -- opened_at is the start of the failure streak (first failure / last
    -- evidence of life), not the moment the threshold was crossed.
    opened_at  timestamptz NOT NULL,
    closed_at  timestamptz,        -- NULL = still open
    -- error text of a failure in the opening streak, for display.
    open_error text
);

CREATE INDEX outage_events_recent_idx ON outage_events (opened_at DESC);
CREATE INDEX outage_events_open_idx ON outage_events (agent_id) WHERE closed_at IS NULL;
-- Belt and braces for "exactly one open event": per failing series, and per
-- offline agent. Racing openers resolve here, not in application logic.
CREATE UNIQUE INDEX outage_events_probe_open_uidx ON outage_events (agent_id, probe_id)
    WHERE kind = 'probe_failing' AND closed_at IS NULL;
CREATE UNIQUE INDEX outage_events_offline_open_uidx ON outage_events (agent_id)
    WHERE kind = 'agent_offline' AND closed_at IS NULL;

-- Restart-durable hysteresis counters, one row per series, updated inside
-- the ingest transaction. last_time orders spool replays: results at or
-- before it are ignored, so re-pushed batches can never double-count.
CREATE TABLE series_state (
    agent_id      uuid NOT NULL,
    probe_id      uuid NOT NULL,
    target_id     uuid NOT NULL,
    probe_type    smallint NOT NULL,
    consec_fails  int NOT NULL DEFAULT 0,
    consec_oks    int NOT NULL DEFAULT 0,
    first_fail_at timestamptz,     -- start of the current failure streak
    first_ok_at   timestamptz,     -- start of the current success streak
    last_status   smallint NOT NULL,
    last_time     timestamptz NOT NULL,
    open_event_id uuid REFERENCES outage_events(id),
    PRIMARY KEY (agent_id, probe_id)
);

-- Latest complete traceroute path per series. Hops mirror the wire Hop
-- message: [{"ttl": 1, "addrs": ["10.0.0.1"], "rtt_us": [311, 290]}, ...].
-- Traceroute hops live here and in path_events, never in the hypertable.
CREATE TABLE traceroute_current (
    agent_id     uuid NOT NULL,
    probe_id     uuid NOT NULL,
    target_id    uuid NOT NULL,
    updated_at   timestamptz NOT NULL,
    dest_reached boolean NOT NULL,
    path_hash    bytea NOT NULL,
    hops         jsonb NOT NULL,
    PRIMARY KEY (agent_id, probe_id)
);

CREATE TABLE path_events (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    time          timestamptz NOT NULL,
    agent_id      uuid NOT NULL,
    probe_id      uuid NOT NULL,
    target_id     uuid NOT NULL,
    old_path_hash bytea NOT NULL,
    new_path_hash bytea NOT NULL,
    old_hops      jsonb NOT NULL,
    new_hops      jsonb NOT NULL
);

CREATE INDEX path_events_time_idx ON path_events (time DESC);
