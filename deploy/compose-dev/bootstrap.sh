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
echo "bootstrap: done"
