# Lighthouse build system.
# `make build` / `make test` are fully offline (vendored deps only).
# proto/web/vendor targets need dev tooling and are never part of a release build.

GO        ?= go
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS    = -s -w \
             -X github.com/devalexllc/lighthouse/internal/version.Version=$(VERSION) \
             -X github.com/devalexllc/lighthouse/internal/version.Commit=$(COMMIT)
GOBUILD    = CGO_ENABLED=0 $(GO) build -mod=vendor -trimpath -ldflags '$(LDFLAGS)'

COMPOSE_BASE = deploy/compose/docker-compose.yml
COMPOSE_DEV  = deploy/compose-dev/docker-compose.dev.yml
# Dev environments ALWAYS compose base + overlay together. Composing the base
# alone silently removes overlay services (fake agents, their enrollment state).
COMPOSE      = docker compose -f $(COMPOSE_BASE) -f $(COMPOSE_DEV)

.PHONY: all build server agent test lint vet proto web vendor up down logs ps seed clean

all: build

build: server agent

server:
	$(GOBUILD) -o bin/lighthouse-server ./cmd/lighthouse-server

agent:
	$(GOBUILD) -o bin/lighthouse-agent ./cmd/lighthouse-agent

test:
	CGO_ENABLED=0 $(GO) test -mod=vendor ./...

vet:
	$(GO) vet -mod=vendor ./...

lint: vet
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "staticcheck not installed; ran go vet only"

# ---- dev-time regeneration (network/tooling allowed; outputs are committed) ----

proto:
	$(GO) run -mod=vendor github.com/bufbuild/buf/cmd/buf generate 2>/dev/null || buf generate

web:
	cd web && npm ci && npm run build

vendor:
	$(GO) mod tidy
	$(GO) mod vendor

# ---- dev stack ----

# Dev default password; production sets LIGHTHOUSE_DB_PASSWORD explicitly.
up:
	LIGHTHOUSE_DB_PASSWORD=$${LIGHTHOUSE_DB_PASSWORD:-lighthouse-dev} $(COMPOSE) up -d --build

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100

ps:
	$(COMPOSE) ps

# Load 90 days of synthetic probe history for the aggregate/percentile
# pipeline (M5 gate). Needs the dev stack up with agents enrolled.
seed:
	LIGHTHOUSE_DB_PASSWORD=$${LIGHTHOUSE_DB_PASSWORD:-lighthouse-dev} $(COMPOSE) exec -T server lighthouse-server seed --config /etc/lighthouse/server.yaml --days 90

clean:
	rm -rf bin/
