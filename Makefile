SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c
CARGO_TARGET_DIR ?= $(CURDIR)/target
BIN := $(CURDIR)/bin
GO_PKGS := $(foreach d,mgmt gen bench deploy,$(if $(wildcard $(d)),./$(d)/...))

.PHONY: proto engine-test mgmt-test web-test e2e-build e2e lint build web-build webui-placeholder fuzz-smoke bench images

proto:
	protoc -I proto \
	  --go_out=gen/go --go_opt=paths=source_relative \
	  --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative \
	  proto/nexora/control/v1/control.proto
	cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml
	cd web && pnpm install --frozen-lockfile && pnpm run gen:api

engine-test:
	cargo test --locked -p nexora-engine --all-targets

webui-placeholder:
	@mkdir -p mgmt/internal/webui/dist
	@test -f mgmt/internal/webui/dist/index.html || echo '<!doctype html><title>Nexora</title>' > mgmt/internal/webui/dist/index.html

mgmt-test: webui-placeholder
	go test -race -count=1 $(GO_PKGS)

web-build:
	cd web && pnpm install --frozen-lockfile && pnpm run build
	rm -rf mgmt/internal/webui/dist && cp -r web/dist mgmt/internal/webui/dist

web-test:
	cd web && pnpm install --frozen-lockfile && pnpm run typecheck && pnpm run lint && pnpm run build

# web-build once web/ exists (Task 19); before that the embedded GUI is a placeholder page.
e2e-build: $(if $(wildcard web/package.json),web-build,webui-placeholder)
	cargo build --locked --release -p nexora-engine
	mkdir -p $(BIN)
	cp $(CARGO_TARGET_DIR)/release/nexora-engine $(BIN)/nexora-engine
	if [ -d mgmt/cmd/nexora-mgmt ]; then go build -o $(BIN)/nexora-mgmt ./mgmt/cmd/nexora-mgmt; fi
	if [ -d e2e/fixtures/cmd/nexora-fixture ]; then go build -o $(BIN)/nexora-fixture ./e2e/fixtures/cmd/nexora-fixture; fi
	if [ -d bench/cmd/perfgate ]; then go build -o $(BIN)/perfgate ./bench/cmd/perfgate; fi

e2e: e2e-build
	NEXORA_E2E_BIN_DIR=$(BIN) go test -count=1 -timeout 60m ./e2e/...

lint: webui-placeholder
	cargo fmt --all -- --check
	cargo clippy --locked -p nexora-engine --all-targets -- -D warnings
	cargo +nightly check --locked --manifest-path engine/fuzz/Cargo.toml --bins
	gofmt -l mgmt e2e bench | (! grep .)
	go vet ./...
	cd web && pnpm install --frozen-lockfile && pnpm run lint

build: web-build
	cargo build --locked --release -p nexora-engine
	mkdir -p $(BIN)
	go build -o $(BIN)/nexora-mgmt ./mgmt/cmd/nexora-mgmt

fuzz-smoke:
	cd engine/fuzz && cargo +nightly fuzz run parse_query -- -max_total_time=60

# The PR-tier performance gate run once against the working tree (dev pod): cache-hit dnsperf
# against a standalone engine; the result JSON lands in $(BIN)/perf.json.
bench:
	cargo build --locked --release -p nexora-engine
	mkdir -p $(BIN)
	cp $(CARGO_TARGET_DIR)/release/nexora-engine $(BIN)/nexora-engine
	go build -o $(BIN)/nexora-fixture ./e2e/fixtures/cmd/nexora-fixture
	go build -o $(BIN)/perfgate ./bench/cmd/perfgate
	$(BIN)/perfgate run --engine $(BIN)/nexora-engine --fixture $(BIN)/nexora-fixture --names 10000 --seconds 20 --workers 2 --out $(BIN)/perf.json

# Engine and management plane images on the kw BuildKit (laptop; arm64), tagged dev-<sha>[-dirty].
images:
	scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine .
	scripts/build-image.sh -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt .
