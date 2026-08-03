# Lighthouse installation and user guide

This guide starts with an empty environment and ends with a production
control plane, enrolled agents, a two-site probe mesh, and measurements in the
dashboard. It covers online image pulls, offline release bundles, RPM agents,
and container agents.

The control plane is always deployed as containers. Agents may run from the
published container or from the RPM on RHEL-family systems.

## 1. Plan the installation

Choose these values before changing any configuration. The examples below use
the values in the right-hand column; replace them everywhere with the values
for your environment.

| Value | Purpose | Example |
|---|---|---|
| `<version>` | Lighthouse release/image tag | `v1.0.0` |
| `<arch>` | Control-plane CPU architecture | `amd64` or `arm64` |
| `<control-plane-ip>` | Address of the Docker host | `192.0.2.10` |
| `<dashboard-name>` | Browser-facing DNS name | `lighthouse.example.com` |
| `<grpc-name>` | Agent-facing DNS name and TLS SNI | `grpc.lighthouse.example.com` |
| `<site-name>` | Stable, short site identifier | `nyc`, `lon` |
| `<probe-address>` | Address other agents can probe | `10.20.0.15` |

Both DNS names must resolve to the control-plane host. They intentionally use
the same IP and TCP port 443. The proxy reads the TLS SNI and sends
`<grpc-name>` to the agent gRPC listener; every other SNI goes to the
dashboard. TLS is not terminated at the proxy.

Use stable site names because they identify sites in mesh and probe
assignments. Each agent also needs a stable probe address reachable from the
other agent sites. This may be a private WAN address, VPN address, or resolvable
hostname. Do not use `localhost`, a container-only address, or a NAT address
that peers cannot reach.

For a useful first deployment, prepare at least two agent systems in different
sites. A single agent can monitor external targets, but directional site-to-site
measurements require two sites.

### Host prerequisites

The control-plane host needs:

- Linux with Docker Engine and the Docker Compose plugin
- persistent storage for Docker volumes
- inbound TCP 443 from operators and every agent
- DNS records for `<dashboard-name>` and `<grpc-name>`
- an HTTPS certificate and private key valid for `<dashboard-name>`

Confirm the container tools before continuing:

```sh
docker version
docker compose version
```

The dashboard certificate may come from a public CA or your organization's
internal CA. Browser clients must trust its issuer. Do not use this certificate
for agent identity: Lighthouse creates and manages a separate built-in CA for
agent mTLS and the gRPC server certificate.

## 2. Obtain the release files and images

Create a dedicated installation directory on the control-plane host. All
remaining control-plane commands in this guide run from that directory.

The directory must contain:

```text
docker-compose.yml
env.example
server.example.yaml
```

These files are in every release bundle. They are also available under
`deploy/compose/` in the source repository.

### Online installation

Check out the matching release tag, copy its production deployment files into
a clean install directory, and enter that directory:

```sh
git clone --depth 1 --branch <version> \
  https://github.com/devalexllc/lighthouse.git lighthouse-source
mkdir lighthouse-install
cp lighthouse-source/deploy/compose/docker-compose.yml lighthouse-install/
cp lighthouse-source/deploy/compose/env.example lighthouse-install/
cp lighthouse-source/deploy/compose/server.example.yaml lighthouse-install/
cd lighthouse-install
```

Alternatively, extract the matching release bundle and use its directory even
on an online host. Then pull all four runtime images. Pin a release tag; do not
use `latest` for a production installation.

```sh
export LIGHTHOUSE_VERSION=<version>

docker pull ghcr.io/devalexllc/lighthouse-server:${LIGHTHOUSE_VERSION}
docker pull ghcr.io/devalexllc/lighthouse-proxy:${LIGHTHOUSE_VERSION}
docker pull ghcr.io/devalexllc/lighthouse-agent:${LIGHTHOUSE_VERSION}
docker pull timescale/timescaledb-ha:pg16-all
```

The agent image is pulled here even though it is not a control-plane Compose
service. Container-based agent systems may pull it directly instead.

### Offline installation

On an internet-connected transfer system, download the release artifact named
`lighthouse-<version>-<arch>-bundle.tar.gz`. Transfer it to the control-plane
host, then extract and verify it:

```sh
tar xzf lighthouse-<version>-<arch>-bundle.tar.gz
cd lighthouse-<version>-<arch>-bundle
sha256sum -c SHA256SUMS
```

Load the server, proxy, agent, and TimescaleDB images from the one bundled
archive:

```sh
docker load -i images/lighthouse-images-<version>-<arch>.tar
```

The bundle also contains the agent RPMs under `rpms/`, when published for the
release. Transfer the matching RPM to each RPM-based agent system. For an
offline container agent, save just the already-loaded agent image, transfer the
result, and load it on the agent system:

```sh
# On the control-plane or transfer host after the bundle's docker load:
docker save -o lighthouse-agent-<version>-<arch>.tar \
  ghcr.io/devalexllc/lighthouse-agent:<version>

# On the offline agent host:
docker load -i lighthouse-agent-<version>-<arch>.tar
```

## 3. Configure DNS, TLS, and the firewall

Create these DNS records before starting Lighthouse:

```text
<dashboard-name>  A/AAAA  <control-plane-ip>
<grpc-name>       A/AAAA  <control-plane-ip>
```

The dashboard certificate file should contain the full certificate chain and
must contain `<dashboard-name>` in its Subject Alternative Name. Lighthouse
auto-issues the gRPC listener certificate with `<grpc-name>` as its SAN, so do
not add the gRPC name to the dashboard certificate unless it is also useful
for your environment.

Open inbound TCP 443 on the control-plane host. Do not expose the server's
ports 8080 or 8443 or TimescaleDB port 5432; those stay on the private Compose
network.

Agent and probe firewall requirements are listed in
[Firewall requirements](#firewall-requirements). Review them before enrolling
agents, especially when sites are connected through restrictive WAN
firewalls.

## 4. Configure the proxy and control plane

### 4.1 Create `.env`

Copy the environment example:

```sh
cp env.example .env
chmod 600 .env
```

Generate a long database password. A hexadecimal value avoids URL-encoding
problems when the same password is placed in `server.yaml`:

```sh
openssl rand -hex 32
```

Edit `.env` and set all three values:

```dotenv
LIGHTHOUSE_DB_PASSWORD=<generated-hex-password>
LIGHTHOUSE_VERSION=<version>
LIGHTHOUSE_GRPC_SNI=<grpc-name>
```

`LIGHTHOUSE_GRPC_SNI` configures the proxy. It must exactly match the gRPC DNS
name agents send as TLS SNI. The supplied nginx proxy performs TCP passthrough
with this routing rule:

```text
SNI = <grpc-name>  -> server:8443  (agent gRPC and mTLS)
all other SNI      -> server:8080  (dashboard HTTPS)
```

### 4.2 Create `server.yaml`

Copy and protect the server example. The file is bind-mounted into the
server container, which runs as UID 10001, so the container must be able to
read it — owner-only permissions with the operator's ownership would break
migration, CA initialization, and startup:

```sh
cp server.example.yaml server.yaml
sudo chown 10001 server.yaml
chmod 600 server.yaml
```

Edit `server.yaml`. A minimal production configuration is:

```yaml
listen:
  grpc: ':8443'
  grpc_hostname: <grpc-name>
  http: ':8080'

db:
  url: postgres://lighthouse:<generated-hex-password>@timescaledb:5432/lighthouse

tls:
  cert_file: /etc/lighthouse/tls/server.crt
  key_file: /etc/lighthouse/tls/server.key

ca:
  dir: /var/lib/lighthouse-server/ca

log:
  level: info
```

The value of `listen.grpc_hostname` must exactly equal
`LIGHTHOUSE_GRPC_SNI`. It is also the SAN on the automatically issued gRPC
server certificate.

Configuration loading is strict. Unknown or misspelled YAML keys stop the
server, so copy keys from the supplied example instead of guessing them. The
default agent certificate lifetime is 30 days and the default gRPC server
certificate lifetime is 90 days; both rotate automatically.

### 4.3 Install the dashboard certificate

The server container runs as UID 10001. Copy the operator-managed dashboard
certificate and key into the Compose `tls` volume and set readable ownership.
Replace the two `/absolute/path/...` values:

```sh
docker compose run --rm --no-deps \
  -v /absolute/path/dashboard-cert.pem:/in/cert.pem:ro \
  -v /absolute/path/dashboard-key.pem:/in/key.pem:ro \
  --entrypoint sh --user 0 server -c \
  'cp /in/cert.pem /etc/lighthouse/tls/server.crt && \
   cp /in/key.pem /etc/lighthouse/tls/server.key && \
   chown 10001:10001 /etc/lighthouse/tls/server.crt /etc/lighthouse/tls/server.key && \
   chmod 0644 /etc/lighthouse/tls/server.crt && \
   chmod 0600 /etc/lighthouse/tls/server.key'
```

On an SELinux-enforcing host, add the appropriate bind-mount relabel option
for the two source files if Docker cannot read them.

## 5. Initialize and deploy the control plane

Validate that Compose can resolve the configuration without printing the
expanded file:

```sh
docker compose config --quiet
```

Start TimescaleDB and wait until it reports healthy:

```sh
docker compose up -d timescaledb
docker compose ps
```

Apply the database migrations. Production never auto-migrates:

```sh
docker compose run --rm server migrate \
  --config /etc/lighthouse/server.yaml
```

Create the built-in Lighthouse CA. Run this once on a new installation. The
command refuses to overwrite an existing CA:

```sh
docker compose run --rm server ca init \
  --config /etc/lighthouse/server.yaml
```

Start the complete control plane:

```sh
docker compose up -d
docker compose ps
```

Inspect startup logs. Resolve every error before enrolling agents:

```sh
docker compose logs --tail=100 timescaledb server proxy
```

Create the first dashboard administrator. The command prompts twice for a
password of at least eight characters:

```sh
docker compose exec server lighthouse-server user add \
  --config /etc/lighthouse/server.yaml \
  --username admin --admin
```

Open `https://<dashboard-name>/` and sign in. A successful health request also
confirms that DNS, the proxy's default route, dashboard TLS, and the HTTP
listener work together:

```sh
curl https://<dashboard-name>/healthz
```

If DNS is not live yet, test against the control-plane IP while preserving
the TLS SNI:

```sh
curl --resolve <dashboard-name>:443:<control-plane-ip> \
  https://<dashboard-name>/healthz
```

Do not use `curl -k` as the final verification; it hides certificate trust and
hostname errors that browsers will also encounter.

## 6. Understand agent configuration

Agent YAML configures only the local agent and its control-plane connection.
Probe targets, mesh membership, intervals, and timeouts are configured
centrally after enrollment and streamed to agents. Do not put probe definitions
in `agent.yaml`.

Use this configuration on every RPM or container agent, changing only values
that differ for that host:

```yaml
server:
  address: <grpc-name>:443
  sni: <grpc-name>

state_dir: /var/lib/lighthouse-agent

spool:
  max_bytes: 268435456
  max_age: 168h

log:
  level: info
```

The fields mean:

- `server.address`: the reachable proxy endpoint in `host:port` form. Normally
  this is `<grpc-name>:443`.
- `server.sni`: must equal the control plane's `listen.grpc_hostname` and
  `LIGHTHOUSE_GRPC_SNI`.
- `state_dir`: persistent PKI identity and offline result spool. Do not share
  one state directory or volume between agents.
- `spool.max_bytes`: maximum local spool size; the oldest results are dropped
  first when full, and the loss is reported to the control plane.
- `spool.max_age`: maximum age of disconnected results before dropping them.
- `log.level`: `debug`, `info`, `warn`, or `error`.

The enrollment command also takes `--probe-address`. This is not the control
plane address. It is the IP address or DNS name other agents should probe when
this agent participates in a mesh. Always supply it in the standard proxied
deployment: the control plane otherwise sees the nginx proxy as the connection
source and records the wrong address.

## 7. Create an enrollment token

Create one single-use token for each agent. The site is created automatically
the first time its name is used:

```sh
docker compose exec server lighthouse-server token create \
  --config /etc/lighthouse/server.yaml \
  --site <site-name> --ttl 24h
```

Save both values printed by the command:

- the join token, which is valid until its TTL expires and can be used once
- the `sha256:<hex>` built-in CA fingerprint

Transfer them to the intended agent securely. If a site has multiple agents,
create a separate token for each agent using the same site name.

Enrollment deliberately has no trust-on-first-use mode. The examples below pin
the printed fingerprint. As an alternative, export the public CA certificate,
transfer it to the agent, and use `--ca-cert <file>` instead of
`--fingerprint`:

```sh
docker compose cp \
  server:/var/lib/lighthouse-server/ca/ca.crt ./lighthouse-ca.crt
```

## 8. Deploy an RPM agent

Use this path on a RHEL-family system. Copy the RPM matching the host's CPU
architecture onto the agent system (`x86_64` for AMD64 or `aarch64` for
ARM64).

### 8.1 Install and configure the package

```sh
sudo dnf install ./<rpm-file>
sudo vi /etc/lighthouse/agent.yaml
```

Put the configuration from [Understand agent
configuration](#6-understand-agent-configuration) in the file, then preserve
the package's expected permissions:

```sh
sudo chown root:lighthouse /etc/lighthouse/agent.yaml
sudo chmod 0640 /etc/lighthouse/agent.yaml
sudo install -d -m 0700 -o lighthouse -g lighthouse \
  /var/lib/lighthouse-agent
```

### 8.2 Enroll as the service user

Run enrollment as `lighthouse`, not root, so the private key and certificate
belong to the account that runs the service:

```sh
sudo -u lighthouse lighthouse-agent enroll \
  --config /etc/lighthouse/agent.yaml \
  --token '<join-token>' \
  --fingerprint 'sha256:<hex>' \
  --probe-address '<probe-address>'
```

Enrollment succeeds with output containing the new agent ID and certificate
expiration time. It refuses to overwrite an existing identity.

### 8.3 Start and verify the service

```sh
sudo systemctl enable --now lighthouse-agent
sudo systemctl status lighthouse-agent
sudo journalctl -u lighthouse-agent -b --no-pager
```

The systemd unit grants `CAP_NET_RAW`, creates the state directory, runs the
agent as the unprivileged `lighthouse` user, and executes `selfcheck` before
every start. A fatal configuration, PKI-permission, spool, or ICMP problem
prevents startup and appears in the journal with a remedy.

## 9. Deploy a container agent

Use one persistent named volume per agent. The example below enrolls first and
then creates the long-running container, avoiding a restart loop from an
unenrolled agent.

### 9.1 Create the configuration and state volume

```sh
sudo install -d -m 0755 /opt/lighthouse-agent
sudo vi /opt/lighthouse-agent/agent.yaml
sudo chmod 0644 /opt/lighthouse-agent/agent.yaml
docker volume create lighthouse-agent-state
```

Put the configuration from [Understand agent
configuration](#6-understand-agent-configuration) in `agent.yaml`.

If this host is offline, load the agent image tar transferred from the release
bundle. If it is online and the image has not already been pulled, run:

```sh
docker pull ghcr.io/devalexllc/lighthouse-agent:<version>
```

### 9.2 Enroll into the persistent volume

```sh
docker run --rm \
  --mount type=bind,src=/opt/lighthouse-agent/agent.yaml,dst=/etc/lighthouse/agent.yaml,readonly \
  --mount type=volume,src=lighthouse-agent-state,dst=/var/lib/lighthouse-agent \
  ghcr.io/devalexllc/lighthouse-agent:<version> \
  enroll --config /etc/lighthouse/agent.yaml \
  --token '<join-token>' \
  --fingerprint 'sha256:<hex>' \
  --probe-address '<probe-address>'
```

On an SELinux-enforcing host, add the appropriate relabel option to the config
bind mount if Docker cannot read it.

### 9.3 Start and verify the agent

```sh
docker run -d \
  --name lighthouse-agent \
  --restart unless-stopped \
  --cap-add NET_RAW \
  --mount type=bind,src=/opt/lighthouse-agent/agent.yaml,dst=/etc/lighthouse/agent.yaml,readonly \
  --mount type=volume,src=lighthouse-agent-state,dst=/var/lib/lighthouse-agent \
  ghcr.io/devalexllc/lighthouse-agent:<version>

docker exec lighthouse-agent lighthouse-agent selfcheck \
  --config /etc/lighthouse/agent.yaml
docker logs --tail=100 lighthouse-agent
```

`NET_RAW` enables ICMP and traceroute. TCP, TLS, HTTP, and DNS probes do not
need it, but the recommended standard agent includes it so all supported probe
types work.

## 10. Enroll the remaining sites

Repeat the token, configuration, enrollment, and service/container steps for
every agent. Use a unique persistent state directory or volume and the correct
probe address on each host.

On the control plane, confirm the server sees the connections:

```sh
docker compose logs --tail=100 server
```

The logs should contain `agent enrolled`, `agent connected`, and
`config snapshot sent`. The dashboard's **Agents** page should show every
agent with a recent last-update time.

Optional map metadata can be added after a site's first token creates it:

```sh
docker compose exec server lighthouse-server site set \
  --config /etc/lighthouse/server.yaml \
  --name nyc --display-name 'New York' --location 'New York, US' \
  --lat 40.7128 --lon -74.0060
```

Coordinates must be supplied together. Repeat for each site so it appears on
the dashboard map.

## 11. Configure probe workloads

An administrator can configure targets, meshes, and probes in the dashboard
from **user menu -> Settings**. The **Targets**, **Meshes**, and **Probes** tabs
provide the same validation as the server CLI. Changes normally reach
connected agents within about 30 seconds; agent YAML edits and restarts are not
required.

The CLI examples below create a minimal working two-site mesh. Replace `nyc`
and `lon` with the exact site names used when their tokens were created.

### 11.1 Create a full mesh

```sh
docker compose exec server lighthouse-server mesh create \
  --config /etc/lighthouse/server.yaml --name wan

docker compose exec server lighthouse-server mesh add \
  --config /etc/lighthouse/server.yaml --name wan --site nyc

docker compose exec server lighthouse-server mesh add \
  --config /etc/lighthouse/server.yaml --name wan --site lon

docker compose exec server lighthouse-server mesh list \
  --config /etc/lighthouse/server.yaml
```

A mesh expands into ordered directions. With two sites, Lighthouse creates
both `nyc -> lon` and `lon -> nyc` assignments using the probe addresses
recorded during enrollment.

### 11.2 Add baseline ICMP and traceroute probes

```sh
docker compose exec server lighthouse-server probe add \
  --config /etc/lighthouse/server.yaml \
  --mesh wan --type icmp --interval 30s --timeout 5s \
  --train-count 10 --train-spacing 200ms

docker compose exec server lighthouse-server probe add \
  --config /etc/lighthouse/server.yaml \
  --mesh wan --type traceroute --interval 5m --timeout 30s

docker compose exec server lighthouse-server probe list \
  --config /etc/lighthouse/server.yaml
```

The ICMP train must fit inside the timeout, and every timeout must be shorter
than its interval. The CLI and dashboard reject invalid combinations.

TCP and TLS mesh probes require a real service listening at the same port on
each peer probe address. The port belongs on the mesh probe as a parameter:

```sh
docker compose exec server lighthouse-server probe add \
  --config /etc/lighthouse/server.yaml \
  --mesh wan --type tcp --interval 30s --timeout 5s --param port=443
```

Do not add that example unless port 443 is actually reachable on every mesh
member. The Lighthouse agent itself does not open a probe-listener port.

### 11.3 Add external targets and direct probes

External targets let one site monitor infrastructure that is not a Lighthouse
agent. Create a target with the fields required by the probe type:

```sh
# Address target for ICMP, traceroute, TCP, TLS, or DNS.
docker compose exec server lighthouse-server target add \
  --config /etc/lighthouse/server.yaml \
  --name public-dns --address 203.0.113.53 --port 53

# URL target for HTTP(S).
docker compose exec server lighthouse-server target add \
  --config /etc/lighthouse/server.yaml \
  --name status-page --url https://status.example.com/health
```

Assign direct probes to the site whose agents should execute them:

```sh
docker compose exec server lighthouse-server probe add \
  --config /etc/lighthouse/server.yaml \
  --site nyc --target public-dns --type dns \
  --interval 30s --timeout 5s \
  --param dns.qname=example.com --param dns.qtype=A

docker compose exec server lighthouse-server probe add \
  --config /etc/lighthouse/server.yaml \
  --site nyc --target status-page --type http \
  --interval 30s --timeout 10s \
  --param http.method=GET --param http.expect_status=2xx
```

Probe-specific rules include:

| Type | Target/parameters |
|---|---|
| `icmp` | target address; optional train count and spacing |
| `traceroute` | target address; agent needs `NET_RAW` |
| `tcp` | direct target address and port, or mesh `port` parameter |
| `tls` | TCP requirements; optional `tls.sni` and `tls.insecure_skip_verify` |
| `http` | direct URL target only; optional method, expected status, and TLS verification override |
| `dns` | target address and required `dns.qname`; optional qtype, expected RCODE, and resolver override |

Avoid `*.insecure_skip_verify=true` except for intentionally self-signed test
services. Unknown parameters are rejected rather than silently ignored.

## 12. Verify the complete system

Wait at least one probe interval plus the roughly 30-second configuration
distribution interval, then verify each layer:

1. `docker compose ps` shows TimescaleDB healthy and the control-plane
   containers running.
2. `https://<dashboard-name>/healthz` succeeds with normal TLS validation.
3. The dashboard **Agents** page shows every agent recently connected.
4. `lighthouse-agent selfcheck` passes fatal checks on every host.
5. `probe list` shows the enabled mesh and direct probes you created.
6. The dashboard **Overview** map or matrix shows both directions between
   mesh sites.
7. The **Routes** page begins to populate after traceroute runs.

Useful logs while verifying are:

```sh
# Control plane
docker compose logs --tail=200 proxy server timescaledb

# RPM agent
sudo journalctl -u lighthouse-agent -b --no-pager

# Container agent
docker logs --tail=200 lighthouse-agent
```

The proxy access log includes the received SNI and selected backend. Agent
connections must show `sni="<grpc-name>"` and `backend=server_grpc`. Browser
connections should use `backend=server_dashboard`.

## Firewall requirements

### Control-plane host

| Direction | Protocol/port | Peer | Purpose |
|---|---|---|---|
| Inbound | TCP 443 | operator browsers | dashboard HTTPS using `<dashboard-name>` |
| Inbound | TCP 443 | every agent | enrollment, config, results, and renewal using `<grpc-name>` |

The control plane initiates no Lighthouse connections to agents. Agent config
uses a long-lived TLS connection with keepalive traffic about once a minute.
Stateful middleboxes must allow long-lived connections and must not reap flows
idle for less than about two minutes, or agents reconnect repeatedly.

### Agent hosts

Outbound requirements depend on configured probes:

| Protocol/port | Peer | Purpose |
|---|---|---|
| TCP 443 | control plane | all agent-to-server traffic |
| ICMP echo request | peer agents/targets | latency, loss, and jitter |
| TCP target port | peer agents/targets | TCP and TLS probes |
| TCP URL port | external target | HTTP(S) probes |
| UDP 53 or configured port | DNS target/resolver | DNS probes; no TCP fallback |
| UDP 33434-33523 | peer agents/targets | traceroute probes |

Inbound requirements for mesh destinations are:

| Protocol/port | Purpose |
|---|---|
| ICMP echo request | peers' ICMP probes; the kernel replies |
| TCP mesh-template ports | peers' TCP/TLS probes against a real local service |
| UDP 33434-33523 | peers' traceroutes; the kernel's ICMP port-unreachable reply marks destination reached |

Allow related ICMP echo replies, time-exceeded messages, and port/host
unreachable messages back to the probing host. Blanket inbound ICMP drops
break ICMP and traceroute. IPv6 targets require the ICMPv6 equivalents, and
ICMPv6 must not be blanket-dropped.

Agents expose no operator-facing management port.

## Troubleshooting

### Agent cannot enroll or reconnects continuously

- Confirm `server.address` resolves and TCP 443 is reachable from the agent.
- Confirm `server.sni`, `listen.grpc_hostname`, and
  `LIGHTHOUSE_GRPC_SNI` are identical.
- Check the proxy log for the SNI and `backend=server_grpc`.
- Confirm the token is unexpired, unused, and was created for the intended
  site.
- Confirm the fingerprint exactly matches the one printed with the token.
- Check middlebox idle timeouts if logs repeatedly show `config stream failed`.

### Dashboard works but agents do not

This usually means the default proxy route works but the gRPC SNI route does
not. Recheck all three gRPC-name settings and DNS. The proxy must pass TLS
through; an upstream load balancer that terminates and replaces TLS breaks
agent mTLS unless it is specifically configured for TCP passthrough.

### Agents connect but mesh results are absent or target the proxy

The agent was probably enrolled without the correct `--probe-address`.
Re-enroll with a fresh token and a peer-reachable address. Enrollment refuses
to overwrite identity state; stop the agent and deliberately remove only that
agent's `<state_dir>/pki` directory or replace its dedicated state volume
before re-enrolling. Treat this as identity replacement, not routine repair.

Also verify the WAN firewall permits the selected probe protocol in both
directions.

### RPM service fails its pre-start check

```sh
sudo journalctl -u lighthouse-agent -b --no-pager
```

The check names the broken YAML key, state-directory permission, expired
identity, private-key mode, or missing socket capability. Run manual
selfchecks as the service user when checking filesystem ownership; a root-run
selfcheck can create a root-owned spool directory. The systemd service itself
supplies `CAP_NET_RAW` for ICMP/traceroute.

### Container agent cannot write its state volume

The image runs as UID 10001. Use a dedicated volume initialized by the image,
and do not pre-populate its files as root. Confirm the same volume is mounted
for enrollment and the long-running container.

## Certificate lifecycle

- Agent certificates are valid for 30 days by default and renew automatically
  at two-thirds of their actual lifetime. No normal operator action is needed.
- An agent offline longer than its certificate lifetime cannot renew. Its
  selfcheck reports expiration; re-enroll it with a fresh token.
- The gRPC server certificate is auto-issued by the built-in CA and rotates in
  process.
- The dashboard certificate is operator-managed. Replace the files in the
  `tls` volume and restart the server when renewing it.
- The database is the sole agent-certificate revocation authority. There is no
  CRL or OCSP endpoint.

There is not yet a certificate-revocation CLI. To revoke a known serial:

```sh
docker compose exec timescaledb psql -U lighthouse -d lighthouse -c \
  "UPDATE certificates SET revoked_at = now() WHERE serial = '<serial>'"
```

Existing agent streams using that certificate are dropped within about 30
seconds.

## Upgrades

For an online control plane, update `LIGHTHOUSE_VERSION` in `.env` and pull the
new pinned images:

```sh
docker compose pull server proxy timescaledb
```

For an offline control plane, load the new bundle image archive and then update
`LIGHTHOUSE_VERSION` in `.env`. In either case, migrate and recreate the
services:

```sh
docker compose run --rm server migrate \
  --config /etc/lighthouse/server.yaml
docker compose up -d
docker compose ps
```

Migrations are ordered and safe to rerun because only pending migrations are
applied. Large upgrades may need a larger `migrate --timeout`; do not interrupt
an aggregate backfill.

RPM agents upgrade with the new package:

```sh
sudo dnf upgrade ./<new-rpm-file>
```

For container agents, pull/load the new image, remove and recreate only the
container with the same config bind mount and state volume. Do not delete the
state volume: it contains the agent identity and offline spool. Agents and the
control plane may be upgraded independently because protocol changes are
additive.

## Backup scope

A recoverable control-plane backup must include all three persistent Compose
volumes **and** the installation directory's configuration:

- `dbdata`: sites, agents, users, probe configuration, results, and events
- `server-state`: the built-in CA private key and issued gRPC state
- `tls`: the operator-managed dashboard certificate and key
- `.env` and `server.yaml`: the database password, pinned version, and SNI —
  a restored `dbdata` volume keeps the password the role was created with
  (`POSTGRES_PASSWORD` only applies on first initialization), so without the
  original `.env`/`server.yaml` the server cannot reconnect after a restore
  short of manual database recovery

Protect the database, CA, and configuration secrets as one security
boundary. Restoring the database without the matching built-in CA, or the CA
without its database certificate records, does not reproduce the original
agent trust state.
