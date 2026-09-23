SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c
CARGO_TARGET_DIR ?= $(CURDIR)/target
BIN := $(CURDIR)/bin
OAPI_CODEGEN := go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0
GO_PKGS := $(foreach d,mgmt gen bench deploy,$(if $(wildcard $(d)),./$(d)/...))

.PHONY: proto engine-test mgmt-test web-test e2e-build e2e lint build web-build webui-placeholder fuzz-smoke bench images operator-generate operator-test

proto:
	protoc -I proto \
	  --go_out=gen/go --go_opt=paths=source_relative \
	  --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative \
	  proto/nexora/control/v1/control.proto
	cd mgmt/api && $(OAPI_CODEGEN) -config oapi-codegen.yaml openapi.yaml
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
	NEXORA_E2E_BIN_DIR=$(BIN) go test -count=1 -timeout 60m ./e2e/... ./mgmt/internal/querylog/e2e/...

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

CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.20.1
ENVTEST_K8S ?= 1.34.x

operator-generate:
	cd operator && $(CONTROLLER_GEN) object paths=./api/...
	cd operator && $(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=../deploy/operator/crds
	mkdir -p deploy/helm/nexora-operator/crds && cp deploy/operator/crds/*.yaml deploy/helm/nexora-operator/crds/
	if [ -f operator/internal/mgmtapi/oapi-codegen.yaml ]; then cd operator/internal/mgmtapi && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml ../../../mgmt/api/openapi.yaml; fi
	if [ -f deploy/helm/nexora-operator/Chart.yaml ]; then helm template nexora-operator deploy/helm/nexora-operator --namespace nexora-operator --set rbac.scope=cluster --set createNamespace=true > deploy/operator/operator.yaml; fi

operator-test:
	cd operator && KUBEBUILDER_ASSETS="$$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.0 use $(ENVTEST_K8S) --bin-dir $(CURDIR)/bin/envtest -p path)" go test -race -count=1 ./...

# Ordinary supported Linux verification. Live kw/kernel experiments remain separate opt-ins.
.PHONY: ci-race ci-e2e ci-failover web-unit-test ci-coverage-test
ci-race:
	bash scripts/ci-prerequisites.sh race
	$(MAKE) webui-placeholder
	mkdir -p $(BIN)
	go build -o $(BIN)/nexora-fixture ./e2e/fixtures/cmd/nexora-fixture
	go test -json -race -count=1 -timeout 30m $(GO_PKGS) ./e2e/harness/...

ci-e2e:
	bash scripts/ci-prerequisites.sh linux
	cd web && pnpm install --frozen-lockfile && pnpm exec playwright install chromium
	bash scripts/ci-prerequisites.sh e2e
	$(MAKE) e2e-build
	NEXORA_E2E_BIN_DIR=$(BIN) go test -json -count=1 -timeout 120m ./e2e/... ./mgmt/internal/querylog/e2e/...

ci-failover:
	bash scripts/ci-prerequisites.sh contracts
	PYTHONDONTWRITEBYTECODE=1 python3 -O -m unittest discover -s deploy/failoverlab -v
	PYTHONDONTWRITEBYTECODE=1 python3 -O -m unittest discover -s deploy/failoverlab/crosshost -v
	PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest discover -s deploy/failover/platform -v
	PYTHONDONTWRITEBYTECODE=1 $(MAKE) -C deploy/failover/fence test

web-unit-test:
	cd web && pnpm install --frozen-lockfile && pnpm exec playwright test e2e/unit --output test-results/unit

ci-coverage-test:
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/ci-coverage-test.py
