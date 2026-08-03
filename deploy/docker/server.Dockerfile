# lighthouse-server image. Build context is the repo root.
# Build is fully offline once base images are present (vendored deps only).
#
# The build stage runs on the BUILD platform and cross-compiles via
# GOOS/GOARCH — multi-arch buildx never emulates the Go compiler.
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
    -o /out/lighthouse-server ./cmd/lighthouse-server

FROM alpine:3.22
ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.title="lighthouse-server" \
      org.opencontainers.image.description="Lighthouse control plane (gRPC ingest + dashboard)" \
      org.opencontainers.image.source="https://github.com/devalexllc/lighthouse" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$COMMIT"
# Fixed uid so volume ownership survives image rebuilds. The state volume
# inherits this ownership on first create.
RUN adduser -S -D -H -u 10001 -s /sbin/nologin lighthouse \
    && mkdir -p /var/lib/lighthouse-server \
    && chown 10001 /var/lib/lighthouse-server
COPY --from=build /out/lighthouse-server /usr/local/bin/lighthouse-server
USER 10001
# /healthz is unauthenticated by contract (httpapi tests enforce it); the
# dashboard cert is typically self-signed or internal, hence no verify.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s \
    CMD wget -q --no-check-certificate -O /dev/null https://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["lighthouse-server"]
CMD ["serve", "--config", "/etc/lighthouse/server.yaml"]
