# lighthouse-server image. Build context is the repo root.
# Build is fully offline once base images are present (vendored deps only).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags '-s -w' \
    -o /out/lighthouse-server ./cmd/lighthouse-server

FROM alpine:3.22
COPY --from=build /out/lighthouse-server /usr/local/bin/lighthouse-server
ENTRYPOINT ["lighthouse-server"]
CMD ["serve", "--config", "/etc/lighthouse/server.yaml"]
