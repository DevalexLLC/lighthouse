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
  `make down`, and `make reset` for teardown INCLUDING volumes (fresh
  DB/CA/tokens — what older notes call `down -v`; plain `make down -v`
  does NOT work, make eats `-v`). All three default
  `LIGHTHOUSE_DB_PASSWORD`. ALWAYS composes base + dev overlay together;
  never `docker compose up` the base file alone (silently drops overlay
  services).
- Regenerate protos: `make proto` (buf + protoc-gen-go{,-grpc} in ~/go/bin),
  commit the diff under `internal/pb/`.
- SPA style: `make web` now runs `npm run lint && npm run fmt:check` BEFORE
  building, so a finding or an unformatted file blocks the dist rebuild;
  `make web-fix` = `oxlint --fix` + `oxfmt`. `make lint` is untouched
  (Go-only, offline — CONTRIBUTING.md promises that).
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

## Status (as of 2026-08-03)

- `docs/install.md` is the zero-to-working-system operator guide: online image
  pulls and offline bundles, DNS/dashboard TLS/SNI proxy setup, explicit
  production migration and CA initialization, RPM and container agents,
  mandatory proxied-enrollment `--probe-address`, a baseline two-site mesh,
  direct-target examples, end-to-end verification, firewall rules,
  troubleshooting, lifecycle, upgrades, and backup scope. It uses the actual
  dashboard-user CLI (`user add --admin`) and makes clear that probe workloads
  are configured centrally rather than in agent YAML. `README.md` links it as
  the production installation entry point.

- M6 (packaging, rotation, air-gap hardening + ghcr publishing) — done.
  Agent cert renewal: the renewer (`internal/agent/uplink/renew.go`) fires
  at 2/3 of the LEAF's validity (no agent config knob — it can't disagree
  with what was issued), reuses the existing private key (commit = one
  atomic rename of agent.crt; response parsed + key-matched before commit),
  then recycles the gRPC conn so the new cert is presented immediately
  (ClientTLS also moved to GetClientCertificate reading disk per
  handshake). Failure retry = min(24h, max(30s, validity/20)). Server:
  `ca.agent_cert_lifetime`/`ca.server_cert_lifetime` config keys (defaults
  unchanged 30d/90d; <24h agent lifetime logs a TEST MODE banner; e2e
  switch is a commented 10m line in server.dev.yaml — enable + `make
  reset`), and the gRPC listener cert now rotates in-process via a
  GetCertificate provider + periodic needsReissue check (startup-only
  reissue would serve an expired cert once uptime exceeded lifetime).
  selfcheck gained identity (expired=fatal→re-enroll, final-third=warn
  renewal failing) and key-mode (non-0600=fatal) checks; run manual
  selfchecks AS THE SERVICE USER — it creates spool/ and a root-owned one
  bricks first start. Packaging: hardened unit (ProtectSystem=strict etc.,
  AF_NETLINK kept — Go runtime uses rtnetlink), classic rpmbuild spec
  `packaging/rpm/` packaging a PRE-BUILT binary (no compile inside
  rpmbuild; `make rpm`, container recipe in Makefile; verified in
  rockylinux:9 incl. unprivileged-ICMP via ping_group_range). Images:
  server/agent/proxy Dockerfiles are multi-arch (cross-compile via
  $BUILDPLATFORM, no QEMU for Go), non-root uid 10001, VERSION/COMMIT
  build-args stamp internal/version, OCI labels; the agent Dockerfile's
  DEFAULT target is `dev` (stage-order necessity) — always name the
  target (release for prod); the proxy image bakes
  deploy/proxy/nginx.conf.template (envsubst of LIGHTHOUSE_GRPC_SNI only —
  nginx runtime $vars survive). Compose base now points at
  ghcr.io/devalexllc/* images (env.example documents
  DB_PASSWORD/VERSION/GRPC_SNI); dev overlay builds :dev tags incl. the
  proxy, bootstrap runs as user 0 (tokens volume is root-owned), certgen
  chowns the dev TLS key to 10001 — pre-existing dev stacks need one
  `make reset` for volume ownership. `.dockerignore` exists and must
  NEVER exclude vendor//internal/pb//web/dist. Release: `make images` /
  `make rpm` / `make bundle` (deploy/release/build-bundle.sh, one
  docker-load tar of all four images + compose + docs + RPMs +
  SHA256SUMS, refuses overwrite); CI `.github/workflows/ci.yml` enforces
  the offline build (GOPROXY=off GOTOOLCHAIN=local + git diff
  --exit-code; NO proto-drift gate — buf is not vendored/pinned) and
  builds all three images on PRs; `release.yml` on v* tags pushes
  multi-arch images to ghcr (tag+latest), builds per-arch RPMs, and
  attaches per-arch bundles to a GitHub release. Revocation still has no
  CLI (DB update; documented in docs/install.md). New docs:
  docs/install.md (offline install/upgrade/cert lifecycle),
  docs/airgap-build.md (what enforces zero-network + version flow).

- Probe-workload management moved into the web UI (2026-08-03): Settings is
  now a tabbed page (Thresholds / Targets / Meshes / Probes, hash
  `#/settings/<tab>`, plain `#/settings` = thresholds; still reached only
  via the admin-only user-menu entry, soft-gated — viewers see read-only).
  New `/api/v1/config/*` surface: `probe-types` (the param registry),
  `targets`, `meshes` (+`/members/{site}`), `probes` — reads any-session,
  writes `requireRole("admin")` + CSRF, wire cadence in integer ms.
  Validation lives ONCE in `internal/server/probeadmin` (type names,
  cadence/train rules with per-surface `FieldNames`, and the per-type param
  registry mirroring exactly what the agent probers read — tcp/tls mesh
  `port`, `tls.*`, `http.*`, `dns.*`); both the CLI and httpapi call it, so
  the CLI now REJECTS unknown `--param` keys, requires `dns.qname`, and
  requires mesh tcp/tls `port` (fail-loud; bootstrap seeds comply). The
  UUIDv5 mesh probe-ID derivation moved to leaf pkg
  `internal/server/probeid` (meshexpand imports store, so store couldn't
  reach it) — derivation unchanged, pinned by meshexpand tests. Probe PUT
  edits cadence/params/enabled IN PLACE (identity — type/site/target/mesh —
  is 400; probe IDs live in probe_results, so re-target = delete+create).
  Deleting/disabling a probe (or mesh/member) cleans up at mutation time:
  open `probe_failing` events get `closed_at = now()` (ingest's 3-OK close
  can never fire for a probe that stopped existing — they'd stay open
  forever), delete also drops `series_state`/`traceroute_current` rows,
  disable only resets hysteresis counters and keeps `last_time`. Mesh
  delete cascades templates (FK) and reports `probes_deleted`; in-use
  target deletes are 409 `InUseError` naming the count (store has typed
  `ErrNotFound`/`ErrConflict`/`InUseError` — httpapi maps without string
  matching). `probe_configs` gained `updated_at`/`updated_by` (CLI writes
  "cli", UI the session username) by editing 0002 in place (pre-release
  convention — dev DBs need `make reset`). CLI additions: `mesh delete`.
  SPA: `apiDelete` in api.ts, config types in types.ts, panels
  `TargetsPanel`/`MeshesPanel`/`ProbesPanel` + shared `ConfirmButton`
  (inline two-step confirm carrying blast radius; the codebase stays
  modal-free), probe param fields render FROM the registry endpoint
  (bool→checkbox, enum→select) so form and server can't drift, forms follow
  the ThresholdSettings draft-null/dirty/save pattern, `button.primary` is
  now a general style (was `.threshold-foot`-scoped). Codex-review
  follow-ups: ingest ownership is now a (probe, target) PAIR check —
  grpcapi's `assignmentCache` builds each agent's probe→target map from
  the same `meshexpand.BuildSnapshot` the agent receives (30 s TTL, one
  load per agent per batch; `store.TargetAssignedToAgent` is gone) — a
  target-only check let spooled results for a deleted/disabled probe slip
  through whenever its target stayed assigned via another config,
  recreating retired `series_state` and reopening an incident nothing
  would ever close. Two guaranteed-broken configs are now rejected at
  write time (shared `probeadmin`, so CLI + API agree): http mesh
  templates (the prober needs `Target.Url`; mesh expansion carries only
  address/port — the registry marks http `direct_only`, the UI hides it
  in mesh mode) and direct probes against agent-kind targets (their rows
  carry no address; `store.ErrInvalid` → 400, UI filters the picker to
  external).

- Template-theme refactor (2026-08-03): the whole dashboard was restyled to
  a shadcn-flavored admin look (zinc neutrals, hairline borders, indigo
  `--accent`, 8/12 px radii, `--shadow-card` on light only) via retokenized
  `styles.css` — series/status colors untouched. Theme system: the resolved
  scheme lives in `data-theme` on `<html>`, stamped pre-paint by
  `web/public/theme-init.js` (an EXTERNAL classic script — the CSP has no
  inline allowance) and owned afterwards by `web/src/theme.ts`
  (localStorage `lighthouse-theme` stores only explicit light/dark; absence
  = system, which tracks OS changes live). A topbar icon button cycles
  light→dark→system; PairDetail rebuilds uPlot options on toggle. The
  Connectivity page is retired: `#/connectivity` and `#/sightlines` alias
  to `#/`, nav is Overview/Incidents/Routes/Agents (a `NAV` array in
  App.tsx), and the map/matrix switch moved into the Overview's
  `ConnectivityCard` (segmented control in the card header; the "Healthy
  directions" tile flips it to matrix). The matrix table extracted verbatim
  to `components/MatrixTable.tsx` (`#/pair` links intact). Overview's main
  row is `ConnectivityCard` (span 7) + `FleetAgentsCard` (span 5,
  equal-height via stretch + `flex:1 1 0; min-height:0` scroll region);
  tiles are compact with a semantic
  `.stat-badge`. Fleet card: per-agent last update, 48×30-min hand-rolled
  SVG success strip, and 24 h uptime % from `GET /api/v1/agents/health`
  (additive endpoint; traceroute excluded from ratios, `status <> 1` =
  failure, sparse buckets, absent agents render "—" never 100 %; full
  confidence requires every COMPLETED slot covered — partial coverage
  renders muted with the measured span spelled out). The three original
  lower list cards (Active incidents / Agent attention / Recent route
  changes) were subsequently dropped — Overview is tiles + the main row
  only, and no longer polls /path-events. CERT_WARN_DAYS is 7 in BOTH
  Overview.tsx and Agents.tsx: agents renew at 2/3 of the 30-day cert
  lifetime (10 days left), so a 30-day warn window flagged every healthy
  agent from issuance; 7 days fires only when renewal has been failing
  for 3+ days. The dev seed no longer includes the deliberate failing
  port-9 TCP mesh probe (bootstrap removes it from pre-existing dev DBs
  on rerun, matching its exact printed shape) — a fresh dev stack
  converges to an all-healthy board; inject failures per the M4 gate
  recipes when a broken board is wanted. The map
  is now a dot-matrix landmass (`MAP_DOTS` baked by
  `web/tools/build-map-geo.mjs` point-in-polygon sampling; projection
  constants + `geo.ts` lockstep unchanged) with severity-tinted translucent
  bubbles sized by link count — healthy wears the accent (map operations
  palette, as carrier cyan did before), degraded/down/stale take the status
  ramp — and a template-style hover/pin info card (code, location, status
  pill, best live latency, direction breakdown bar). Carriers, pulses, seam
  markers, graticule, and map labels are deleted (with
  `greatCircleGeometry` and the dead `worldPath.ts`); the unplaced-sites
  chip strip and severity vocabulary are unchanged.

- Dashboard information architecture and visual system were unified for
  public use. `#/` is now the operator Overview (availability, healthy
  directions, correlated active-incident count, fleet attention, topology,
  and recent route changes); the map/matrix switch now lives in the
  Overview's Connectivity card (see above);
  primary navigation is Overview, Incidents, Routes, and
  Agents; the user menu holds identity, role, logout, and admin-only Settings.
  The legacy `#/outages`, `#/paths`, `#/sightlines`, and now
  `#/connectivity` hashes remain aliases so bookmarks keep working. The
  standard shell now has task-oriented labels, a keyboard skip control,
  consistent page/toolbar/loading/error/empty patterns,
  responsive layouts, and AA-safe semantic text colors. Incidents correlate
  rows by active/resolved state, kind, probe, and normalized error, with
  active/all/resolved filters, search, impact counts, and expandable target
  detail. Routes and Agents gained task-specific search and health filters;
  thresholds moved from the map into Settings; Pair Detail and Login now use
  the shared hierarchy. All API contracts and polling behavior are unchanged.

- Screenshot-led polish corrected semantic and density problems across the
  shell: Overview summary accents now reflect each value instead of defaulting
  to green; Settings initializes server values and disables Save until dirty;
  matrix cells label best working latency separately from worst-probe loss;
  matrix cells, header chips, and the matrix legend grade through the same
  directionSeverity fold as the map and Overview (warn/crit render the
  shared Degraded treatment), so a threshold-violating direction can never
  read Healthy in one connectivity mode and Degraded in the other;
  route rows and incident targets disclose 25/10 items at a time; toolbars,
  supporting type, Login proportions, and responsive form layouts are aligned.
  The user menu is the sole entry point for admin Settings. Overview fleet
  attention names the most severe trigger (cert, offline, never connected,
  failing probes, drops) and counts spool drops only within the last 24 h —
  never the lifetime `dropped_results` total, which would flag forever.
  Exactly antipodal site pairs get a deterministic nudged geodesic instead
  of a degenerate slerp. Agents' Attention filter partitions the fleet on
  needsAttention (not healthy OR cert inside the warning window OR
  spool drops in the last 24 h — in lockstep with Overview's
  attentionReason) so All = Attention + Healthy, the two views always
  agree, and a row whose cert cell shows a degraded-styled warning can
  never sort under Healthy; counts labeled "affected targets" dedupe per
  rendered target (the API emits one event per failing series); incident
  groups key on the FULL normalized error (truncation is display-only, so
  long errors sharing a prefix never merge); the Settings page polls
  /settings every 30 s like other views and the threshold form clears its
  draft after a save, so transient failures retry and remote edits
  converge instead of being shadowed by a stale draft; the map info card's
  link count/best latency cover monitored pairs with unplaced peers (a
  bubble needs only its own site's coordinates).

- Shared topology map (map/matrix switch now in the Overview's
  Connectivity card; the map is the dot-matrix version described in the top
  bullet). Site
  bubbles at `sites.latitude/longitude` (nullable, both-or-neither CHECK; set
  via the new `lighthouse-server site list|set` CLI, never at enrollment;
  unplaced sites render in a fail-loud chip strip, never vanish), colored
  by a client-side severity fold
  (`web/src/severity.ts`: status first — down/stale can't look healthy —
  then shared warn/crit thresholds on the cell's headline latency/loss;
  rank ok<warn<stale<crit<down). Thresholds live in the single-row
  `dashboard_settings` table, served by `GET /api/v1/settings` (any
  session) and `PUT /api/v1/settings` — the first `requireRole("admin")`
  endpoint (withSession outermost; CSRF applies) — and are edited from a
  form on the Settings page (ms/% form, mirrors server validation; server
  names every problem, `DisallowUnknownFields`). Settings ride the
  existing 30 s matrix poll, so other browsers converge ≤30 s. The map uses
  the baked dot grid in `assets/mapGeo.ts` plus the lockstep projection in
  `geo.ts`, with a hover/pin info card (code, location, health, link count,
  best live latency, direction breakdown). The map remains inside the
  standard responsive Lighthouse card. Schema landed by editing 0001/0003 in place (pre-release
  convention) — existing dev DBs need `down -v`. Verified on a fresh
  compose stack through the proxy: CLI set/list + typo/range errors,
  settings round-trip/validation/403 viewer/401 anon, coords in
  sites+matrix payloads, defaults restored. Vocabulary: the map's warn and crit
  tiers are both publicly labeled "Degraded"; crit remains an internal
  stronger visual intensity, not a competing health state. Direction/series colors
  are categorical blue + magenta (`--series-a`/`--series-b`, mirrored in
  `PairDetail.tsx` COLORS): never orange, which would read as the
  crit/down alarm ramp; the magenta passes CVD+contrast on both schemes.
  The map's healthy tint is the accent indigo (its operations palette, as
  the carrier cyan was before); degraded/down/stale bubbles take the
  status ramp. Carriers and pulses are gone — per-direction detail lives
  in matrix mode and the hover card's breakdown bar.
- Dashboard branding uses the lighthouse mark with no runtime network or
  font dependency. In-app renders (header, login, loading) go through
  `components/LogoMark.tsx`, which picks the static
  `web/public/lighthouse-mark-{light,dark}.svg` variant from the resolved
  theme — the adaptive `web/public/lighthouse-mark.svg`'s embedded media
  query tracks the OS scheme and would ignore the manual toggle; the
  favicon keeps the adaptive mark (browser chrome follows the OS). The
  README shows the same mark via `<picture>` with per-theme copies in
  `docs/assets/`; keep all five files' geometry in lockstep. They live
  outside `web/public/` on purpose — anything there is copied into
  `web/dist/` at the next build. Public navigation is task-oriented; legacy
  route ids remain aliases and API/path-hop vocabulary is unchanged.
- Agents fleet-health view (`#/agents`): `/api/v1/agents` now carries
  enrolled_at, config_hash, newest-cert not_after/revoked_at, open
  outage rollups (offline flag + probes_failing count), series totals,
  and spool-drop accounting. `dropped_since_last_push` is persisted
  (`agents.dropped_results` running total + `last_dropped_at`) — the
  wire value is a delta (agent clears only after an acked push), so the
  server accumulates; a failed drop-write fails the RPC (Unavailable) so
  the report is retried, never silently lost. Schema landed by editing
  `0001_init.sql` in place (pre-release convention) — existing dev DBs
  need `down -v`. Verified on a fresh compose stack through the proxy.
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
- M5 (Toolkit percentiles, hourly/daily continuous aggregates, retention
  policies, window→source resolution, p50/p95/p99 + jitter/tcp/tls in the
  pair API and SPA, `.notx.sql` migration escape hatch, `make seed`) —
  done; gate verified in compose: fresh stack auto-migrated 0005–0010
  (caggs and backfills applied outside transactions); `make seed` loaded 90 d ×
  6 directions (777 600 rows) in ~10 s and printed exact empirical
  percentiles; 90 d pair + series answered in 30–60 ms (≪500 ms) with
  `source: hourly`, EXPLAIN showing `_materialized_hypertable` chunk
  scans; API p50/p95/p99 within 0.13 % of the seed distribution; 365 d
  series `source: daily`, 91 daily points, percentiles on every point;
  24h/7d stayed raw with percentile keys absent; 2 refresh + 3 retention
  jobs listed; a migrated-then-stripped scratch DB made serve exit
  `preflight: timescaledb_toolkit extension is not installed …`; re-run
  `make seed` left row counts identical; matrix/outages/path-events
  regression-clean; offline `make build`/`make test` green. Follow-up
  (pre-release, so 0006/0007 were rewritten in place rather than adding
  migrations): the caggs now partition by successful timing family so RTT
  never mixes with TCP/application timings, `PairLatencySource` applies a
  5 % coverage floor, and the SPA surfaces per-probe health, honest chart
  gaps, adaptive loss scales, responsive tables, and operator-first
  outage/path details. Re-verified on a fresh compose stack: 0001→0010
  auto-migrated (family-partitioned caggs, `latency_source` column,
  2 refresh + 3 retention jobs); `make seed` percentiles within 0.3 % via
  the API; 90 d pair `source: hourly` in ~50 ms with per-direction
  `latency_source: rtt` and per-probe `checks`; 365 d `daily` (91 points,
  percentiles present); 24h raw with percentile keys absent;
  matrix/outages regression-clean (tcp `conn_refused` cells + 6 open
  `probe_failing` are the dev mesh's intentional port-9 TCP probe).
- SPA linting/formatting (2026-08-03): oxlint 1.77.0 + oxfmt 0.62.0, both
  exact-pinned; their platform binaries are os/cpu-guarded optional deps, so
  `package-lock.json` carries all 19+19 bindings exactly like TypeScript 7's.
  `web/.oxfmtrc.json` = printWidth 120, 2-space, single quotes, no
  semicolons, trailing commas — and `sortPackageJson: false`, which defaults
  ON and would otherwise reorder package.json keys npm owns. Formatting
  covers TS/TSX/CSS/HTML/loose JS; `src/assets/mapGeo.ts` is excluded from
  BOTH tools because regenerating it via `tools/build-map-geo.mjs` would
  immediately drift from formatted output. `web/.nvmrc` (24) is the single
  node pin, read by CI's `web-lint` job — that job runs `npm ci` and is the
  one CI check deliberately outside the offline guarantee (it gates sources,
  never release artifacts; still NO dist-drift gate). Rule names differ from
  ESLint's: hooks rules live under the `react` plugin (`react/rules-of-hooks`,
  `react/exhaustive-deps`), and `plugins` REPLACES the default set (core
  eslint rules stay on regardless). Four waivers, each with its reason in the
  config or inline: `react/react-in-jsx-scope` (automatic JSX runtime),
  `import/no-unassigned-import` (side-effect CSS imports),
  `jsx-a11y/prefer-tag-over-role` (the dashboard's role="status"/"group"/"img"
  are correct ARIA; output/fieldset/img carry different semantics and UA
  styling), and per-site disables for Login's autofocus, WorldMap's hover-card
  handlers, WorldMap's intentional memo-name shadowing, and one sort of a
  freshly-spread array.
- Next: post-M6 follow-ups worth tracking: revocation CLI, vendored+pinned
  buf for a CI proto-drift gate, `:edge` image tags on main, type-aware
  oxlint (`--type-aware`, needs tsgolint), and possibly oxfmt `sortImports`
  (off for now — `main.tsx`'s side-effect CSS import order is load-bearing).

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
- Typechecking is TypeScript 7 (the native Go port; `typescript` pulls a
  per-platform binary from 20 `@typescript/typescript-*` optional deps —
  all 20 are in `package-lock.json` with `os`/`cpu` guards, so `npm ci`
  resolves on any dev machine). TS 7 errors (TS2882) on side-effect
  imports of modules with no declarations, which is why `src/vite-env.d.ts`
  (`/// <reference types="vite/client" />`) exists — it supplies the
  `declare module '*.css'` that `src/main.tsx`'s two CSS imports need.
  Deleting it breaks `npm run build`, not just the editor. Emitted `dist/`
  bytes are unchanged by the compiler swap: Vite transpiles with its own
  bundler and only ever runs `tsc -b` as a gate.
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

### M5 notes worth knowing

- Migrations named `NNNN_name.notx.sql` run as a single autocommit Exec
  (no transaction) — required because cagg creation is refused inside a
  transaction block, and a multi-statement simple-query message gets an
  *implicit* transaction that Timescale rejects the same way. Hence the
  two enforced invariants (pinned by `migrate_test.go`): exactly ONE
  top-level statement per notx file, and idempotent DDL (`IF NOT
  EXISTS`), because recording in `schema_migrations` is a separate
  statement and the crash window between the two must converge on re-run.
- The caggs store only sums/counts/min/max plus a UddSketch
  (`percentile_agg`); `probe_results_daily` is a hierarchical `rollup()`
  of hourly. Never add an `avg()` (or any non-re-aggregable) column to a
  cagg — daily would silently produce wrong numbers. Averages are always
  `sum/count` at query time. Hourly rows are also partitioned by
  `latency_source` (the row's timing family), and all timing statistics
  include successful probes only — a fast failure must never read as low
  latency.
- The COALESCE latency ladder exists in TWO places: `latencyExpr` in
  `store/dashboard.go` and frozen inside `0006_m5_hourly_cagg.notx.sql`.
  Once a release ships, changing the ladder requires a forward-only cagg
  rebuild, or raw and aggregate windows will disagree.
- Window→source: 24h/7d→raw, 30/90d→hourly, 365d→daily
  (`httpapi/windows.go`, pinned by `TestWindows`). Aggregate-sourced
  responses carry `p50_us/p95_us/p99_us` (omitted, not null, on raw
  windows — clients key off absence) and every pair/series response has
  `source`. Matrix/DirectionLatest stay on raw (10-min horizon).
- `PairLatencySource` chooses one successful family per direction/window in
  priority order RTT→TCP connect→TLS handshake→TTFB→total, with a 5 %
  coverage floor (`chooseLatencySource`, unit-tested offline): a family
  must hold ≥5 % of the window's successful latency samples to win, so a
  just-enabled ICMP prober can't blank a 365 d chart of TCP history; if
  nothing clears the floor, the purest family present wins. Pair summaries
  and latency series filter to that family; loss continues to fold all probes.
  Series responses carry a source per direction (the legacy top-level field
  remains the A→B compatibility alias).
- Policy offsets are ordered on purpose: hourly refresh `start_offset` 8 d
  > agent spool `max_age` 7 d (late replay lands refreshable) and < raw
  retention 14 d (refresh never reads a dropped region); daily 10 d >
  hourly's window. Both caggs are `materialized_only = false`, so the
  un-refreshed tail is served live — correct before the first refresh.
- Migration ORDER is load-bearing: 0008/0009 do a one-time
  `refresh_continuous_aggregate` backfill over the last 400 d (the longest
  retained horizon; the hourly backfill uses 400 d, not its own 100 d,
  because it feeds the daily one) and MUST precede the policies (0010).
  Real-time aggregation only unions data above the materialization
  watermark, so on an upgrade with pre-existing history the bounded policy
  refresh would otherwise advance the watermark past everything older than
  8 d — instantly hiding it — and raw retention would then delete it
  unrecoverably (policy jobs fire within ~a minute of creation, verified).
  Backfills can be slow on big upgrades: `migrate --timeout` (default
  30 m) raises the deadline — an over-deadline backfill restarts from
  scratch on every retry and would never complete.
- `make seed` → `lighthouse-server seed` (runs inside compose; DB is not
  host-exposed): deterministic per-pair history (seeded RNG, diurnal +
  long-tail noise, scripted outages), seed-owned UUIDv5 probe IDs so
  re-runs delete exactly their prior rows, CopyFrom, then explicit
  `refresh_continuous_aggregate` on both caggs (never rely on the policy
  schedule — retention could drop the >14 d raw region first; the CALLs
  need the simple protocol and no surrounding tx). It prints exact
  empirical p50/p95/p99 per direction for gate comparison (UddSketch is
  approximate; ~5 % tolerance, observed ≤0.2 %). Seeded history ends 2 min
  in the past so live probes stay newest per series and the matrix keeps
  reflecting reality.
- `DROP EXTENSION timescaledb_toolkit` is blocked by cagg column
  dependencies once 0006+ are applied — to exercise the serve preflight,
  migrate a scratch DB, drop the two matviews, then the extension.
