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

for site in nyc lon syd; do
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

# Full mesh across all three fake sites. The TCP mesh probes port 9
# (discard) — peers run no listener there, so those cells stay
# CONN_REFUSED, deliberately keeping a mixed-health board next to the
# green ICMP mesh.
lighthouse-server mesh create --config "$CONFIG" --name core
for site in nyc lon syd; do
    lighthouse-server mesh add --config "$CONFIG" --name core --site "$site"
done
if ! printf '%s\n' "$probes" | grep -qE "tcp +mesh:core "; then
    lighthouse-server probe add --config "$CONFIG" \
        --mesh core --type tcp --interval 30s --timeout 5s --param port=9
fi

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
