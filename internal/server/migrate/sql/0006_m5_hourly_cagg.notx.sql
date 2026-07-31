-- M5: hourly continuous aggregate over probe_results.
--
-- notx file: exactly ONE statement, idempotent (see migrate.go package doc).
--
-- Group keys (bucket, agent_id, target_id, probe_type) serve the existing
-- SiteEndpoints agent_id/target_id = ANY(...) filters with no joins, same
-- as the raw queries.
--
-- Every measure is a sum/count/min/max — never avg() — so the daily cagg
-- in 0007 rolls up exactly (avg of avgs would be wrong once bucket sample
-- counts differ). Averages are computed at query time as sum/count.
--
-- The COALESCE latency ladder mirrors latencyExpr in
-- internal/server/store/dashboard.go and must change in lockstep with it;
-- this copy is frozen once the migration ships (a ladder change later needs
-- a new cagg version). Traceroute rows carry NULL timings, so they fall out
-- of every latency aggregate but still count into samples/sent/received —
-- identical to the raw-query semantics.
--
-- materialized_only = false: queries union the not-yet-refreshed tail live
-- from raw, so charts are correct before the first policy refresh.

CREATE MATERIALIZED VIEW IF NOT EXISTS probe_results_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    time_bucket('1 hour', time) AS bucket,
    agent_id,
    target_id,
    probe_type,
    count(*)                            AS samples,
    count(*) FILTER (WHERE status = 1)  AS ok_samples,
    sum(sent)                           AS sent,
    sum(received)                       AS received,
    max(time) FILTER (WHERE status = 1) AS last_ok_at,
    min(COALESCE(rtt_avg_us, tcp_connect_us, tls_handshake_us, ttfb_us, total_us))         AS lat_min_us,
    max(COALESCE(rtt_avg_us, tcp_connect_us, tls_handshake_us, ttfb_us, total_us))         AS lat_max_us,
    sum(COALESCE(rtt_avg_us, tcp_connect_us, tls_handshake_us, ttfb_us, total_us)::bigint) AS lat_sum_us,
    count(COALESCE(rtt_avg_us, tcp_connect_us, tls_handshake_us, ttfb_us, total_us))       AS lat_count,
    percentile_agg(COALESCE(rtt_avg_us, tcp_connect_us, tls_handshake_us, ttfb_us, total_us)::double precision) AS lat_pctl,
    sum(jitter_us::bigint)              AS jitter_sum_us,
    count(jitter_us)                    AS jitter_count,
    max(jitter_us)                      AS jitter_max_us,
    sum(tcp_connect_us::bigint)         AS tcp_sum_us,
    count(tcp_connect_us)               AS tcp_count,
    sum(tls_handshake_us::bigint)       AS tls_sum_us,
    count(tls_handshake_us)             AS tls_count
FROM probe_results
GROUP BY bucket, agent_id, target_id, probe_type
WITH NO DATA;
