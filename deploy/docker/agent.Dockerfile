# lighthouse-agent DEV image (real deployments use the RPM on RHEL hosts).
# Build context is the repo root.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags '-s -w' \
    -o /out/lighthouse-agent ./cmd/lighthouse-agent

FROM alpine:3.22
COPY --from=build /out/lighthouse-agent /usr/local/bin/lighthouse-agent
COPY deploy/compose-dev/agent-entrypoint.sh /usr/local/bin/agent-entrypoint.sh
RUN chmod +x /usr/local/bin/agent-entrypoint.sh
ENTRYPOINT ["agent-entrypoint.sh"]
