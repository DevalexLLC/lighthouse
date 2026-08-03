#!/bin/sh
# Assemble the air-gapped control-plane install bundle: one docker-load
# image tarball (server, proxy, agent, timescaledb) + the production compose
# file + config examples + docs + RPMs (if built) + SHA256SUMS.
#
# Usage: deploy/release/build-bundle.sh <version> [arch]
#   version  image tag to bundle (e.g. v1.0.0); must exist locally or be
#            pullable from $REGISTRY
#   arch     amd64 (default) | arm64
#
# Offline-friendly: local images are used as-is; pulling only happens for
# images that are absent. Fails loudly rather than assembling a partial
# bundle.
set -eu

VERSION="${1:?usage: build-bundle.sh <version> [arch]}"
ARCH="${2:-amd64}"
REGISTRY="${REGISTRY:-ghcr.io/devalexllc}"
TSDB_IMAGE="timescale/timescaledb-ha:pg16-all"

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
NAME="lighthouse-${VERSION}-${ARCH}-bundle"
OUT="${ROOT}/dist/${NAME}"

if [ -e "$OUT" ] || [ -e "${OUT}.tar.gz" ]; then
    echo "error: ${OUT}(.tar.gz) already exists — remove it first (refusing to overwrite)" >&2
    exit 1
fi

IMAGES="${REGISTRY}/lighthouse-server:${VERSION} \
${REGISTRY}/lighthouse-agent:${VERSION} \
${REGISTRY}/lighthouse-proxy:${VERSION} \
${TSDB_IMAGE}"

for img in $IMAGES; do
    if docker image inspect "$img" >/dev/null 2>&1; then
        echo "bundle: using local image $img"
    else
        echo "bundle: pulling $img (linux/${ARCH})"
        docker pull --platform "linux/${ARCH}" "$img"
    fi
done

mkdir -p "${OUT}/images"
echo "bundle: docker save → images/lighthouse-images-${VERSION}-${ARCH}.tar"
# shellcheck disable=SC2086
docker save -o "${OUT}/images/lighthouse-images-${VERSION}-${ARCH}.tar" $IMAGES

cp "${ROOT}/deploy/compose/docker-compose.yml" "${OUT}/"
cp "${ROOT}/deploy/compose/server.example.yaml" "${OUT}/"
cp "${ROOT}/deploy/compose/env.example" "${OUT}/"
cp "${ROOT}/docs/install.md" "${OUT}/"
printf '%s\n' "$VERSION" > "${OUT}/VERSION"

if ls "${ROOT}"/dist/rpm/RPMS/*/lighthouse-agent-*.rpm >/dev/null 2>&1; then
    mkdir -p "${OUT}/rpms"
    cp "${ROOT}"/dist/rpm/RPMS/*/lighthouse-agent-*.rpm "${OUT}/rpms/"
    echo "bundle: included agent RPMs"
else
    echo "bundle: no RPMs under dist/rpm/RPMS — bundle ships without them (make rpm to include)"
fi

(cd "$OUT" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
tar -C "${ROOT}/dist" -czf "${OUT}.tar.gz" "$NAME"
echo "bundle: wrote ${OUT}.tar.gz"
sha256sum "${OUT}.tar.gz"
