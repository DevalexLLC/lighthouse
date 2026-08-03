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
