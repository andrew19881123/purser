# Purser monorepo — root Makefile.
#
# Every toolchain is PROJECT-LOCAL under .toolchain/ (nothing is installed
# globally). This Makefile exports the toolchain environment from $(CURDIR)
# and invokes the tools by absolute path, so `make` works without first
# sourcing env.sh.

ROOT      := $(CURDIR)
TOOLCHAIN := $(ROOT)/.toolchain

export RUSTUP_HOME := $(TOOLCHAIN)/rustup
export CARGO_HOME  := $(TOOLCHAIN)/cargo
export GOROOT      := $(TOOLCHAIN)/go
export GOPATH      := $(TOOLCHAIN)/gopath
export GOBIN       := $(GOPATH)/bin
export GOMODCACHE  := $(GOPATH)/pkg/mod
export GOCACHE     := $(TOOLCHAIN)/gocache
export GOENV       := $(TOOLCHAIN)/goenv
export GOTOOLCHAIN := local
# Redirect the XDG base dirs so tools that ignore GO*/CARGO_* (Go telemetry,
# buf's module/plugin cache, ...) still write project-local, never into $HOME.
export XDG_CONFIG_HOME := $(TOOLCHAIN)/xdg/config
export XDG_CACHE_HOME  := $(TOOLCHAIN)/xdg/cache
export XDG_DATA_HOME   := $(TOOLCHAIN)/xdg/data
export PATH        := $(CARGO_HOME)/bin:$(GOROOT)/bin:$(GOBIN):$(PATH)

CARGO := $(CARGO_HOME)/bin/cargo
GO    := $(GOROOT)/bin/go
BUF   := $(GOBIN)/buf
NFPM  := $(GOBIN)/nfpm

RUST_MANIFEST := rust/Cargo.toml
GO_MODULES    := gen planner controlplane

.PHONY: all help setup gen build test lint fmt clean release package-agent demo demo-stop demo-seed demo-agent dev-agent dev status

all: gen build

help:
	@echo "Purser monorepo — make targets:"
	@echo "  make setup   Install the project-local toolchain into .toolchain/"
	@echo "  make gen     Regenerate Go code from the .proto contracts (buf)"
	@echo "  make build   Build the Rust workspace and every Go module"
	@echo "  make test    Run Rust and Go tests"
	@echo "  make lint    clippy (Rust) + go vet (Go)"
	@echo "  make fmt     rustfmt (Rust) + go fmt (Go)"
	@echo "  make clean   Remove build artifacts"
	@echo "  make demo    Start the demo stack (compose) — dashboard on :3000"
	@echo "  make demo-seed  Register a demo model in the running demo stack"
	@echo "  make demo-stop  Stop the demo stack"
	@echo "  make release Build stripped release binaries + stage dist/ (scripts/build-release.sh)"
	@echo "  make status  Show stack health (CP, fleet, catalog, deployments)"
	@echo "  make package-agent  Build the agent .deb + .rpm into dist/ (nfpm)"

setup:
	./tools/setup-toolchain.sh

gen:
	cd proto && "$(BUF)" generate

build:
	"$(CARGO)" build --manifest-path $(RUST_MANIFEST)
	@for m in $(GO_MODULES); do \
		echo ">> go build ./...  (go/$$m)"; \
		( cd go/$$m && "$(GO)" build ./... ) || exit 1; \
	done

test:
	"$(CARGO)" test --manifest-path $(RUST_MANIFEST)
	@for m in $(GO_MODULES); do \
		echo ">> go test ./...  (go/$$m)"; \
		( cd go/$$m && "$(GO)" test ./... ) || exit 1; \
	done

lint:
	"$(CARGO)" clippy --manifest-path $(RUST_MANIFEST) --workspace --all-targets -- -D warnings
	@for m in $(GO_MODULES); do \
		echo ">> go vet ./...  (go/$$m)"; \
		( cd go/$$m && "$(GO)" vet ./... ) || exit 1; \
	done

fmt:
	"$(CARGO)" fmt --manifest-path $(RUST_MANIFEST)
	@for m in $(GO_MODULES); do \
		( cd go/$$m && "$(GO)" fmt ./... ); \
	done

release:
	./scripts/build-release.sh

# Build the purser-agent native packages (.deb + .rpm) into dist/ with nfpm.
# Rebuilds the stripped release binary first (CARGO_INCREMENTAL=0: incremental
# artifacts only bloat target/ for a release build). Requires the project-local
# nfpm ($(NFPM)); install it once with:
#   GOBIN=$(GOBIN) $(GO) install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest
package-agent:
	@test -x "$(NFPM)" || { echo "error: nfpm not found at $(NFPM); install it with:"; \
		echo "  GOBIN=$(GOBIN) \"$(GO)\" install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest"; exit 1; }
	CARGO_INCREMENTAL=0 "$(CARGO)" build --release --manifest-path $(RUST_MANIFEST) -p purser-agent
	@mkdir -p dist
	"$(NFPM)" package -f packaging/nfpm/purser-agent.yaml -p deb -t dist/
	"$(NFPM)" package -f packaging/nfpm/purser-agent.yaml -p rpm -t dist/
	@echo ""
	@echo "Packages in dist/:"
	@ls -lh dist/purser-agent_$(shell sed -n 's/^version:[[:space:]]*//p' packaging/nfpm/purser-agent.yaml)_amd64.deb dist/purser-agent-$(shell sed -n 's/^version:[[:space:]]*//p' packaging/nfpm/purser-agent.yaml)-1.x86_64.rpm 2>/dev/null | awk '{print "  " $$9 "\t" $$5}'

clean:
	-"$(CARGO)" clean --manifest-path $(RUST_MANIFEST)
	@for m in $(GO_MODULES); do \
		( cd go/$$m && "$(GO)" clean -cache -testcache ./... 2>/dev/null || true ); \
	done

# Start the Purser demo stack (no GPU required).
#
# The compose stack publishes exactly ONE host port: the `proxy` service maps
# 3000:80. nginx (deploy/docker/demo-nginx.conf) then path-routes /api/ to the
# control plane (:8080) and /v1/ to the gateway (:8081) — both are
# container-internal only and are NOT published to the host. Every host-facing
# URL below therefore goes through :3000; printing :8080/:8081 here handed the
# user a connection-refused command at the moment of peak interest.
demo:
	docker compose up -d
	@echo ""
	@echo "Purser demo started!"
	@echo "  Dashboard:        http://localhost:3000"
	@echo "  Control Plane:    http://localhost:3000/api"
	@echo "  Gateway (OpenAI): http://localhost:3000/v1"
	@echo "  API Key: demo-key-12345"
	@echo ""
	@echo "Next: make demo-seed   # register a demo model in the catalog"
	@echo "Try:  curl http://localhost:3000/v1/models -H 'Authorization: Bearer demo-key-12345'"
	@echo "Stop: make demo-stop"

demo-stop:
	docker compose down

# Seed the demo catalog: register a small mock model, then try to deploy it.
# Idempotent — safe to re-run. Requires the `make demo` stack to be up.
#
# Note: the compose stack ships no agent, so the deploy step reports
# "awaiting a node" rather than reaching ACTIVE. tools/demo_seed.sh explains
# this in situ and prints the next step.
demo-seed:
	@./tools/demo_seed.sh

# Enroll a mock agent against the `make dev` NATIVE stack.
#
# NOTE: this targets `make dev` (control plane run natively, REST on :8080 and
# gRPC registration on :9443) and NOT the `make demo` compose stack — compose
# publishes neither :8080 nor :9443 to the host. It also needs ./bin/purser-agent,
# which `make build` produces. `dev-agent` is the accurate name; `demo-agent`
# is kept as an alias so existing muscle memory and docs keep working.
demo-agent dev-agent:
	@echo "Minting join token..."
	@TOKEN=$$(curl -s -X POST http://localhost:8080/api/v1/join-token \
	  -H 'Content-Type: application/json' -d '{"ttl_seconds":3600}' \
	  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])') && \
	echo "Join token: $$TOKEN" && \
	PURSER_CONTROL_PLANE_ADDR=http://localhost:9443 \
	PURSER_JOIN_TOKEN=$$TOKEN \
	./bin/purser-agent

## dev: Start local development stack (mock engine, no GPU required)
dev: build
	@echo "Starting Purser development stack (mock engine)..."
	@mkdir -p /tmp/purser-dev bin
	@cd go/controlplane && CGO_ENABLED=0 go build -o ../../bin/control-plane . 2>/dev/null || true
	@echo "Control Plane: http://localhost:8080"
	PURSER_DB=/tmp/purser-dev/registry.db \
	PURSER_ADDR=:8080 \
	PURSER_GRPC_ADDR=:9443 \
	PURSER_PKI_DIR=/tmp/purser-dev/pki \
	PURSER_ENGINE_BACKEND=mock \
	./bin/control-plane

## status: Show stack health (CP, fleet, catalog, deployments)
status:
	@./tools/purser-status.sh
