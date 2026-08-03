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

# RPM naming: Version cannot contain '-', so the git describe output is
# sanitized (strip leading v, '-' → '.').
RPM_VERSION = $(shell echo '$(VERSION)' | sed 's/^v//; s/-/./g')
RPM_ARCHS  ?= x86_64 aarch64

.PHONY: all build server agent test lint vet proto web vendor up down reset logs ps seed clean rpm

all: build

build: server agent

server:
	$(GOBUILD) -o bin/lighthouse-server ./cmd/lighthouse-server

agent:
	$(GOBUILD) -o bin/lighthouse-agent ./cmd/lighthouse-agent

# Cross-compiled agent binaries for packaging (static, CGO off already).
agent-linux-%:
	GOOS=linux GOARCH=$* $(GOBUILD) -o bin/lighthouse-agent-linux-$* ./cmd/lighthouse-agent

# Agent RPMs for RHEL-family hosts. Packages the pre-built binary — no
# compilation inside rpmbuild, so this stays offline. On dev boxes without
# rpmbuild, compile on the host and run only the rpmbuild step in a
# container (Go is not needed inside):
#   make agent-linux-amd64
#   docker run --rm -v $$PWD:/src -w /src rockylinux:9 sh -c \
#     'dnf install -y -q rpm-build systemd-rpm-macros make && \
#      make rpm-build RPM_TARGET=x86_64 RPM_BINARY=bin/lighthouse-agent-linux-amd64'
rpm: $(RPM_ARCHS:%=rpm-arch-%)

rpm-arch-x86_64: agent-linux-amd64
	$(MAKE) rpm-build RPM_TARGET=x86_64 RPM_BINARY=bin/lighthouse-agent-linux-amd64

rpm-arch-aarch64: agent-linux-arm64
	$(MAKE) rpm-build RPM_TARGET=aarch64 RPM_BINARY=bin/lighthouse-agent-linux-arm64

# Internal: stage sources and invoke rpmbuild for one arch.
rpm-build:
	rm -rf dist/rpm/SOURCES-$(RPM_TARGET)
	mkdir -p dist/rpm/SOURCES-$(RPM_TARGET)
	cp $(RPM_BINARY) dist/rpm/SOURCES-$(RPM_TARGET)/lighthouse-agent
	cp packaging/systemd/lighthouse-agent.service packaging/rpm/agent.yaml LICENSE dist/rpm/SOURCES-$(RPM_TARGET)/
	rpmbuild -bb packaging/rpm/lighthouse-agent.spec \
		--target $(RPM_TARGET) \
		--define "lh_version $(RPM_VERSION)" \
		--define "lh_release 1" \
		--define "_topdir $(CURDIR)/dist/rpm" \
		--define "_sourcedir $(CURDIR)/dist/rpm/SOURCES-$(RPM_TARGET)"

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
	LIGHTHOUSE_DB_PASSWORD=$${LIGHTHOUSE_DB_PASSWORD:-lighthouse-dev} $(COMPOSE) down

# Full dev reset: tear down INCLUDING volumes so the next `make up` gets a
# fresh DB/CA/tokens. This is the "recreate dev DBs" step the docs call
# `down -v` — which plain `make down -v` cannot do (make eats -v as its
# own --version flag).
reset:
	LIGHTHOUSE_DB_PASSWORD=$${LIGHTHOUSE_DB_PASSWORD:-lighthouse-dev} $(COMPOSE) down -v

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
