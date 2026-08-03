# Lighthouse — offline installation

The control plane installs from the release bundle
(`lighthouse-<version>-<arch>-bundle.tar.gz`, attached to each GitHub
release) on any Docker host — no internet required. Agents install from the
bundled RPM on RHEL-family hosts, or run the published container image.

Online hosts can skip the bundle and pull the same images from
`ghcr.io/devalexllc/lighthouse-{server,agent,proxy}`.

## Control plane

Bundle contents: `images/lighthouse-images-<version>-<arch>.tar` (server,
proxy, agent, timescaledb — one `docker load`), `docker-compose.yml`,
`server.example.yaml`, `env.example`, `rpms/` (agent RPMs), `VERSION`,
`SHA256SUMS`, and this document.

```sh
tar xzf lighthouse-<version>-<arch>-bundle.tar.gz && cd lighthouse-<version>-<arch>-bundle
sha256sum -c SHA256SUMS

docker load -i images/lighthouse-images-<version>-<arch>.tar

cp env.example .env             # set LIGHTHOUSE_DB_PASSWORD, LIGHTHOUSE_VERSION,
                                # LIGHTHOUSE_GRPC_SNI (edit .env)
cp server.example.yaml server.yaml   # edit: listen.grpc_hostname MUST equal
                                     # LIGHTHOUSE_GRPC_SNI; set db.url password

# Operator TLS for the dashboard listener goes in the "tls" volume,
# readable by uid 10001 (the server runs non-root):
docker compose up -d timescaledb
docker compose run --rm -v ./your-cert.pem:/in/cert.pem:ro -v ./your-key.pem:/in/key.pem:ro \
  --entrypoint sh --user 0 server -c \
  'cp /in/cert.pem /etc/lighthouse/tls/server.crt && cp /in/key.pem /etc/lighthouse/tls/server.key && chown 10001 /etc/lighthouse/tls/*'

# Explicit migration + CA init (production never auto-migrates):
docker compose run --rm server migrate --config /etc/lighthouse/server.yaml
docker compose run --rm server ca init --config /etc/lighthouse/server.yaml

docker compose up -d

# First dashboard login + first agent join token:
docker compose exec server lighthouse-server user add --config /etc/lighthouse/server.yaml --username admin --role admin
docker compose exec server lighthouse-server token create --config /etc/lighthouse/server.yaml --site <site-name>
# token create prints the CA fingerprint agents pin at enrollment.
```

The only exposed port is 443 (SNI passthrough). DNS for both the dashboard
hostname and the gRPC hostname (`LIGHTHOUSE_GRPC_SNI`) must point at the
proxy host.

## Agent — RPM (RHEL-family hosts)

```sh
dnf install ./rpms/lighthouse-agent-<version>.<arch>.rpm
vi /etc/lighthouse/agent.yaml        # set server.address and server.sni
install -d -m 0700 -o lighthouse -g lighthouse /var/lib/lighthouse-agent
sudo -u lighthouse lighthouse-agent enroll --config /etc/lighthouse/agent.yaml \
  --token <join-token> --fingerprint sha256:<hex-from-token-create>
systemctl enable --now lighthouse-agent
```

The unit refuses to start on any selfcheck failure and names each problem
(`journalctl -u lighthouse-agent`). ICMP needs `CAP_NET_RAW` (granted by the
unit) or `net.ipv4.ping_group_range` covering the `lighthouse` group —
traceroute always needs the capability. See `packaging/systemd/README.md`.

## Agent — container

```sh
docker run -d --name lighthouse-agent --cap-add NET_RAW \
  -v ./agent.yaml:/etc/lighthouse/agent.yaml:ro \
  -v lighthouse-agent-state:/var/lib/lighthouse-agent \
  ghcr.io/devalexllc/lighthouse-agent:<version>
# One-time enrollment into the state volume:
docker run --rm -v ./agent.yaml:/etc/lighthouse/agent.yaml:ro \
  -v lighthouse-agent-state:/var/lib/lighthouse-agent \
  ghcr.io/devalexllc/lighthouse-agent:<version> \
  enroll --config /etc/lighthouse/agent.yaml --token <join-token> --fingerprint sha256:<hex>
docker restart lighthouse-agent
```

## Firewall requirements

### Control plane host

| Direction | Proto/port | Peer | Purpose |
|---|---|---|---|
| Inbound | TCP 443 | operator browsers | dashboard HTTPS (SNI = dashboard hostname) |
| Inbound | TCP 443 | every agent | enrollment, config stream, result pushes, cert renewal — gRPC over mTLS (SNI = `LIGHTHOUSE_GRPC_SNI`) |

That single port carries both traffic classes; the proxy splits them by TLS
SNI without terminating TLS. Nothing else is host-exposed — the server's
internal listeners (8443 gRPC, 8080 dashboard) and TimescaleDB (5432) exist
only on the compose network and need no firewall rules. The control plane
initiates no outbound connections.

The agent config stream is a long-lived TLS connection that can sit idle;
keepalive pings flow about once a minute in each direction. Stateful
middleboxes between agents and the control plane must allow long-lived
connections and not reap flows idle for under ~2 minutes, or agents will
reconnect-loop (visible as `config stream failed` churn in agent logs).

### Agent hosts

Outbound:

| Proto/port | Peer | Purpose |
|---|---|---|
| TCP 443 | control plane | everything agent↔server (single port, as above) |
| ICMP echo-request | peer agents / targets | echo (rtt/loss/jitter) probes; replies return as echo-reply |
| TCP <target port> | peer agents / targets | tcp and tls probes (the port is per-probe config; mesh templates name it explicitly) |
| TCP 80/443/custom | targets | http probes (whatever the configured URL uses) |
| UDP 53 | target / `dns.resolver` override | dns probes (UDP only — no TCP fallback; port 53 unless the resolver param names another) |
| UDP 33434–33523 | peer agents / targets | traceroute probes (30 hops × 3 probes, port encodes hop/index); replies return as ICMP time-exceeded / port-unreachable |

Inbound — mesh probes are symmetric, so every peer agent sends the same
traffic at this host:

| Proto/port | Purpose |
|---|---|
| ICMP echo-request | peers' echo probes (the kernel answers; no listener involved) |
| TCP <mesh template ports> | peers' tcp/tls probes against this agent |
| UDP 33434–33523 | peers' traceroutes; the kernel's ICMP **port-unreachable** reply is the destination-reached signal — a firewall that drops these inbound packets (or rate-limits/suppresses the outbound unreachable) makes every traceroute toward this site read as never arriving |

ICMP time-exceeded and port/host-unreachable must be allowed back in on the
prober side (stateful firewalls that track ICMP errors as related traffic
handle this; a blanket inbound ICMP drop breaks echo and traceroute both —
and note a blanket drop also kills the *reverse* direction's echo replies,
so both directions of a pair go red, not one). IPv6 targets need the ICMPv6
equivalents; ICMPv6 must never be blanket-dropped on v6 networks.

Agent hosts accept no operator-facing connections — there is no inbound
management port to open.

## Certificate lifecycle

- Agent certs are valid 30 days; agents renew automatically at 2/3 of the
  leaf's validity over the existing mTLS channel and retry daily on
  failure. No operator action.
- An agent dark longer than its cert lifetime cannot renew — re-enroll it
  with a fresh token (`selfcheck` says exactly this).
- Revocation is database-backed (the DB is the sole revocation
  authority — no CRL/OCSP). No CLI exists yet; mark the certificate row:
  `docker compose exec timescaledb psql -U lighthouse -c "UPDATE
  certificates SET revoked_at = now() WHERE serial = '<serial>'"` — live
  connections drop within 30 s.
- The gRPC server certificate is auto-issued by the built-in CA and
  rotated in-process; the dashboard certificate (`tls.*`) is
  operator-managed.

## Upgrades

```sh
docker load -i images/lighthouse-images-<new-version>-<arch>.tar
vi .env                                  # bump LIGHTHOUSE_VERSION
docker compose run --rm server migrate --config /etc/lighthouse/server.yaml
docker compose up -d
```

Migrations are strictly ordered and immutable once released; `migrate` is
always safe to run (applies only what's pending). Large upgrades can take
time backfilling aggregates — raise `migrate --timeout` rather than
interrupting. Agents upgrade independently (`dnf upgrade` the new RPM);
protocol compatibility is additive by contract.
