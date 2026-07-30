# Lighthouse — project instructions

Central control plane + per-site Go agents measuring inter-site connectivity
(latency, loss, jitter, TCP/TLS timings, traceroute) with directional history.
Full design + milestone plan: `docs/architecture.md`.

## Hard constraints (user-mandated)

- **Zero network access at build time.** `vendor/` is committed (`-mod=vendor`
  everywhere), generated protobuf code is committed (`internal/pb/`), the
  built SPA will be committed (`web/dist/`). New Go deps: `make vendor` in the
  same change. Never add a build step that fetches anything.
- **Control plane is containers-only** (proxy + server + TimescaleDB via
  compose). The nginx proxy does SNI passthrough on 443 — it never terminates
  TLS; agent mTLS is verified end-to-end in the Go server.
- **The agent is a single static Go binary** (`CGO_ENABLED=0`), no runtime
  deps; systemd + RPM on RHEL.
- **Fail loud.** Unknown YAML keys are fatal (`internal/strictyaml`),
  preflight names every problem, spool overflow is reported to the server,
  unsupported probe types report `UNSUPPORTED` instead of being skipped.

## Architecture invariants

- Agent identity = mTLS client cert URI SAN `lighthouse://agent/<uuid>`,
  never message fields. Direction (site A→B vs B→A) derives from cert + the
  server-side target row; it is unforgeable by construction.
- The DB is the sole certificate revocation authority (checked per-RPC +
  30 s stream sweep). No CRL/OCSP.
- Enrollment trust is explicit: `--ca-cert` or `--fingerprint` — no TOFU.
- The built-in CA signs agent client certs AND the auto-issued gRPC server
  cert (`listen.grpc_hostname` SAN). Operator TLS (`tls.*`) covers only the
  dashboard listener.
- Server streams FULL config snapshots keyed by `config_hash`; agents diff
  locally. All wire timings are int64 microseconds, -1 = not measured.
- Proto compatibility: fields are only added, never renumbered/repurposed
  (old agents in other languages must keep working).

## Workflows

- Build/test (offline): `make build`, `make test`. Dev stack: `make up` /
  `make down` — ALWAYS composes base + dev overlay together; never
  `docker compose up` the base file alone (silently drops overlay services).
- Regenerate protos: `make proto` (buf + protoc-gen-go{,-grpc} in ~/go/bin),
  commit the diff under `internal/pb/`.
- Dev host ports: proxy publishes on **9443** (443 is usually taken on dev
  boxes); inside the network agents use `proxy:443` as in production.
- Migrations: `internal/server/migrate/sql/NNNN_*.sql`, applied in filename
  order, one transaction each. Dev server auto-migrates; prod runs
  `lighthouse-server migrate` explicitly. **Once a migration has shipped in
  any release, it is immutable** — schema changes get a new numbered file
  (the `Pending` preflight tracks filenames, not content, so editing an
  applied file silently skips the change on upgrades). Pre-first-release,
  editing `0001_init.sql` is fine; recreate dev DBs with `down -v`.
- Conventional Commits (`feat(scope): ...`); see CONTRIBUTING.md.

## Status (as of 2026-07-30)

- M0 (scaffolding, strict config, compose stack) — done.
- M1 (protos, CA, enrollment, mTLS session, revocation) — done; verified
  e2e in compose: enroll → connect through SNI proxy → last_seen updates →
  revocation drops live stream ≤30 s.
- M2 (scheduler, TCP/TLS/HTTP probers, spool, `probe_results` hypertable,
  PushResults ingest, `meshexpand` config distribution, probe-config CLI)
  — done; gate verified in compose: TCP rows land with ~1 ms
  `tcp_connect_us`; 5-minute server outage replays from spool with no gap
  (max per-probe gap == its interval); tiny `spool.max_bytes` drops oldest
  segments and the loss surfaces as `dropped_since_last_push`; revocation
  still drops live streams ≤30 s.
- M3 (dashboard MVP: httpapi auth/sites/agents/matrix/pair series from raw,
  React/TS SPA with uPlot, `web/embed.go`, `user add`) — done; gate verified
  in compose through the SNI proxy: login → live matrix (6 ordered pairs,
  mesh cells CONN_REFUSED/down as expected pre-M4), pair series bucketed and
  advancing, CSRF/logout/rate-limit behave, dev `lon→dashboard` HTTP probe
  still OK against the SPA. Browser-visual pass happens on a machine with a
  browser (`https://localhost:9443`, self-signed cert).
- M4 (ICMP/DNS/traceroute probers with loss + RFC 3550 jitter, `outage`
  hysteresis + agent-silence sweep, `pathwatch`, migration 0004,
  outages/path-events/traceroute API + SPA views, selfcheck, systemd unit
  with CAP_NET_RAW, CLI probe types + train flags) — done; gate verified
  in compose: all mesh cells flip to real RTT (`latency_source: rtt`,
  ~100 µs trains with jitter); `iptables -p icmp -j DROP` in agent-syd →
  exactly one `probe_failing` per genuinely failing series after 3
  failures (a blanket ICMP drop also kills the reverse direction's echo
  replies — narrow to `--icmp-type echo-request` to fail only one
  direction), closed after unblock + 3 successes with `opened_at`/
  `closed_at` bracketing the block window truthfully; re-IP of agent-nyc
  (`docker network disconnect` + `connect --ip --alias agent-nyc`) →
  `path_events` rows from both peers within one 2 m traceroute cadence,
  hop diff rendered in the Paths view; `docker stop agent-lon` → one
  `agent_offline` ≤2.5 m, closed 9 s after restart; zero duplicate open
  events; offline `make build`/`make test` green.
- Next: M5 — Toolkit percentiles, continuous aggregates, retention. See
  the milestone table in `docs/architecture.md`.

### M2 notes worth knowing

- Probe assignment lives in `probe_configs`: either direct
  (`site_id`+`target_id`, run by every agent at that site) or a mesh
  template (`mesh_id`, expanded over ordered site pairs). Mesh probe IDs
  are `UUIDv5(mesh, "src|dst|type")` — stable across rebuilds and stored
  in `probe_results`, so that derivation must never change.
- Config changes propagate by DB polling on `StreamConfig`'s existing 30 s
  tick (the admin CLI is a separate process), so edits converge in ≤30 s.
- Ingest is strict: a result whose target is not currently assigned to the
  sending agent is rejected and logged, keeping direction unforgeable.
  Spooled results for a probe deleted mid-outage are lost by design.
- Spool replay is at-least-once (in-segment read offset is memory-only);
  the unique `(agent_id, probe_id, time)` index dedupes on insert.
- `ConfigSnapshot.spool` (server-sent SpoolPolicy) is deliberately not
  applied yet: agent config cannot distinguish unset from explicit, so
  precedence is undecidable. Revisit when config gets pointer fields.

### M3 notes worth knowing

- Until M4's ICMP prober lands, no probe measures true RTT: dashboard
  latency is `COALESCE(rtt_avg_us, tcp_connect_us, tls_handshake_us,
  ttfb_us, total_us)` per row (`internal/server/store/dashboard.go`), and
  every API response carries `latency_source` so the UI labels the axis
  honestly. Keep that order if columns are added.
- Auth model: argon2id (PHC-encoded, params parsed from the stored hash —
  cost changes never invalidate users), `lighthouse_session` cookie
  (HttpOnly/Secure/SameSite=Strict, 7-day absolute expiry), sessions store
  only the sha256 of the token, CSRF = per-session token required in
  `X-CSRF-Token` on non-GET. Login burns a dummy hash for unknown users
  (timing) and returns byte-identical 401s; per-IP fixed-window rate limit
  (RemoteAddr is real — the proxy is TCP passthrough, never trust XFF).
- Matrix = latest result per (agent, agent-target, probe type) within a
  10-min horizon folded per ordered site pair; configured-but-silent pairs
  render `stale`; external targets are excluded (no destination site).
  Window→bucket map lives in `httpapi/windows.go` (24h window is a
  dev-facing extra beyond the spec'd 7/30/90/365d; same code path).
- httpapi's `DB` is an interface; handler tests run offline against
  `fakeDB` (`httpapi_test.go`) — keep new endpoints testable that way. SQL
  correctness is compose-gate territory.
- UI changes: edit `web/src/`, then `make web` and commit the regenerated
  `web/dist/` in the SAME commit (go:embed all:dist — a missing/stale dist
  breaks or lies). Dev loop: `make up` + `cd web && npm run dev` (Vite
  proxies /api to https://localhost:9443). Dev dashboard login:
  `admin`/`lighthouse-dev` (seeded by bootstrap).
### M4 notes worth knowing

- A series is `(agent_id, probe_id)`. Hysteresis (open after 3 consecutive
  failures, close after 3 successes) folds ONLY rows the insert genuinely
  added — `InsertResultsTx` uses `ON CONFLICT DO NOTHING ... RETURNING` so
  spool re-pushes can never double-count — and `series_state.last_time`
  ignores out-of-order stragglers. `opened_at`/`closed_at` are the START of
  the streak, not the threshold crossing. UNSUPPORTED counts as failure by
  design. "Exactly one open event" is enforced by partial unique indexes,
  not application logic.
- `agent_offline` needs prior contact (`last_seen_at IS NOT NULL`) and both
  signals silent: no result in 3× the agent's fastest applicable interval
  AND `last_seen_at` older than 2 m. It coexists with `probe_failing`
  (silence never advances series counters). `series_state` doubles as the
  activity ledger so the sweep never scans the hypertable.
- Traceroute rows carry run-level accounting only (`sent=1`,
  `received=dest_reached`, ALL timings NULL) so pair loss/latency
  aggregates stay unpoisoned; hops live in `traceroute_current`/
  `path_events` only. Only complete (dest-reached) runs with a valid
  32-byte agent-computed `path_hash` update paths — the server trusts the
  hash per the proto contract. Traceroute strictly requires a raw ICMP
  socket (CAP_NET_RAW); the echo prober works with datagram
  (`ping_group_range`) OR raw, tried in that order each run.
- The ICMP prober keeps per-series RFC 3550 jitter state in memory keyed by
  probe_id (registry shares one instance); jitter is -1 until two
  consecutive RTTs have ever been seen. DNS uses `codeberg.org/miekg/dns`
  (v2, pre-1.0, pinned by vendoring) — its default transport has fixed 2 s
  timeouts, so the prober derives transport timeouts from the run deadline.
- Dev agents get `NET_ADMIN` + iptables/iproute2 (overlay/dev image only)
  for gate injections. Path-change injection: move the destination's IP
  with `docker network disconnect` + `connect --ip <new> --alias
  agent-<site> --alias lighthouse-agent-<site>-1` — probers resolve the
  alias fresh each run. netem can't change hops on the flat bridge.
- `probe add` validates `train_count × train_spacing < timeout` because the
  agent budgets the whole train inside the per-run timeout.
- The SPA fallback serves index.html for unknown non-/api GETs only;
  unmatched `/api/*` must stay JSON 404 and `/healthz` unauthenticated
  (tests enforce both).
