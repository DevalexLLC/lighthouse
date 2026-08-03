#!/bin/sh
# Dev-only: mint one join token per fake site so the dev agents can enroll.
# Runs in the lighthouse-server image with DB + CA access; restarts until the
# schema exists (migrate runs in the server service).
set -eu

CONFIG=/etc/lighthouse/server.yaml

# Wait for the server service to have migrated the DB and created the CA.
until [ -s /var/lib/lighthouse-server/ca/ca.crt ]; do
    echo "bootstrap: waiting for CA (server still initializing)"
    sleep 2
done
cp /var/lib/lighthouse-server/ca/ca.crt /bootstrap/ca.crt

for site in nyc lon syd va co tx; do
    f="/bootstrap/${site}.token"
    if [ -s "$f" ]; then
        echo "bootstrap: token for $site already minted"
        continue
    fi
    echo "bootstrap: minting join token for site $site"
    lighthouse-server token create --config "$CONFIG" \
        --site "$site" --ttl 24h --quiet > "$f.tmp"
    mv "$f.tmp" "$f"
done

# Seed M2 probe config. target/mesh commands are upserts and each probe add
# is guarded individually against the current probe list, so a rerun — even
# one recovering from a partial previous run — converges to the full seed.
echo "bootstrap: seeding probe config"
lighthouse-server target add --config "$CONFIG" \
    --name pg --address timescaledb --port 5432
lighthouse-server target add --config "$CONFIG" \
    --name dashboard --url https://server:8080/

# Plain assignment so a probe-list failure aborts the script (set -e) instead
# of vanishing into a pipeline and reading as "already seeded".
probes=$(lighthouse-server probe list --config "$CONFIG")

# Direct probes: every nyc agent TCPs the compose Postgres; every lon agent
# HTTPs the dashboard (self-signed cert, expects the placeholder page).
if ! printf '%s\n' "$probes" | grep -qE "tcp +nyc -> pg "; then
    lighthouse-server probe add --config "$CONFIG" \
        --site nyc --target pg --type tcp --interval 10s --timeout 5s
fi
if ! printf '%s\n' "$probes" | grep -qE "http +lon -> dashboard "; then
    lighthouse-server probe add --config "$CONFIG" \
        --site lon --target dashboard --type http --interval 15s --timeout 5s \
        --param http.insecure_skip_verify=true --param http.expect_status=200
fi

# Full mesh across all six fake sites. All seeded probes have working
# targets, so a fresh stack converges to an all-healthy board. (The old
# deliberate port-9 TCP mesh probe — CONN_REFUSED everywhere to keep a
# mixed-health board — is gone; inject failures per the M4 gate recipes
# when a broken board is wanted.)
lighthouse-server mesh create --config "$CONFIG" --name core
for site in nyc lon syd va co tx; do
    lighthouse-server mesh add --config "$CONFIG" --name core --site "$site"
done

# Retired seed: remove the port-9 probe from a dev DB that predates its
# retirement so reruns converge (agents drop it on the next 30 s config
# poll). probe list does not print params, so match the retired seed's full
# printed shape (tcp mesh:core 30s 5s enabled) and remove each match
# individually — a user-added TCP mesh probe with different settings is
# left alone (one that is column-for-column identical is indistinguishable
# without the DB; that limitation is accepted for a dev seed). probe rm
# only deletes the config — the probe's failure history and any still-open
# probe_failing events persist, so a stack that ran it stays mixed-health
# until `make reset && make up` (plain `make down -v` does NOT work: make
# consumes -v itself) or until the 24 h dashboards age the history out.
legacy=$(printf '%s\n' "$probes" \
    | awk '$2 == "tcp" && $3 == "mesh:core" && $4 == "30s" && $5 == "5s" && $6 == "true" { print $1 }')
for id in $legacy; do
    echo "bootstrap: removing retired port-9 TCP mesh probe $id"
    lighthouse-server probe rm --config "$CONFIG" --id "$id"
    echo "bootstrap: WARNING: its open incidents and 24 h failure history persist — run 'make reset && make up' for a clean all-healthy board"
done

# M4: ICMP mesh gives every ordered pair real RTT/loss/jitter (train of
# 10 × 200 ms = 2 s fits the 5 s timeout); traceroute mesh watches paths on
# a faster-than-prod 2 m cadence so the gate turns around quickly.
if ! printf '%s\n' "$probes" | grep -qE "icmp +mesh:core "; then
    lighthouse-server probe add --config "$CONFIG" \
        --mesh core --type icmp --interval 10s --timeout 5s
fi
if ! printf '%s\n' "$probes" | grep -qE "traceroute +mesh:core "; then
    lighthouse-server probe add --config "$CONFIG" \
        --mesh core --type traceroute --interval 2m --timeout 30s
fi

# DNS: Docker's embedded resolver answers compose service names.
lighthouse-server target add --config "$CONFIG" \
    --name resolver --address 127.0.0.11 --port 53
if ! printf '%s\n' "$probes" | grep -qE "dns +nyc -> resolver "; then
    lighthouse-server probe add --config "$CONFIG" \
        --site nyc --target resolver --type dns --interval 15s --timeout 5s \
        --param dns.qname=proxy --param dns.qtype=A
fi

# Map coordinates + display names so the Sightlines map is populated out of
# the box. site set is a plain update — reruns converge.
echo "bootstrap: seeding site coordinates"
lighthouse-server site set --config "$CONFIG" --name nyc --lat 40.7128 --lon -74.0060 --display-name "New York"
lighthouse-server site set --config "$CONFIG" --name lon --lat 51.5074 --lon -0.1278 --display-name "London"
lighthouse-server site set --config "$CONFIG" --name syd --lat -33.8688 --lon 151.2093 --display-name "Sydney"
lighthouse-server site set --config "$CONFIG" --name va --lat 39.0438 --lon -77.4874 --display-name "Virginia"
lighthouse-server site set --config "$CONFIG" --name co --lat 39.7392 --lon -104.9903 --display-name "Colorado"
lighthouse-server site set --config "$CONFIG" --name tx --lat 32.7767 --lon -96.7970 --display-name "Texas"

# Dev-only dashboard login (admin / lighthouse-dev). Piped stdin exercises
# user add's non-interactive mode; a rerun hits the unique username and is
# tolerated, any other failure aborts loudly.
echo "bootstrap: seeding dashboard admin user"
if ! out=$(printf 'lighthouse-dev' | lighthouse-server user add \
        --config "$CONFIG" --username admin --admin 2>&1); then
    case "$out" in
        *"already exists"*) echo "bootstrap: dashboard admin already exists" ;;
        *) echo "$out" >&2; exit 1 ;;
    esac
fi
echo "bootstrap: done"
