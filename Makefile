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

RUST_MANIFEST := rust/Cargo.toml
GO_MODULES    := gen planner controlplane

.PHONY: all help setup gen build test lint fmt clean release

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
	@echo "  make release Build stripped release binaries + stage dist/ (scripts/build-release.sh)"

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

clean:
	-"$(CARGO)" clean --manifest-path $(RUST_MANIFEST)
	@for m in $(GO_MODULES); do \
		( cd go/$$m && "$(GO)" clean -cache -testcache ./... 2>/dev/null || true ); \
	done
