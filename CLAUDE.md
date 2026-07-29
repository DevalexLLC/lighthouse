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

## Status (as of 2026-07-29)

- M0 (scaffolding, strict config, compose stack) — done.
- M1 (protos, CA, enrollment, mTLS session, revocation) — done; verified
  e2e in compose: enroll → connect through SNI proxy → last_seen updates →
  revocation drops live stream ≤30 s.
- Next: M2 — scheduler, TCP/TLS/HTTP probes, spool, `probe_results`
  hypertable, real config distribution (`meshexpand`). See the milestone
  table in `docs/architecture.md`.
