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

# Full mesh across all three fake sites. Peers run no listener until the
# ICMP milestone, so these land as CONN_REFUSED/TIMEOUT rows — still real
# directional data exercising meshexpand end to end.
lighthouse-server mesh create --config "$CONFIG" --name core
for site in nyc lon syd; do
    lighthouse-server mesh add --config "$CONFIG" --name core --site "$site"
done
if ! printf '%s\n' "$probes" | grep -qE "tcp +mesh:core "; then
    lighthouse-server probe add --config "$CONFIG" \
        --mesh core --type tcp --interval 30s --timeout 5s --param port=9
fi
echo "bootstrap: done"
