# Lighthouse

Real-time visibility into connectivity, latency, packet loss, and service
reachability between geographically dispersed sites.

Lighthouse is a central control plane plus a lightweight agent that runs at
each site. Agents probe each other (full mesh) and designated endpoints over
ICMP, TCP, TLS, HTTP(S), and DNS, run periodic traceroutes, and push results
to the control plane over mTLS on port 443. The dashboard shows current and
historical latency (min/avg/max/percentiles), packet loss, jitter, TCP connect
and TLS handshake times, recent outages, and path changes — **in both
directions for every site pair** — over 7/30/90/365-day windows.

## Design highlights

- **Directional by construction.** Site A → Site B and Site B → Site A are
  distinct measurement series; the source identity comes from the agent's mTLS
  client certificate and cannot be spoofed. Asymmetric routing, firewall, and
  return-path problems are visible instead of averaged away.
- **Store and forward.** Agents spool results to disk when the control plane
  is unreachable and replay them on reconnect. Overflow is dropped oldest-first
  and *reported* — never silent.
- **Built-in PKI.** The control plane runs its own CA. Agents enroll with a
  one-time join token, receive a short-lived client certificate, and rotate it
  automatically.
- **Air-gap friendly.** The repository vendors everything a build needs: Go
  dependencies (`vendor/`), generated protobuf code (`internal/pb/`), and the
  built dashboard (`web/dist/`). `go build -mod=vendor` with no network is the
  supported build. The control plane ships as container image tarballs plus a
  compose file; the agent is a single static Go binary (RPM for RHEL).
- **Fail loud.** Unknown config keys are fatal. Missing hard dependencies fail
  preflight at startup with the problem named. No silent no-ops.

## Architecture

```
site A                          control plane (containers)             site B
┌────────────────┐          ┌──────────────────────────────┐   ┌────────────────┐
│ lighthouse-    │ mTLS/gRPC│ nginx        lighthouse-serv │   │ lighthouse-    │
│ agent ─────────┼──:443───▶│ (SNI      ──▶ gRPC :8443     │◀──┼─ agent         │
│  probes ───────┼──────────┼─passthrough)  dashboard :8080│   │   probes       │
│  spool         │          │           ──▶ TimescaleDB    │   │   spool        │
└────────────────┘          └──────────────────────────────┘   └────────────────┘
        └───────────── ICMP/TCP/TLS/HTTP/DNS/traceroute ─────────────┘
```

- **`lighthouse-server`** — control plane: gRPC API for agents (enrollment,
  config distribution, result ingestion, certificate renewal), REST API + SPA
  for humans, outage and path-change detection, TimescaleDB storage with
  continuous aggregates for long-window percentile queries.
- **`lighthouse-agent`** — probe engine: scheduler, per-type probers, disk
  spool, certificate lifecycle. Single static binary, runs under systemd.

## Building

Requires only the Go toolchain — no network access:

```
make build        # bin/lighthouse-server, bin/lighthouse-agent
make test
```

Regenerating vendored artifacts (dev-time only, needs tooling/network):

```
make proto        # regenerate internal/pb from proto/ (buf)
make web          # rebuild web/dist from web/src (node)
make vendor       # re-vendor Go dependencies
```

## Development environment

```
make up           # full containerized stack: proxy + server + TimescaleDB
                  # + three fake site agents (dev overlay) — see deploy/
make down
```

## Status

Early development. See `docs/architecture.md` for the full design.

## License

Apache-2.0
