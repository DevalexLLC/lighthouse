-- M5: TimescaleDB Toolkit is a hard dependency — percentile_agg (UddSketch)
-- backs the p50/p95/p99 columns in the continuous aggregates. It ships in
-- the timescale/timescaledb-ha image; CREATE EXTENSION needs superuser
-- (compose's POSTGRES_USER is superuser in that image). serve preflight
-- re-checks the extension so a hand-built DB fails loud, not at query time.

CREATE EXTENSION IF NOT EXISTS timescaledb_toolkit;
