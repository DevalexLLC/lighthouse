# lighthouse-agent images. Build context is the repo root.
#
# CAUTION — stage order makes `dev` the DEFAULT target (dev must follow
# release to build FROM it). Every consumer MUST name its target:
#   production/CI:  --target release
#   dev overlay:    build.target: dev
#
# The RPM remains the primary distribution for RHEL hosts; the release
# image serves container-native sites. Run it with cap_add: [NET_RAW]
# (required on runtimes whose default bounding set drops it, e.g. podman;
# the binary also carries the file capability for runtimes that honor it).
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG VERSION=dev
ARG COMMIT=none
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 \
    go build -mod=vendor -trimpath \
    -ldflags "-s -w \
      -X github.com/devalexllc/lighthouse/internal/version.Version=$VERSION \
      -X github.com/devalexllc/lighthouse/internal/version.Commit=$COMMIT" \
    -o /out/lighthouse-agent ./cmd/lighthouse-agent

FROM alpine:3.22 AS release
ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.title="lighthouse-agent" \
      org.opencontainers.image.description="Lighthouse site connectivity agent" \
      org.opencontainers.image.source="https://github.com/devalexllc/lighthouse" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$COMMIT"
COPY --from=build /out/lighthouse-agent /usr/local/bin/lighthouse-agent
# File capability grants raw ICMP (echo fallback + traceroute) to the
# non-root user; libcap is build-time only.
RUN apk add --no-cache libcap \
    && setcap cap_net_raw+ep /usr/local/bin/lighthouse-agent \
    && apk del libcap \
    && adduser -S -D -H -u 10001 -s /sbin/nologin lighthouse \
    && mkdir -p /var/lib/lighthouse-agent \
    && chown 10001 /var/lib/lighthouse-agent
USER 10001
ENTRYPOINT ["lighthouse-agent"]
CMD ["run", "--config", "/etc/lighthouse/agent.yaml"]

# Dev image for the compose overlay ONLY: root + iptables/iproute2 so the
# M4 gate can inject outages in-container, and an entrypoint that enrolls
# from the bootstrap token volume. Never published.
FROM release AS dev
USER root
RUN apk add --no-cache iptables iproute2
COPY deploy/compose-dev/agent-entrypoint.sh /usr/local/bin/agent-entrypoint.sh
RUN chmod +x /usr/local/bin/agent-entrypoint.sh
ENTRYPOINT ["agent-entrypoint.sh"]
