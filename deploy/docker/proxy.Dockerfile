# lighthouse-proxy: stock nginx with the SNI-passthrough config baked in,
# so the production compose file and the air-gap bundle need no repo
# checkout for a bind mount. Build context is the repo root.
#
# TLS is NEVER terminated here (see the template). The routed gRPC SNI is
# configurable at runtime via LIGHTHOUSE_GRPC_SNI; the official entrypoint
# renders /etc/nginx/nginx.conf from the template at start. Operators with
# a fully custom config bind-mount their own /etc/nginx/nginx.conf AND an
# empty dir over /etc/nginx/templates (so the render doesn't overwrite it).
FROM nginx:1.29-alpine
ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.title="lighthouse-proxy" \
      org.opencontainers.image.description="Lighthouse edge proxy (SNI-based TCP passthrough, no TLS termination)" \
      org.opencontainers.image.source="https://github.com/devalexllc/lighthouse" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$COMMIT"
# Apache-2.0 §4(d): the NOTICE travels with every redistribution, images
# included. /licenses is the OCI convention.
# THIRD-PARTY-NOTICES covers the Go modules linked into server/agent, none of
# which ship here — carried anyway so NOTICE's "every release artifact" claim
# holds and all three images are identical in this respect.
COPY LICENSE NOTICE THIRD-PARTY-NOTICES /licenses/
COPY deploy/proxy/nginx.conf.template /etc/nginx/templates/nginx.conf.template
ENV NGINX_ENVSUBST_OUTPUT_DIR=/etc/nginx \
    LIGHTHOUSE_GRPC_SNI=grpc.lighthouse.local
