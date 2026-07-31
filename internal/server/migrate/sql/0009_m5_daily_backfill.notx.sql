-- M5: one-time full materialization of the daily cagg from the (just
-- backfilled) hourly one. Same rationale, rules, and 400 d bound as 0008;
-- must run after it and before the policies in 0010.

CALL refresh_continuous_aggregate('probe_results_daily', now() - interval '400 days', NULL);
