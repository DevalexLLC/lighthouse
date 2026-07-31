-- M5: one-time full materialization of the hourly cagg, BEFORE any policy
-- exists (0010). On an upgrade with pre-existing probe history the refresh
-- policy only covers its start_offset window (8 d); without this backfill,
-- anything older would fall below the materialization watermark on the
-- policy's first run — invisible to aggregate queries, since real-time
-- aggregation only unions data above the watermark — and raw retention
-- (14 d) would then delete it before it could ever be rolled up.
--
-- The window starts at the longest retained horizon (400 d = daily
-- retention, NOT hourly's 100 d — this materialization is also the source
-- the daily backfill in 0009 reads) so upgrade cost is capped instead of
-- proportional to arbitrarily old raw that 0010's policies would discard
-- anyway.
--
-- notx file: exactly ONE statement (the CALL manages its own transactions
-- and is refused inside a transaction block); naturally idempotent (a
-- re-run recomputes the same buckets). No-op on a fresh install.

CALL refresh_continuous_aggregate('probe_results_hourly', now() - interval '400 days', NULL);
