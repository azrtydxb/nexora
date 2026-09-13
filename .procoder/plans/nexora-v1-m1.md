# nexora-v1-m1 — implementation plan

Status: draft
Spec: .procoder/specs/nexora-v1.md

## Goal

Ship milestone M1 "Forwarder + fast path": a Linux Rust engine that forwards, caches, filters and exports telemetry without hot-path allocation, controlled over gRPC/mTLS by a stateless Go management plane with auth, audit, blocklists and query-log backends, a React GUI covering every M1 API operation, CI fuzz and perf gates, container images, and a verified first deployment on the kw cluster.

## Architecture

Everything follows `docs/architecture.md`: the engine is the Rust crate `nexora-engine` (per-core `current_thread` workers with `SO_REUSEPORT` sockets, `recvmmsg`/`sendmmsg`, an `ArcSwap<Runtime>` config, a wire-format `quick_cache` cache, `inflight::InFlight` coalescing and a separate `nexora-telemetry` thread), and the management plane is `github.com/piwi3910/nexora/mgmt` (pgx + goose, chi + oapi-codegen strict server, `EngineControl` gRPC server with `pg_notify('nexora_config', version)` fan-out). Engines dial out to the management plane per `proto/nexora/control/v1/control.proto`, and the GUI in `web/` is generated against `mgmt/api/openapi.yaml` and embedded in `nexora-mgmt`. All M1 acceptance tests are black-box Go tests in `e2e/` driving real binaries started by `e2e/harness` inside the dev pod on kw (`deploy/dev/`), with Playwright specs in `web/e2e/` invoked from Go wrappers.

## Constraints

From the spec (verbatim):

- DNS engine: Rust, hickory-proto used as wire codec only; server loop, cache
  and resolver are Nexora code.
- Platform: engine targets Linux only (free to use SO_REUSEPORT, recvmmsg,
  io_uring and other Linux-specific APIs). Management plane and GUI ship as
  Linux builds/containers.
- Deployment: multi-node from v1 — one management plane controls N engines
  across hosts; single-host deployment is the N=1 case of the same model.
- Management plane: Go.
- GUI: React on Vite (not Next.js).
- Performance (reference box: 8-core x86_64 Linux, 10GbE, dnsperf from a
  separate load host): cache-hit throughput >= 1,000,000 QPS; p99 latency
  < 500 us at sustained load; a > 5% regression against the recorded baseline
  fails the gate. This implies batched I/O (recvmmsg/sendmmsg or io_uring)
  from M1.
- Performance gate is two-tier: every PR runs a relative dnsperf regression
  benchmark on a CI runner (fails on > 5% drop vs main's baseline); the
  absolute 1M QPS / p99 < 500 us gate runs on a self-hosted reference box
  nightly and before every release tag.
- Management plane is stateless: all state in PostgreSQL, any number of
  instances behind a load balancer, engines may connect to any instance.
- Distribution: multi-arch (amd64, arm64) OCI container images for engine and
  management plane, a docker-compose example, and a Helm chart.
- Security: DNS upstream queries use random transaction IDs and random source
  ports and match responses on address + ID + question (fixes dns-c's
  poisoning hole); resolvers refuse recursion to clients outside configured
  ACLs by default; no default passwords (first admin set at install).
- The query path never logs synchronously, never touches a database, and never
  performs per-packet heap allocation on the cache-hit path.

From `docs/architecture.md` "Hot path rules" (verbatim):

- No logging, no allocation, no locks held across packets on the cache-hit
  path. Enforced by `cache_hit_path_does_not_allocate` (counting allocator).
- Config is read through `ArcSwap<Runtime>::load()` once per packet.
- Counters are per-worker `CachePadded<AtomicU64>` arrays summed at scrape.

Plan-wide rules every task inherits:

- Names, paths, modules, packages, env vars, metric names, API conventions and harness design are exactly those in `docs/architecture.md`; nothing is renamed.
- Pinned versions (exact): Rust 1.97, hickory-proto 0.26.3, tokio 1.53, socket2 0.6, tonic 0.14.6, prost 0.14.4, tonic-prost / tonic-prost-build 0.14, rustls 0.23.44, tokio-rustls 0.26.5, reqwest 0.13.5 (features `rustls`, `http2`), opentelemetry / opentelemetry_sdk / opentelemetry-otlp / opentelemetry-proto 0.32, prometheus-client 0.25.1, arc-swap 1.9, quick_cache 0.7, rustc-hash 2.1, bytes 1.12, libc 0.2.189, nix 0.31, thiserror 2, anyhow 1, serde 1, toml 1.1, siphasher 1.0, zstd 0.14, crossbeam-queue 0.3, crossbeam-utils 0.8, rand 0.10, parking_lot 0.12, webpki-roots 1.0, rcgen 0.14, hyper 1.11, hyper-util 0.1.20, clap 4.6, ipnet 2.12, sha2 0.11, hex 0.4, tempfile 3. Go 1.27, pgx v5.11.0, grpc v1.83.2, protobuf v1.36.12, chi v5.3.2, oapi-codegen v2.8.0 + runtime v1.7.0, goose v3.28.0, go-oidc v3.21.0, x/oauth2 v0.37.0, x/crypto v0.57.0, miekg/dns v1.1.73 (tests/fixtures only), opensearch-go v4.7.3, otel v1.46.0, otlp proto v1.11.0, klauspost/compress v1.20.0, prometheus client_golang v1.24.1. Node 26, pnpm 10, Vite 8.3, React 19.3, react-router 7.13, @tanstack/react-query 5.102, tailwindcss 4.3, openapi-typescript 7.13 + openapi-fetch 0.17, Radix primitives, lucide-react 1.45, recharts 3.10, @playwright/test 1.63. otelcol-contrib 0.160.0, OpenSearch 3.8.0, dnsperf 2.14.
- Every build and test command runs in the dev pod through `scripts/dev-exec.sh <cmd>`, which first syncs the working tree with `scripts/dev-sync.sh`. `git` commands run on the laptop.
- Every e2e test that asserts that something does not happen first asserts the positive path in the same run.
- Mutating HTTP requests send `Content-Type: application/json`; errors are `{"code": "...", "message": "..."}`; editable resources carry `revision` and a stale revision returns 409 `conflict`.

## Task 1: Dev pod, sync/exec scripts and Makefile

As built: the toolbox image, the pod manifest and the sync/exec scripts existed before this task (committed with the spec and architecture) and were verified working; this task reconciled them with the plan instead of recreating the pod. The Deployment is `toolbox` (container `toolbox`, label `app=toolbox`, PVC `work`), so every later `kubectl exec` uses `deploy/toolbox -c toolbox`.

Files:

- `deploy/dev/Dockerfile` (existing) — toolbox image: Rust 1.97 + nightly + cargo-fuzz, Go 1.27, Node 26 + pnpm 10, protoc + protoc-gen-go v1.36.12 + protoc-gen-go-grpc 1.6.2, oapi-codegen v2.8.0, PostgreSQL 17, dnsperf, bind9, otelcol-contrib 0.160.0, Playwright chromium
- `deploy/dev/dev-pod.yaml` (existing) — namespace `nexora-dev`, PVC `work` (100Gi `longhorn-single`), Deployment `toolbox`
- `scripts/dev-sync.sh` (existing, modified) — rsync of the working tree into `/work/nexora` over `kubectl exec`; pod-side build outputs excluded
- `scripts/dev-exec.sh` (existing, modified) — sync, then run a command in `/work/nexora` in the pod
- `Makefile` (create) — in-pod targets `proto`, `engine-test`, `mgmt-test`, `web-test`, `e2e-build`, `e2e`, `lint`, `build`, `web-build`, `webui-placeholder`, `fuzz-smoke`
- `scripts/dev-selftest.sh` (create) — asserts the pod has every toolchain M1 uses

Interfaces:

- Produces `scripts/dev-sync.sh` (no args; env `NEXORA_DEV_CONTEXT` default `kw`, `NEXORA_DEV_NAMESPACE` default `nexora-dev`) and `scripts/dev-exec.sh <cmd...>` (exit status of `<cmd>`), consumed by every later task. One argument is run as a shell string (`scripts/dev-exec.sh 'make lint && make engine-test'`); several arguments are shell-quoted word by word (`scripts/dev-exec.sh bash -c 'cd web && pnpm install'`). The command runs under non-login `bash -c`; the image `ENV` supplies `PATH`, `CARGO_TARGET_DIR=/work/target`, `GOCACHE`, `GOMODCACHE`, `PNPM_HOME`.
- In-pod files are copied back to the laptop with `kubectl --context kw -n nexora-dev exec deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - <paths> | tar -xf -`.
- Produces Makefile targets named above; `e2e-build` puts `nexora-engine`, `nexora-mgmt`, `nexora-fixture`, `perfgate` into `bin/` (the harness default `NEXORA_E2E_BIN_DIR`).
- Consumes image `192.168.10.131/azrtydxb/nexora-dev:toolbox-1` built from `deploy/dev/Dockerfile` with `scripts/build-image.sh -f Dockerfile -n nexora-dev -t toolbox-1 deploy/dev`.

- [x] Write the self-test `scripts/dev-selftest.sh`:

```bash
#!/usr/bin/env bash
# Runs inside the dev pod: fails unless every M1 toolchain is present at the pinned version.
set -euo pipefail
fail=0
check() { if ! out=$("$@" 2>&1); then
	echo "MISSING: $*"
	fail=1
else echo "ok: $* -> ${out%%$'\n'*}"; fi; }
check rustc --version
rustc --version | grep -q '^rustc 1\.97' || {
	echo "WRONG rustc"
	fail=1
}
check go version
go version | grep -q 'go1\.27' || {
	echo "WRONG go"
	fail=1
}
check node --version
node --version | grep -q '^v26\.' || {
	echo "WRONG node"
	fail=1
}
check pnpm --version
check protoc --version
check protoc-gen-go --version
check protoc-gen-go-grpc --version
check oapi-codegen --version
check initdb --version
check otelcol-contrib --version
command -v dnsperf >/dev/null || {
	echo "MISSING: dnsperf"
	fail=1
}
check cargo fuzz --help
check rsync --version
[ -d /work ] && [ -w /work ] || {
	echo "MISSING: writable /work"
	fail=1
}
df -BG /work | awk 'NR==2 { if ($2+0 < 90) { print "PVC too small: " $2; exit 1 } }' || fail=1
test "$(nproc)" -ge 7 || {
	echo "fewer than 7 CPUs: $(nproc)"
	fail=1
}
exit $fail
```

- [x] The pod already existed, so the "fails before the pod exists" run does not apply; the self-test is the guard against a regressed image.
- [x] `deploy/dev/dev-pod.yaml` is kept as committed (applied; pod `toolbox-*` running on kw). Compared with the originally planned manifest it names the Deployment `toolbox` and the PVC `work`, sets no `env` (the image `ENV` covers the cache dirs), adds `NET_ADMIN`/`NET_BIND_SERVICE`/`SYS_PTRACE` capabilities and a 2Gi memory `/dev/shm` for Chromium, and requests 4 CPU / 8Gi (limit 7 CPU / 16Gi). To change it: edit, then `kubectl --context kw apply -f deploy/dev/dev-pod.yaml && kubectl --context kw -n nexora-dev rollout status deploy/toolbox --timeout=10m`.
- [x] `scripts/dev-sync.sh`:

```bash
#!/usr/bin/env bash
# Mirror the working tree into the kw dev pod at /work/nexora (rsync over kubectl exec).
# The remote "host" is literally `rsync` and --rsync-path is empty, so the exec'd
# command becomes `rsync --server ...` inside the pod. Pod-side build outputs are
# excluded so --delete leaves them alone.
set -euo pipefail
ctx="${NEXORA_DEV_CONTEXT:-kw}"
ns="${NEXORA_DEV_NAMESPACE:-nexora-dev}"
root=$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)
kubectl --context "$ctx" -n "$ns" exec deploy/toolbox -c toolbox -- mkdir -p /work/nexora
rsync -a --delete --blocking-io \
	--exclude /.git/ --exclude target/ --exclude node_modules/ --exclude /web/dist/ \
	--exclude /bin/ --exclude /web/test-results/ --exclude /web/playwright-report/ \
	--exclude /mgmt/internal/webui/dist/ \
	--rsync-path= \
	-e "kubectl --context $ctx -n $ns exec -i deploy/toolbox -c toolbox --" \
	"$root/" rsync:/work/nexora/
```

Until `mgmt/internal/` exists on the laptop (Task 12), a sync after `make webui-placeholder` prints harmless `cannot delete non-empty directory: mgmt/...` warnings.

- [x] `scripts/dev-exec.sh`:

```bash
#!/usr/bin/env bash
# Sync, then run a command in the kw dev pod from /work/nexora (non-login bash).
#   scripts/dev-exec.sh 'make engine-test && make lint'   # one argument: a shell string
#   scripts/dev-exec.sh bash -c 'cd web && pnpm install'  # several arguments: quoted as words
set -euo pipefail
[ $# -gt 0 ] || {
	echo "usage: $0 <command...>" >&2
	exit 2
}
ctx="${NEXORA_DEV_CONTEXT:-kw}"
ns="${NEXORA_DEV_NAMESPACE:-nexora-dev}"
"$(dirname "$0")/dev-sync.sh"
if [ $# -eq 1 ]; then cmd="$1"; else cmd=$(printf '%q ' "$@"); fi
exec kubectl --context "$ctx" -n "$ns" exec -i deploy/toolbox -c toolbox -- bash -c "cd /work/nexora && $cmd"
```

- [x] Write `Makefile` (runs inside the dev pod):

```makefile
SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c
CARGO_TARGET_DIR ?= $(CURDIR)/target
BIN := $(CURDIR)/bin
GO_PKGS := ./mgmt/... ./gen/... ./bench/...

.PHONY: proto engine-test mgmt-test web-test e2e-build e2e lint build web-build webui-placeholder fuzz-smoke

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
	gofmt -l mgmt e2e bench | (! grep .)
	go vet ./...
	cd web && pnpm install --frozen-lockfile && pnpm run lint

build: web-build
	cargo build --locked --release -p nexora-engine
	mkdir -p $(BIN)
	go build -o $(BIN)/nexora-mgmt ./mgmt/cmd/nexora-mgmt

fuzz-smoke:
	cd engine/fuzz && cargo +nightly fuzz run parse_query -- -max_total_time=60
```

- [x] Run `scripts/dev-exec.sh bash scripts/dev-selftest.sh` — expect PASS: every line starts with `ok:` and exit status 0.
- [x] Run `scripts/dev-exec.sh 'make webui-placeholder && make -n e2e-build lint proto >/dev/null && echo parse-ok'` — expect `parse-ok`.
- [x] Commit: `git add scripts/dev-sync.sh scripts/dev-exec.sh scripts/dev-selftest.sh Makefile .procoder/plans/nexora-v1-m1.md && git commit -m "M1 Task 1: dev self-test, Makefile, sync/exec reconciled with the plan"`.

## Task 2: Repository skeleton, control proto and code generation

Files:

- `go.mod`, `go.sum` (create) — module `github.com/piwi3910/nexora`
- `Cargo.toml` (create) — workspace `members = ["engine"]`, `exclude = ["engine/fuzz"]`
- `Cargo.lock` (create, generated)
- `rust-toolchain.toml` (create) — channel `1.97`
- `engine/Cargo.toml` (create) — crate `nexora-engine`, all engine dependencies pinned
- `engine/build.rs` (create) — tonic-prost codegen from `proto/`
- `engine/src/lib.rs` (create) — module declarations only
- `engine/src/proto.rs` (create) — `tonic::include_proto!("nexora.control.v1")`
- `engine/src/main.rs` (create) — CLI entry stub (`--config`), filled in Task 9
- `proto/nexora/control/v1/control.proto` (create) — the engine <-> management plane contract
- `gen/go/nexora/control/v1/control.pb.go`, `gen/go/nexora/control/v1/control_grpc.pb.go` (create, generated, committed)
- `gen/go/nexora/control/v1/roundtrip_test.go` (create)
- `engine/tests/proto_roundtrip.rs` (create)

Interfaces:

- Proto package `nexora.control.v1`, Go import `controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"`, Rust path `nexora_engine::proto`.
- Service `EngineControl { rpc Enroll(EnrollRequest) returns (EnrollResponse); rpc Connect(stream EngineMessage) returns (stream ServerMessage); rpc GetBlob(GetBlobRequest) returns (stream BlobChunk); }`.
- Messages produced here and consumed by Tasks 7, 8, 9, 10, 13, 16, 18: `ConfigSnapshot`, `ResolverConfig`, `CacheConfig`, `Upstream`, `FilterConfig`, `BlobRef`, `TelemetryConfig`, `Hello`, `Applied`, `Rejected`, `Stats`, `UpstreamStatus`, `VersionAhead`, enums `UpstreamStrategy`, `UpstreamProtocol`, `BlockMode`.
- `engine/src/lib.rs` declares: `pub mod proto; pub mod bootstrap; pub mod wire; pub mod edns; pub mod clock; pub mod cache; pub mod acl; pub mod filter; pub mod upstream; pub mod inflight; pub mod runtime; pub mod server; pub mod control; pub mod snapshot; pub mod telemetry;` — each later task creates its module file; until then the declaration list contains only the modules that exist (this task: `pub mod proto;`).

- [ ] Write the failing Go test `gen/go/nexora/control/v1/roundtrip_test.go`:

```go
package controlv1_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

func TestConfigSnapshotRoundTrip(t *testing.T) {
	in := &controlv1.ConfigSnapshot{
		Version:  7,
		Resolver: &controlv1.ResolverConfig{Strategy: controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_FASTEST},
		Cache:    &controlv1.CacheConfig{MaxBytes: 1 << 26, MinTtl: 0, MaxTtl: 86400, NegativeMaxTtl: 3600, StaleWindow: 86400},
		Upstreams: []*controlv1.Upstream{{
			Id: "u1", Name: "quad9", Protocol: controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_DOT,
			Address: "9.9.9.9:853", TlsServerName: "dns.quad9.net", TimeoutMs: 250,
		}},
		AclAllowCidrs: []string{"10.0.0.0/8"},
		Filter: &controlv1.FilterConfig{
			Blocklists: []*controlv1.BlobRef{{Sha256: "ab", Size: 10, Name: "ads"}},
			BlockMode:  controlv1.BlockMode_BLOCK_MODE_NULL_IP, BlockTtl: 60,
		},
		Telemetry: &controlv1.TelemetryConfig{OtlpEndpoint: "http://otel:4317", TraceSampleOneIn: 1000, TraceSlowThresholdUs: 100000, QuerylogToManagement: true},
	}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out := &controlv1.ConfigSnapshot{}
	if err := proto.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round trip mismatch:\n in=%v\nout=%v", in, out)
	}
}
```

- [ ] Write the failing Rust test `engine/tests/proto_roundtrip.rs`:

```rust
use nexora_engine::proto::{ConfigSnapshot, Upstream, UpstreamProtocol};
use prost::Message;

#[test]
fn config_snapshot_round_trips() {
    let snap = ConfigSnapshot {
        version: 3,
        upstreams: vec![Upstream {
            id: "u1".into(),
            name: "fixture".into(),
            protocol: UpstreamProtocol::Udp as i32,
            address: "127.0.0.1:5300".into(),
            timeout_ms: 250,
            ..Default::default()
        }],
        acl_allow_cidrs: vec!["127.0.0.0/8".into()],
        ..Default::default()
    };
    let bytes = snap.encode_to_vec();
    let back = ConfigSnapshot::decode(bytes.as_slice()).unwrap();
    assert_eq!(snap, back);
}
```

- [ ] Run `scripts/dev-exec.sh go test ./gen/...` — expect FAIL with `go: cannot find main module` (no go.mod yet).
- [ ] Write `proto/nexora/control/v1/control.proto`:

```proto
syntax = "proto3";

package nexora.control.v1;

option go_package = "github.com/piwi3910/nexora/gen/go/nexora/control/v1;controlv1";

// EngineControl is served by every management plane instance on NEXORA_GRPC_LISTEN.
// Enroll uses server-auth TLS (engine pins the CA fingerprint from the join token);
// Connect and GetBlob require the engine's client certificate (CN = engine UUID).
service EngineControl {
  rpc Enroll(EnrollRequest) returns (EnrollResponse);
  rpc Connect(stream EngineMessage) returns (stream ServerMessage);
  rpc GetBlob(GetBlobRequest) returns (stream BlobChunk);
}

message EnrollRequest {
  string join_secret = 1;     // base32 secret part of nxj1.<secret>.<ca sha256>
  string node_name = 2;       // [a-z0-9-]{1,63}
  bytes csr_der = 3;          // PKCS#10, ECDSA P-256
  string engine_version = 4;
}

message EnrollResponse {
  string engine_id = 1;       // UUID, also the certificate CN
  bytes certificate_der = 2;
  bytes ca_certificate_der = 3;
}

message EngineMessage {
  oneof msg {
    Hello hello = 1;
    Applied applied = 2;
    Rejected rejected = 3;
    Stats stats = 4;
  }
}

message Hello {
  string engine_id = 1;
  string node_name = 2;
  uint64 applied_version = 3; // 0 when nothing applied
  string engine_version = 4;
}

message Applied {
  uint64 version = 1;
  string persist_error = 2;   // non-empty when snapshot.binpb could not be written
}

message Rejected {
  uint64 version = 1;
  string reason = 2;
}

message ServerMessage {
  oneof msg {
    ConfigSnapshot snapshot = 1;
    VersionAhead version_ahead = 2;
  }
}

// Sent when the engine reports a version newer than the database (restored backup).
message VersionAhead {
  uint64 server_version = 1;
}

message ConfigSnapshot {
  uint64 version = 1;
  int64 created_unix_ms = 2;
  ResolverConfig resolver = 3;
  CacheConfig cache = 4;
  repeated Upstream upstreams = 5;
  repeated string acl_allow_cidrs = 6;
  FilterConfig filter = 7;
  TelemetryConfig telemetry = 8;
}

enum UpstreamStrategy {
  UPSTREAM_STRATEGY_UNSPECIFIED = 0;
  UPSTREAM_STRATEGY_ORDERED = 1;
  UPSTREAM_STRATEGY_FASTEST = 2;
}

message ResolverConfig {
  UpstreamStrategy strategy = 1;
}

message CacheConfig {
  uint64 max_bytes = 1;         // >= 1048576
  uint32 min_ttl = 2;
  uint32 max_ttl = 3;
  uint32 negative_max_ttl = 4;
  uint32 stale_window = 5;
}

enum UpstreamProtocol {
  UPSTREAM_PROTOCOL_UNSPECIFIED = 0;
  UPSTREAM_PROTOCOL_UDP = 1;
  UPSTREAM_PROTOCOL_TCP = 2;
  UPSTREAM_PROTOCOL_DOT = 3;
  UPSTREAM_PROTOCOL_DOH = 4;
}

message Upstream {
  string id = 1;
  string name = 2;
  UpstreamProtocol protocol = 3;
  string address = 4;           // ip:port for UDP/TCP/DoT; empty for DoH
  string tls_server_name = 5;   // DoT SNI and certificate name
  string doh_url = 6;           // https URL for DoH
  uint32 timeout_ms = 7;        // 50..=5000
  string ca_certificate_pem = 8; // extra trust anchor for DoT/DoH; empty = webpki roots
}

enum BlockMode {
  BLOCK_MODE_UNSPECIFIED = 0;
  BLOCK_MODE_NULL_IP = 1;
  BLOCK_MODE_NXDOMAIN = 2;
  BLOCK_MODE_REFUSED = 3;
}

message BlobRef {
  string sha256 = 1;            // 64 lowercase hex of the zstd bytes
  uint64 size = 2;              // compressed size in bytes
  string name = 3;
}

message FilterConfig {
  repeated BlobRef blocklists = 1;
  repeated BlobRef allowlists = 2;
  BlockMode block_mode = 3;
  uint32 block_ttl = 4;
}

message TelemetryConfig {
  string otlp_endpoint = 1;          // gRPC OTLP endpoint for traces/metrics (and logs unless querylog_to_management)
  uint32 trace_sample_one_in = 2;    // 0 = head sampling off
  uint32 trace_slow_threshold_us = 3;
  bool querylog_to_management = 4;   // send OTLP logs to the connected management instance (builtin backend)
}

message GetBlobRequest {
  string sha256 = 1;
}

message BlobChunk {
  bytes data = 1;                    // <= 1 MiB
}

message UpstreamStatus {
  string id = 1;
  string name = 2;
  bool up = 3;
  uint32 rtt_us = 4;
  uint64 queries_total = 5;
  uint64 failures_total = 6;
}

message Stats {
  int64 unix_ms = 1;
  uint64 queries_total = 2;
  uint64 cache_hits_total = 3;
  uint64 cache_misses_total = 4;
  uint64 cache_stale_served_total = 5;
  uint64 filter_blocked_total = 6;
  uint64 servfail_total = 7;
  repeated uint64 duration_bucket_bounds_us = 8;
  repeated uint64 duration_bucket_counts = 9;   // cumulative, same length as bounds
  uint64 duration_sum_us = 10;
  repeated UpstreamStatus upstreams = 11;
  map<string, uint64> export_dropped_total = 12; // keyed by signal: logs, traces, metrics
  uint64 cache_entries = 13;
  uint64 cache_bytes = 14;
}
```

- [ ] Write `go.mod`:

```
module github.com/piwi3910/nexora

go 1.27

require (
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/go-chi/chi/v5 v5.3.2
	github.com/jackc/pgx/v5 v5.11.0
	github.com/klauspost/compress v1.20.0
	github.com/miekg/dns v1.1.73
	github.com/oapi-codegen/runtime v1.7.0
	github.com/opensearch-project/opensearch-go/v4 v4.7.3
	github.com/pressly/goose/v3 v3.28.0
	github.com/prometheus/client_golang v1.24.1
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/proto/otlp v1.11.0
	golang.org/x/crypto v0.57.0
	golang.org/x/oauth2 v0.37.0
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
)
```

- [ ] Write root `Cargo.toml` and `rust-toolchain.toml`:

```toml
# Cargo.toml
[workspace]
resolver = "3"
members = ["engine"]
exclude = ["engine/fuzz"]

[profile.release]
lto = "fat"
codegen-units = 1
panic = "abort"
debug = "line-tables-only"
```

```toml
# rust-toolchain.toml
[toolchain]
channel = "1.97"
components = ["clippy", "rustfmt"]
```

- [ ] Write `engine/Cargo.toml`:

```toml
[package]
name = "nexora-engine"
version = "0.1.0"
edition = "2024"
rust-version = "1.97"
publish = false

[lib]
path = "src/lib.rs"

[[bin]]
name = "nexora-engine"
path = "src/main.rs"

[dependencies]
hickory-proto = { version = "=0.26.3", default-features = false, features = ["std"] }
tokio = { version = "=1.53", features = ["rt", "rt-multi-thread", "net", "time", "sync", "macros", "io-util", "signal", "fs"] }
socket2 = { version = "0.6", features = ["all"] }
tonic = { version = "=0.14.6", features = ["tls-ring"] }
tonic-prost = "0.14"
prost = "=0.14.4"
rustls = { version = "=0.23.44", default-features = false, features = ["ring", "std", "tls12"] }
tokio-rustls = { version = "=0.26.5", default-features = false, features = ["ring"] }
reqwest = { version = "=0.13.5", default-features = false, features = ["rustls", "http2"] }
opentelemetry-proto = { version = "0.32", features = ["gen-tonic", "logs", "trace", "metrics"] }
prometheus-client = "=0.25.1"
arc-swap = "1.9"
quick_cache = "0.7"
rustc-hash = "2.1"
bytes = "1.12"
libc = "=0.2.189"
nix = { version = "0.31", features = ["socket", "uio", "net", "signal"] }
thiserror = "2"
anyhow = "1"
serde = { version = "1", features = ["derive"] }
toml = "1.1"
siphasher = "1.0"
zstd = "0.14"
crossbeam-queue = "0.3"
crossbeam-utils = "0.8"
rand = "0.10"
parking_lot = "0.12"
webpki-roots = "1.0"
rcgen = { version = "0.14", features = ["pem", "x509-parser"] }
hyper = { version = "1.11", features = ["server", "http1"] }
hyper-util = { version = "=0.1.20", features = ["tokio"] }
clap = { version = "4.6", features = ["derive"] }
ipnet = "2.12"
sha2 = "0.11"
hex = "0.4"

[dev-dependencies]
tempfile = "3"
hickory-proto = { version = "=0.26.3", default-features = false, features = ["std"] }

[build-dependencies]
tonic-prost-build = "0.14"
```

- [ ] Write `engine/build.rs`:

```rust
fn main() -> Result<(), Box<dyn std::error::Error>> {
    println!("cargo:rerun-if-changed=../proto/nexora/control/v1/control.proto");
    tonic_prost_build::configure()
        .build_server(true)
        .build_client(true)
        .compile_protos(&["../proto/nexora/control/v1/control.proto"], &["../proto"])?;
    Ok(())
}
```

- [ ] Write `engine/src/proto.rs` as `tonic::include_proto!("nexora.control.v1");`, `engine/src/lib.rs` as `pub mod proto;`, and `engine/src/main.rs` as a clap stub: `#[derive(clap::Parser)] struct Args { #[arg(long, default_value = "/etc/nexora/engine.toml")] config: std::path::PathBuf }` with `fn main() { let _args = <Args as clap::Parser>::parse(); }`.
- [ ] Generate Go code and lock files: `scripts/dev-exec.sh bash -c 'protoc -I proto --go_out=gen/go --go_opt=paths=source_relative --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative proto/nexora/control/v1/control.proto && go mod tidy && cargo generate-lockfile'`, then copy the generated files back: `kubectl --context kw -n nexora-dev exec deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - go.sum Cargo.lock gen/go | tar -xf -` (the full `make proto` target also runs oapi-codegen and openapi-typescript, whose inputs arrive in Tasks 15 and 19; this step runs protoc directly).
- [ ] Run `scripts/dev-exec.sh go test ./gen/... && scripts/dev-exec.sh cargo test --locked -p nexora-engine --test proto_roundtrip` — expect PASS: `ok github.com/piwi3910/nexora/gen/go/nexora/control/v1` and `test config_snapshot_round_trips ... ok`.
- [ ] Commit: `git add go.mod go.sum Cargo.toml Cargo.lock rust-toolchain.toml engine proto gen && git commit -m "proto: EngineControl contract, Go/Rust codegen and repository skeleton"`.

## Task 3: Zero-copy query parser, EDNS/cookies, and the fuzz workflow

Files:

- `engine/src/wire.rs` (create) — `parse_query`, `NameKey`, response writers, RR walker
- `engine/src/edns.rs` (create) — OPT parsing/writing, DNS cookies (RFC 7873/9018), size limits
- `engine/src/lib.rs` (modify) — add `pub mod wire; pub mod edns;`
- `engine/fuzz/Cargo.toml` (create) — cargo-fuzz crate `nexora-engine-fuzz`
- `engine/fuzz/fuzz_targets/parse_query.rs` (create) — fuzz target
- `engine/fuzz/corpus/parse_query/` (create) — seed queries (`a.bin`, `edns_cookie.bin`, `compressed_name.bin`, `long_name.bin`) written by the test in this task
- `.github/workflows/fuzz.yml` (create) — 1 hour `cargo fuzz run parse_query`

Interfaces (produced; consumed by Tasks 4, 5, 7, 8):

- `pub struct NameKey { len: u8, buf: [u8; 255] }` — lowercase uncompressed wire name; `impl NameKey { pub fn as_wire(&self) -> &[u8]; pub fn from_wire_lowercase(wire: &[u8]) -> Option<NameKey> }`; derives `Clone, Copy, PartialEq, Eq, Hash`.
- `pub struct QueryView<'a> { pub id: u16, pub flags: u16, pub qname: &'a [u8], pub key: NameKey, pub qtype: u16, pub qclass: u16, pub question_end: usize, pub opt: Option<edns::OptView<'a>> }` with `pub fn rd(&self) -> bool; pub fn cd(&self) -> bool; pub fn do_bit(&self) -> bool`.
- `#[derive(Debug, PartialEq, Eq, thiserror::Error)] pub enum ParseError { #[error("short")] TooShort, #[error("formerr")] FormErr, #[error("notimp")] NotImp, #[error("response")] IsResponse }`.
- `pub fn parse_query(buf: &[u8]) -> Result<QueryView<'_>, ParseError>`.
- `pub fn write_error_reply(query: &[u8], rcode: u8, out: &mut [u8]) -> Option<usize>` — header-only reply echoing ID/opcode/RD (question echoed when it parses); `None` for `< 12` bytes.
- `pub fn write_rcode_reply(q: &QueryView<'_>, rcode: u8, out: &mut [u8], opt: Option<&edns::ReplyOpt>) -> usize`.
- `pub fn write_synth_reply(q: &QueryView<'_>, rcode: u8, answer: Option<SynthAnswer>, ttl: u32, out: &mut [u8], opt: Option<&edns::ReplyOpt>) -> usize` with `pub enum SynthAnswer { A([u8; 4]), Aaaa([u8; 16]) }`.
- `pub struct ResponseInfo { pub rcode: u8, pub tc: bool, pub min_ttl: Option<u32>, pub negative_ttl: Option<u32>, pub ttl_offsets: Vec<u16>, pub opt_range: Option<std::ops::Range<usize>>, pub cname_targets: Vec<NameKey>, pub ancount: u16 }` and `pub fn walk_response(msg: &[u8], expect: &QueryView<'_>) -> Result<ResponseInfo, ParseError>` (miss path; allocation allowed).
- `pub fn question_matches(msg: &[u8], qname_key: &NameKey, qtype: u16, qclass: u16) -> bool`.
- `edns.rs`: `pub struct OptView<'a> { pub udp_size: u16, pub version: u8, pub do_bit: bool, pub client_cookie: Option<[u8; 8]>, pub server_cookie: Option<&'a [u8]>, pub bad_cookie_len: bool }`; `pub struct ReplyOpt { pub udp_size: u16, pub do_bit: bool, pub ext_rcode: u8, pub cookie: Option<([u8; 8], [u8; 16])> }`; `pub struct CookieSecret(pub [u8; 16])`; `pub fn server_cookie(secret: &CookieSecret, client_cookie: &[u8; 8], client: std::net::IpAddr, now_secs: u32) -> [u8; 16]`; `pub fn write_opt(out: &mut [u8], opt: &ReplyOpt) -> usize` (returns 0 when `out` is too small); `pub const OPT_BASE_LEN: usize = 11; pub const OPT_COOKIE_LEN: usize = 4 + 24;`; `pub enum Transport { Udp, Tcp }`; `pub fn reply_limit(opt: Option<&OptView<'_>>, transport: Transport) -> usize`.

- [ ] Create `engine/src/wire.rs` containing only this test module (add `pub mod wire; pub mod edns;` to `lib.rs` now so the tests compile against the missing items):

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{Name, RecordType};
    use hickory_proto::serialize::binary::BinEncodable;

    fn query(name: &str, rtype: RecordType) -> Vec<u8> {
        let mut m = Message::new();
        m.set_id(0xbeef).set_message_type(MessageType::Query).set_op_code(OpCode::Query).set_recursion_desired(true);
        m.add_query(Query::query(Name::from_ascii(name).unwrap(), rtype));
        m.to_bytes().unwrap()
    }

    #[test]
    fn parses_simple_query_and_lowercases_key() {
        let q = query("WwW.Example.COM.", RecordType::AAAA);
        let v = parse_query(&q).unwrap();
        assert_eq!(v.id, 0xbeef);
        assert_eq!(v.qtype, 28);
        assert_eq!(v.qclass, 1);
        assert_eq!(v.key.as_wire(), b"\x03www\x07example\x03com\x00");
        assert_eq!(&v.qname[1..4], b"WwW");
        assert!(v.rd());
        assert!(v.opt.is_none());
    }

    #[test]
    fn short_packet_is_too_short() {
        assert_eq!(parse_query(&[0u8; 11]).unwrap_err(), ParseError::TooShort);
    }

    #[test]
    fn response_bit_is_rejected() {
        let mut q = query("a.", RecordType::A);
        q[2] |= 0x80;
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::IsResponse);
    }

    #[test]
    fn unknown_opcode_is_notimp() {
        let mut q = query("a.", RecordType::A);
        q[2] = (q[2] & 0x87) | (4 << 3); // NOTIFY
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::NotImp);
    }

    #[test]
    fn compression_pointer_in_question_is_formerr() {
        let mut q = vec![0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0];
        q.extend_from_slice(&[0xc0, 0x0c, 0, 1, 0, 1]);
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::FormErr);
    }

    #[test]
    fn label_over_63_and_name_over_255_are_formerr() {
        let mut q = vec![0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 64];
        q.extend_from_slice(&[b'a'; 64]);
        q.extend_from_slice(&[0, 0, 1, 0, 1]);
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::FormErr);
        let mut q = vec![0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0];
        for _ in 0..5 { q.push(63); q.extend_from_slice(&[b'b'; 63]); }
        q.extend_from_slice(&[0, 0, 1, 0, 1]);
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::FormErr);
    }

    #[test]
    fn counts_exceeding_packet_are_formerr() {
        let mut q = query("a.", RecordType::A);
        q[11] = 1; // ARCOUNT=1 but no additional record present
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::FormErr);
        let mut q = query("a.", RecordType::A);
        q[5] = 2; // QDCOUNT=2
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::FormErr);
    }

    #[test]
    fn escaped_dot_and_binary_label_round_trip_in_key() {
        let mut q = vec![0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0];
        q.extend_from_slice(&[3, b'a', b'.', 0xff, 3, b'c', b'o', b'm', 0, 0, 1, 0, 1]);
        let v = parse_query(&q).unwrap();
        assert_eq!(v.key.as_wire(), &[3, b'a', b'.', 0xff, 3, b'c', b'o', b'm', 0]);
    }

    #[test]
    fn edns_opt_with_cookie_is_parsed() {
        let mut m = Message::from_bytes(&query("example.com.", RecordType::A)).unwrap();
        let mut e = Edns::new();
        e.set_max_payload(4096).set_dnssec_ok(true);
        e.options_mut().insert(hickory_proto::rr::rdata::opt::EdnsOption::Unknown(10, vec![1, 2, 3, 4, 5, 6, 7, 8]));
        m.set_edns(e);
        let bytes = m.to_bytes().unwrap();
        let v = parse_query(&bytes).unwrap();
        let opt = v.opt.unwrap();
        assert_eq!(opt.udp_size, 4096);
        assert!(opt.do_bit);
        assert_eq!(opt.client_cookie, Some([1, 2, 3, 4, 5, 6, 7, 8]));
    }

    #[test]
    fn formerr_reply_echoes_id() {
        let mut q = query("a.", RecordType::A);
        q[5] = 2;
        let mut out = [0u8; 512];
        let n = write_error_reply(&q, 1, &mut out).unwrap();
        assert!(n >= 12);
        assert_eq!(&out[0..2], &[0xbe, 0xef]);
        assert_eq!(out[2] & 0x80, 0x80);
        assert_eq!(out[3] & 0x0f, 1);
        assert_eq!(write_error_reply(&q[..11], 1, &mut out), None);
    }

    #[test]
    fn walk_response_collects_ttl_offsets_and_negative_ttl() {
        use hickory_proto::rr::{rdata::SOA, RData, Record};
        let qb = query("nx.example.com.", RecordType::A);
        let qv = parse_query(&qb).unwrap();
        let mut r = Message::from_bytes(&qb).unwrap();
        r.set_message_type(MessageType::Response).set_response_code(hickory_proto::op::ResponseCode::NXDomain);
        let soa = SOA::new(Name::from_ascii("ns.example.com.").unwrap(), Name::from_ascii("h.example.com.").unwrap(), 1, 2, 3, 4, 300);
        r.add_name_server(Record::from_rdata(Name::from_ascii("example.com.").unwrap(), 900, RData::SOA(soa)));
        let bytes = r.to_bytes().unwrap();
        let info = walk_response(&bytes, &qv).unwrap();
        assert_eq!(info.rcode, 3);
        assert_eq!(info.negative_ttl, Some(300));
        assert_eq!(info.ttl_offsets.len(), 1);
        let off = info.ttl_offsets[0] as usize;
        assert_eq!(u32::from_be_bytes(bytes[off..off + 4].try_into().unwrap()), 900);
    }

    #[test]
    fn walk_response_rejects_pointer_loop() {
        let qb = query("a.", RecordType::A);
        let qv = parse_query(&qb).unwrap();
        let mut r = qb.clone();
        r[2] |= 0x80;
        r[7] = 1; // ANCOUNT=1
        let loop_at = r.len();
        r.extend_from_slice(&[0xc0, loop_at as u8, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 1, 2, 3, 4]);
        assert_eq!(walk_response(&r, &qv).unwrap_err(), ParseError::FormErr);
    }
}
```

- [ ] Write the failing tests at the bottom of `engine/src/edns.rs`:

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use std::net::{IpAddr, Ipv4Addr};

    #[test]
    fn reply_limit_rules() {
        assert_eq!(reply_limit(None, Transport::Udp), 512);
        let big = OptView { udp_size: 4096, version: 0, do_bit: false, client_cookie: None, server_cookie: None, bad_cookie_len: false };
        assert_eq!(reply_limit(Some(&big), Transport::Udp), 1232);
        let small = OptView { udp_size: 100, ..big };
        assert_eq!(reply_limit(Some(&small), Transport::Udp), 512);
        let mid = OptView { udp_size: 1000, ..big };
        assert_eq!(reply_limit(Some(&mid), Transport::Udp), 1000);
        assert_eq!(reply_limit(None, Transport::Tcp), 65535);
    }

    #[test]
    fn server_cookie_is_deterministic_per_client_and_changes_with_ip() {
        let s = CookieSecret([7; 16]);
        let c = [1u8; 8];
        let a = server_cookie(&s, &c, IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1)), 1_700_000_000);
        let b = server_cookie(&s, &c, IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1)), 1_700_000_000);
        let d = server_cookie(&s, &c, IpAddr::V4(Ipv4Addr::new(10, 0, 0, 2)), 1_700_000_000);
        assert_eq!(a, b);
        assert_ne!(a, d);
        assert_eq!(a[0], 1, "RFC 9018 version byte");
        assert_eq!(&a[4..8], &1_700_000_000u32.to_be_bytes());
    }

    #[test]
    fn write_opt_with_cookie_has_expected_length() {
        let mut out = [0u8; 64];
        let n = write_opt(&mut out, &ReplyOpt { udp_size: 1232, do_bit: true, ext_rcode: 0, cookie: Some(([1; 8], [2; 16])) });
        assert_eq!(n, OPT_BASE_LEN + OPT_COOKIE_LEN);
        assert_eq!(out[0], 0);
        assert_eq!(&out[1..3], &41u16.to_be_bytes());
        assert_eq!(&out[3..5], &1232u16.to_be_bytes());
        assert_eq!(out[7] & 0x80, 0x80, "DO bit");
        assert_eq!(write_opt(&mut out[..5], &ReplyOpt { udp_size: 1232, do_bit: false, ext_rcode: 0, cookie: None }), 0);
    }
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib wire:: edns::` — expect FAIL with `cannot find function `parse_query` in this scope`.
- [ ] Implement `engine/src/edns.rs`: `Transport` enum; `parse_opt(rr: &[u8]) -> Result<(OptView<'_>, usize), ParseError>` requiring owner name `0`, type 41, rdlength within the buffer; iterates options, option code 10 (COOKIE) with length 8 sets `client_cookie`, length 16..=40 also sets `server_cookie` to the trailing bytes, any other cookie length sets `bad_cookie_len = true`; `reply_limit`: TCP returns 65535; UDP without OPT 512; with OPT `udp_size.clamp(512, 1232)`; `server_cookie` per RFC 9018: bytes `[1, 0, 0, 0]`, then `now_secs` big-endian, then the 8-byte SipHash-2-4 (`siphasher::sip::SipHasher24::new_with_key(&secret.0)`) over `client_cookie || version/reserved || timestamp || client IP octets`; `write_opt` writes root name, TYPE 41, CLASS=`udp_size`, TTL=`ext_rcode<<24 | version 0 | DO<<15`, RDLENGTH, and the COOKIE option (code 10, length 24) when `cookie` is `Some`.
- [ ] Implement `engine/src/wire.rs`: constants `pub const RCODE_NOERROR: u8 = 0; RCODE_FORMERR: u8 = 1; RCODE_SERVFAIL: u8 = 2; RCODE_NXDOMAIN: u8 = 3; RCODE_NOTIMP: u8 = 4; RCODE_REFUSED: u8 = 5;`. `parse_query` order: `len < 12` -> `TooShort`; QR=1 -> `IsResponse`; opcode != 0 -> `NotImp`; QDCOUNT != 1 or ANCOUNT != 0 or NSCOUNT != 0 or ARCOUNT > 1 -> `FormErr`; walk question labels without allocation: a length byte `& 0xC0 != 0` -> `FormErr`, label length > 63 -> `FormErr`, running total including length bytes and root > 255 -> `FormErr`, running past the buffer -> `FormErr`; copy each byte into `NameKey.buf` with `u8::to_ascii_lowercase` (only `A-Z` change, so binary octets and escaped dots are preserved); read QTYPE/QCLASS (missing -> `FormErr`); when ARCOUNT=1 call `edns::parse_opt` on the remainder (non-OPT additional record or trailing garbage after it -> `FormErr`); trailing bytes when ARCOUNT=0 -> `FormErr`. `write_error_reply` copies ID, sets QR, keeps opcode and RD, sets RA, rcode, and when the question walk of the query succeeds copies the question with QDCOUNT=1, otherwise QDCOUNT=0. `walk_response` follows compression pointers only backwards (target offset < pointer offset), with at most 128 pointer hops per name and names capped at 255 octets; for every RR in answer/authority/additional it records the TTL field offset (skipping the OPT RR, whose range goes into `opt_range`), tracks the minimum TTL of answer RRs, sets `negative_ttl = min(SOA TTL, SOA MINIMUM)` from an authority SOA when ANCOUNT=0 or rcode=NXDOMAIN, and collects CNAME targets (lowercased `NameKey`) from the answer section; RDLENGTH past the end -> `FormErr`. `question_matches` compares the response question to `qname_key` case-insensitively plus type and class. `write_synth_reply` writes the header (QR, RD copied, RA, AA=0, rcode), the question copied from the query bytes (client casing), an answer RR using a compression pointer `0xC00C` when `answer` is `Some`, then the OPT from `opt`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib wire:: edns::` — expect PASS: `test result: ok. 15 passed`.
- [ ] Write `engine/fuzz/Cargo.toml`:

```toml
[package]
name = "nexora-engine-fuzz"
version = "0.0.0"
publish = false
edition = "2024"

[package.metadata]
cargo-fuzz = true

[dependencies]
libfuzzer-sys = "0.4"
nexora-engine = { path = ".." }

[[bin]]
name = "parse_query"
path = "fuzz_targets/parse_query.rs"
test = false
doc = false
bench = false
```

- [ ] Write `engine/fuzz/fuzz_targets/parse_query.rs`:

```rust
#![no_main]
use libfuzzer_sys::fuzz_target;
use nexora_engine::{edns, wire};

fuzz_target!(|data: &[u8]| {
    let mut out = [0u8; 1232];
    match wire::parse_query(data) {
        Ok(q) => {
            let opt = q.opt.as_ref().map(|o| edns::ReplyOpt { udp_size: 1232, do_bit: o.do_bit, ext_rcode: 0, cookie: None });
            let _ = wire::write_rcode_reply(&q, wire::RCODE_SERVFAIL, &mut out, opt.as_ref());
            let _ = wire::write_synth_reply(&q, wire::RCODE_NOERROR, Some(wire::SynthAnswer::A([0; 4])), 60, &mut out, opt.as_ref());
            // Treat the same bytes as an upstream response for this question.
            let _ = wire::walk_response(data, &q);
        }
        Err(_) => {
            let _ = wire::write_error_reply(data, wire::RCODE_FORMERR, &mut out);
        }
    }
});
```

- [ ] Add a seed-corpus writer test in `engine/src/wire.rs` tests: `#[test] #[ignore] fn write_fuzz_seeds()` writing `query("example.com.", A)`, the EDNS-cookie query, the compressed-name packet and a 255-octet name into `concat!(env!("CARGO_MANIFEST_DIR"), "/fuzz/corpus/parse_query/")` as `a.bin`, `edns_cookie.bin`, `compressed_name.bin`, `long_name.bin`; run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib write_fuzz_seeds -- --ignored` and copy the four files back with `kubectl --context kw -n nexora-dev exec deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - engine/fuzz/corpus | tar -xf -`.
- [ ] Run the fuzz smoke locally in the pod: `scripts/dev-exec.sh make fuzz-smoke` — expect PASS: libFuzzer prints `Done` with no `crash-` artifact.
- [ ] Write `.github/workflows/fuzz.yml`:

```yaml
name: fuzz
on:
  schedule:
    - cron: "17 2 * * *"
  workflow_dispatch: {}
  push:
    branches: [main]
    paths: ["engine/src/wire.rs", "engine/src/edns.rs", "engine/fuzz/**"]
permissions:
  contents: read
concurrency:
  group: fuzz-${{ github.ref }}
  cancel-in-progress: true
jobs:
  parse_query:
    runs-on: ubuntu-24.04
    timeout-minutes: 80
    steps:
      - uses: actions/checkout@08eba0b27e820071cde6df949e0beb9ba4906955 # v4.3.0
      - name: Install toolchains
        run: |
          rustup toolchain install nightly --profile minimal
          sudo apt-get update && sudo apt-get install -y protobuf-compiler
          cargo +nightly install cargo-fuzz --locked
      - name: Fuzz parse_query for one hour
        working-directory: engine/fuzz
        run: cargo +nightly fuzz run parse_query corpus/parse_query -- -max_total_time=3600 -timeout=10 -rss_limit_mb=2048 -print_final_stats=1
      - name: Upload crashes
        if: failure()
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2
        with:
          name: fuzz-artifacts
          path: engine/fuzz/artifacts/
```

- [ ] Commit: `git add engine/src/wire.rs engine/src/edns.rs engine/src/lib.rs engine/fuzz .github/workflows/fuzz.yml && git commit -m "engine: zero-copy query parser, EDNS cookies, fuzz target and workflow"`.

## Task 4: Coarse clock and wire-format response cache with the allocation guard

Files:

- `engine/src/clock.rs` (create) — coarse monotonic seconds clock
- `engine/src/cache.rs` (create) — `Cache`, `CacheKey`, `CachedResponse`, serve writer
- `engine/src/lib.rs` (modify) — add `pub mod clock; pub mod cache;`
- `engine/tests/cache_alloc.rs` (create) — counting-allocator test of the cache serve path (the full packet-path guard `cache_hit_path_does_not_allocate` is added in Task 8)

Interfaces:

- Consumes `wire::{QueryView, NameKey, walk_response, ResponseInfo}`, `edns::{ReplyOpt, write_opt, OPT_BASE_LEN, OPT_COOKIE_LEN}` from Task 3.
- `clock.rs`: `pub fn now_secs() -> u32` (seconds since process start + 1, read from a static `AtomicU32`); `pub fn start_ticker() -> std::thread::JoinHandle<()>` (thread `nexora-clock`, updates every 100 ms, idempotent); `pub fn now_micros() -> u64` (monotonic, `Instant`-based, used only for stage timestamps). Cache functions take `now: u32` explicitly, so tests never need to drive the clock.
- `cache.rs`:
  - `#[derive(Clone, Copy, PartialEq, Eq, Hash)] pub struct CacheKey { pub name: NameKey, pub qtype: u16, pub qclass: u16, pub do_bit: bool, pub cd_bit: bool }` and `impl CacheKey { pub fn from_query(q: &QueryView<'_>) -> CacheKey }`.
  - `#[derive(Clone, Copy, Debug, PartialEq)] pub struct CacheSettings { pub max_bytes: u64, pub min_ttl: u32, pub max_ttl: u32, pub negative_max_ttl: u32, pub stale_window: u32 }`.
  - `pub struct CachedResponse { pub wire: Box<[u8]>, pub question_name_len: usize, pub ttl_offsets: Box<[u16]>, pub inserted_at: u32, pub ttl: u32, pub stale_deadline: u32, pub rcode: u8 }`.
  - `pub enum Lookup { Fresh(Arc<CachedResponse>), Stale(Arc<CachedResponse>), Miss }`.
  - `#[derive(Debug, PartialEq)] pub enum InsertOutcome { Inserted { ttl: u32 }, NotCacheable(&'static str) }`.
  - `pub struct Cache`; `impl Cache { pub fn new(settings: CacheSettings) -> Cache; pub fn settings(&self) -> CacheSettings; pub fn lookup(&self, key: &CacheKey, now: u32) -> Lookup; pub fn insert(&self, key: CacheKey, upstream: &[u8], query: &QueryView<'_>, now: u32) -> InsertOutcome; pub fn entries(&self) -> u64; pub fn bytes(&self) -> u64 }`.
  - `pub enum ServeMode { Fresh, Stale }` and `pub fn write_cached(entry: &CachedResponse, q: &QueryView<'_>, now: u32, mode: ServeMode, out: &mut [u8], limit: usize, opt: Option<&ReplyOpt>) -> usize`.

- [ ] Create `engine/src/cache.rs` with only this test module (and `pub mod clock; pub mod cache;` in `lib.rs`):

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use crate::wire::parse_query;
    use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
    use hickory_proto::rr::{rdata::{A, CNAME, SOA}, Name, RData, Record, RecordType};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};

    fn settings() -> CacheSettings {
        CacheSettings { max_bytes: 1 << 20, min_ttl: 0, max_ttl: 86400, negative_max_ttl: 3600, stale_window: 60 }
    }
    fn q(name: &str, id: u16) -> Vec<u8> {
        let mut m = Message::new();
        m.set_id(id).set_message_type(MessageType::Query).set_op_code(OpCode::Query).set_recursion_desired(true);
        m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
        m.to_bytes().unwrap()
    }
    fn answer(query: &[u8], ttls: &[u32], rcode: ResponseCode) -> Vec<u8> {
        let mut m = Message::from_bytes(query).unwrap();
        m.set_message_type(MessageType::Response).set_response_code(rcode).set_recursion_available(true);
        let name = m.queries()[0].name().clone();
        for (i, t) in ttls.iter().enumerate() {
            m.add_answer(Record::from_rdata(name.clone(), *t, RData::A(A::new(192, 0, 2, i as u8 + 1))));
        }
        m.to_bytes().unwrap()
    }

    #[test]
    fn serves_with_patched_id_client_casing_and_decremented_ttl() {
        let c = Cache::new(settings());
        let q1 = q("example.com.", 1);
        let v1 = parse_query(&q1).unwrap();
        let resp = answer(&q1, &[300, 120], ResponseCode::NoError);
        assert_eq!(c.insert(CacheKey::from_query(&v1), &resp, &v1, 1000), InsertOutcome::Inserted { ttl: 120 });
        let q2 = q("ExAmPlE.CoM.", 0x4242);
        let v2 = parse_query(&q2).unwrap();
        let Lookup::Fresh(e) = c.lookup(&CacheKey::from_query(&v2), 1030) else { panic!("expected fresh") };
        let mut out = [0u8; 1232];
        let n = write_cached(&e, &v2, 1030, ServeMode::Fresh, &mut out, 512, None);
        let m = Message::from_bytes(&out[..n]).unwrap();
        assert_eq!(m.id(), 0x4242);
        assert_eq!(m.queries()[0].name().to_ascii(), "ExAmPlE.CoM.");
        let ttls: Vec<u32> = m.answers().iter().map(|r| r.ttl()).collect();
        assert_eq!(ttls, vec![270, 90]);
    }

    #[test]
    fn ttl_zero_servfail_and_tc_are_not_cached() {
        let c = Cache::new(settings());
        let q1 = q("zero.example.", 1);
        let v = parse_query(&q1).unwrap();
        let k = CacheKey::from_query(&v);
        assert!(matches!(c.insert(k, &answer(&q1, &[0], ResponseCode::NoError), &v, 10), InsertOutcome::NotCacheable(_)));
        assert!(matches!(c.insert(k, &answer(&q1, &[], ResponseCode::ServFail), &v, 10), InsertOutcome::NotCacheable(_)));
        let mut tc = answer(&q1, &[60], ResponseCode::NoError);
        tc[2] |= 0x02;
        assert!(matches!(c.insert(k, &tc, &v, 10), InsertOutcome::NotCacheable(_)));
        assert!(matches!(c.lookup(&k, 10), Lookup::Miss));
    }

    #[test]
    fn negative_answer_uses_soa_minimum_and_no_soa_is_not_cached() {
        let c = Cache::new(settings());
        let q1 = q("nx.example.", 1);
        let v = parse_query(&q1).unwrap();
        let k = CacheKey::from_query(&v);
        assert!(matches!(c.insert(k, &answer(&q1, &[], ResponseCode::NXDomain), &v, 10), InsertOutcome::NotCacheable(_)));
        let mut m = Message::from_bytes(&answer(&q1, &[], ResponseCode::NXDomain)).unwrap();
        let soa = SOA::new(Name::from_ascii("ns.example.").unwrap(), Name::from_ascii("h.example.").unwrap(), 1, 2, 3, 4, 120);
        m.add_name_server(Record::from_rdata(Name::from_ascii("example.").unwrap(), 900, RData::SOA(soa)));
        assert_eq!(c.insert(k, &m.to_bytes().unwrap(), &v, 10), InsertOutcome::Inserted { ttl: 120 });
    }

    #[test]
    fn expired_entry_is_stale_within_window_then_miss() {
        let c = Cache::new(settings());
        let q1 = q("stale.example.", 1);
        let v = parse_query(&q1).unwrap();
        let k = CacheKey::from_query(&v);
        c.insert(k, &answer(&q1, &[10], ResponseCode::NoError), &v, 100);
        assert!(matches!(c.lookup(&k, 105), Lookup::Fresh(_)));
        let Lookup::Stale(e) = c.lookup(&k, 111) else { panic!("expected stale") };
        let mut out = [0u8; 512];
        let n = write_cached(&e, &v, 111, ServeMode::Stale, &mut out, 512, None);
        let m = Message::from_bytes(&out[..n]).unwrap();
        assert_eq!(m.answers()[0].ttl(), 30);
        assert!(matches!(c.lookup(&k, 171), Lookup::Miss));
    }

    #[test]
    fn oversize_for_limit_sets_tc_with_empty_answer() {
        let c = Cache::new(settings());
        let q1 = q("big.example.", 1);
        let v = parse_query(&q1).unwrap();
        let ttls: Vec<u32> = (0..40).map(|_| 300).collect();
        let resp = answer(&q1, &ttls, ResponseCode::NoError);
        assert!(resp.len() > 512);
        c.insert(CacheKey::from_query(&v), &resp, &v, 1);
        let Lookup::Fresh(e) = c.lookup(&CacheKey::from_query(&v), 1) else { panic!() };
        let mut out = [0u8; 1232];
        let n = write_cached(&e, &v, 1, ServeMode::Fresh, &mut out, 512, None);
        let m = Message::from_bytes(&out[..n]).unwrap();
        assert!(m.truncated());
        assert_eq!(m.answer_count(), 0);
        let n = write_cached(&e, &v, 1, ServeMode::Fresh, &mut out, 1232, None);
        assert!(!Message::from_bytes(&out[..n]).unwrap().truncated());
    }

    #[test]
    fn keys_differ_by_do_and_cd_bits() {
        let q1 = q("k.example.", 1);
        let v = parse_query(&q1).unwrap();
        let a = CacheKey::from_query(&v);
        let b = CacheKey { do_bit: true, ..a };
        let d = CacheKey { cd_bit: true, ..a };
        assert_ne!(a, b);
        assert_ne!(a, d);
        let _ = CNAME(Name::root());
    }
}
```

- [ ] Write the failing integration test `engine/tests/cache_alloc.rs`:

```rust
use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicU64, Ordering};

struct Counting;
static ALLOCS: AtomicU64 = AtomicU64::new(0);
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, l: Layout) -> *mut u8 { ALLOCS.fetch_add(1, Ordering::Relaxed); unsafe { System.alloc(l) } }
    unsafe fn dealloc(&self, p: *mut u8, l: Layout) { unsafe { System.dealloc(p, l) } }
    unsafe fn realloc(&self, p: *mut u8, l: Layout, n: usize) -> *mut u8 { ALLOCS.fetch_add(1, Ordering::Relaxed); unsafe { System.realloc(p, l, n) } }
}
#[global_allocator]
static GLOBAL: Counting = Counting;

#[test]
fn cache_lookup_and_serve_do_not_allocate() {
    use hickory_proto::op::{Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{rdata::A, Name, RData, Record, RecordType};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
    use nexora_engine::cache::*;
    use nexora_engine::wire::parse_query;

    let mut m = Message::new();
    m.set_id(9).set_message_type(MessageType::Query).set_op_code(OpCode::Query).set_recursion_desired(true);
    m.add_query(Query::query(Name::from_ascii("hot.example.").unwrap(), RecordType::A));
    let query = m.to_bytes().unwrap();
    let mut r = Message::from_bytes(&query).unwrap();
    r.set_message_type(MessageType::Response);
    r.add_answer(Record::from_rdata(Name::from_ascii("hot.example.").unwrap(), 300, RData::A(A::new(192, 0, 2, 1))));
    let resp = r.to_bytes().unwrap();

    let cache = Cache::new(CacheSettings { max_bytes: 1 << 20, min_ttl: 0, max_ttl: 86400, negative_max_ttl: 3600, stale_window: 0 });
    let v = parse_query(&query).unwrap();
    cache.insert(CacheKey::from_query(&v), &resp, &v, 1);
    let mut out = [0u8; 1232];
    for _ in 0..16 { // warm up quick_cache internals
        if let Lookup::Fresh(e) = cache.lookup(&CacheKey::from_query(&v), 2) { write_cached(&e, &v, 2, ServeMode::Fresh, &mut out, 512, None); }
    }
    let before = ALLOCS.load(Ordering::Relaxed);
    for _ in 0..10_000 {
        let v = parse_query(&query).unwrap();
        let Lookup::Fresh(e) = cache.lookup(&CacheKey::from_query(&v), 2) else { panic!("miss") };
        let n = write_cached(&e, &v, 2, ServeMode::Fresh, &mut out, 512, None);
        assert!(n > 12);
    }
    assert_eq!(ALLOCS.load(Ordering::Relaxed) - before, 0, "cache hit path allocated");
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib cache:: && scripts/dev-exec.sh cargo test --locked -p nexora-engine --test cache_alloc` — expect FAIL with ``unresolved import `crate::cache` `` / ``could not find `cache` in `nexora_engine` ``.
- [ ] Implement `engine/src/clock.rs`: `static START: OnceLock<Instant>`, `static NOW: AtomicU32`; `start_ticker` spawns one thread (guarded by `OnceLock<JoinHandle>` semantics via `std::sync::Once`) that stores `START.elapsed().as_secs() as u32 + 1` every 100 ms; `now_secs` loads `NOW` with `Ordering::Relaxed` (calls the elapsed computation directly when the ticker has not started, which happens only in tests).
- [ ] Implement `engine/src/cache.rs`: `struct EntryWeighter; impl quick_cache::Weighter<CacheKey, Arc<CachedResponse>> for EntryWeighter { fn weight(&self, _: &CacheKey, v: &Arc<CachedResponse>) -> u64 { (v.wire.len() + v.ttl_offsets.len() * 2 + 96) as u64 } }`; `Cache::new` builds `quick_cache::sync::Cache::with_weighter(estimated_items = max_bytes / 256, weight_capacity = max_bytes, EntryWeighter)`. `insert` calls `wire::walk_response(upstream, query)` and refuses (`NotCacheable`) for: parse error, TC=1, rcode SERVFAIL/REFUSED/other than NOERROR/NXDOMAIN, NOERROR with answers whose min TTL is 0, NXDOMAIN/NODATA without SOA; positive TTL = `min_ttl_of_answers.clamp(min_ttl, max_ttl)`; negative TTL = `negative_ttl.min(negative_max_ttl)`; builds `wire` as the upstream bytes with the OPT RR removed (ARCOUNT decremented) and the question name lowercased, recomputes `ttl_offsets` on the stripped copy, `stale_deadline = now + ttl + stale_window`. `lookup`: `get(key)`; `now < inserted_at + ttl` -> `Fresh`; `now < stale_deadline` -> `Stale`; else `Miss`. `write_cached` computes `full = entry.wire.len() + opt_len` first; when `full > limit` writes header + question only with TC=1, ANCOUNT/NSCOUNT/ARCOUNT=0 plus the OPT; otherwise copies `entry.wire` into `out`, writes `q.id`, copies RD from the query, copies `q.qname` bytes over offset 12 (client casing), writes each TTL as `ttl - elapsed` (`ServeMode::Fresh`, saturating) or `30` (`ServeMode::Stale`), then appends `edns::write_opt` and increments ARCOUNT when `opt` is `Some`. No `Vec`, `Box` or `String` is created in `lookup` or `write_cached`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib cache:: && scripts/dev-exec.sh cargo test --locked -p nexora-engine --test cache_alloc` — expect PASS: `6 passed` and `test cache_lookup_and_serve_do_not_allocate ... ok`.
- [ ] Commit: `git add engine/src/clock.rs engine/src/cache.rs engine/src/lib.rs engine/tests/cache_alloc.rs && git commit -m "engine: coarse clock and wire-format response cache with allocation guard"`.

## Task 5: Upstream UDP/TCP transports with anti-spoofing and health scoring

Files:

- `engine/src/upstream/mod.rs` (create) — `UpstreamSpec`, `UpstreamSet`, `Health`, strategy, `WorkerUpstreams`, `forward`
- `engine/src/upstream/udp.rs` (create) — per-worker pool of 16 connected sockets
- `engine/src/upstream/tcp.rs` (create) — one-shot TCP exchange
- `engine/src/lib.rs` (modify) — add `pub mod upstream;`
- `engine/tests/upstream_udp_tcp.rs` (create)

Interfaces:

- Consumes `wire::{NameKey, question_matches}` (Task 3), `clock::now_secs` (Task 4).
- `upstream/mod.rs`:
  - `#[derive(Clone, Copy, PartialEq, Eq, Debug)] pub struct Question { pub key: NameKey, pub qtype: u16, pub qclass: u16 }`
  - `#[derive(Clone, Copy, PartialEq, Eq, Debug)] pub enum Protocol { Udp, Tcp, Dot, Doh }`; `#[derive(Clone, Copy, PartialEq, Eq, Debug)] pub enum Strategy { Ordered, Fastest }`
  - `#[derive(Clone, Debug, PartialEq)] pub struct UpstreamSpec { pub id: String, pub name: String, pub protocol: Protocol, pub addr: Option<std::net::SocketAddr>, pub tls_server_name: String, pub doh_url: String, pub timeout: std::time::Duration, pub ca_pem: String }`
  - `pub struct Health { pub consecutive_failures: AtomicU32, pub down_until: AtomicU32, pub probe_taken: AtomicBool, pub ewma_rtt_us: AtomicU32, pub queries: AtomicU64, pub failures: AtomicU64 }` with `pub fn is_up(&self, now: u32) -> bool; pub fn admit(&self, now: u32) -> bool; pub fn record_success(&self, rtt: Duration); pub fn record_failure(&self, now: u32)`
  - `pub struct UpstreamSet { pub specs: Vec<UpstreamSpec>, pub health: Vec<Arc<Health>>, pub strategy: Strategy }` with `pub fn new(specs: Vec<UpstreamSpec>, strategy: Strategy, previous: Option<&UpstreamSet>) -> UpstreamSet` (reuses `Arc<Health>` for equal `id`) and `pub fn order(&self, now: u32, out: &mut Vec<usize>)`
  - `#[derive(Debug, thiserror::Error)] pub enum UpstreamError { #[error("timeout")] Timeout, #[error("io: {0}")] Io(#[from] std::io::Error), #[error("tls: {0}")] Tls(String), #[error("http status {0}")] Http(u16), #[error("malformed reply")] Malformed, #[error("no upstream available")] NoneAvailable, #[error("deadline exceeded")] Deadline }`
  - `pub struct Forwarded { pub response: bytes::Bytes, pub upstream_index: usize, pub rtt: Duration }`
  - `pub struct WorkerUpstreams` with `pub fn new(mismatched: Arc<CachePadded<AtomicU64>>) -> WorkerUpstreams` (worker-local, `!Send`; lazily creates one transport per upstream id)
  - `pub const OVERALL_DEADLINE: Duration = Duration::from_millis(2000);`
  - `pub async fn forward(set: &UpstreamSet, worker: &WorkerUpstreams, query: &[u8], question: &Question) -> Result<Forwarded, UpstreamError>`
- `upstream/udp.rs`: `pub const POOL_SIZE: usize = 16; pub const QUERIES_PER_SOCKET: u32 = 1024;` `pub struct UdpPool`; `impl UdpPool { pub fn new(addr: SocketAddr, mismatched: Arc<CachePadded<AtomicU64>>) -> std::io::Result<UdpPool>; pub async fn exchange(&self, query: &[u8], question: &Question, timeout: Duration) -> Result<Bytes, UpstreamError>; pub fn local_ports(&self) -> Vec<u16>; pub fn sockets_created(&self) -> u64 }`
- `upstream/tcp.rs`: `pub async fn exchange_tcp(addr: SocketAddr, query: &[u8], question: &Question, timeout: Duration) -> Result<Bytes, UpstreamError>`

- [ ] Write the failing test `engine/tests/upstream_udp_tcp.rs`:

```rust
use bytes::Bytes;
use crossbeam_utils::CachePadded;
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{rdata::A, Name, RData, Record, RecordType};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use nexora_engine::upstream::{self, tcp, udp::UdpPool, Health, Protocol, Question, Strategy, UpstreamSet, UpstreamSpec, WorkerUpstreams};
use nexora_engine::wire::parse_query;
use std::net::SocketAddr;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;
use tokio::net::UdpSocket;

fn query(name: &str) -> (Vec<u8>, Question) {
    let mut m = Message::new();
    m.set_id(1).set_message_type(MessageType::Query).set_op_code(OpCode::Query).set_recursion_desired(true);
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    let b = m.to_bytes().unwrap();
    let v = parse_query(&b).unwrap();
    let q = Question { key: v.key, qtype: v.qtype, qclass: v.qclass };
    (b, q)
}
fn reply_to(req: &[u8], id: u16, name: Option<&str>) -> Vec<u8> {
    let mut m = Message::from_bytes(req).unwrap();
    m.set_id(id).set_message_type(MessageType::Response);
    if let Some(n) = name {
        let mut qs = m.take_queries();
        qs[0].set_name(Name::from_ascii(n).unwrap());
        m.add_queries(qs);
    }
    let owner = m.queries()[0].name().clone();
    m.add_answer(Record::from_rdata(owner, 60, RData::A(A::new(192, 0, 2, 7))));
    m.to_bytes().unwrap()
}
fn counter() -> Arc<CachePadded<AtomicU64>> { Arc::new(CachePadded::new(AtomicU64::new(0))) }
async fn local<F: std::future::Future>(f: F) -> F::Output { tokio::task::LocalSet::new().run_until(f).await }

#[tokio::test(flavor = "current_thread")]
async fn spoofed_wrong_id_and_wrong_question_are_dropped_and_counted() {
    local(async {
        let server = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let addr = server.local_addr().unwrap();
        let mism = counter();
        let pool = UdpPool::new(addr, mism.clone()).unwrap();
        let (q, question) = query("spoof.example.");
        tokio::task::spawn_local(async move {
            let mut buf = [0u8; 1500];
            let (n, peer) = server.recv_from(&mut buf).await.unwrap();
            let req = buf[..n].to_vec();
            let id = u16::from_be_bytes([req[0], req[1]]);
            server.send_to(&reply_to(&req, id.wrapping_add(1), None), peer).await.unwrap();
            server.send_to(&reply_to(&req, id, Some("evil.example.")), peer).await.unwrap();
            server.send_to(&reply_to(&req, id, None), peer).await.unwrap();
        });
        let resp = pool.exchange(&q, &question, Duration::from_millis(500)).await.unwrap();
        let m = Message::from_bytes(&resp).unwrap();
        assert_eq!(m.queries()[0].name().to_ascii(), "spoof.example.");
        assert_eq!(mism.load(Ordering::Relaxed), 2);
    }).await;
}

#[tokio::test(flavor = "current_thread")]
async fn reply_from_other_source_port_never_accepted() {
    local(async {
        let server = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let attacker = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let pool = UdpPool::new(server.local_addr().unwrap(), counter()).unwrap();
        let (q, question) = query("port.example.");
        tokio::task::spawn_local(async move {
            let mut buf = [0u8; 1500];
            let (n, peer) = server.recv_from(&mut buf).await.unwrap();
            let id = u16::from_be_bytes([buf[0], buf[1]]);
            attacker.send_to(&reply_to(&buf[..n], id, None), peer).await.unwrap();
        });
        let err = pool.exchange(&q, &question, Duration::from_millis(300)).await.unwrap_err();
        assert!(matches!(err, upstream::UpstreamError::Timeout), "got {err:?}");
    }).await;
}

#[tokio::test(flavor = "current_thread")]
async fn ids_random_ports_spread_and_sockets_rotate() {
    local(async {
        let server = Arc::new(UdpSocket::bind("127.0.0.1:0").await.unwrap());
        let addr = server.local_addr().unwrap();
        let seen = Arc::new(parking_lot::Mutex::new((Vec::<u16>::new(), Vec::<SocketAddr>::new())));
        let (s2, seen2) = (server.clone(), seen.clone());
        tokio::task::spawn_local(async move {
            let mut buf = [0u8; 1500];
            loop {
                let (n, peer) = s2.recv_from(&mut buf).await.unwrap();
                let id = u16::from_be_bytes([buf[0], buf[1]]);
                { let mut g = seen2.lock(); g.0.push(id); g.1.push(peer); }
                s2.send_to(&reply_to(&buf[..n], id, None), peer).await.unwrap();
            }
        });
        let pool = UdpPool::new(addr, counter()).unwrap();
        let (q, question) = query("rand.example.");
        for _ in 0..(udp_pool_total()) {
            pool.exchange(&q, &question, Duration::from_millis(500)).await.unwrap();
        }
        let g = seen.lock();
        let mut ids = g.0.clone();
        let sequential = ids.windows(2).filter(|w| w[1] == w[0].wrapping_add(1)).count();
        assert!(sequential < 5, "ids look sequential");
        ids.sort(); ids.dedup();
        assert!(ids.len() > (g.0.len() * 9) / 10, "ids repeat too often");
        let mut ports: Vec<u16> = g.1.iter().map(|p| p.port()).collect();
        ports.sort(); ports.dedup();
        assert!(ports.len() >= nexora_engine::upstream::udp::POOL_SIZE + 1, "sockets were not rotated: {} ports", ports.len());
        assert!(pool.sockets_created() > nexora_engine::upstream::udp::POOL_SIZE as u64);
    }).await;
}
fn udp_pool_total() -> usize { nexora_engine::upstream::udp::POOL_SIZE * 1024 + 64 }

#[tokio::test(flavor = "current_thread")]
async fn tcp_exchange_validates_question() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    tokio::spawn(async move {
        for evil in [true, false] {
            let (mut s, _) = l.accept().await.unwrap();
            let len = s.read_u16().await.unwrap() as usize;
            let mut req = vec![0u8; len];
            s.read_exact(&mut req).await.unwrap();
            let id = u16::from_be_bytes([req[0], req[1]]);
            let r = reply_to(&req, id, if evil { Some("evil.example.") } else { None });
            s.write_u16(r.len() as u16).await.unwrap();
            s.write_all(&r).await.unwrap();
        }
    });
    let (q, question) = query("tcp.example.");
    assert!(matches!(tcp::exchange_tcp(addr, &q, &question, Duration::from_millis(500)).await, Err(upstream::UpstreamError::Malformed)));
    let ok: Bytes = tcp::exchange_tcp(addr, &q, &question, Duration::from_millis(500)).await.unwrap();
    assert_eq!(Message::from_bytes(&ok).unwrap().answers().len(), 1);
}

#[test]
fn health_marks_down_after_three_failures_and_admits_one_probe() {
    let h = Health::default();
    for _ in 0..3 { assert!(h.is_up(100)); h.record_failure(100); }
    assert!(!h.is_up(100));
    assert!(!h.admit(104));
    assert!(h.admit(105), "one probe after 5 s");
    assert!(!h.admit(105), "only one probe");
    h.record_success(Duration::from_millis(3));
    assert!(h.is_up(105));
}

#[test]
fn strategies_order_candidates() {
    let spec = |id: &str| UpstreamSpec { id: id.into(), name: id.into(), protocol: Protocol::Udp, addr: Some("127.0.0.1:53".parse().unwrap()), tls_server_name: String::new(), doh_url: String::new(), timeout: Duration::from_millis(250), ca_pem: String::new() };
    let set = UpstreamSet::new(vec![spec("a"), spec("b"), spec("c")], Strategy::Fastest, None);
    set.health[0].record_success(Duration::from_millis(40));
    set.health[1].record_success(Duration::from_millis(5));
    set.health[2].record_success(Duration::from_millis(20));
    let mut out = Vec::new();
    set.order(10, &mut out);
    assert_eq!(out, vec![1, 2, 0]);
    let ordered = UpstreamSet::new(vec![spec("a"), spec("b"), spec("c")], Strategy::Ordered, Some(&set));
    assert!(Arc::ptr_eq(&ordered.health[1], &set.health[1]), "health carried across snapshots");
    for _ in 0..3 { ordered.health[0].record_failure(10); }
    ordered.order(10, &mut out);
    assert_eq!(out, vec![1, 2]);
    let _ = WorkerUpstreams::new(counter());
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test upstream_udp_tcp` — expect FAIL with ``could not find `upstream` in `nexora_engine` ``.
- [ ] Implement `upstream/udp.rs`: each pooled socket is created with `socket2::Socket::new(Domain::for_address(addr), Type::DGRAM, Some(Protocol::UDP))`, `set_nonblocking(true)`, bound to `0.0.0.0:0` / `[::]:0` (kernel-random port), `connect(addr)`, converted into `tokio::net::UdpSocket`. State per socket: `pending: RefCell<FxHashMap<u16, (Question, tokio::sync::oneshot::Sender<Bytes>)>>`, `sent: Cell<u32>`, `retired: Cell<bool>`. On first use a `tokio::task::spawn_local` reader loop calls `recv` into a 4096-byte buffer; a datagram is accepted only when `len >= 12`, QR=1, the ID is in `pending`, and `wire::question_matches(reply, &q.key, q.qtype, q.qclass)`; otherwise `mismatched.fetch_add(1)`. Accepted replies remove the pending entry and send `Bytes::copy_from_slice`. The reader exits when `retired` and `pending` is empty. `exchange` picks sockets round-robin; when a socket's `sent` reaches `QUERIES_PER_SOCKET` it is marked retired and replaced in the slot with a fresh socket (`sockets_created` increments). IDs come from `rand::rng().random::<u16>()` retried until not present in that socket's `pending`. The query is copied into a stack `[u8; 1232]`-sized buffer (fallback `Vec` for larger) with the new ID, sent, and awaited with `tokio::time::timeout`; on timeout the pending entry is removed and `UpstreamError::Timeout` returned. The returned bytes get the original client query ID written back.
- [ ] Implement `upstream/tcp.rs`: `TcpStream::connect` inside the timeout, set `TCP_NODELAY`, random ID, write 2-byte length + query, read 2-byte length + body; reply must have QR=1, the sent ID and a matching question, else `UpstreamError::Malformed`; the original ID is restored.
- [ ] Implement `upstream/mod.rs`: `Health::default()` sets `ewma_rtt_us = 0` meaning unmeasured (sorted after measured ones for `Fastest`, stable by position); `record_failure` increments `consecutive_failures`, `failures`, and at 3 stores `down_until = now + 5` and clears `probe_taken`; `is_up(now)` is `consecutive_failures < 3`; `admit(now)` returns true when up, or when `now >= down_until` and `probe_taken.compare_exchange(false, true)` succeeds; `record_success` zeroes failures, clears `probe_taken`, and updates `ewma = 0.8*ewma + 0.2*rtt_us` (first sample stored directly). `order` pushes indices where `is_up(now) || now >= down_until` (up, or due a probe), sorted by position (`Ordered`) or by `ewma_rtt_us` (`Fastest`). `WorkerUpstreams` holds `RefCell<FxHashMap<String, Rc<Transport>>>` where `enum Transport { Udp(UdpPool), Tcp(SocketAddr), Dot(dot::DotClient), Doh(doh::DohClient) }` (the `Dot`/`Doh` variants are added in Task 6). `forward` records `start = Instant::now()`, iterates `order` output, calls `health.admit(now)`; for each attempt uses `min(spec.timeout, OVERALL_DEADLINE - elapsed)`; UDP replies with TC=1 are retried over TCP to the same address; success calls `record_success(rtt)` and increments `queries`; errors call `record_failure`; returns `NoneAvailable` when the order is empty and `Deadline` when the overall 2000 ms elapses.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test upstream_udp_tcp` — expect PASS: `test result: ok. 6 passed`.
- [ ] Commit: `git add engine/src/upstream engine/src/lib.rs engine/tests/upstream_udp_tcp.rs && git commit -m "engine: UDP/TCP upstream transports with anti-spoofing and health scoring"`.

## Task 6: DoT and DoH upstreams, and cross-worker request coalescing

Files:

- `engine/src/upstream/dot.rs` (create) — persistent pipelined DoT connection per worker
- `engine/src/upstream/doh.rs` (create) — reqwest HTTP/2 POST client per worker
- `engine/src/upstream/mod.rs` (modify) — `Transport::Dot`/`Transport::Doh`, `client_tls_config`
- `engine/src/inflight.rs` (create) — `InFlight`, `Pending`, `Join`, `LeaderGuard`
- `engine/src/lib.rs` (modify) — add `pub mod inflight;`
- `engine/tests/upstream_encrypted.rs` (create)
- `engine/tests/inflight.rs` (create)

Interfaces:

- Consumes `upstream::{Question, UpstreamError}` (Task 5), `cache::CacheKey` (Task 4), `wire::question_matches` (Task 3).
- `upstream/mod.rs`: `pub fn client_tls_config(ca_pem: &str) -> Result<Arc<rustls::ClientConfig>, UpstreamError>` — empty `ca_pem` uses `webpki_roots::TLS_SERVER_ROOTS`, otherwise only the PEM certificates.
- `upstream/dot.rs`: `pub struct DotClient`; `impl DotClient { pub fn new(addr: SocketAddr, server_name: &str, ca_pem: &str, mismatched: Arc<CachePadded<AtomicU64>>) -> Result<DotClient, UpstreamError>; pub async fn exchange(&self, query: &[u8], question: &Question, timeout: Duration) -> Result<Bytes, UpstreamError>; pub fn connections_opened(&self) -> u64 }`.
- `upstream/doh.rs`: `pub struct DohClient`; `impl DohClient { pub fn new(url: &str, ca_pem: &str) -> Result<DohClient, UpstreamError>; pub async fn exchange(&self, query: &[u8], question: &Question, timeout: Duration) -> Result<Bytes, UpstreamError> }`.
- `inflight.rs`: `#[derive(Clone, Debug)] pub enum Resolution { Answer(Bytes), ServFail }`; `pub struct Pending { tx: tokio::sync::watch::Sender<Option<Resolution>> }`; `pub struct InFlight { shards: Box<[parking_lot::Mutex<FxHashMap<CacheKey, Arc<Pending>>>; 64]> }`; `pub enum Join { Leader(LeaderGuard), Follower(tokio::sync::watch::Receiver<Option<Resolution>>) }`; `impl InFlight { pub fn new() -> InFlight; pub fn join(&self, key: CacheKey) -> Join; pub fn len(&self) -> usize }`; `impl LeaderGuard { pub fn complete(self, r: Resolution) }` (removes the entry, then sends); `impl Drop for LeaderGuard` sends `Resolution::ServFail` and removes the entry when `complete` was not called; `pub async fn wait(rx: watch::Receiver<Option<Resolution>>) -> Resolution` (returns `ServFail` if the sender is gone).

- [ ] Write the failing test `engine/tests/inflight.rs`:

```rust
use bytes::Bytes;
use nexora_engine::cache::CacheKey;
use nexora_engine::inflight::{wait, InFlight, Join, Resolution};
use nexora_engine::wire::NameKey;
use std::sync::Arc;

fn key(n: &[u8]) -> CacheKey {
    CacheKey { name: NameKey::from_wire_lowercase(n).unwrap(), qtype: 1, qclass: 1, do_bit: false, cd_bit: false }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn thousand_waiters_on_many_threads_all_get_the_answer() {
    let inf = Arc::new(InFlight::new());
    let Join::Leader(leader) = inf.join(key(b"\x04herd\x00")) else { panic!("first must lead") };
    let mut handles = Vec::new();
    for _ in 0..1000 {
        let inf = inf.clone();
        handles.push(tokio::spawn(async move {
            match inf.join(key(b"\x04herd\x00")) { Join::Follower(rx) => wait(rx).await, Join::Leader(_) => panic!("second leader") }
        }));
    }
    tokio::time::sleep(std::time::Duration::from_millis(50)).await;
    leader.complete(Resolution::Answer(Bytes::from_static(b"answer")));
    for h in handles {
        match tokio::time::timeout(std::time::Duration::from_secs(2), h).await.expect("waiter hung").unwrap() {
            Resolution::Answer(b) => assert_eq!(&b[..], b"answer"),
            Resolution::ServFail => panic!("servfail"),
        }
    }
    assert_eq!(inf.len(), 0);
}

#[tokio::test(flavor = "current_thread")]
async fn dropped_leader_wakes_followers_with_servfail_and_frees_key() {
    let inf = InFlight::new();
    let Join::Leader(leader) = inf.join(key(b"\x04drop\x00")) else { panic!() };
    let Join::Follower(rx) = inf.join(key(b"\x04drop\x00")) else { panic!() };
    drop(leader);
    assert!(matches!(wait(rx).await, Resolution::ServFail));
    assert!(matches!(inf.join(key(b"\x04drop\x00")), Join::Leader(_)));
}

#[tokio::test(flavor = "current_thread")]
async fn follower_joining_after_value_sent_still_gets_it() {
    let inf = InFlight::new();
    let Join::Leader(leader) = inf.join(key(b"\x04late\x00")) else { panic!() };
    let Join::Follower(rx) = inf.join(key(b"\x04late\x00")) else { panic!() };
    leader.complete(Resolution::Answer(Bytes::from_static(b"x")));
    assert!(matches!(wait(rx).await, Resolution::Answer(_)));
}
```

- [ ] Write the failing test `engine/tests/upstream_encrypted.rs`:

```rust
use bytes::Bytes;
use crossbeam_utils::CachePadded;
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{rdata::A, Name, RData, Record, RecordType};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use nexora_engine::upstream::{doh::DohClient, dot::DotClient, Question, UpstreamError};
use nexora_engine::wire::parse_query;
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

fn query(name: &str) -> (Vec<u8>, Question) {
    let mut m = Message::new();
    m.set_id(77).set_message_type(MessageType::Query).set_op_code(OpCode::Query).set_recursion_desired(true);
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    let b = m.to_bytes().unwrap();
    let v = parse_query(&b).unwrap();
    let q = Question { key: v.key, qtype: v.qtype, qclass: v.qclass };
    (b, q)
}
fn answer(req: &[u8]) -> Vec<u8> {
    let mut m = Message::from_bytes(req).unwrap();
    m.set_message_type(MessageType::Response);
    let n = m.queries()[0].name().clone();
    m.add_answer(Record::from_rdata(n, 60, RData::A(A::new(192, 0, 2, 53))));
    m.to_bytes().unwrap()
}
fn self_signed() -> (String, rustls::ServerConfig) {
    let ck = rcgen::generate_simple_self_signed(vec!["dns.test".into()]).unwrap();
    let pem = ck.cert.pem();
    let key = rustls::pki_types::PrivateKeyDer::Pkcs8(ck.signing_key.serialize_der().into());
    let mut cfg = rustls::ServerConfig::builder().with_no_client_auth()
        .with_single_cert(vec![ck.cert.der().clone()], key).unwrap();
    cfg.alpn_protocols = vec![b"h2".to_vec()];
    (pem, cfg)
}

#[tokio::test(flavor = "current_thread")]
async fn dot_pipelines_fifty_concurrent_queries_over_one_connection() {
    let (pem, cfg) = self_signed();
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(cfg));
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    let accepted = Arc::new(AtomicUsize::new(0));
    let acc2 = accepted.clone();
    tokio::spawn(async move {
        loop {
            let (s, _) = l.accept().await.unwrap();
            acc2.fetch_add(1, Ordering::SeqCst);
            let mut tls = acceptor.accept(s).await.unwrap();
            let mut batch = Vec::new();
            while batch.len() < 50 {
                let len = tls.read_u16().await.unwrap() as usize;
                let mut req = vec![0u8; len];
                tls.read_exact(&mut req).await.unwrap();
                batch.push(req);
            }
            for req in batch.iter().rev() { // answer out of order
                let r = answer(req);
                tls.write_u16(r.len() as u16).await.unwrap();
                tls.write_all(&r).await.unwrap();
            }
        }
    });
    tokio::task::LocalSet::new().run_until(async move {
        let client = std::rc::Rc::new(DotClient::new(addr, "dns.test", &pem, Arc::new(CachePadded::new(AtomicU64::new(0)))).unwrap());
        let mut tasks = Vec::new();
        for i in 0..50 {
            let c = client.clone();
            tasks.push(tokio::task::spawn_local(async move {
                let (q, question) = query(&format!("n{i}.example."));
                let r: Bytes = c.exchange(&q, &question, Duration::from_secs(2)).await.unwrap();
                assert_eq!(Message::from_bytes(&r).unwrap().queries()[0].name().to_ascii(), format!("n{i}.example."));
                assert_eq!(u16::from_be_bytes([r[0], r[1]]), 77);
            }));
        }
        for t in tasks { t.await.unwrap(); }
        assert_eq!(client.connections_opened(), 1);
    }).await;
    assert_eq!(accepted.load(Ordering::SeqCst), 1);
}

#[tokio::test(flavor = "current_thread")]
async fn dot_rejects_untrusted_certificate() {
    let (_pem, cfg) = self_signed();
    let (other_pem, _) = self_signed();
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(cfg));
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    tokio::spawn(async move { let (s, _) = l.accept().await.unwrap(); let _ = acceptor.accept(s).await; });
    tokio::task::LocalSet::new().run_until(async move {
        let c = DotClient::new(addr, "dns.test", &other_pem, Arc::new(CachePadded::new(AtomicU64::new(0)))).unwrap();
        let (q, question) = query("x.example.");
        assert!(matches!(c.exchange(&q, &question, Duration::from_secs(1)).await, Err(UpstreamError::Tls(_))));
    }).await;
}

#[tokio::test(flavor = "current_thread")]
async fn doh_posts_dns_message_over_http2() {
    use http_body_util::{BodyExt, Full};
    use hyper::{service::service_fn, Request, Response};
    let (pem, cfg) = self_signed();
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(cfg));
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = l.local_addr().unwrap().port();
    tokio::spawn(async move {
        loop {
            let (s, _) = l.accept().await.unwrap();
            let tls = acceptor.accept(s).await.unwrap();
            tokio::spawn(async move {
                let svc = service_fn(|req: Request<hyper::body::Incoming>| async move {
                    assert_eq!(req.version(), hyper::Version::HTTP_2);
                    assert_eq!(req.method(), hyper::Method::POST);
                    assert_eq!(req.headers()["content-type"], "application/dns-message");
                    let body = req.into_body().collect().await.unwrap().to_bytes();
                    assert_eq!(&body[0..2], &[0, 0], "DoH queries use ID 0");
                    Ok::<_, std::convert::Infallible>(Response::builder()
                        .header("content-type", "application/dns-message")
                        .body(Full::new(Bytes::from(answer(&body)))).unwrap())
                });
                hyper::server::conn::http2::Builder::new(hyper_util::rt::TokioExecutor::new())
                    .serve_connection(hyper_util::rt::TokioIo::new(tls), svc).await.unwrap();
            });
        }
    });
    unsafe { std::env::set_var("NEXORA_DOH_RESOLVE", "dns.test=127.0.0.1") };
    let client = DohClient::new(&format!("https://dns.test:{port}/dns-query"), &pem).unwrap();
    let (q, question) = query("doh.example.");
    let r = client.exchange(&q, &question, Duration::from_secs(2)).await.unwrap();
    let m = Message::from_bytes(&r).unwrap();
    assert_eq!(m.id(), 77);
    assert_eq!(m.answers().len(), 1);
}
```

- [ ] Add `http-body-util = "0.1"` and `hyper = { version = "1.11", features = ["server", "http1", "http2"] }` to `engine/Cargo.toml` (`http2` is used by this test's DoH server; `http-body-util` is also used by the metrics server in Task 9). `DohClient::new` honours `NEXORA_DOH_RESOLVE` (comma-separated `host=ip` pairs, applied with `reqwest::ClientBuilder::resolve(host, SocketAddr::new(ip, url_port))`) so tests and the e2e fixture can use certificate names that are not in DNS.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test inflight --test upstream_encrypted` — expect FAIL with ``could not find `inflight` in `nexora_engine` `` and ``could not find `dot` in `upstream` ``.
- [ ] Implement `inflight.rs`: shard index = `rustc_hash::FxHasher` hash of the key `& 63`; `join` locks the shard, returns `Follower(pending.tx.subscribe())` when present, else inserts `Arc<Pending>` built from `watch::channel(None)` and returns `Leader(LeaderGuard { inflight: *const/Arc ref, key, pending, done: false })` (the guard holds `&'static`-free ownership by storing `Arc<InFlightInner>`; `InFlight` wraps `Arc<Inner>` so guards can outlive a borrow). `complete` removes the map entry first, then `tx.send_replace(Some(r))`, sets `done`. `wait` loops `rx.changed()` until the value is `Some`, checking `borrow()` first so a value already sent is returned immediately; a closed channel yields `ServFail`.
- [ ] Implement `upstream/dot.rs`: lazily connects (`TcpStream::connect` + `TlsConnector::from(client_tls_config(ca_pem)?).connect(ServerName::try_from(server_name))`, TLS errors mapped to `UpstreamError::Tls`); the connection is `Rc<DotConn { writer: tokio::sync::Mutex<WriteHalf<TlsStream<TcpStream>>>, pending: RefCell<FxHashMap<u16, (Question, oneshot::Sender<Bytes>)>>, dead: Cell<bool> }>` with a `spawn_local` reader that reads length-prefixed replies and routes by ID with the same QR/ID/question checks as UDP (mismatches counted); on read error it marks `dead`, drops all pending senders (waiters see `UpstreamError::Io`), and the next `exchange` reconnects (`connections_opened` increments). IDs are random and unique within the connection's `pending`; the original ID is restored in the returned bytes.
- [ ] Implement `upstream/doh.rs`: `reqwest::Client::builder()` (the only TLS backend compiled in is rustls, from the `rustls` feature) with `.http2_prior_knowledge()`, `tls_built_in_root_certs(false)` plus `add_root_certificate(reqwest::Certificate::from_pem(ca_pem))` when `ca_pem` is non-empty, `pool_max_idle_per_host(1)`, the `NEXORA_DOH_RESOLVE` overrides, `timeout` per request. The URL must be `https` (else `UpstreamError::Tls("doh url must be https")`). `exchange` copies the query with ID 0, POSTs with `content-type: application/dns-message` and `accept: application/dns-message`, requires status 200 (else `Http(status)`), validates QR=1 and question, and writes the original ID into the reply.
- [ ] Extend `Transport` in `upstream/mod.rs` with `Dot(DotClient)` and `Doh(DohClient)` and dispatch them in `forward`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test inflight --test upstream_encrypted --test upstream_udp_tcp` — expect PASS: `3 passed`, `3 passed`, `6 passed`.
- [ ] Commit: `git add engine/Cargo.toml engine/src/upstream engine/src/inflight.rs engine/src/lib.rs engine/tests/inflight.rs engine/tests/upstream_encrypted.rs && git commit -m "engine: DoT/DoH upstreams and cross-worker request coalescing"`.

## Task 7: Filter sets, ACL, applied runtime and snapshot validation/persistence

Files:

- `engine/src/filter.rs` (create) — blocklist/allowlist matcher and block replies
- `engine/src/acl.rs` (create) — client CIDR allow list
- `engine/src/runtime.rs` (create) — `Runtime` built from a `ConfigSnapshot`
- `engine/src/snapshot.rs` (create) — validation, blob verification, persistence, apply
- `engine/src/cache.rs` (modify) — add `pub fn clear(&self)`
- `engine/src/lib.rs` (modify) — add `pub mod filter; pub mod acl; pub mod runtime; pub mod snapshot;`
- `engine/tests/snapshot_apply.rs` (create)

Interfaces:

- Consumes `proto::{ConfigSnapshot, BlobRef, UpstreamProtocol, UpstreamStrategy, BlockMode as ProtoBlockMode}` (Task 2), `wire::{QueryView, NameKey, write_synth_reply, SynthAnswer, RCODE_NXDOMAIN, RCODE_REFUSED, RCODE_NOERROR}`, `edns::ReplyOpt` (Task 3), `cache::{Cache, CacheSettings}` (Task 4), `upstream::{UpstreamSet, UpstreamSpec, Protocol, Strategy}` (Task 5).
- `filter.rs`: `#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum BlockMode { NullIp, NxDomain, Refused }`; `#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum FilterDecision { None, Blocked, Allowed }`; `#[derive(Debug, Default, PartialEq, Eq)] pub struct ListStats { pub entries: usize, pub invalid_lines: usize }`; `pub struct FilterSet { pub mode: BlockMode, pub ttl: u32, .. }`; `impl FilterSet { pub fn empty() -> FilterSet; pub fn build(blocklists: &[Vec<u8>], allowlists: &[Vec<u8>], mode: BlockMode, ttl: u32) -> (FilterSet, ListStats); pub fn decide(&self, name_wire: &[u8]) -> FilterDecision; pub fn cloaked(&self, cname_targets: &[NameKey]) -> bool; pub fn write_block_reply(&self, q: &QueryView<'_>, out: &mut [u8], opt: Option<&ReplyOpt>) -> usize }`; `pub fn decode_blob(zstd_bytes: &[u8]) -> std::io::Result<Vec<u8>>`; `pub fn domain_to_wire(domain: &[u8]) -> Option<Box<[u8]>>`.
- `acl.rs`: `pub struct Acl`; `impl Acl { pub fn parse(cidrs: &[String]) -> Result<Acl, String>; pub fn allows(&self, ip: std::net::IpAddr) -> bool }`.
- `runtime.rs`: `pub struct TelemetrySettings { pub otlp_endpoint: String, pub trace_sample_one_in: u32, pub trace_slow_threshold_us: u32, pub querylog_to_management: bool }`; `pub struct Runtime { pub version: u64, pub acl: Acl, pub filter: FilterSet, pub filter_hashes: Vec<String>, pub filter_stats: ListStats, pub cache: Arc<Cache>, pub upstreams: Arc<UpstreamSet>, pub telemetry: TelemetrySettings }`; `impl Runtime { pub fn initial() -> Runtime; pub fn build(s: &ConfigSnapshot, blobs: &dyn BlobSource, previous: Option<&Runtime>) -> Result<Runtime, SnapshotError> }`. `Runtime::initial()` has version 0, an empty ACL (every client REFUSED), no upstreams, a 16 MiB cache.
- `snapshot.rs`: `#[derive(Debug, thiserror::Error)] pub enum SnapshotError { #[error("invalid snapshot: {0}")] Invalid(String), #[error("blob {sha256}: {reason}")] Blob { sha256: String, reason: String }, #[error("io: {0}")] Io(#[from] std::io::Error), #[error("decode: {0}")] Decode(#[from] prost::DecodeError) }`; `pub trait BlobSource { fn read(&self, r: &BlobRef) -> Result<Vec<u8>, SnapshotError>; }`; `pub struct DirBlobs { pub dir: std::path::PathBuf }` (reads `<dir>/<sha256>` and verifies); `pub fn verify_blob(r: &BlobRef, bytes: &[u8]) -> Result<(), SnapshotError>`; `pub fn validate(s: &ConfigSnapshot, applied_version: u64) -> Result<(), SnapshotError>`; `pub const SNAPSHOT_FILE: &str = "snapshot.binpb";`; `pub fn persist(state_dir: &Path, s: &ConfigSnapshot) -> std::io::Result<()>`; `pub fn load(state_dir: &Path) -> Result<Option<ConfigSnapshot>, SnapshotError>`; `pub fn load_file(path: &Path) -> Result<ConfigSnapshot, SnapshotError>`; `#[derive(Debug, PartialEq)] pub enum ApplyOutcome { Applied { version: u64, persist_error: Option<String> }, Rejected { version: u64, reason: String } }`; `pub fn apply(current: &arc_swap::ArcSwap<Runtime>, s: ConfigSnapshot, blobs: &dyn BlobSource, state_dir: Option<&Path>) -> ApplyOutcome`.

- [ ] Write the failing test `engine/tests/snapshot_apply.rs`:

```rust
use arc_swap::ArcSwap;
use nexora_engine::acl::Acl;
use nexora_engine::filter::{domain_to_wire, BlockMode, FilterDecision, FilterSet};
use nexora_engine::proto::*;
use nexora_engine::runtime::Runtime;
use nexora_engine::snapshot::{self, ApplyOutcome, DirBlobs};
use nexora_engine::wire::NameKey;
use sha2::{Digest, Sha256};
use std::sync::Arc;

fn base(version: u64) -> ConfigSnapshot {
    ConfigSnapshot {
        version,
        resolver: Some(ResolverConfig { strategy: UpstreamStrategy::Ordered as i32 }),
        cache: Some(CacheConfig { max_bytes: 4 << 20, min_ttl: 0, max_ttl: 86400, negative_max_ttl: 3600, stale_window: 60 }),
        upstreams: vec![Upstream { id: "u1".into(), name: "fx".into(), protocol: UpstreamProtocol::Udp as i32, address: "127.0.0.1:5353".into(), timeout_ms: 250, ..Default::default() }],
        acl_allow_cidrs: vec!["127.0.0.0/8".into(), "::1/128".into()],
        filter: Some(FilterConfig { block_mode: BlockMode::NullIp as i32, block_ttl: 60, ..Default::default() }),
        telemetry: Some(TelemetryConfig::default()),
        ..Default::default()
    }
}
fn blob(dir: &std::path::Path, text: &str) -> BlobRef {
    let z = zstd::encode_all(text.as_bytes(), 3).unwrap();
    let sha = hex::encode(Sha256::digest(&z));
    std::fs::write(dir.join(&sha), &z).unwrap();
    BlobRef { sha256: sha, size: z.len() as u64, name: "list".into() }
}
fn outcome_reason(o: ApplyOutcome) -> String {
    match o { ApplyOutcome::Rejected { reason, .. } => reason, other => panic!("expected rejection, got {other:?}") }
}

#[test]
fn valid_snapshot_applies_persists_and_reloads() {
    let dir = tempfile::tempdir().unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let blobs = DirBlobs { dir: dir.path().to_path_buf() };
    assert_eq!(snapshot::apply(&cur, base(1), &blobs, Some(dir.path())), ApplyOutcome::Applied { version: 1, persist_error: None });
    assert_eq!(cur.load().version, 1);
    assert!(dir.path().join("snapshot.binpb").exists());
    assert!(!dir.path().join("snapshot.binpb.tmp").exists());
    assert_eq!(snapshot::load(dir.path()).unwrap().unwrap(), base(1));
}

#[test]
fn invalid_snapshots_are_rejected_and_previous_runtime_kept() {
    let dir = tempfile::tempdir().unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let blobs = DirBlobs { dir: dir.path().to_path_buf() };
    assert!(matches!(snapshot::apply(&cur, base(5), &blobs, None), ApplyOutcome::Applied { .. }));
    let held = cur.load_full();

    assert!(outcome_reason(snapshot::apply(&cur, base(5), &blobs, None)).contains("version"));
    let mut s = base(6); s.acl_allow_cidrs.push("10.0.0.300/8".into());
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("cidr"));
    let mut s = base(6); s.cache.as_mut().unwrap().max_bytes = 1048575;
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("max_bytes"));
    let mut s = base(6); s.upstreams[0].timeout_ms = 49;
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("timeout"));
    let mut s = base(6); s.upstreams[0].timeout_ms = 5001;
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("timeout"));
    let mut s = base(6); s.upstreams[0] = Upstream { id: "d".into(), protocol: UpstreamProtocol::Doh as i32, doh_url: "http://x/dns-query".into(), timeout_ms: 250, ..Default::default() };
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("https"));
    let mut s = base(6); s.upstreams[0].address = "not-an-address".into();
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("address"));
    let mut s = base(6); s.filter.as_mut().unwrap().blocklists.push(BlobRef { sha256: "abc".into(), size: 1, name: "bad".into() });
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("hex"));
    let mut s = base(6); s.filter.as_mut().unwrap().blocklists.push(BlobRef { sha256: "a".repeat(64), size: 1, name: "missing".into() });
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("blob"));
    let mut s = base(6);
    let mut r = blob(dir.path(), "ads.example\n");
    std::fs::write(dir.path().join(&r.sha256), b"tampered").unwrap();
    r.size = 8;
    s.filter.as_mut().unwrap().blocklists.push(r);
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("sha256"));

    assert_eq!(cur.load().version, 5, "previous runtime still serving");
    assert!(Arc::ptr_eq(&held, &cur.load_full()));
}

#[test]
fn in_flight_holders_keep_old_runtime_and_cache_survives_unchanged_settings() {
    let dir = tempfile::tempdir().unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let blobs = DirBlobs { dir: dir.path().to_path_buf() };
    snapshot::apply(&cur, base(1), &blobs, None);
    let old = cur.load_full();
    snapshot::apply(&cur, base(2), &blobs, None);
    assert_eq!(old.version, 1);
    assert!(Arc::ptr_eq(&old.cache, &cur.load().cache), "same cache settings keep the cache");
    let mut s = base(3); s.cache.as_mut().unwrap().max_bytes = 8 << 20;
    snapshot::apply(&cur, s, &blobs, None);
    assert!(!Arc::ptr_eq(&old.cache, &cur.load().cache));
}

#[test]
fn persist_failure_still_applies_and_reports() {
    let dir = tempfile::tempdir().unwrap();
    let not_a_dir = dir.path().join("file");
    std::fs::write(&not_a_dir, b"x").unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    match snapshot::apply(&cur, base(1), &DirBlobs { dir: dir.path().to_path_buf() }, Some(&not_a_dir)) {
        ApplyOutcome::Applied { version: 1, persist_error: Some(e) } => assert!(!e.is_empty()),
        other => panic!("{other:?}"),
    }
    assert_eq!(cur.load().version, 1);
}

#[test]
fn filter_subdomains_allowlist_invalid_lines_and_cloaking() {
    let block = b"ads.example\ntracker.example.net\nnot a domain\n-bad-.example\n".to_vec();
    let allow = b"good.ads.example\n".to_vec();
    let (f, stats) = FilterSet::build(&[block], &[allow], BlockMode::NxDomain, 60);
    assert_eq!(stats.entries, 3);
    assert_eq!(stats.invalid_lines, 2);
    let w = |s: &str| domain_to_wire(s.as_bytes()).unwrap();
    assert_eq!(f.decide(&w("ads.example")), FilterDecision::Blocked);
    assert_eq!(f.decide(&w("x.y.ads.example")), FilterDecision::Blocked);
    assert_eq!(f.decide(&w("good.ads.example")), FilterDecision::Allowed);
    assert_eq!(f.decide(&w("sub.good.ads.example")), FilterDecision::Allowed);
    assert_eq!(f.decide(&w("example")), FilterDecision::None);
    assert_eq!(f.decide(&w("notads.example")), FilterDecision::None);
    let target = NameKey::from_wire_lowercase(&w("cdn.tracker.example.net")).unwrap();
    assert!(f.cloaked(&[target]));
    assert!(!FilterSet::empty().cloaked(&[target]));
}

#[test]
fn acl_matches_v4_v6_and_mapped() {
    let acl = Acl::parse(&["10.0.0.0/8".into(), "fc00::/7".into()]).unwrap();
    assert!(acl.allows("10.1.2.3".parse().unwrap()));
    assert!(acl.allows("::ffff:10.1.2.3".parse().unwrap()));
    assert!(acl.allows("fd00::1".parse().unwrap()));
    assert!(!acl.allows("192.168.1.1".parse().unwrap()));
    assert!(Acl::parse(&["300.0.0.0/8".into()]).is_err());
    assert!(!Acl::parse(&[]).unwrap().allows("127.0.0.1".parse().unwrap()));
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test snapshot_apply` — expect FAIL with ``could not find `snapshot` in `nexora_engine` ``.
- [ ] Implement `acl.rs` with `Vec<ipnet::Ipv4Net>` and `Vec<ipnet::Ipv6Net>`; `allows` converts IPv4-mapped IPv6 (`to_ipv4_mapped`) to IPv4 first and scans the vectors (M1 lists are short; the management plane seeds 8 CIDRs); parse errors return `format!("invalid cidr {c}: {e}")`.
- [ ] Implement `filter.rs`: `domain_to_wire` accepts lines of `[a-z0-9-_.]` ASCII (uppercase lowered), rejects empty labels, labels starting or ending with `-`, labels > 63, total > 253 text octets, and lines containing whitespace or `#`; returns the uncompressed wire name including the root byte. `build` decodes every line of every list (both already decompressed), inserts valid names into `FxHashSet<Box<[u8]>>` (`blocked` / `allowed`), counts invalid non-empty lines. `decide(name_wire)` walks label offsets `0, 1+len0, ...` and for each suffix slice checks `allowed.contains(suffix)` first (returns `Allowed` on the first allowlisted suffix), then `blocked.contains(suffix)` (returns `Blocked`); a name matching both at different depths is `Allowed`. `cloaked` returns true when any target decides `Blocked`. `write_block_reply`: `NullIp` answers A `0.0.0.0` / AAAA `::` with `ttl`, other qtypes NOERROR/NODATA; `NxDomain` -> NXDOMAIN; `Refused` -> REFUSED (via `wire::write_synth_reply`). `decode_blob` is `zstd::decode_all` capped at 512 MiB output.
- [ ] Add `pub fn clear(&self) { self.inner.clear() }` to `Cache` in `engine/src/cache.rs`.
- [ ] Implement `snapshot.rs`: `validate` checks, in order, with these exact reason substrings: `version {v} is not newer than applied {a}`; `cache.max_bytes must be >= 1048576`; `invalid cidr {c}`; each upstream `timeout_ms {t} outside 50..=5000`, UDP/TCP/DoT `address {a} must be ip:port`, DoT non-empty `tls_server_name`, DoH `doh_url must be https`, protocol unspecified -> `upstream {id} protocol unspecified`; each blob `sha256 {h} must be 64 lowercase hex`. `verify_blob` requires `bytes.len() == size` and `hex(sha256(bytes)) == sha256` else `Blob { reason: "sha256 mismatch" }`; `DirBlobs::read` maps a missing file to `Blob { reason: "blob not present" }`. `persist` writes `state_dir/snapshot.binpb.tmp`, `sync_all`, renames to `snapshot.binpb`, then fsyncs the directory. `apply` runs `validate(&s, current.load().version)`, then `Runtime::build(&s, blobs, Some(&current.load()))`; any error -> `Rejected { version, reason: err.to_string() }` without touching `current`; on success `current.store(Arc::new(rt))`, then `persist` when `state_dir` is `Some`, capturing an error string into `persist_error`.
- [ ] Implement `runtime.rs` `Runtime::build`: parse ACL; map upstreams to `UpstreamSpec` (`Duration::from_millis(timeout_ms)`), strategy `Fastest` only when `UPSTREAM_STRATEGY_FASTEST` else `Ordered`; `UpstreamSet::new(specs, strategy, previous.map(|p| &*p.upstreams))`; cache: reuse `previous.cache` when `CacheSettings` equal, else `Cache::new`; filter: read and `decode_blob` every blocklist/allowlist blob through `blobs`, `FilterSet::build`; when `filter_hashes` differ from `previous.filter_hashes` and the cache is reused, call `cache.clear()` so cached answers are re-filtered (CNAME cloaking is evaluated on the miss path); telemetry settings copied.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test snapshot_apply` — expect PASS: `test result: ok. 6 passed`.
- [ ] Commit: `git add engine/src/filter.rs engine/src/acl.rs engine/src/runtime.rs engine/src/snapshot.rs engine/src/cache.rs engine/src/lib.rs engine/tests/snapshot_apply.rs && git commit -m "engine: filter sets, ACL, runtime build and snapshot apply/persist"`.

## Task 8: Per-core UDP/TCP listeners, query pipeline, counters, query-log ring and standalone engine binary

Files:

- `engine/src/bootstrap.rs` (create) — `engine.toml` parsing and validation
- `engine/src/telemetry/mod.rs` (create) — `pub mod metrics; pub mod querylog; pub mod otlp;` (`otlp` arrives in Task 9; this task declares `metrics` and `querylog`)
- `engine/src/telemetry/metrics.rs` (create) — per-worker `CachePadded<AtomicU64>` counters
- `engine/src/telemetry/querylog.rs` (create) — `QueryRecord`, ring push with drop counter
- `engine/src/server/mod.rs` (create) — `Shared`, `WorkerCtx`, `handle_packet`, `resolve_miss`, `spawn_workers`
- `engine/src/server/udp.rs` (create) — `recvmmsg`/`sendmmsg` loop on `AsyncFd`
- `engine/src/server/tcp.rs` (create) — length-framed TCP listener
- `engine/src/cache.rs` (modify) — add `pub fn prepare_uncached(upstream: &[u8], query: &QueryView<'_>) -> Option<CachedResponse>`
- `engine/src/main.rs` (modify) — full CLI entry: bootstrap, runtime load, workers, SIGHUP reload in standalone mode
- `engine/src/lib.rs` (modify) — add `pub mod bootstrap; pub mod server; pub mod telemetry;`
- `engine/tests/hot_path_alloc.rs` (create) — `cache_hit_path_does_not_allocate`
- `engine/tests/server_pipeline.rs` (create)

Interfaces:

- Consumes everything from Tasks 3–7: `wire::parse_query`, `edns::{reply_limit, server_cookie, CookieSecret, ReplyOpt, Transport}`, `cache::{Cache, CacheKey, Lookup, ServeMode, write_cached}`, `upstream::{forward, Question, WorkerUpstreams}`, `inflight::{InFlight, Join, Resolution, wait}`, `filter::FilterDecision`, `runtime::Runtime`, `snapshot::{apply, load, load_file, DirBlobs, ApplyOutcome}`.
- `bootstrap.rs`: `#[derive(Debug, Clone, serde::Deserialize)] #[serde(deny_unknown_fields)] pub struct Bootstrap { pub node_name: String, pub state_dir: PathBuf, #[serde(default)] pub management_urls: Vec<String>, #[serde(default = "default_join_token_file")] pub join_token_file: PathBuf, #[serde(default = "default_listen")] pub listen_udp: Vec<SocketAddr>, #[serde(default = "default_listen")] pub listen_tcp: Vec<SocketAddr>, #[serde(default = "default_metrics_listen")] pub metrics_listen: SocketAddr, #[serde(default)] pub workers: usize, #[serde(default)] pub standalone_snapshot: String, #[serde(default)] pub standalone_blob_dir: String }`; `pub fn load(path: &Path) -> anyhow::Result<Bootstrap>`; `impl Bootstrap { pub fn worker_count(&self) -> usize; pub fn is_standalone(&self) -> bool }`. Defaults: `listen_udp`/`listen_tcp` = `["0.0.0.0:53", "[::]:53"]`, `metrics_listen` = `0.0.0.0:9153`, `join_token_file` = `/etc/nexora/join-token`.
- `telemetry/metrics.rs`: `pub const DURATION_BOUNDS_US: [u64; 15] = [50, 100, 250, 500, 1_000, 2_500, 5_000, 10_000, 25_000, 50_000, 100_000, 250_000, 500_000, 1_000_000, 2_000_000];`; `pub const RCODE_SLOTS: usize = 7;` (0..=5 plus "other"); `pub const TRANSPORT_SLOTS: usize = 2;`; `pub struct WorkerCounters { pub queries: [[CachePadded<AtomicU64>; RCODE_SLOTS]; TRANSPORT_SLOTS], pub duration_buckets: [CachePadded<AtomicU64>; 16], pub duration_sum_us: CachePadded<AtomicU64>, pub cache_hits: CachePadded<AtomicU64>, pub cache_misses: CachePadded<AtomicU64>, pub stale_served: CachePadded<AtomicU64>, pub filter_blocked: CachePadded<AtomicU64>, pub mismatched_replies: Arc<CachePadded<AtomicU64>> }`; `impl WorkerCounters { pub fn observe(&self, t: Transport, rcode: u8, duration_us: u64) }`; `#[derive(Clone, Copy)] pub enum Signal { Logs = 0, Traces = 1, Metrics = 2 }`; `pub struct Metrics { pub workers: Box<[WorkerCounters]>, pub export_dropped: [CachePadded<AtomicU64>; 3], pub config_version: AtomicU64, pub control_connected: AtomicBool }`; `impl Metrics { pub fn new(workers: usize) -> Metrics; pub fn sum_queries(&self) -> u64; pub fn sum_cache_hits(&self) -> u64; pub fn sum_cache_misses(&self) -> u64; pub fn dropped(&self, s: Signal) -> u64 }`.
- `telemetry/querylog.rs`: `pub const RING_CAPACITY: usize = 65536;`; `#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum CacheOutcome { Hit, Miss, Stale, None }` (`as_str`: `hit|miss|stale|none`); `#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum FilterOutcome { None, Blocked, Allowed }` (`none|blocked|allowed`); `#[derive(Clone, Copy)] pub struct QueryRecord { pub unix_micros: u64, pub client: IpAddr, pub name: NameKey, pub qtype: u16, pub rcode: u8, pub cache: CacheOutcome, pub filter: FilterOutcome, pub upstream: u8, pub config_version: u64, pub transport: Transport, pub filter_us: u32, pub cache_us: u32, pub upstream_start_us: u32, pub upstream_us: u32, pub duration_us: u32 }` (`upstream == u8::MAX` means none); `pub fn push(ring: &ArrayQueue<QueryRecord>, metrics: &Metrics, r: QueryRecord)`.
- `server/mod.rs`: `pub struct Shared { pub runtime: ArcSwap<Runtime>, pub inflight: InFlight, pub metrics: Metrics, pub querylog: ArrayQueue<QueryRecord>, pub cookie_secret: CookieSecret }` with `pub fn new(workers: usize) -> Arc<Shared>`; `pub struct WorkerCtx { pub index: usize, pub shared: Arc<Shared>, pub upstreams: WorkerUpstreams }` with `pub fn new(index: usize, shared: Arc<Shared>) -> WorkerCtx`; `pub enum FastOutcome { Reply(usize), Drop, Miss(MissJob) }`; `pub struct MissJob { pub query: Box<[u8]>, pub client: SocketAddr, pub transport: Transport, pub key: CacheKey, pub question: Question, pub limit: usize, pub opt: Option<ReplyOpt>, pub started: std::time::Instant, pub filter_us: u32, pub cache_us: u32 }`; `pub fn handle_packet(ctx: &WorkerCtx, rt: &Runtime, packet: &[u8], client: SocketAddr, transport: Transport, out: &mut [u8]) -> FastOutcome`; `pub async fn resolve_miss(ctx: Rc<WorkerCtx>, rt: Arc<Runtime>, job: MissJob) -> Vec<u8>`; `pub fn spawn_workers(shared: Arc<Shared>, boot: &Bootstrap) -> std::io::Result<Vec<std::thread::JoinHandle<()>>>`.
- `server/udp.rs`: `pub const BATCH: usize = 64;` `pub fn bind_udp(addr: SocketAddr) -> std::io::Result<std::net::UdpSocket>` (SO_REUSEPORT, non-blocking, 4 MiB SO_RCVBUF/SO_SNDBUF, IPV6_V6ONLY for `[::]`); `pub async fn run_udp(ctx: Rc<WorkerCtx>, sock: std::net::UdpSocket)`.
- `server/tcp.rs`: `pub fn bind_tcp(addr: SocketAddr) -> std::io::Result<std::net::TcpListener>` (SO_REUSEPORT, backlog 1024); `pub async fn run_tcp(ctx: Rc<WorkerCtx>, listener: std::net::TcpListener)`; `pub const IDLE_TIMEOUT: Duration = Duration::from_secs(10);`.
- Engine CLI: `nexora-engine --config <path>`; stderr lines `nexora-engine: serving version {v}` after the first runtime is stored and `nexora-engine: snapshot rejected: {reason}` on standalone rejection.

- [ ] Write the failing test `engine/tests/hot_path_alloc.rs`:

```rust
use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};

struct Counting;
static ALLOCS: AtomicU64 = AtomicU64::new(0);
static ARMED: AtomicBool = AtomicBool::new(false);
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, l: Layout) -> *mut u8 { if ARMED.load(Ordering::Relaxed) { ALLOCS.fetch_add(1, Ordering::Relaxed); } unsafe { System.alloc(l) } }
    unsafe fn dealloc(&self, p: *mut u8, l: Layout) { unsafe { System.dealloc(p, l) } }
    unsafe fn realloc(&self, p: *mut u8, l: Layout, n: usize) -> *mut u8 { if ARMED.load(Ordering::Relaxed) { ALLOCS.fetch_add(1, Ordering::Relaxed); } unsafe { System.realloc(p, l, n) } }
}
#[global_allocator]
static GLOBAL: Counting = Counting;

#[test]
fn cache_hit_path_does_not_allocate() {
    use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{rdata::opt::EdnsOption, rdata::A, Name, RData, Record, RecordType};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
    use nexora_engine::cache::CacheKey;
    use nexora_engine::edns::Transport;
    use nexora_engine::proto::*;
    use nexora_engine::server::{handle_packet, FastOutcome, Shared, WorkerCtx};
    use nexora_engine::snapshot::{apply, DirBlobs};
    use nexora_engine::wire::parse_query;

    let shared = Shared::new(1);
    let snap = ConfigSnapshot {
        version: 1,
        cache: Some(CacheConfig { max_bytes: 8 << 20, min_ttl: 0, max_ttl: 86400, negative_max_ttl: 3600, stale_window: 0 }),
        acl_allow_cidrs: vec!["127.0.0.0/8".into()],
        filter: Some(FilterConfig { block_mode: BlockMode::NullIp as i32, block_ttl: 60, ..Default::default() }),
        telemetry: Some(TelemetryConfig::default()),
        resolver: Some(ResolverConfig::default()),
        ..Default::default()
    };
    let tmp = tempfile::tempdir().unwrap();
    apply(&shared.runtime, snap, &DirBlobs { dir: tmp.path().into() }, None);

    let mut m = Message::new();
    m.set_id(1).set_message_type(MessageType::Query).set_op_code(OpCode::Query).set_recursion_desired(true);
    m.add_query(Query::query(Name::from_ascii("Hot.Example.").unwrap(), RecordType::A));
    let mut e = Edns::new();
    e.set_max_payload(1232);
    e.options_mut().insert(EdnsOption::Unknown(10, vec![9; 8]));
    m.set_edns(e);
    let query = m.to_bytes().unwrap();
    let mut r = Message::from_bytes(&query).unwrap();
    r.set_message_type(MessageType::Response);
    r.add_answer(Record::from_rdata(Name::from_ascii("hot.example.").unwrap(), 300, RData::A(A::new(192, 0, 2, 1))));
    let upstream = r.to_bytes().unwrap();
    {
        let rt = shared.runtime.load();
        let v = parse_query(&query).unwrap();
        rt.cache.insert(CacheKey::from_query(&v), &upstream, &v, nexora_engine::clock::now_secs());
    }
    let ctx = WorkerCtx::new(0, shared.clone());
    let client: std::net::SocketAddr = "127.0.0.1:40000".parse().unwrap();
    let mut out = [0u8; 1232];
    for _ in 0..64 {
        let rt = shared.runtime.load();
        assert!(matches!(handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out), FastOutcome::Reply(_)));
    }
    ARMED.store(true, Ordering::Relaxed);
    for _ in 0..50_000 {
        let rt = shared.runtime.load();
        match handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out) {
            FastOutcome::Reply(n) => assert!(n > 40),
            _ => panic!("expected cache hit"),
        }
    }
    ARMED.store(false, Ordering::Relaxed);
    assert_eq!(ALLOCS.load(Ordering::Relaxed), 0, "cache-hit path allocated");
    assert!(shared.metrics.sum_cache_hits() >= 50_000);
    assert!(shared.querylog.len() > 0, "query records were pushed");
}
```

- [ ] Write the failing test `engine/tests/server_pipeline.rs` (in-process workers against an in-test UDP upstream):

```rust
use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{rdata::A, Name, RData, Record, RecordType};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::proto::*;
use nexora_engine::server::{spawn_workers, Shared};
use nexora_engine::snapshot::{apply, DirBlobs};
use std::net::{SocketAddr, UdpSocket};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

fn free_port() -> u16 { std::net::TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap().port() }

fn fake_upstream(count: Arc<AtomicUsize>, answers: usize) -> SocketAddr {
    let s = UdpSocket::bind("127.0.0.1:0").unwrap();
    let addr = s.local_addr().unwrap();
    std::thread::spawn(move || {
        let mut buf = [0u8; 4096];
        loop {
            let (n, peer) = s.recv_from(&mut buf).unwrap();
            count.fetch_add(1, Ordering::SeqCst);
            let mut m = Message::from_bytes(&buf[..n]).unwrap();
            m.set_message_type(MessageType::Response).set_recursion_available(true);
            let name = m.queries()[0].name().clone();
            for i in 0..answers { m.add_answer(Record::from_rdata(name.clone(), 300, RData::A(A::new(192, 0, (i / 250) as u8, (i % 250) as u8)))); }
            let _ = s.send_to(&m.to_bytes().unwrap(), peer);
        }
    });
    addr
}

fn start_engine(upstream: SocketAddr, acl: &str, blocklist: Option<&str>) -> (SocketAddr, Arc<Shared>) {
    let port = free_port();
    let dir = tempfile::tempdir().unwrap().keep();
    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"t\"\nstate_dir = \"{}\"\nlisten_udp = [\"127.0.0.1:{port}\"]\nlisten_tcp = [\"127.0.0.1:{port}\"]\nworkers = 2\nstandalone_snapshot = \"x\"\n",
        dir.display())).unwrap();
    let shared = Shared::new(2);
    let mut filter = FilterConfig { block_mode: BlockMode::NullIp as i32, block_ttl: 60, ..Default::default() };
    if let Some(text) = blocklist {
        use sha2::Digest;
        let z = zstd::encode_all(text.as_bytes(), 3).unwrap();
        let sha = hex::encode(sha2::Sha256::digest(&z));
        std::fs::write(dir.join(&sha), &z).unwrap();
        filter.blocklists.push(BlobRef { sha256: sha, size: z.len() as u64, name: "l".into() });
    }
    let snap = ConfigSnapshot {
        version: 1,
        resolver: Some(ResolverConfig { strategy: UpstreamStrategy::Ordered as i32 }),
        cache: Some(CacheConfig { max_bytes: 8 << 20, min_ttl: 0, max_ttl: 86400, negative_max_ttl: 3600, stale_window: 0 }),
        upstreams: vec![Upstream { id: "u".into(), name: "u".into(), protocol: UpstreamProtocol::Udp as i32, address: upstream.to_string(), timeout_ms: 500, ..Default::default() }],
        acl_allow_cidrs: vec![acl.into()],
        filter: Some(filter),
        telemetry: Some(TelemetryConfig::default()),
        ..Default::default()
    };
    assert!(matches!(apply(&shared.runtime, snap, &DirBlobs { dir: dir.clone() }, None), nexora_engine::snapshot::ApplyOutcome::Applied { .. }));
    spawn_workers(shared.clone(), &boot).unwrap();
    std::thread::sleep(Duration::from_millis(200));
    (format!("127.0.0.1:{port}").parse().unwrap(), shared)
}

fn ask(server: SocketAddr, name: &str, edns: Option<u16>) -> Message {
    let c = UdpSocket::bind("127.0.0.1:0").unwrap();
    c.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let mut m = Message::new();
    m.set_id(rand_id()).set_message_type(MessageType::Query).set_op_code(OpCode::Query).set_recursion_desired(true);
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    if let Some(sz) = edns { let mut e = Edns::new(); e.set_max_payload(sz); m.set_edns(e); }
    c.send_to(&m.to_bytes().unwrap(), server).unwrap();
    let mut buf = [0u8; 4096];
    let (n, _) = c.recv_from(&mut buf).unwrap();
    let r = Message::from_bytes(&buf[..n]).unwrap();
    assert_eq!(r.id(), m.id());
    r
}
fn rand_id() -> u16 { (std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().subsec_nanos() & 0xffff) as u16 }

#[test]
fn miss_then_hit_with_decremented_ttl_and_one_upstream_query() {
    let count = Arc::new(AtomicUsize::new(0));
    let (srv, shared) = start_engine(fake_upstream(count.clone(), 1), "127.0.0.0/8", None);
    let first = ask(srv, "cache.example.", None);
    assert_eq!(first.response_code(), ResponseCode::NoError);
    assert_eq!(first.answers()[0].ttl(), 300);
    std::thread::sleep(Duration::from_millis(2100));
    let second = ask(srv, "CACHE.example.", None);
    assert_eq!(second.queries()[0].name().to_ascii(), "CACHE.example.");
    assert!(second.answers()[0].ttl() < 300);
    assert_eq!(count.load(Ordering::SeqCst), 1);
    assert!(shared.metrics.sum_cache_hits() >= 1);
}

#[test]
fn acl_refuses_and_blocklist_blocks() {
    let count = Arc::new(AtomicUsize::new(0));
    let (srv, _) = start_engine(fake_upstream(count.clone(), 1), "127.0.0.0/8", Some("ads.example\n"));
    assert_eq!(ask(srv, "ok.example.", None).response_code(), ResponseCode::NoError);
    let blocked = ask(srv, "x.ads.example.", None);
    assert_eq!(blocked.answers()[0].data().as_a().unwrap().0, std::net::Ipv4Addr::UNSPECIFIED);
    let (srv2, _) = start_engine(fake_upstream(count, 1), "10.0.0.0/8", None);
    assert_eq!(ask(srv2, "ok.example.", None).response_code(), ResponseCode::Refused);
}

#[test]
fn large_answer_truncates_over_udp_and_completes_over_tcp() {
    use std::io::{Read, Write};
    let count = Arc::new(AtomicUsize::new(0));
    let (srv, _) = start_engine(fake_upstream(count.clone(), 100), "127.0.0.0/8", None);
    let udp = ask(srv, "big.example.", Some(1232));
    assert!(udp.truncated());
    assert_eq!(udp.answers().len(), 0);
    let mut s = std::net::TcpStream::connect(srv).unwrap();
    s.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let mut m = Message::new();
    m.set_id(9).set_message_type(MessageType::Query).set_op_code(OpCode::Query).set_recursion_desired(true);
    m.add_query(Query::query(Name::from_ascii("big.example.").unwrap(), RecordType::A));
    let q = m.to_bytes().unwrap();
    s.write_all(&(q.len() as u16).to_be_bytes()).unwrap();
    s.write_all(&q).unwrap();
    let mut len = [0u8; 2];
    s.read_exact(&mut len).unwrap();
    let mut body = vec![0u8; u16::from_be_bytes(len) as usize];
    s.read_exact(&mut body).unwrap();
    let full = Message::from_bytes(&body).unwrap();
    assert!(!full.truncated());
    assert_eq!(full.answers().len(), 100);
}

#[test]
fn malformed_and_notimp() {
    let count = Arc::new(AtomicUsize::new(0));
    let (srv, _) = start_engine(fake_upstream(count, 1), "127.0.0.0/8", None);
    let c = UdpSocket::bind("127.0.0.1:0").unwrap();
    c.set_read_timeout(Some(Duration::from_millis(500))).unwrap();
    let mut buf = [0u8; 512];
    c.send_to(&[0xab, 0xcd, 0x01, 0x00, 0, 2, 0, 0, 0, 0, 0, 0], srv).unwrap();
    let (n, _) = c.recv_from(&mut buf).unwrap();
    assert_eq!(&buf[0..2], &[0xab, 0xcd]);
    assert_eq!(buf[3] & 0x0f, 1, "FORMERR");
    assert!(n >= 12);
    c.send_to(&[0xab, 0xce, 0x20, 0x00, 0, 0, 0, 0, 0, 0, 0, 0], srv).unwrap();
    let _ = c.recv_from(&mut buf).unwrap();
    assert_eq!(buf[3] & 0x0f, 4, "NOTIMP");
    c.send_to(&[1, 2, 3], srv).unwrap();
    assert!(c.recv_from(&mut buf).is_err(), "short packets are dropped");
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test hot_path_alloc --test server_pipeline` — expect FAIL with ``could not find `server` in `nexora_engine` ``.
- [ ] Implement `bootstrap.rs`: `load` reads TOML, validates `node_name` against `^[a-z0-9-]{1,63}$` (manual byte check), `state_dir` non-empty, `management_urls` non-empty unless `standalone_snapshot` is non-empty, every management URL starts with `https://`; errors name the field. `worker_count` returns `workers` or `std::thread::available_parallelism()`.
- [ ] Implement `telemetry/metrics.rs` and `telemetry/querylog.rs` as specified; `observe` maps rcode > 5 to slot 6 and finds the first bound `>= duration_us` (slot 15 = +Inf) with a linear scan of the 15 constants; `push` calls `ring.push(r)` and on `Err` increments `export_dropped[Signal::Logs]`.
- [ ] Add `prepare_uncached` to `cache.rs`: builds a `CachedResponse` from any parseable upstream reply (including TTL 0, SERVFAIL, TC) with `inserted_at = 0`, `ttl = u32::MAX`, OPT stripped and TTL offsets computed, so `write_cached(.., now = 0, ServeMode::Fresh, ..)` emits the upstream TTLs unchanged.
- [ ] Implement `server/mod.rs` `handle_packet` in this order, with no allocation before the `Miss` branch: `started = Instant::now()`; `parse_query` — `TooShort`/`IsResponse` -> `Drop`; `FormErr` -> `write_error_reply(RCODE_FORMERR)`; `NotImp` -> `write_error_reply(RCODE_NOTIMP)`; `rt.acl.allows(client.ip())` false -> REFUSED via `write_rcode_reply`; `opt.bad_cookie_len` -> FORMERR; build `ReplyOpt { udp_size: 1232, do_bit: q.do_bit(), ext_rcode: 0, cookie: client_cookie.map(|c| (c, server_cookie(&secret, &c, client.ip(), clock::now_secs()))) }` when the query had OPT; `limit = reply_limit(q.opt.as_ref(), transport)`; `rt.filter.decide(q.key.as_wire())` `Blocked` -> `write_block_reply`, count `filter_blocked`; cache `lookup` `Fresh` -> `write_cached(ServeMode::Fresh)`, count `cache_hits`; `Stale` or `Miss` -> count `cache_misses`, build `MissJob` (the only allocation: `Box<[u8]>` of the packet). Every `Reply` path calls `counters.observe(...)` and `querylog::push(...)` with stage microseconds. `resolve_miss`: join `shared.inflight` with the key; leader calls `upstream::forward(&rt.upstreams, &ctx.upstreams, &job.query, &job.question)`; on success re-parses `job.query`, walks the reply, and if `rt.filter.cloaked(&info.cname_targets)` writes a block reply and completes with `Resolution::Answer(reply)` without caching; otherwise `rt.cache.insert(...)` then `complete(Resolution::Answer(bytes))` (insert happens before the in-flight entry is removed); on error serves `Lookup::Stale` with `ServeMode::Stale` (counting `stale_served`) or SERVFAIL, completing with `Resolution::ServFail`. A follower awaits `inflight::wait`, then serves from `rt.cache.lookup` when present, else `prepare_uncached` of the answer bytes, else stale/SERVFAIL. The reply for each client is written with its own ID, casing, OPT and `limit` via `write_cached`. Push the query record with `cache: Miss|Stale`, `upstream` index and `upstream_us`.
- [ ] Implement `server/udp.rs`: `run_udp` wraps the socket in `tokio::io::unix::AsyncFd`; per worker preallocates `BATCH` receive buffers of 4096 bytes, `BATCH` output buffers of 1232 bytes, `libc::mmsghdr`/`iovec`/`sockaddr_storage` arrays; loop: `readable().await`, `guard.try_io(|fd| recvmmsg(fd, msgs, BATCH, MSG_DONTWAIT, null))`; for each datagram convert the `sockaddr_storage` to `SocketAddr`, `let rt = ctx.shared.runtime.load();`, call `handle_packet` into the matching output buffer; `Reply(n)` entries are gathered into an output `mmsghdr` array and flushed with one `sendmmsg` (retrying the unsent tail on `EAGAIN` after `writable().await`); `Miss(job)` spawns `tokio::task::spawn_local` that runs `resolve_miss(ctx.clone(), rt_arc, job)` with `rt_arc = ctx.shared.runtime.load_full()` and sends the reply with `libc::sendto` on the shared `Rc<AsyncFd>` (waiting on `writable()` on `EAGAIN`).
- [ ] Implement `server/tcp.rs`: `run_tcp` accepts connections on `tokio::net::TcpListener::from_std`; each connection is a `spawn_local` task with one 65,537-byte buffer that reads a 2-byte length and body under `IDLE_TIMEOUT`, calls `handle_packet(.., Transport::Tcp, ..)`, writes `Reply(n)` with its length prefix, awaits `resolve_miss` for `Miss`, and closes on `Drop`, EOF, a zero length, or timeout.
- [ ] Implement `spawn_workers`: for `i in 0..boot.worker_count()` spawn thread `nexora-worker-{i}` that builds `tokio::runtime::Builder::new_current_thread().enable_all()`, creates `Rc<WorkerCtx>`, binds one UDP socket per `listen_udp` and one TCP listener per `listen_tcp` (errors are returned from `spawn_workers` by binding all sockets before spawning the threads), and runs all loops in a `LocalSet`.
- [ ] Implement `main.rs`: parse `--config`, `bootstrap::load`, `clock::start_ticker()`, `Shared::new(workers)`; standalone mode: `snapshot::load_file(standalone_snapshot)` then `apply` with `DirBlobs { dir: standalone_blob_dir }` and `state_dir: None`, printing the rejection and exiting 1 if the first load is rejected; managed mode: `snapshot::load(state_dir)` and apply when present (blobs from `state_dir/blobs`); `spawn_workers`; build the `nexora-control` runtime (`tokio::runtime::Builder::new_multi_thread().worker_threads(2).thread_name("nexora-control")`); in standalone mode register `tokio::signal::unix::signal(SignalKind::hangup())` on it and re-run `load_file` + `apply` on each SIGHUP, printing the outcome; `shared.metrics.config_version` is stored after every successful apply; the main thread joins the worker handles.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test hot_path_alloc --test server_pipeline` — expect PASS: `test cache_hit_path_does_not_allocate ... ok` and `test result: ok. 4 passed`.
- [ ] Run the whole engine suite and lints: `scripts/dev-exec.sh make engine-test && scripts/dev-exec.sh cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` — expect PASS with no warnings.
- [ ] Commit: `git add engine && git commit -m "engine: per-core recvmmsg/sendmmsg listeners, query pipeline, counters and standalone binary"`.

## Task 9: Prometheus endpoint, OTLP logs/traces/metrics export and stats snapshot

Files:

- `engine/src/telemetry/metrics.rs` (modify) — `render`, `stats`, `serve_metrics`
- `engine/src/telemetry/otlp.rs` (create) — `nexora-telemetry` thread: batching, bounded queue, log/trace/metric export
- `engine/src/telemetry/mod.rs` (modify) — add `pub mod otlp;`
- `engine/src/server/mod.rs` (modify) — add `pub engine_id: ArcSwap<String>`, `pub node_name: ArcSwap<String>`, `pub mgmt_channel: ArcSwapOption<tonic::transport::Channel>` to `Shared`
- `engine/src/main.rs` (modify) — spawn the telemetry thread and the metrics server
- `engine/tests/telemetry_export.rs` (create)

Interfaces:

- Consumes `Shared`, `QueryRecord`, `CacheOutcome`, `FilterOutcome`, `Metrics`, `Signal`, `DURATION_BOUNDS_US` (Task 8), `Runtime`/`TelemetrySettings` (Task 7), `proto::{Stats, UpstreamStatus}` (Task 2), `opentelemetry_proto::tonic::{collector::{logs::v1::*, trace::v1::*, metrics::v1::*}, logs::v1::LogRecord, trace::v1::Span, common::v1::{AnyValue, KeyValue}}`.
- `metrics.rs`: `impl Metrics { pub fn render(&self, rt: &Runtime) -> String; pub fn stats(&self, rt: &Runtime) -> Stats }`; `pub async fn serve_metrics(addr: SocketAddr, shared: Arc<Shared>) -> std::io::Result<()>` (hyper 1 HTTP/1.1; `GET /metrics` -> 200 `text/plain; version=0.0.4`; anything else 404). Exposed names exactly: `nexora_queries_total{transport,rcode}`, `nexora_query_duration_seconds` (histogram, buckets from `DURATION_BOUNDS_US` in seconds), `nexora_cache_hits_total`, `nexora_cache_misses_total`, `nexora_cache_stale_served_total`, `nexora_cache_entries`, `nexora_cache_bytes`, `nexora_filter_blocked_total`, `nexora_upstream_up{upstream}`, `nexora_upstream_rtt_seconds{upstream}`, `nexora_upstream_queries_total{upstream}`, `nexora_upstream_failures_total{upstream}`, `nexora_upstream_mismatched_replies_total`, `nexora_export_dropped_total{signal}`, `nexora_config_version`, `nexora_control_connected`.
- `otlp.rs`: `pub const BATCH_MAX: usize = 1000; pub const BATCH_INTERVAL: Duration = Duration::from_secs(1); pub const MAX_QUEUED_BATCHES: usize = 8; pub const EXPORT_TIMEOUT: Duration = Duration::from_secs(2); pub const METRICS_INTERVAL: Duration = Duration::from_secs(15);`; `pub fn spawn_telemetry_thread(shared: Arc<Shared>) -> std::thread::JoinHandle<()>`; `pub fn log_record(r: &QueryRecord, upstream_name: &str, engine_id: &str) -> LogRecord`; `pub fn should_trace(r: &QueryRecord, t: &TelemetrySettings, seq: u64) -> bool`; `pub fn spans_for(r: &QueryRecord, trace_id: [u8; 16], upstream_name: &str) -> Vec<Span>`; `pub fn resource(shared: &Shared) -> opentelemetry_proto::tonic::resource::v1::Resource` (`service.name=nexora-engine`, `nexora.engine.id`, `host.name` = node name).

- [ ] Write the failing test `engine/tests/telemetry_export.rs`:

```rust
use nexora_engine::edns::Transport;
use nexora_engine::proto::*;
use nexora_engine::server::Shared;
use nexora_engine::snapshot::{apply, DirBlobs};
use nexora_engine::telemetry::metrics::{serve_metrics, Signal};
use nexora_engine::telemetry::otlp::{log_record, should_trace, spans_for, spawn_telemetry_thread};
use nexora_engine::telemetry::querylog::{push, CacheOutcome, FilterOutcome, QueryRecord};
use nexora_engine::wire::NameKey;
use opentelemetry_proto::tonic::collector::logs::v1::{logs_service_server::{LogsService, LogsServiceServer}, ExportLogsServiceRequest, ExportLogsServiceResponse};
use opentelemetry_proto::tonic::collector::trace::v1::{trace_service_server::{TraceService, TraceServiceServer}, ExportTraceServiceRequest, ExportTraceServiceResponse};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

fn record(rcode: u8) -> QueryRecord {
    QueryRecord {
        unix_micros: 1_700_000_000_000_000, client: "127.0.0.1".parse().unwrap(),
        name: NameKey::from_wire_lowercase(b"\x07example\x03com\x00").unwrap(), qtype: 1, rcode,
        cache: CacheOutcome::Miss, filter: FilterOutcome::None, upstream: 0, config_version: 1,
        transport: Transport::Udp, filter_us: 3, cache_us: 5, upstream_start_us: 6, upstream_us: 900, duration_us: 950,
    }
}

#[derive(Default, Clone)]
struct Sink { logs: Arc<AtomicUsize>, spans: Arc<AtomicUsize> }
#[tonic::async_trait]
impl LogsService for Sink {
    async fn export(&self, req: tonic::Request<ExportLogsServiceRequest>) -> Result<tonic::Response<ExportLogsServiceResponse>, tonic::Status> {
        let n: usize = req.into_inner().resource_logs.iter().flat_map(|r| &r.scope_logs).map(|s| s.log_records.len()).sum();
        self.logs.fetch_add(n, Ordering::SeqCst);
        Ok(tonic::Response::new(ExportLogsServiceResponse::default()))
    }
}
#[tonic::async_trait]
impl TraceService for Sink {
    async fn export(&self, req: tonic::Request<ExportTraceServiceRequest>) -> Result<tonic::Response<ExportTraceServiceResponse>, tonic::Status> {
        let n: usize = req.into_inner().resource_spans.iter().flat_map(|r| &r.scope_spans).map(|s| s.spans.len()).sum();
        self.spans.fetch_add(n, Ordering::SeqCst);
        Ok(tonic::Response::new(ExportTraceServiceResponse::default()))
    }
}

fn shared_with_endpoint(endpoint: &str) -> Arc<Shared> {
    let shared = Shared::new(1);
    let tmp = tempfile::tempdir().unwrap();
    let snap = ConfigSnapshot {
        version: 1,
        cache: Some(CacheConfig { max_bytes: 2 << 20, max_ttl: 86400, negative_max_ttl: 60, ..Default::default() }),
        upstreams: vec![Upstream { id: "u".into(), name: "fixture".into(), protocol: UpstreamProtocol::Udp as i32, address: "127.0.0.1:9".into(), timeout_ms: 100, ..Default::default() }],
        resolver: Some(ResolverConfig::default()), filter: Some(FilterConfig::default()),
        telemetry: Some(TelemetryConfig { otlp_endpoint: endpoint.into(), trace_sample_one_in: 0, trace_slow_threshold_us: 0, querylog_to_management: false }),
        ..Default::default()
    };
    apply(&shared.runtime, snap, &DirBlobs { dir: tmp.path().into() }, None);
    shared
}

#[test]
fn log_record_attributes_and_trace_rules() {
    let lr = log_record(&record(0), "fixture", "engine-uuid");
    let keys: Vec<&str> = lr.attributes.iter().map(|kv| kv.key.as_str()).collect();
    for k in ["client.address", "dns.question.name", "dns.question.type", "dns.response.code", "nexora.cache", "nexora.filter", "nexora.upstream", "nexora.duration_us", "nexora.transport", "nexora.engine.id"] {
        assert!(keys.contains(&k), "missing {k}");
    }
    let t = nexora_engine::runtime::TelemetrySettings { otlp_endpoint: String::new(), trace_sample_one_in: 0, trace_slow_threshold_us: 0, querylog_to_management: false };
    assert!(!should_trace(&record(0), &t, 1));
    assert!(should_trace(&record(2), &t, 1), "SERVFAIL always traced");
    let slow = nexora_engine::runtime::TelemetrySettings { trace_slow_threshold_us: 900, ..t };
    assert!(should_trace(&record(0), &slow, 1));
    let sampled = nexora_engine::runtime::TelemetrySettings { trace_sample_one_in: 10, trace_slow_threshold_us: 0, ..slow };
    assert!(should_trace(&record(0), &sampled, 20) && !should_trace(&record(0), &sampled, 21));
    let spans = spans_for(&record(2), [1; 16], "fixture");
    let names: Vec<&str> = spans.iter().map(|s| s.name.as_str()).collect();
    assert_eq!(names, vec!["dns.query", "nexora.filter", "nexora.cache", "nexora.upstream"]);
    assert!(spans[1..].iter().all(|s| s.parent_span_id == spans[0].span_id));
}

#[test]
fn logs_and_servfail_traces_reach_the_collector() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    let sink = Sink::default();
    let listener = rt.block_on(tokio::net::TcpListener::bind("127.0.0.1:0")).unwrap();
    let addr = listener.local_addr().unwrap();
    let s2 = sink.clone();
    rt.spawn(tonic::transport::Server::builder()
        .add_service(LogsServiceServer::new(s2.clone())).add_service(TraceServiceServer::new(s2))
        .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener)));
    let shared = shared_with_endpoint(&format!("http://{addr}"));
    for _ in 0..9 { push(&shared.querylog, &shared.metrics, record(0)); }
    push(&shared.querylog, &shared.metrics, record(2));
    let _t = spawn_telemetry_thread(shared.clone());
    let deadline = Instant::now() + Duration::from_secs(5);
    while (sink.logs.load(Ordering::SeqCst) < 10 || sink.spans.load(Ordering::SeqCst) < 4) && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
    }
    assert_eq!(sink.logs.load(Ordering::SeqCst), 10);
    assert_eq!(sink.spans.load(Ordering::SeqCst), 4);
    assert_eq!(shared.metrics.dropped(Signal::Logs), 0);
}

#[test]
fn unreachable_collector_drops_with_counter_and_never_blocks_push() {
    let dead = std::net::TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap();
    let shared = shared_with_endpoint(&format!("http://{dead}"));
    let _t = spawn_telemetry_thread(shared.clone());
    let start = Instant::now();
    for _ in 0..200_000 { push(&shared.querylog, &shared.metrics, record(0)); }
    assert!(start.elapsed() < Duration::from_secs(1), "push blocked: {:?}", start.elapsed());
    let deadline = Instant::now() + Duration::from_secs(10);
    while shared.metrics.dropped(Signal::Logs) == 0 && Instant::now() < deadline { std::thread::sleep(Duration::from_millis(50)); }
    assert!(shared.metrics.dropped(Signal::Logs) > 0);
}

#[test]
fn metrics_endpoint_exposes_every_architecture_name() {
    use std::io::{Read, Write};
    let shared = shared_with_endpoint("");
    shared.metrics.workers[0].observe(Transport::Udp, 0, 120);
    let port = std::net::TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap().port();
    let addr: std::net::SocketAddr = format!("127.0.0.1:{port}").parse().unwrap();
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.spawn(serve_metrics(addr, shared.clone()));
    std::thread::sleep(Duration::from_millis(200));
    let mut s = std::net::TcpStream::connect(addr).unwrap();
    s.write_all(b"GET /metrics HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n").unwrap();
    let mut body = String::new();
    s.read_to_string(&mut body).unwrap();
    assert!(body.starts_with("HTTP/1.1 200"));
    for name in ["nexora_queries_total{", "nexora_query_duration_seconds_bucket{", "nexora_cache_hits_total", "nexora_cache_misses_total",
        "nexora_cache_stale_served_total", "nexora_cache_entries", "nexora_cache_bytes", "nexora_filter_blocked_total",
        "nexora_upstream_up{upstream=\"fixture\"}", "nexora_upstream_rtt_seconds{", "nexora_upstream_queries_total{", "nexora_upstream_failures_total{",
        "nexora_upstream_mismatched_replies_total", "nexora_export_dropped_total{signal=\"logs\"}", "nexora_config_version 1", "nexora_control_connected"] {
        assert!(body.contains(name), "missing {name}");
    }
    let stats = shared.metrics.stats(&shared.runtime.load());
    assert_eq!(stats.queries_total, 1);
    assert_eq!(stats.duration_bucket_bounds_us.len(), 15);
}
```

- [ ] Add `tokio-stream = { version = "0.1", features = ["net"] }` under `[dev-dependencies]` in `engine/Cargo.toml` and run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test telemetry_export` — expect FAIL with ``could not find `otlp` in `telemetry` ``.
- [ ] Implement `Metrics::render` with a `prometheus_client::registry::Registry` rebuilt per scrape: sum each per-worker counter across `workers`, emit counters/gauges through `prometheus_client::encoding::text::encode` using `Family<Labels, Counter>` / `Gauge` / a `Histogram`-shaped custom `EncodeMetric` built from the summed cumulative bucket counts (no per-packet registry work); upstream gauges come from `rt.upstreams.health[i]` (`up` = `is_up(clock::now_secs())`, rtt = `ewma_rtt_us / 1e6`) labelled by upstream `name`; `nexora_export_dropped_total` has `signal` in `logs|traces|metrics`. `Metrics::stats` fills every `Stats` field from the same sums.
- [ ] Implement `serve_metrics` with `hyper::server::conn::http1::Builder` over `tokio::net::TcpListener`, one task per connection.
- [ ] Implement `otlp.rs`: the thread runs a `current_thread` runtime; every 100 ms it drains `shared.querylog` (`pop` until empty or `BATCH_MAX`) into the current batch; a batch closes at `BATCH_MAX` records or `BATCH_INTERVAL` age. Closed batches enter a `VecDeque` capped at `MAX_QUEUED_BATCHES`; pushing onto a full queue pops the oldest and adds its length to `export_dropped[Logs]`. One export at a time: destination is `shared.mgmt_channel` when `querylog_to_management` (no channel -> batch dropped and counted), else a lazily built `tonic::transport::Endpoint::from_shared(otlp_endpoint).connect_timeout(Duration::from_millis(500)).timeout(EXPORT_TIMEOUT).connect_lazy()` rebuilt whenever the endpoint string changes; empty endpoint and no management export -> records are discarded without counting. A failed or timed-out export adds the batch length to `export_dropped[Logs]`. Records where `should_trace` holds (sequence counter increments per record) produce `spans_for` spans with a random trace ID, exported via `TraceServiceClient` to `otlp_endpoint` (failures add span counts to `export_dropped[Traces]`). Every `METRICS_INTERVAL` it sends an `ExportMetricsServiceRequest` of the summed counters (`nexora.queries`, `nexora.cache.hits`, `nexora.cache.misses`, `nexora.filter.blocked`, `nexora.upstream.up` per upstream) to `otlp_endpoint`, a failure increments `export_dropped[Metrics]` by 1. `log_record` sets `time_unix_nano`, `severity_text = "INFO"`, body = question name, and the ten attributes named in `docs/architecture.md` (`nexora.upstream` empty string when `upstream == u8::MAX`). `spans_for` builds root `dns.query` spanning `duration_us` with attributes `dns.question.name`, `dns.question.type`, `dns.response.code`, and children `nexora.filter` `[0, filter_us]`, `nexora.cache` `[filter_us, cache_us]`, `nexora.upstream` `[upstream_start_us, upstream_start_us + upstream_us]` with attribute `nexora.upstream`; a root with rcode 2 gets `status.code = STATUS_CODE_ERROR`.
- [ ] Extend `Shared::new` to initialise `engine_id` and `node_name` to `""` and `mgmt_channel` to `None`; in `main.rs` set `node_name` from the bootstrap, call `spawn_telemetry_thread(shared.clone())`, and spawn `serve_metrics(boot.metrics_listen, shared.clone())` on the `nexora-control` runtime.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test telemetry_export` — expect PASS: `test result: ok. 4 passed`.
- [ ] Commit: `git add engine && git commit -m "engine: Prometheus endpoint and non-blocking OTLP logs/traces/metrics export"`.

## Task 10: End-to-end harness and fixture servers

Files:

- `e2e/fixtures/cmd/nexora-fixture/main.go` (create) — subcommand dispatch `dns | http | oidc`
- `e2e/fixtures/cmd/nexora-fixture/dns.go` (create) — fixture upstream over UDP, TCP, DoT, DoH with a control API
- `e2e/fixtures/cmd/nexora-fixture/http.go` (create) — blocklist file server with failure toggle
- `e2e/fixtures/cmd/nexora-fixture/oidc.go` (create) — minimal OIDC provider (discovery, JWKS, authorize, token, userinfo)
- `e2e/fixtures/cmd/nexora-fixture/certs.go` (create) — self-signed CA and server certificate writer
- `e2e/fixtures/cmd/nexora-fixture/dns_test.go` (create)
- `e2e/harness/harness.go` (create) — `Env`, ports, binaries, process lifecycle
- `e2e/harness/fixture.go` (create) — typed clients for the three fixtures
- `e2e/harness/engine.go` (create) — standalone engine start, reload, metrics scraping
- `e2e/harness/snapshot.go` (create) — snapshot and blob builders
- `e2e/harness/dnsclient.go` (create) — miekg/dns query helpers
- `e2e/harness/postgres.go` (create) — `initdb`/`pg_ctl` into a temp dir
- `e2e/harness/otelcol.go` (create) — `otelcol-contrib` with generated config
- `e2e/harness/harness_test.go` (create)

Interfaces (produced; consumed by Tasks 11, 12, 13, 14, 15, 16, 17, 18, 19, 20):

- `harness.New(t *testing.T) *Env`; `type Env struct { T *testing.T; Dir string }`; `func (e *Env) FreePort() int`; `func (e *Env) Bin(name string) string` (looks in `NEXORA_E2E_BIN_DIR`, then `<repo>/bin`, then `<repo>/target/release`, then `$CARGO_TARGET_DIR/release`; `t.Fatal` when missing); `func (e *Env) Start(name string, args, env []string) *Proc`; `type Proc struct { Name string; Cmd *exec.Cmd; LogPath string }`; `func (p *Proc) Stop()` (SIGTERM, 5 s, SIGKILL); `func (p *Proc) Kill()`; `func (p *Proc) Signal(sig os.Signal)`; `func (p *Proc) WaitLog(re *regexp.Regexp, timeout time.Duration) []string`; `func Eventually(t *testing.T, timeout time.Duration, cond func() error)`.
- `type DNSFixture struct { UDP, TCP, DoT, DoH, Control, CACertPEM, TLSName string }`; `func (e *Env) StartDNSFixture() *DNSFixture`; `func (f *DNSFixture) Count(t *testing.T, name string, qtype uint16) int`; `func (f *DNSFixture) Total(t *testing.T) int`; `func (f *DNSFixture) SetMode(t *testing.T, mode string)` (`normal|blackhole|servfail`); `func (f *DNSFixture) SetDelay(t *testing.T, d time.Duration)`; `func (f *DNSFixture) Reset(t *testing.T)`.
- `type HTTPFixture struct { Base string }`; `func (e *Env) StartHTTPFixture() *HTTPFixture`; `func (f *HTTPFixture) SetList(t *testing.T, name, body string)`; `func (f *HTTPFixture) SetFailing(t *testing.T, name string, failing bool)`; `func (f *HTTPFixture) Hits(t *testing.T, name string) int`; `func (f *HTTPFixture) URL(name string) string`.
- `type OIDCUser struct { Username, Email string; Groups []string }`; `type OIDCFixture struct { Issuer, ClientID, ClientSecretFile string; Proc *Proc }`; `func (e *Env) StartOIDCFixture(users ...OIDCUser) *OIDCFixture`.
- `type Engine struct { DNS, Metrics, StateDir, ConfigPath string; Proc *Proc }`; `func (e *Env) StartStandaloneEngine(snap *controlv1.ConfigSnapshot, blobs map[string][]byte) *Engine`; `func (en *Engine) Reload(t *testing.T, snap *controlv1.ConfigSnapshot, blobs map[string][]byte)`; `func (en *Engine) Metric(t *testing.T, name string, labels map[string]string) float64`; `func (en *Engine) WaitVersion(t *testing.T, v uint64)`.
- `func BaseSnapshot(version uint64, ups ...*controlv1.Upstream) *controlv1.ConfigSnapshot`; `func UDPUpstream(id, addr string) *controlv1.Upstream`; `func DoTUpstream(id string, f *DNSFixture) *controlv1.Upstream`; `func DoHUpstream(id string, f *DNSFixture) *controlv1.Upstream`; `func BlocklistBlob(name string, domains ...string) (*controlv1.BlobRef, []byte)`.
- `type QueryOpts struct { TCP bool; EDNSSize uint16; Cookie []byte; Timeout time.Duration }`; `func Query(t *testing.T, server, name string, qtype uint16, o QueryOpts) (*dns.Msg, time.Duration, error)` (never calls `t.Fatal`, safe from goroutines); `func MustQuery(t *testing.T, server, name string, qtype uint16, o QueryOpts) *dns.Msg`; `func UniqueName(prefix string) string` (`<prefix>-<12 hex>.example.`).
- `type Postgres struct { URL, Dir string; Port int }`; `func (e *Env) StartPostgres() *Postgres`.
- `type OtelcolConfig struct { OpenSearchURL, JaegerOTLP string; DebugFile string }`; `type Otelcol struct { OTLPGRPC string; Proc *Proc; ConfigPath string }`; `func (e *Env) StartOtelcol(cfg OtelcolConfig) *Otelcol`; `func (o *Otelcol) Stop()`; `func (o *Otelcol) Restart(e *Env)`.
- Fixture DNS behaviour by first label prefix: default -> one A `192.0.2.1` TTL 300 (AAAA `2001:db8::1`); `big-*` -> 100 A records TTL 300; `tc-*` -> over UDP an empty reply with TC=1, over TCP/DoT/DoH 100 A records; `nx-*` -> NXDOMAIN with SOA `example. 900 SOA ns.example. h.example. 1 2 3 4 120`; `zero-*` -> one A with TTL 0; `cloak-*` -> CNAME to `cdn.tracker.blocked.test.` plus A `192.0.2.9`. Control API (JSON over HTTP on `--control`): `GET /stats` -> `{"total": n, "queries": {"<lowercase name>|<type number>": n}}`; `POST /reset`; `POST /mode` `{"mode": "normal|blackhole|servfail"}`; `POST /delay` `{"ms": n}`.
- Fixture CLI: `nexora-fixture dns --udp ADDR --tcp ADDR --dot ADDR --doh ADDR --control ADDR --cert-dir DIR`; `nexora-fixture http --listen ADDR`; `nexora-fixture oidc --listen ADDR --client-id ID --client-secret-file F --users-file F`. Each prints `fixture ready` to stdout once listening. The DoT/DoH certificate is for `fixture.nexora.test` and IP `127.0.0.1`, CA at `<cert-dir>/ca.pem`.

- [ ] Write the failing fixture test `e2e/fixtures/cmd/nexora-fixture/dns_test.go`:

```go
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestDNSFixtureBehaviours(t *testing.T) {
	dir := t.TempDir()
	cfg := dnsConfig{UDP: freeAddr(t), TCP: freeAddr(t), DoT: freeAddr(t), DoH: freeAddr(t), Control: freeAddr(t), CertDir: dir}
	fx, err := startDNSFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer fx.Close()

	c := &dns.Client{Net: "udp", Timeout: time.Second}
	m := new(dns.Msg)
	m.SetQuestion("hello.example.", dns.TypeA)
	r, _, err := c.Exchange(m, cfg.UDP)
	if err != nil || len(r.Answer) != 1 || r.Answer[0].Header().Ttl != 300 {
		t.Fatalf("default answer: %v %v", r, err)
	}
	m.SetQuestion("tc-1.example.", dns.TypeA)
	if r, _, _ = c.Exchange(m, cfg.UDP); !r.Truncated {
		t.Fatal("tc-* over UDP must be truncated")
	}
	tcp := &dns.Client{Net: "tcp", Timeout: time.Second}
	if r, _, _ = tcp.Exchange(m, cfg.TCP); len(r.Answer) != 100 {
		t.Fatalf("tc-* over TCP answers = %d", len(r.Answer))
	}
	m.SetQuestion("nx-1.example.", dns.TypeA)
	if r, _, _ = c.Exchange(m, cfg.UDP); r.Rcode != dns.RcodeNameError || len(r.Ns) != 1 {
		t.Fatalf("nx-*: %v", r)
	}

	pem, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	dot := &dns.Client{Net: "tcp-tls", Timeout: time.Second, TLSConfig: &tls.Config{RootCAs: pool, ServerName: "fixture.nexora.test"}}
	m.SetQuestion("dot.example.", dns.TypeA)
	if r, _, err = dot.Exchange(m, cfg.DoT); err != nil || len(r.Answer) != 1 {
		t.Fatalf("dot: %v %v", r, err)
	}
	wire, _ := m.Pack()
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "fixture.nexora.test"}, ForceAttemptHTTP2: true}}
	resp, err := hc.Post("https://"+cfg.DoH+"/dns-query", "application/dns-message", bytes.NewReader(wire))
	if err != nil || resp.StatusCode != 200 || resp.ProtoMajor != 2 {
		t.Fatalf("doh: %v %v", resp, err)
	}
	body, _ := io.ReadAll(resp.Body)
	var dm dns.Msg
	if err := dm.Unpack(body); err != nil || len(dm.Answer) != 1 {
		t.Fatalf("doh body: %v", err)
	}

	if n := fx.count("hello.example.", dns.TypeA); n != 1 {
		t.Fatalf("count = %d", n)
	}
	fx.setMode("blackhole")
	m.SetQuestion("hole.example.", dns.TypeA)
	if _, _, err = (&dns.Client{Net: "udp", Timeout: 300 * time.Millisecond}).Exchange(m, cfg.UDP); err == nil {
		t.Fatal("blackhole answered")
	}
}
```

- [ ] Write the failing harness test `e2e/harness/harness_test.go`:

```go
package harness_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestHarnessStandaloneEngineAnswersViaFixture(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	eng := env.StartStandaloneEngine(harness.BaseSnapshot(1, harness.UDPUpstream("fx", fx.UDP)), nil)
	name := harness.UniqueName("harness")
	r := harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
	if r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 {
		t.Fatalf("unexpected answer: %v", r)
	}
	if got := fx.Count(t, name, dns.TypeA); got != 1 {
		t.Fatalf("fixture count = %d", got)
	}
	if v := eng.Metric(t, "nexora_config_version", nil); v != 1 {
		t.Fatalf("config version metric = %v", v)
	}
}

func TestHarnessPostgres(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var one int
	if err := conn.QueryRow(ctx, "select 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("select 1: %v %d", err, one)
	}
}

func TestHarnessOtelcolStarts(t *testing.T) {
	env := harness.New(t)
	col := env.StartOtelcol(harness.OtelcolConfig{DebugFile: env.Dir + "/otel.jsonl"})
	if col.OTLPGRPC == "" {
		t.Fatal("no OTLP address")
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./e2e/fixtures/... ./e2e/harness/...` — expect FAIL with `undefined: startDNSFixture` and `no required module provides package github.com/piwi3910/nexora/e2e/harness`.
- [ ] Implement `certs.go`: `func writeCerts(dir string) (caPEM []byte, cert tls.Certificate, err error)` creating an ECDSA P-256 CA (`CN=nexora-fixture-ca`, 1 day) and a server certificate with DNS SAN `fixture.nexora.test` and IP SAN `127.0.0.1`, writing `ca.pem`, `server.pem`, `server-key.pem`.
- [ ] Implement `dns.go`: `type dnsConfig struct { UDP, TCP, DoT, DoH, Control, CertDir string }`; `func startDNSFixture(cfg dnsConfig) (*dnsFixture, error)` starting `dns.Server{Net: "udp"}`, `{Net: "tcp"}`, `{Net: "tcp-tls", TLSConfig}` with one handler, plus an `http.Server` with `TLSConfig{NextProtos: ["h2"]}` serving `POST /dns-query` (and `GET ?dns=` base64url) and the control `http.Server`. The handler increments `counts[strings.ToLower(name)+"|"+qtype]` under a mutex before applying `mode` (`blackhole` returns without writing; `servfail` replies SERVFAIL) and `delay` (`time.Sleep`), then answers by prefix per the table above; the UDP path of `tc-*` writes a reply with `Truncated=true` and no answers. `func (f *dnsFixture) count(name string, qtype uint16) int`, `setMode(string)`, `Close()`.
- [ ] Implement `http.go`: `PUT /lists/{name}` stores the body; `GET /lists/{name}` returns it (`text/plain`) or 500 when failing, increments hits; `POST /lists/{name}/fail` with `{"failing": bool}`; `GET /hits/{name}` -> `{"hits": n}`.
- [ ] Implement `oidc.go`: RSA-2048 signing key generated at start; `GET /.well-known/openid-configuration` (issuer = `http://<listen>`, `authorization_endpoint`, `token_endpoint`, `jwks_uri`, `userinfo_endpoint`, `code_challenge_methods_supported: ["S256"]`); `GET /jwks`; `GET /authorize` renders an HTML page with one `<button name="user" value="<username>">Sign in as <username></button>` per user inside a form that posts to `/authorize` carrying `client_id`, `redirect_uri`, `state`, `nonce`, `code_challenge`; `POST /authorize` issues a one-time code bound to the user, nonce and challenge and redirects to `redirect_uri?code=..&state=..`; `POST /token` checks `client_secret` (basic or form), the PKCE verifier (`base64url(sha256(verifier)) == challenge`), and returns `id_token` (RS256 JWT via `github.com/go-jose/go-jose/v4` with `iss`, `aud`, `sub`, `email`, `preferred_username`, `groups`, `nonce`, `exp`), `access_token`, `token_type: Bearer`; `GET /userinfo`. Users come from `--users-file` JSON `[{"username":..,"email":..,"groups":[..]}]`.
- [ ] Implement `main.go`: `flag.NewFlagSet` per subcommand, start, print `fixture ready`, block until SIGTERM.
- [ ] Implement `harness.go`: `New` creates `t.TempDir()`, registers `t.Cleanup` that stops every started `Proc` in reverse order and, when the test failed, prints the last 200 lines of each log with `t.Logf`; `FreePort` binds TCP and UDP on the same `127.0.0.1` port to confirm both are free; `Start` runs the binary with stdout/stderr to `<Dir>/<name>.log` and `Setpgid: true`; `WaitLog` polls the log file every 50 ms; `Eventually` retries `cond` every 100 ms until nil or `t.Fatalf` with the last error.
- [ ] Implement `fixture.go`: `StartDNSFixture` picks five ports, starts `nexora-fixture dns ...` with `--cert-dir <Dir>/fixture-<n>`, waits for `fixture ready`, reads `ca.pem` into `CACertPEM`, sets `TLSName = "fixture.nexora.test"`; `Count`/`Total` GET `/stats`; the HTTP and OIDC fixtures are analogous (`StartOIDCFixture` writes the users file and a random client secret file, `ClientID = "nexora"`).
- [ ] Implement `snapshot.go`:

```go
package harness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

func BaseSnapshot(version uint64, ups ...*controlv1.Upstream) *controlv1.ConfigSnapshot {
	return &controlv1.ConfigSnapshot{
		Version:       version,
		Resolver:      &controlv1.ResolverConfig{Strategy: controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_ORDERED},
		Cache:         &controlv1.CacheConfig{MaxBytes: 64 << 20, MinTtl: 0, MaxTtl: 86400, NegativeMaxTtl: 3600, StaleWindow: 86400},
		Upstreams:     ups,
		AclAllowCidrs: []string{"127.0.0.0/8", "::1/128"},
		Filter:        &controlv1.FilterConfig{BlockMode: controlv1.BlockMode_BLOCK_MODE_NULL_IP, BlockTtl: 60},
		Telemetry:     &controlv1.TelemetryConfig{},
	}
}

func UDPUpstream(id, addr string) *controlv1.Upstream {
	return &controlv1.Upstream{Id: id, Name: id, Protocol: controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_UDP, Address: addr, TimeoutMs: 250}
}

func DoTUpstream(id string, f *DNSFixture) *controlv1.Upstream {
	return &controlv1.Upstream{Id: id, Name: id, Protocol: controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_DOT, Address: f.DoT, TlsServerName: f.TLSName, TimeoutMs: 1000, CaCertificatePem: f.CACertPEM}
}

func DoHUpstream(id string, f *DNSFixture) *controlv1.Upstream {
	_, port, _ := strings.Cut(f.DoH, ":")
	return &controlv1.Upstream{Id: id, Name: id, Protocol: controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_DOH, DohUrl: "https://" + f.TLSName + ":" + port + "/dns-query", TimeoutMs: 1000, CaCertificatePem: f.CACertPEM}
}

func BlocklistBlob(name string, domains ...string) (*controlv1.BlobRef, []byte) {
	d := append([]string(nil), domains...)
	sort.Strings(d)
	var buf bytes.Buffer
	enc, _ := zstd.NewWriter(&buf)
	_, _ = enc.Write([]byte(strings.Join(d, "\n") + "\n"))
	_ = enc.Close()
	sum := sha256.Sum256(buf.Bytes())
	return &controlv1.BlobRef{Sha256: hex.EncodeToString(sum[:]), Size: uint64(buf.Len()), Name: name}, buf.Bytes()
}
```

- [ ] Implement `engine.go`: `StartStandaloneEngine` picks a DNS port and a metrics port, writes blobs to `<Dir>/engine-<n>/blobs/<sha256>`, the snapshot to `<Dir>/engine-<n>/snapshot.binpb` (`proto.Marshal`), and `engine.toml`:

```go
toml := fmt.Sprintf(`node_name = "e2e-%d"
state_dir = %q
listen_udp = ["127.0.0.1:%d"]
listen_tcp = ["127.0.0.1:%d"]
metrics_listen = "127.0.0.1:%d"
workers = 2
standalone_snapshot = %q
standalone_blob_dir = %q
`, n, stateDir, dnsPort, dnsPort, metricsPort, snapPath, blobDir)
```

Then starts `nexora-engine --config <path>` with `NEXORA_DOH_RESOLVE=fixture.nexora.test=127.0.0.1` and waits (10 s) for `WaitVersion(snap.Version)`. The process environment passes `NEXORA_DOH_RESOLVE` so DoH certificates for `fixture.nexora.test` resolve to loopback. `Reload` rewrites blobs + snapshot, sends SIGHUP and waits for the version. `Metric` GETs `/metrics`, parses with `github.com/prometheus/common/expfmt` (`TextParser`), returns the sum of samples whose labels include `labels`.

- [ ] Implement `dnsclient.go` with `miekg/dns`: `Query` sets RD, adds OPT when `EDNSSize > 0` or `Cookie != nil` (`dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: hex.EncodeToString(cookie)}`), uses `Net: "tcp"` when `TCP`, default timeout 2 s, and returns the round-trip time.
- [ ] Implement `postgres.go`: data dir `<Dir>/pg`; when `os.Geteuid() == 0` look up user `dev`, `chown -R` the dir and run `initdb`/`pg_ctl` with `SysProcAttr.Credential{Uid, Gid}`; `initdb -D <dir> -U nexora --auth=trust -E UTF8`; `pg_ctl -D <dir> -o "-p <port> -k <dir> -c listen_addresses=127.0.0.1 -c fsync=off" -l <dir>/log start -w`; `createdb -h 127.0.0.1 -p <port> -U nexora nexora`; `URL = "postgres://nexora@127.0.0.1:<port>/nexora?sslmode=disable"`; cleanup runs `pg_ctl stop -m immediate`.
- [ ] Implement `otelcol.go`: writes this config (sections omitted when the corresponding field is empty) and starts `otelcol-contrib --config <path>`, waiting until the OTLP gRPC port accepts connections:

```yaml
receivers:
  otlp:
    protocols:
      grpc: { endpoint: "127.0.0.1:{{.GRPCPort}}" }
processors:
  batch: { timeout: 200ms }
exporters:
  debug: { verbosity: basic }
  file: { path: "{{.DebugFile}}" }
  opensearch:
    http:
      { endpoint: "{{.OpenSearchURL}}", tls: { insecure_skip_verify: true } }
    logs_index: "nexora-querylog"
    logs_index_time_format: "yyyy.MM.dd"
  otlp/jaeger:
    endpoint: "{{.JaegerOTLP}}"
    tls: { insecure: true }
service:
  telemetry: { metrics: { level: none } }
  pipelines:
    logs:
      {
        receivers: [otlp],
        processors: [batch],
        exporters: [debug, file, opensearch],
      }
    traces:
      {
        receivers: [otlp],
        processors: [batch],
        exporters: [debug, otlp/jaeger],
      }
    metrics: { receivers: [otlp], processors: [batch], exporters: [debug] }
```

- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 ./e2e/fixtures/... ./e2e/harness/...'` — expect PASS: `ok github.com/piwi3910/nexora/e2e/fixtures/cmd/nexora-fixture` and `ok github.com/piwi3910/nexora/e2e/harness`.
- [ ] Commit: `git add e2e go.mod go.sum && git commit -m "e2e: process harness, DNS/HTTP/OIDC fixtures, Postgres and otelcol helpers"`.

## Task 11: Forwarding acceptance tests over the wire

Files:

- `e2e/forward_test.go` (create) — `TestForwardCacheTTL`, `TestUpstreamFailover`, `TestDedupAllWaitersAnswered`
- `e2e/edns_test.go` (create) — `TestEDNSTruncationTCP`
- `e2e/main_test.go` (create) — `TestMain` that exits 1 with a clear message when the binaries are missing, and the `hexDecode` helper

Interfaces:

- Consumes `harness.New`, `StartDNSFixture`, `StartStandaloneEngine`, `BaseSnapshot`, `UDPUpstream`, `DoTUpstream`, `DoHUpstream`, `MustQuery`, `Query`, `QueryOpts`, `UniqueName`, `DNSFixture.{Count,SetMode,SetDelay}`, `Engine.Metric` (Task 10); engine behaviour from Tasks 3–9.
- Produces acceptance tests `TestForwardCacheTTL` (subtests `udp`, `dot`, `doh`), `TestUpstreamFailover`, `TestDedupAllWaitersAnswered`, `TestEDNSTruncationTCP`.

- [ ] Write the failing tests `e2e/forward_test.go`:

```go
package e2e

import (
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/e2e/harness"
)

func TestForwardCacheTTL(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	cases := []struct {
		name string
		up   *controlv1.Upstream
	}{
		{"udp", harness.UDPUpstream("udp", fx.UDP)},
		{"dot", harness.DoTUpstream("dot", fx)},
		{"doh", harness.DoHUpstream("doh", fx)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := env.StartStandaloneEngine(harness.BaseSnapshot(1, tc.up), nil)
			name := harness.UniqueName("ttl-" + tc.name)
			first := harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
			if first.Rcode != dns.RcodeSuccess || len(first.Answer) != 1 {
				t.Fatalf("first answer: %v", first)
			}
			if got := fx.Count(t, name, dns.TypeA); got != 1 {
				t.Fatalf("miss must reach the upstream once, count=%d", got)
			}
			firstTTL := first.Answer[0].Header().Ttl
			time.Sleep(2200 * time.Millisecond)
			second := harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
			if got := fx.Count(t, name, dns.TypeA); got != 1 {
				t.Fatalf("second query reached the upstream: count=%d", got)
			}
			if len(second.Answer) != 1 || second.Answer[0].Header().Ttl >= firstTTL {
				t.Fatalf("TTL not decremented: first=%d second=%v", firstTTL, second.Answer)
			}
			if hits := eng.Metric(t, "nexora_cache_hits_total", nil); hits < 1 {
				t.Fatalf("cache hit not counted: %v", hits)
			}
		})
	}
}

func TestUpstreamFailover(t *testing.T) {
	env := harness.New(t)
	primary := env.StartDNSFixture()
	secondary := env.StartDNSFixture()
	eng := env.StartStandaloneEngine(harness.BaseSnapshot(1,
		harness.UDPUpstream("primary", primary.UDP), harness.UDPUpstream("secondary", secondary.UDP)), nil)

	warm := harness.UniqueName("warm")
	harness.MustQuery(t, eng.DNS, warm, dns.TypeA, harness.QueryOpts{})
	if primary.Count(t, warm, dns.TypeA) != 1 {
		t.Fatal("ordered strategy must use the primary while it is healthy")
	}

	primary.SetMode(t, "blackhole")
	for i := 0; i < 50; i++ {
		name := harness.UniqueName("failover")
		r, rtt, err := harness.Query(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{Timeout: time.Second})
		if err != nil {
			t.Fatalf("query %d failed within 1s: %v", i, err)
		}
		if r.Rcode != dns.RcodeSuccess {
			t.Fatalf("query %d got %s while secondary is healthy", i, dns.RcodeToString[r.Rcode])
		}
		if rtt > time.Second {
			t.Fatalf("query %d took %v", i, rtt)
		}
	}
	if secondary.Total(t) < 50 {
		t.Fatalf("secondary served %d queries", secondary.Total(t))
	}
	if up := eng.Metric(t, "nexora_upstream_up", map[string]string{"upstream": "primary"}); up != 0 {
		t.Fatalf("primary still reported up: %v", up)
	}
}

func TestDedupAllWaitersAnswered(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	eng := env.StartStandaloneEngine(harness.BaseSnapshot(1, harness.UDPUpstream("fx", fx.UDP)), nil)
	fx.SetDelay(t, 500*time.Millisecond)
	name := harness.UniqueName("herd")

	const clients = 1000
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, _, err := harness.Query(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{Timeout: 3 * time.Second})
			if err != nil {
				errs <- err
				return
			}
			if r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 {
				errs <- &dns.Error{}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	failed := 0
	for err := range errs {
		failed++
		if failed <= 5 {
			t.Logf("client error: %v", err)
		}
	}
	got := fx.Count(t, name, dns.TypeA)
	if got < 1 {
		t.Fatal("upstream never saw the query")
	}
	if failed > 0 {
		t.Fatalf("%d of %d clients were not answered", failed, clients)
	}
	if got != 1 {
		t.Fatalf("upstream queries = %d, want exactly 1", got)
	}
}
```

- [ ] Write the failing test `e2e/edns_test.go`:

```go
package e2e

import (
	"bytes"
	"testing"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func cookieOf(t *testing.T, m *dns.Msg) string {
	t.Helper()
	if o := m.IsEdns0(); o != nil {
		for _, opt := range o.Option {
			if c, ok := opt.(*dns.EDNS0_COOKIE); ok {
				return c.Cookie
			}
		}
	}
	return ""
}

func TestEDNSTruncationTCP(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	eng := env.StartStandaloneEngine(harness.BaseSnapshot(1, harness.UDPUpstream("fx", fx.UDP)), nil)

	// Positive path first: a small answer over UDP is complete.
	small := harness.MustQuery(t, eng.DNS, harness.UniqueName("small"), dns.TypeA, harness.QueryOpts{EDNSSize: 1232})
	if small.Truncated || len(small.Answer) != 1 {
		t.Fatalf("small answer: %v", small)
	}

	big := harness.UniqueName("big")
	udp := harness.MustQuery(t, eng.DNS, big, dns.TypeA, harness.QueryOpts{EDNSSize: 1232})
	if !udp.Truncated || len(udp.Answer) != 0 {
		t.Fatalf("oversized UDP answer must be TC=1 with no answers: tc=%v answers=%d", udp.Truncated, len(udp.Answer))
	}
	tcp := harness.MustQuery(t, eng.DNS, big, dns.TypeA, harness.QueryOpts{TCP: true, EDNSSize: 1232})
	if tcp.Truncated || len(tcp.Answer) != 100 {
		t.Fatalf("TCP answer: tc=%v answers=%d", tcp.Truncated, len(tcp.Answer))
	}
	noEDNS := harness.MustQuery(t, eng.DNS, big, dns.TypeA, harness.QueryOpts{})
	if !noEDNS.Truncated {
		t.Fatal("answer over 512 bytes without EDNS served without TC")
	}
	if fx.Count(t, big, dns.TypeA) != 1 {
		t.Fatalf("big answer fetched %d times; the cached full answer must serve TCP and UDP", fx.Count(t, big, dns.TypeA))
	}

	// Upstream truncation: engine retries over TCP and does not cache the truncated reply.
	tc := harness.UniqueName("tc")
	viaTCP := harness.MustQuery(t, eng.DNS, tc, dns.TypeA, harness.QueryOpts{TCP: true})
	if len(viaTCP.Answer) != 100 {
		t.Fatalf("engine did not retry a truncated upstream reply over TCP: %d answers", len(viaTCP.Answer))
	}
	again := harness.MustQuery(t, eng.DNS, tc, dns.TypeA, harness.QueryOpts{TCP: true})
	if len(again.Answer) != 100 {
		t.Fatalf("a truncated upstream reply was cached: %d answers", len(again.Answer))
	}

	// DNS cookies are echoed.
	client := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	r := harness.MustQuery(t, eng.DNS, harness.UniqueName("cookie"), dns.TypeA, harness.QueryOpts{EDNSSize: 1232, Cookie: client})
	c := cookieOf(t, r)
	if len(c) != 48 || c[:16] != "0102030405060708" {
		t.Fatalf("cookie not echoed with a server cookie: %q", c)
	}
	full, _ := hexDecode(c)
	r2 := harness.MustQuery(t, eng.DNS, harness.UniqueName("cookie2"), dns.TypeA, harness.QueryOpts{EDNSSize: 1232, Cookie: full})
	if c2 := cookieOf(t, r2); len(c2) != 48 || !bytes.Equal([]byte(c2[:16]), []byte(c[:16])) {
		t.Fatalf("second cookie exchange: %q", c2)
	}
}
```

- [ ] Write `e2e/main_test.go`:

```go
package e2e

import (
	"encoding/hex"
	"fmt"
	"os"
	"testing"
)

func hexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }

func TestMain(m *testing.M) {
	if os.Getenv("NEXORA_E2E_BIN_DIR") == "" {
		if _, err := os.Stat("../bin/nexora-engine"); err != nil {
			fmt.Fprintln(os.Stderr, "e2e: no binaries; run `make e2e-build` (or `make e2e`)")
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}
```

- [ ] Prove the tests fail against a broken engine (mutation check; the engine from Tasks 3–9 already exists, so red is demonstrated by breaking it): in `engine/src/server/mod.rs` change the leader/follower join in `resolve_miss` to `let join = crate::inflight::InFlight::new().join(job.key);` (every miss leads) and skip the `rt.cache.insert(...)` call, then run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -run "TestForwardCacheTTL|TestDedupAllWaitersAnswered|TestEDNSTruncationTCP" ./e2e/'` — expect FAIL with `second query reached the upstream`, `upstream queries = ` (a count above 1) and `big answer fetched`. Restore the file with `git checkout engine/src/server/mod.rs`.
- [ ] Prove `TestUpstreamFailover` bites: in `engine/src/upstream/mod.rs` make `forward` return the first attempt's error instead of trying the next candidate, run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -run TestUpstreamFailover ./e2e/'` — expect FAIL with `got SERVFAIL while secondary is healthy`; restore with `git checkout engine/src/upstream/mod.rs`.
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -run "TestForwardCacheTTL|TestUpstreamFailover|TestDedupAllWaitersAnswered|TestEDNSTruncationTCP" -v ./e2e/'` — expect PASS: `--- PASS: TestForwardCacheTTL/udp`, `/dot`, `/doh`, `--- PASS: TestUpstreamFailover`, `--- PASS: TestDedupAllWaitersAnswered`, `--- PASS: TestEDNSTruncationTCP`.
- [ ] Commit: `git add e2e/forward_test.go e2e/edns_test.go e2e/main_test.go && git commit -m "e2e: forwarding, failover, dedup and EDNS truncation acceptance tests"`.

## Task 12: Management plane foundations — env config, PostgreSQL store and schema, PKI, CLI

Files:

- `mgmt/internal/config/config.go` (create), `mgmt/internal/config/config_test.go` (create)
- `mgmt/migrations/00001_init.sql` (create) — full M1 schema
- `mgmt/migrations/embed.go` (create) — `package migrations; //go:embed *.sql; var FS embed.FS`
- `mgmt/internal/store/store.go` (create) — pgx pool, goose migrations, transactions, error mapping
- `mgmt/internal/store/store_test.go` (create)
- `mgmt/internal/pki/pki.go` (create) — CA, server/engine certificates, join tokens
- `mgmt/internal/pki/pki_test.go` (create)
- `mgmt/cmd/nexora-mgmt/main.go` (create) — `serve | migrate | ca init --out <dir> | user create --admin` dispatch (this task wires `migrate` and `ca init`; `serve` is wired in Task 13 and extended in Tasks 15, 17, 18; `user create --admin` in Task 15)

Interfaces (produced):

- `config.Load(getenv func(string) string) (config.Config, error)`; `type Config struct { DatabaseURL, HTTPListen, GRPCListen, CACertFile, CAKeyFile string; GRPCServerNames []string; PublicURL string; SecureCookies bool; OIDC OIDCConfig; QueryLogBackend string; QueryLogBuiltinCapacity int; OpenSearch OpenSearchConfig; OTLPEndpoint string }`; `type OIDCConfig struct { Issuer, ClientID, ClientSecretFile, AdminGroup, OperatorGroup string }`; `func (o OIDCConfig) Enabled() bool`; `type OpenSearchConfig struct { URL, Index, Username, PasswordFile string }`. Env vars and defaults exactly per `docs/architecture.md`: `NEXORA_HTTP_LISTEN=:8080`, `NEXORA_GRPC_LISTEN=:9443`, `NEXORA_SECURE_COOKIES=true`, `NEXORA_QUERYLOG_BACKEND=builtin`, `NEXORA_QUERYLOG_BUILTIN_CAPACITY=200000`, `NEXORA_OPENSEARCH_INDEX=nexora-querylog-*`.
- `store.Open(ctx context.Context, url string) (*store.Store, error)`; `type Store struct { Pool *pgxpool.Pool }`; `func (s *Store) Migrate(ctx context.Context) error`; `func (s *Store) Close()`; `func (s *Store) InTx(ctx context.Context, fn func(pgx.Tx) error) error` (READ COMMITTED, retried up to 3 times on SQLSTATE `40001`/`40P01`); `var ErrNotFound, ErrConflict, ErrUnavailable error`; `func MapError(err error) error` (connection/pool errors and SQLSTATE class `08` -> `ErrUnavailable`, `pgx.ErrNoRows` -> `ErrNotFound`, unique violation `23505` -> `ErrConflict`).
- SQL tables: `users`, `sessions`, `api_tokens`, `setup_tokens`, `oidc_login_states`, `audit_log`, `config_versions`, `resolver_settings`, `upstreams`, `access_control`, `blobs`, `filter_lists`, `allowlist`, `join_tokens`, `engines`, `engine_stats`, `instances`.
- `pki.InitCA(outDir string) error` (writes `ca.crt` 0644 and `ca.key` 0600, refuses to overwrite); `pki.LoadCA(certFile, keyFile string) (*pki.CA, error)`; `type CA struct { Cert *x509.Certificate; Key *ecdsa.PrivateKey; CertPEM []byte }`; `func (ca *CA) Fingerprint() string` (sha256 hex of DER); `func (ca *CA) ServerCertificate(names []string, validity time.Duration) (tls.Certificate, error)` (names parsed as IP SANs when they parse as IPs); `func (ca *CA) SignEngineCSR(csrDER []byte, engineID string, validity time.Duration) (der []byte, serial string, err error)` (requires ECDSA P-256 CSR with a valid signature; CN = engineID; ExtKeyUsage ClientAuth); `func (ca *CA) Pool() *x509.CertPool`; `const EngineCertValidity = 365 * 24 * time.Hour`; `pki.NewJoinToken(caFingerprint string) (token, secret string, err error)`; `pki.ParseJoinToken(token string) (secret, caFingerprint string, err error)`; `pki.HashSecret(secret string) []byte`.
- CLI: `nexora-mgmt migrate` (reads `NEXORA_DATABASE_URL`, prints `migrations applied`), `nexora-mgmt ca init --out <dir>` (prints `ca fingerprint: <hex>`).

- [ ] Write the failing tests `mgmt/internal/config/config_test.go`:

```go
package config_test

import (
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/config"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaults(t *testing.T) {
	c, err := config.Load(env(map[string]string{"NEXORA_DATABASE_URL": "postgres://x/y", "NEXORA_CA_CERT_FILE": "/ca.crt", "NEXORA_CA_KEY_FILE": "/ca.key"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPListen != ":8080" || c.GRPCListen != ":9443" || !c.SecureCookies || c.QueryLogBackend != "builtin" || c.QueryLogBuiltinCapacity != 200000 || c.OpenSearch.Index != "nexora-querylog-*" {
		t.Fatalf("defaults wrong: %+v", c)
	}
	if c.OIDC.Enabled() {
		t.Fatal("OIDC must be disabled without an issuer")
	}
}

func TestLoadValidation(t *testing.T) {
	base := map[string]string{"NEXORA_DATABASE_URL": "postgres://x/y", "NEXORA_CA_CERT_FILE": "/c", "NEXORA_CA_KEY_FILE": "/k"}
	for name, mutate := range map[string]func(m map[string]string){
		"missing db":        func(m map[string]string) { delete(m, "NEXORA_DATABASE_URL") },
		"bad backend":       func(m map[string]string) { m["NEXORA_QUERYLOG_BACKEND"] = "loki" },
		"opensearch no url": func(m map[string]string) { m["NEXORA_QUERYLOG_BACKEND"] = "opensearch" },
		"bad cookies":       func(m map[string]string) { m["NEXORA_SECURE_COOKIES"] = "maybe" },
		"oidc no client":    func(m map[string]string) { m["NEXORA_OIDC_ISSUER"] = "https://idp" },
	} {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		mutate(m)
		if _, err := config.Load(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	c, err := config.Load(env(map[string]string{"NEXORA_DATABASE_URL": "postgres://x/y", "NEXORA_CA_CERT_FILE": "/c", "NEXORA_CA_KEY_FILE": "/k", "NEXORA_GRPC_SERVER_NAMES": "mgmt, 10.0.0.5"}))
	if err != nil || len(c.GRPCServerNames) != 2 || c.GRPCServerNames[1] != "10.0.0.5" {
		t.Fatalf("server names: %v %v", c.GRPCServerNames, err)
	}
}
```

- [ ] Write the failing tests `mgmt/internal/pki/pki_test.go`:

```go
package pki_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

func TestCAInitLoadAndIssue(t *testing.T) {
	dir := t.TempDir()
	if err := pki.InitCA(dir); err != nil {
		t.Fatal(err)
	}
	if err := pki.InitCA(dir); err == nil {
		t.Fatal("InitCA must refuse to overwrite")
	}
	ca, err := pki.LoadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ca.Fingerprint()) != 64 {
		t.Fatalf("fingerprint %q", ca.Fingerprint())
	}
	srv, err := ca.ServerCertificate([]string{"mgmt.nexora.test", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(srv.Certificate[0])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: ca.Pool(), DNSName: "mgmt.nexora.test"}); err != nil {
		t.Fatalf("server cert does not verify: %v", err)
	}
	if len(leaf.IPAddresses) != 1 {
		t.Fatal("IP SAN missing")
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored"}}, key)
	der, serial, err := ca.SignEngineCSR(csr, "0b0e7f3c-1111-4222-8333-944455556666", pki.EngineCertValidity)
	if err != nil || serial == "" {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	if cert.Subject.CommonName != "0b0e7f3c-1111-4222-8333-944455556666" {
		t.Fatalf("CN = %q", cert.Subject.CommonName)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("engine cert: %v", err)
	}
	if _, _, err := ca.SignEngineCSR([]byte("garbage"), "x", time.Hour); err == nil {
		t.Fatal("garbage CSR accepted")
	}
}

func TestJoinTokenFormat(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	tok, secret, err := pki.NewJoinToken(fp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "nxj1.") || !strings.HasSuffix(tok, "."+fp) {
		t.Fatalf("token %q", tok)
	}
	s2, fp2, err := pki.ParseJoinToken(tok)
	if err != nil || s2 != secret || fp2 != fp {
		t.Fatalf("parse: %v %v %v", s2, fp2, err)
	}
	for _, bad := range []string{"nxj2.AAAA." + fp, "nxj1..", "nxj1.AAAA.zz", "nxj1.AAAA"} {
		if _, _, err := pki.ParseJoinToken(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if len(pki.HashSecret(secret)) != 32 {
		t.Fatal("hash length")
	}
}
```

- [ ] Write the failing test `mgmt/internal/store/store_test.go`:

```go
package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func TestMigrateSeedsAndMapsErrors(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	for _, table := range []string{"users", "sessions", "api_tokens", "setup_tokens", "oidc_login_states", "audit_log", "config_versions", "resolver_settings", "upstreams", "access_control", "blobs", "filter_lists", "allowlist", "join_tokens", "engines", "engine_stats", "instances"} {
		var ok bool
		if err := st.Pool.QueryRow(ctx, "select to_regclass($1) is not null", "public."+table).Scan(&ok); err != nil || !ok {
			t.Errorf("table %s missing (%v)", table, err)
		}
	}
	var cidrs int
	if err := st.Pool.QueryRow(ctx, "select cardinality(allow_cidrs) from access_control").Scan(&cidrs); err != nil || cidrs != 8 {
		t.Fatalf("ACL seed = %d (%v)", cidrs, err)
	}
	if _, err := st.Pool.Exec(ctx, "insert into upstreams(name, protocol, address, position) values ('a','udp','1.1.1.1:53',0)"); err != nil {
		t.Fatal(err)
	}
	_, err = st.Pool.Exec(ctx, "insert into upstreams(name, protocol, address, position) values ('a','udp','1.1.1.1:53',1)")
	if !errors.Is(store.MapError(err), store.ErrConflict) {
		t.Fatalf("duplicate name -> %v", store.MapError(err))
	}
	st.Close()
	pgDown := env.StartPostgres()
	st2, err := store.Open(ctx, pgDown.URL)
	if err != nil {
		t.Fatal(err)
	}
	harness.StopPostgres(t, pgDown)
	err = st2.InTx(ctx, func(tx pgxTx) error { _, err := tx.Exec(ctx, "select 1"); return err })
	if !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("down database -> %v, want ErrUnavailable", err)
	}
}
```

with `type pgxTx = pgx.Tx` declared in the test file (`import "github.com/jackc/pgx/v5"`), and add `func StopPostgres(t *testing.T, pg *Postgres)` (runs `pg_ctl stop -m immediate`) to `e2e/harness/postgres.go`.

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/config/... ./mgmt/internal/pki/... ./mgmt/internal/store/...` — expect FAIL with `no required module provides package github.com/piwi3910/nexora/mgmt/internal/config`.
- [ ] Write `mgmt/migrations/00001_init.sql`:

```sql
-- +goose Up
create table instances (
  id text primary key,
  started_at timestamptz not null default now(),
  heartbeat_at timestamptz not null default now()
);

create table users (
  id uuid primary key default gen_random_uuid(),
  username text not null unique check (username ~ '^[A-Za-z0-9._@-]{1,64}$'),
  email text not null default '',
  password_hash text,
  role text not null check (role in ('viewer', 'operator', 'admin')),
  source text not null default 'local' check (source in ('local', 'oidc')),
  oidc_subject text unique,
  disabled boolean not null default false,
  revision bigint not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table sessions (
  token_hash bytea primary key,
  user_id uuid not null references users(id) on delete cascade,
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  last_seen_at timestamptz not null default now()
);
create index sessions_user on sessions(user_id);

create table api_tokens (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  name text not null,
  prefix text not null,
  token_hash bytea not null unique,
  role text not null check (role in ('viewer', 'operator', 'admin')),
  created_at timestamptz not null default now(),
  expires_at timestamptz,
  last_used_at timestamptz,
  revoked_at timestamptz
);

create table setup_tokens (
  singleton boolean primary key default true check (singleton),
  token_hash bytea not null,
  created_by_instance text not null,
  created_at timestamptz not null default now()
);

create table oidc_login_states (
  state text primary key,
  nonce text not null,
  code_verifier text not null,
  return_to text not null default '/',
  expires_at timestamptz not null
);

create table audit_log (
  id bigserial primary key,
  at timestamptz not null default now(),
  actor_type text not null check (actor_type in ('user', 'api_token', 'system')),
  actor_id text not null,
  actor_name text not null,
  action text not null,
  target_type text not null,
  target_id text not null,
  diff jsonb not null,
  config_version bigint
);
create index audit_log_at on audit_log(at desc);

create table config_versions (
  version bigint primary key,
  created_at timestamptz not null default now(),
  created_by text not null,
  summary text not null default '',
  snapshot bytea not null
);

create table resolver_settings (
  singleton boolean primary key default true check (singleton),
  strategy text not null default 'ordered' check (strategy in ('ordered', 'fastest')),
  cache_max_bytes bigint not null default 268435456 check (cache_max_bytes >= 1048576),
  cache_min_ttl integer not null default 0 check (cache_min_ttl >= 0),
  cache_max_ttl integer not null default 86400 check (cache_max_ttl >= cache_min_ttl),
  cache_negative_max_ttl integer not null default 3600 check (cache_negative_max_ttl >= 0),
  cache_stale_window integer not null default 86400 check (cache_stale_window >= 0),
  block_mode text not null default 'null_ip' check (block_mode in ('null_ip', 'nxdomain', 'refused')),
  block_ttl integer not null default 60 check (block_ttl >= 0),
  otlp_endpoint text not null default '',
  trace_sample_one_in integer not null default 0 check (trace_sample_one_in >= 0),
  trace_slow_threshold_us integer not null default 100000 check (trace_slow_threshold_us >= 0),
  revision bigint not null default 1,
  updated_at timestamptz not null default now()
);
insert into resolver_settings default values;

create table upstreams (
  id uuid primary key default gen_random_uuid(),
  name text not null unique,
  protocol text not null check (protocol in ('udp', 'tcp', 'dot', 'doh')),
  address text not null default '',
  tls_server_name text not null default '',
  doh_url text not null default '',
  timeout_ms integer not null default 250 check (timeout_ms between 50 and 5000),
  ca_certificate_pem text not null default '',
  position integer not null,
  enabled boolean not null default true,
  revision bigint not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table access_control (
  singleton boolean primary key default true check (singleton),
  allow_cidrs cidr[] not null default array[
    '127.0.0.0/8', '::1/128', '10.0.0.0/8', '172.16.0.0/12',
    '192.168.0.0/16', '100.64.0.0/10', 'fc00::/7', 'fe80::/10']::cidr[],
  revision bigint not null default 1,
  updated_at timestamptz not null default now()
);
insert into access_control default values;

create table blobs (
  sha256 text primary key check (sha256 ~ '^[0-9a-f]{64}$'),
  size bigint not null,
  data bytea not null,
  created_at timestamptz not null default now()
);

create table filter_lists (
  id uuid primary key default gen_random_uuid(),
  name text not null unique,
  kind text not null check (kind in ('block', 'allow')),
  url text not null check (url ~ '^https?://'),
  refresh_interval_seconds integer not null default 86400 check (refresh_interval_seconds >= 300),
  enabled boolean not null default true,
  current_blob_sha256 text references blobs(sha256),
  entry_count integer not null default 0,
  invalid_line_count integer not null default 0,
  last_success_at timestamptz,
  last_attempt_at timestamptz,
  last_error text not null default '',
  revision bigint not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table allowlist (
  singleton boolean primary key default true check (singleton),
  domains text[] not null default '{}',
  revision bigint not null default 1,
  updated_at timestamptz not null default now()
);
insert into allowlist default values;

create table join_tokens (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  secret_hash bytea not null unique,
  created_by text not null,
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  revoked_at timestamptz,
  uses integer not null default 0
);

create table engines (
  id uuid primary key default gen_random_uuid(),
  node_name text not null,
  join_token_id uuid references join_tokens(id) on delete set null,
  certificate_serial text not null,
  engine_version text not null default '',
  enrolled_at timestamptz not null default now(),
  last_seen_at timestamptz,
  connected_instance text references instances(id) on delete set null,
  applied_version bigint not null default 0,
  rejected_version bigint,
  rejected_reason text not null default '',
  persist_error text not null default '',
  version_ahead boolean not null default false,
  deleted_at timestamptz
);

create table engine_stats (
  engine_id uuid not null references engines(id) on delete cascade,
  at timestamptz not null,
  stats bytea not null,
  primary key (engine_id, at)
);

-- +goose Down
drop table engine_stats, engines, join_tokens, allowlist, filter_lists, blobs, access_control,
  upstreams, resolver_settings, config_versions, audit_log, oidc_login_states, setup_tokens,
  api_tokens, sessions, users, instances;
```

- [ ] Implement `store.go`: `Open` uses `pgxpool.ParseConfig` (MaxConns 16, `ConnectTimeout` 5 s) and `pgxpool.NewWithConfig`; `Migrate` runs `goose.SetBaseFS(migrations.FS)`, `goose.SetDialect("postgres")`, `goose.UpContext(ctx, stdlib.OpenDBFromPool(pool), ".")` under `pg_advisory_lock(hashtext('nexora:migrate'))` so concurrent instances serialise; `InTx` calls `pool.BeginTx`, runs `fn`, commits, maps every error through `MapError`.
- [ ] Implement `config.go` per the interface (booleans parsed by `strconv.ParseBool`, `NEXORA_GRPC_SERVER_NAMES` split on commas and trimmed, errors name the variable; `opensearch` backend requires `NEXORA_OPENSEARCH_URL`; `NEXORA_OIDC_ISSUER` requires `NEXORA_OIDC_CLIENT_ID` and `NEXORA_OIDC_CLIENT_SECRET_FILE`; CA files required).
- [ ] Implement `pki.go`: ECDSA P-256 CA with `CN=Nexora CA`, `IsCA`, `KeyUsageCertSign`, 10-year validity; random 128-bit serials; `ServerCertificate` generates a fresh P-256 key, ExtKeyUsage ServerAuth; `NewJoinToken` uses 20 random bytes encoded with `base32.StdEncoding.WithPadding(base32.NoPadding)`; `ParseJoinToken` requires exactly three dot-separated parts, prefix `nxj1`, a non-empty base32 secret and a 64-char lowercase hex fingerprint; `HashSecret` is SHA-256.
- [ ] Implement `mgmt/cmd/nexora-mgmt/main.go` dispatching `os.Args[1]`: `migrate` -> `config.Load(os.Getenv)` (only `NEXORA_DATABASE_URL` needed; CA vars not required for `migrate`, so `migrate` reads the DB URL directly), `store.Open`, `Migrate`; `ca init --out <dir>` -> `pki.InitCA`, print fingerprint; unknown -> usage on stderr and exit 2.
- [ ] Run `scripts/dev-exec.sh go test -count=1 ./mgmt/internal/config/... ./mgmt/internal/pki/... ./mgmt/internal/store/...` — expect PASS: three `ok` lines.
- [ ] Commit: `git add mgmt e2e/harness/postgres.go go.mod go.sum && git commit -m "mgmt: env config, PostgreSQL schema and store, PKI and CLI skeleton"`.

## Task 13: Snapshot builder, transactional mutations with audit, and the EngineControl gRPC server

Files:

- `mgmt/internal/auth/audit.go` (create) — `Actor`, `Change`, `WriteAudit` (the rest of `internal/auth` arrives in Task 14)
- `mgmt/internal/snapshot/snapshot.go` (create) — `Build`, `Mutate`, `Latest`, `PublishRaw`, `EnsureInitial`
- `mgmt/internal/snapshot/snapshot_test.go` (create)
- `mgmt/internal/control/server.go` (create) — `EngineControl` implementation
- `mgmt/internal/control/hub.go` (create) — per-instance stream registry + `LISTEN nexora_config`
- `mgmt/internal/control/tls.go` (create) — gRPC TLS config, peer identity
- `mgmt/internal/control/jointokens.go` (create) — join token creation/lookup
- `mgmt/internal/control/instances.go` (create) — instance heartbeat
- `mgmt/internal/control/control_test.go` (create)
- `mgmt/cmd/nexora-mgmt/main.go` (modify) — `serve`: config, store, migrate, CA, instance heartbeat, initial snapshot, hub, gRPC listener

Interfaces:

- Consumes `store.{Store, InTx, MapError, ErrNotFound}`, `pki.{CA, SignEngineCSR, ServerCertificate, NewJoinToken, HashSecret, EngineCertValidity}`, `config.Config` (Task 12); `controlv1` (Task 2).
- `auth.Actor{ Type string /* user|api_token|system */; ID, Name string }`; `auth.Change{ Action, TargetType, TargetID string; Before, After any }`; `func WriteAudit(ctx context.Context, tx pgx.Tx, a Actor, c Change, configVersion *uint64) error` (inserts `audit_log` with `diff = {"before": Before, "after": After}` as jsonb).
- `snapshot.BuildConfig{ QueryLogToManagement bool; DefaultOTLPEndpoint string }`; `func Build(ctx context.Context, tx pgx.Tx, version uint64, cfg BuildConfig) (*controlv1.ConfigSnapshot, error)`; `func Mutate(ctx context.Context, st *store.Store, cfg BuildConfig, a auth.Actor, fn func(tx pgx.Tx) (auth.Change, error)) (uint64, error)`; `func Latest(ctx context.Context, q Querier) (uint64, *controlv1.ConfigSnapshot, error)` with `type Querier interface { QueryRow(ctx context.Context, sql string, args ...any) pgx.Row }`; `func PublishRaw(ctx context.Context, st *store.Store, snap *controlv1.ConfigSnapshot, createdBy string) (uint64, error)` (assigns the next version into `snap.Version`, stores it unvalidated, notifies — used by tests and by nothing in production code paths); `func EnsureInitial(ctx context.Context, st *store.Store, cfg BuildConfig) (uint64, error)`; `const NotifyChannel = "nexora_config"`.
- `control.TLSConfig(ca *pki.CA, serverNames []string) (*tls.Config, error)`; `control.EngineID(ctx context.Context) (string, error)`; `control.NewServer(st *store.Store, ca *pki.CA, hub *Hub, instanceID string) *Server`; `type Server struct { OnStats func(ctx context.Context, engineID string, s *controlv1.Stats); ... }` implementing `controlv1.EngineControlServer`; `control.NewHub(st *store.Store, instanceID string) *Hub`; `func (h *Hub) Run(ctx context.Context) error`; `func (h *Hub) Connected() int`; `control.CreateJoinToken(ctx context.Context, tx pgx.Tx, ca *pki.CA, name, createdBy string, ttl time.Duration) (JoinToken, string, error)`; `type JoinToken struct { ID, Name, CreatedBy string; CreatedAt, ExpiresAt time.Time; RevokedAt *time.Time; Uses int }`; `control.RunInstanceHeartbeat(ctx context.Context, st *store.Store, instanceID string)` (upserts `instances` every 5 s; engines count as connected when `connected_instance` heartbeat is younger than 15 s); `control.NewInstanceID() string` (`<hostname>-<8 hex>`); `const BlobChunkSize = 1 << 20`.
- gRPC status codes: invalid/expired/revoked join secret -> `PermissionDenied`; bad CSR or node name -> `InvalidArgument`; `Connect`/`GetBlob` without a verified client certificate -> `Unauthenticated`; unknown or deleted engine -> `PermissionDenied`; unknown blob -> `NotFound`.

- [ ] Write the failing test `mgmt/internal/snapshot/snapshot_test.go`:

```go
package snapshot_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func newStore(t *testing.T) (*store.Store, context.Context) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

func TestMutateWritesRowsAuditVersionAndNotifies(t *testing.T) {
	st, ctx := newStore(t)
	conn, err := st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "listen "+snapshot.NotifyChannel); err != nil {
		t.Fatal(err)
	}
	actor := auth.Actor{Type: "user", ID: "u-1", Name: "alice"}
	v, err := snapshot.Mutate(ctx, st, snapshot.BuildConfig{}, actor, func(tx pgx.Tx) (auth.Change, error) {
		_, err := tx.Exec(ctx, "insert into upstreams(name, protocol, address, position) values ('quad9','udp','9.9.9.9:53',0)")
		return auth.Change{Action: "createUpstream", TargetType: "upstream", TargetID: "quad9", After: map[string]any{"name": "quad9"}}, err
	})
	if err != nil || v != 1 {
		t.Fatalf("mutate: v=%d err=%v", v, err)
	}
	n, err := conn.Conn().WaitForNotification(ctx)
	if err != nil || n.Payload != "1" {
		t.Fatalf("notification: %v %v", n, err)
	}
	var diff []byte
	var cv int64
	if err := st.Pool.QueryRow(ctx, "select diff, config_version from audit_log where action='createUpstream' and actor_name='alice'").Scan(&diff, &cv); err != nil || cv != 1 {
		t.Fatalf("audit row: %v cv=%d", err, cv)
	}
	var d map[string]any
	_ = json.Unmarshal(diff, &d)
	if d["after"] == nil {
		t.Fatalf("diff missing after: %s", diff)
	}
	ver, snap, err := snapshot.Latest(ctx, st.Pool)
	if err != nil || ver != 1 || len(snap.Upstreams) != 1 || snap.Upstreams[0].Address != "9.9.9.9:53" {
		t.Fatalf("latest: %d %v %v", ver, snap, err)
	}

	_, err = snapshot.Mutate(ctx, st, snapshot.BuildConfig{}, actor, func(tx pgx.Tx) (auth.Change, error) {
		_, _ = tx.Exec(ctx, "delete from upstreams")
		return auth.Change{}, store.ErrConflict
	})
	if err == nil {
		t.Fatal("failed mutation returned nil")
	}
	var rows, versions int
	_ = st.Pool.QueryRow(ctx, "select count(*) from upstreams").Scan(&rows)
	_ = st.Pool.QueryRow(ctx, "select count(*) from config_versions").Scan(&versions)
	if rows != 1 || versions != 1 {
		t.Fatalf("failed mutation leaked: upstreams=%d versions=%d", rows, versions)
	}
}

func TestBuildMapsEveryTable(t *testing.T) {
	st, ctx := newStore(t)
	sha := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	stmts := []struct {
		sql  string
		args []any
	}{
		{"insert into upstreams(name, protocol, address, tls_server_name, timeout_ms, position, enabled) values ('b','dot','9.9.9.9:853','dns.quad9.net',300,1,true), ('a','udp','1.1.1.1:53','',250,0,true), ('off','udp','8.8.8.8:53','',250,2,false)", nil},
		{"update resolver_settings set strategy='fastest', cache_max_bytes=2097152, block_mode='nxdomain', block_ttl=30, trace_sample_one_in=100", nil},
		{"update access_control set allow_cidrs=array['10.0.0.0/8']::cidr[]", nil},
		{"insert into blobs(sha256, size, data) values ($1, 3, 'abc')", []any{sha}},
		{"insert into filter_lists(name, kind, url, current_blob_sha256) values ('ads','block','http://x/ads', $1), ('empty','block','http://x/e', null)", []any{sha}},
		{"update allowlist set domains=array['good.example']", nil},
	}
	for _, q := range stmts {
		if _, err := st.Pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	var snap *controlv1.ConfigSnapshot
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		var e error
		snap, e = snapshot.Build(ctx, tx, 7, snapshot.BuildConfig{QueryLogToManagement: true, DefaultOTLPEndpoint: "http://otel:4317"})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version != 7 || len(snap.Upstreams) != 2 || snap.Upstreams[0].Name != "a" || snap.Upstreams[1].Protocol != controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_DOT {
		t.Fatalf("upstreams: %v", snap.Upstreams)
	}
	if snap.Resolver.Strategy != controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_FASTEST || snap.Cache.MaxBytes != 2097152 {
		t.Fatalf("resolver/cache: %v %v", snap.Resolver, snap.Cache)
	}
	if len(snap.AclAllowCidrs) != 1 || snap.AclAllowCidrs[0] != "10.0.0.0/8" {
		t.Fatalf("acl: %v", snap.AclAllowCidrs)
	}
	if len(snap.Filter.Blocklists) != 1 || snap.Filter.Blocklists[0].Sha256 != sha || snap.Filter.BlockMode != controlv1.BlockMode_BLOCK_MODE_NXDOMAIN || snap.Filter.BlockTtl != 30 {
		t.Fatalf("filter: %v", snap.Filter)
	}
	if len(snap.Filter.Allowlists) != 1 {
		t.Fatalf("allowlist blob missing: %v", snap.Filter.Allowlists)
	}
	var stored int
	_ = st.Pool.QueryRow(ctx, "select count(*) from blobs where sha256=$1", snap.Filter.Allowlists[0].Sha256).Scan(&stored)
	if stored != 1 {
		t.Fatal("allowlist blob not stored")
	}
	if !snap.Telemetry.QuerylogToManagement || snap.Telemetry.OtlpEndpoint != "http://otel:4317" || snap.Telemetry.TraceSampleOneIn != 100 {
		t.Fatalf("telemetry: %v", snap.Telemetry)
	}
}
```

- [ ] Write the failing test `mgmt/internal/control/control_test.go`:

```go
package control_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

type fixture struct {
	st   *store.Store
	ca   *pki.CA
	addr []string
	ctx  context.Context
}

func setup(t *testing.T, instances int) *fixture {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := pki.InitCA(dir); err != nil {
		t.Fatal(err)
	}
	ca, _ := pki.LoadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{st: st, ca: ca, ctx: ctx}
	for i := 0; i < instances; i++ {
		id := control.NewInstanceID()
		go control.RunInstanceHeartbeat(ctx, st, id)
		hub := control.NewHub(st, id)
		go func() { _ = hub.Run(ctx) }()
		tlsCfg, err := control.TLSConfig(ca, []string{"127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
		controlv1.RegisterEngineControlServer(srv, control.NewServer(st, ca, hub, id))
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		go func() { _ = srv.Serve(l) }()
		t.Cleanup(srv.Stop)
		f.addr = append(f.addr, l.Addr().String())
	}
	return f
}

func (f *fixture) enroll(t *testing.T, addr string) (controlv1.EngineControlClient, string) {
	var token string
	err := f.st.InTx(f.ctx, func(tx pgx.Tx) error {
		var e error
		_, token, e = control.CreateJoinToken(f.ctx, tx, f.ca, "test", "admin", time.Hour)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	secret, fp, _ := pki.ParseJoinToken(token)
	if fp != f.ca.Fingerprint() {
		t.Fatal("token fingerprint mismatch")
	}
	plain, _ := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: f.ca.Pool(), ServerName: "127.0.0.1"})))
	defer plain.Close()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if _, err := controlv1.NewEngineControlClient(plain).Enroll(f.ctx, &controlv1.EnrollRequest{JoinSecret: "WRONG", NodeName: "e1", CsrDer: csr}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bad secret -> %v", err)
	}
	resp, err := controlv1.NewEngineControlClient(plain).Enroll(f.ctx, &controlv1.EnrollRequest{JoinSecret: secret, NodeName: "e1", CsrDer: csr, EngineVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{resp.CertificateDer}, PrivateKey: key}
	conn, _ := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: f.ca.Pool(), ServerName: "127.0.0.1", Certificates: []tls.Certificate{cert}})))
	t.Cleanup(func() { conn.Close() })
	return controlv1.NewEngineControlClient(conn), resp.EngineId
}

func recvSnapshot(t *testing.T, s controlv1.EngineControl_ConnectClient) *controlv1.ConfigSnapshot {
	t.Helper()
	ch := make(chan *controlv1.ServerMessage, 1)
	go func() { m, _ := s.Recv(); ch <- m }()
	select {
	case m := <-ch:
		if m.GetSnapshot() == nil {
			t.Fatalf("expected snapshot, got %v", m)
		}
		return m.GetSnapshot()
	case <-time.After(3 * time.Second):
		t.Fatal("no snapshot within 3s")
	}
	return nil
}

func TestEnrollConnectPushAckReject(t *testing.T) {
	f := setup(t, 1)
	client, id := f.enroll(t, f.addr[0])
	stream, err := client.Connect(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "e1", AppliedVersion: 0}}})
	if s := recvSnapshot(t, stream); s.Version != 1 {
		t.Fatalf("initial version %d", s.Version)
	}
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Applied{Applied: &controlv1.Applied{Version: 1}}})
	harness.Eventually(t, 3*time.Second, func() error { return expectEngine(f, id, "applied_version", int64(1)) })

	_, err = snapshot.Mutate(f.ctx, f.st, snapshot.BuildConfig{}, auth.Actor{Type: "system", ID: "t", Name: "t"}, func(tx pgx.Tx) (auth.Change, error) {
		_, err := tx.Exec(f.ctx, "update resolver_settings set block_ttl=5")
		return auth.Change{Action: "updateResolverSettings", TargetType: "resolver_settings", TargetID: "singleton"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := recvSnapshot(t, stream); s.Version != 2 || s.Filter.BlockTtl != 5 {
		t.Fatalf("pushed snapshot %v", s)
	}
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Rejected{Rejected: &controlv1.Rejected{Version: 2, Reason: "invalid snapshot: nope"}}})
	harness.Eventually(t, 3*time.Second, func() error { return expectEngine(f, id, "rejected_reason", "invalid snapshot: nope") })
}

func TestVersionAheadIsFlaggedNotDowngraded(t *testing.T) {
	f := setup(t, 1)
	client, id := f.enroll(t, f.addr[0])
	stream, _ := client.Connect(f.ctx)
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, AppliedVersion: 99}}})
	m, err := stream.Recv()
	if err != nil || m.GetVersionAhead() == nil || m.GetVersionAhead().ServerVersion != 1 {
		t.Fatalf("expected VersionAhead, got %v %v", m, err)
	}
	harness.Eventually(t, 3*time.Second, func() error { return expectEngine(f, id, "version_ahead", true) })
}

func TestConnectWithoutClientCertIsUnauthenticated(t *testing.T) {
	f := setup(t, 1)
	conn, _ := grpc.NewClient(f.addr[0], grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: f.ca.Pool(), ServerName: "127.0.0.1"})))
	defer conn.Close()
	s, err := controlv1.NewEngineControlClient(conn).Connect(f.ctx)
	if err == nil {
		_ = s.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{}}})
		_, err = s.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v", err)
	}
}

func TestGetBlobStreamsMiBChunks(t *testing.T) {
	f := setup(t, 1)
	client, _ := f.enroll(t, f.addr[0])
	data := make([]byte, 2*control.BlobChunkSize+12345)
	_, _ = rand.Read(data)
	sha := sha256Hex(data)
	if _, err := f.st.Pool.Exec(f.ctx, "insert into blobs(sha256,size,data) values ($1,$2,$3)", sha, len(data), data); err != nil {
		t.Fatal(err)
	}
	s, err := client.GetBlob(f.ctx, &controlv1.GetBlobRequest{Sha256: sha})
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	chunks := 0
	for {
		c, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		chunks++
		got = append(got, c.Data...)
	}
	if chunks != 3 || sha256Hex(got) != sha {
		t.Fatalf("chunks=%d match=%v", chunks, sha256Hex(got) == sha)
	}
	if _, err := mustRecvErr(client, f.ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown blob -> %v", err)
	}
}

func TestNotifyFansOutToEngineOnOtherInstance(t *testing.T) {
	f := setup(t, 2)
	client, id := f.enroll(t, f.addr[1])
	stream, _ := client.Connect(f.ctx)
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, AppliedVersion: 1}}})
	time.Sleep(300 * time.Millisecond)
	v, err := snapshot.Mutate(f.ctx, f.st, snapshot.BuildConfig{}, auth.Actor{Type: "system", ID: "t", Name: "t"}, func(tx pgx.Tx) (auth.Change, error) {
		_, err := tx.Exec(f.ctx, "update resolver_settings set block_ttl=9")
		return auth.Change{Action: "updateResolverSettings", TargetType: "resolver_settings", TargetID: "singleton"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := recvSnapshot(t, stream); s.Version != v {
		t.Fatalf("version %d, want %d", s.Version, v)
	}
}
```

plus these helpers at the bottom of the same file:

```go
func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func expectEngine(f *fixture, id, column string, want any) error {
	var got any
	if err := f.st.Pool.QueryRow(f.ctx, "select "+column+" from engines where id=$1", id).Scan(&got); err != nil {
		return err
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("%s = %v, want %v", column, got, want)
	}
	return nil
}

func mustRecvErr(c controlv1.EngineControlClient, ctx context.Context) (*controlv1.BlobChunk, error) {
	s, err := c.GetBlob(ctx, &controlv1.GetBlobRequest{Sha256: sha256Hex([]byte("nope"))})
	if err != nil {
		return nil, err
	}
	return s.Recv()
}
```

(add `crypto/sha256`, `encoding/hex`, `fmt` to the imports).

- [ ] Run `scripts/dev-exec.sh go test -count=1 ./mgmt/internal/snapshot/... ./mgmt/internal/control/...` — expect FAIL with `no required module provides package github.com/piwi3910/nexora/mgmt/internal/snapshot`.
- [ ] Implement `auth/audit.go` per the interface (`json.Marshal` of `{"before": c.Before, "after": c.After}`).
- [ ] Implement `snapshot.go`. `Mutate`:

```go
func Mutate(ctx context.Context, st *store.Store, cfg BuildConfig, a auth.Actor, fn func(tx pgx.Tx) (auth.Change, error)) (uint64, error) {
	var version uint64
	err := st.InTx(ctx, func(tx pgx.Tx) error {
		change, err := fn(tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext('nexora:config_version'))"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select coalesce(max(version), 0) + 1 from config_versions").Scan(&version); err != nil {
			return err
		}
		if err := auth.WriteAudit(ctx, tx, a, change, &version); err != nil {
			return err
		}
		snap, err := Build(ctx, tx, version, cfg)
		if err != nil {
			return err
		}
		raw, err := proto.Marshal(snap)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into config_versions(version, created_by, summary, snapshot) values ($1, $2, $3, $4)",
			version, a.Name, change.Action+" "+change.TargetType+" "+change.TargetID, raw); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "select pg_notify($1, $2)", NotifyChannel, strconv.FormatUint(version, 10))
		return err
	})
	return version, err
}
```

`Build` reads `resolver_settings`, enabled `upstreams order by position, name`, `access_control.allow_cidrs::text[]` (host CIDRs rendered by `host()`/`masklen()` as `a.b.c.d/n`), `filter_lists` with `enabled and current_blob_sha256 is not null` joined to `blobs.size` split by `kind`, and `allowlist.domains`: when non-empty it normalises (lowercase, sorted, unique), zstd-compresses with `klauspost/compress/zstd` (`SpeedDefault`, no checksum-dependent options), upserts into `blobs` (`on conflict do nothing`) and appends the `BlobRef{Name: "allowlist"}`. Enum mapping: `ordered|fastest`, `udp|tcp|dot|doh`, `null_ip|nxdomain|refused`. `Telemetry.OtlpEndpoint` = `resolver_settings.otlp_endpoint` when non-empty else `cfg.DefaultOTLPEndpoint`. `EnsureInitial` runs `Mutate` with actor `{system, "bootstrap", "system"}` and change `{Action: "initialSnapshot", TargetType: "config", TargetID: "1"}` only when `config_versions` is empty (checked under the same advisory lock). `PublishRaw` inserts under the lock with the given snapshot bytes and notifies. `Latest` selects the max version row and unmarshals it.

- [ ] Implement `control/tls.go`: server certificate from `ca.ServerCertificate(serverNames, 30*24*time.Hour)` (re-issued in memory at each start), `ClientAuth: tls.VerifyClientCertIfGiven`, `ClientCAs: ca.Pool()`, `MinVersion: tls.VersionTLS13`, `NextProtos: ["h2"]`; `EngineID` reads `peer.FromContext` -> `credentials.TLSInfo.State.VerifiedChains[0][0].Subject.CommonName` and returns `status.Error(codes.Unauthenticated, "client certificate required")` when absent.
- [ ] Implement `control/jointokens.go`: `CreateJoinToken` inserts `join_tokens(name, secret_hash, created_by, expires_at)` and returns `pki.NewJoinToken(ca.Fingerprint())`'s token; `lookupJoinToken(ctx, tx, secret)` selects `where secret_hash=$1 and revoked_at is null and expires_at > now() for update`.
- [ ] Implement `control/server.go`: `Enroll` validates `node_name` (`^[a-z0-9-]{1,63}$`), looks up the join token, inserts `engines(id, node_name, join_token_id, certificate_serial, engine_version)` with `gen_random_uuid()` returned, signs the CSR with CN = id, increments `uses`, returns the DER and `ca.Cert.Raw`. `Connect` resolves `EngineID`, loads the engine (not deleted), requires `Hello` first, updates `node_name, engine_version, connected_instance=$instance, last_seen_at=now()`, then compares `Hello.applied_version` with `Latest`: lower -> send `ServerMessage_Snapshot`; higher -> set `version_ahead=true` and send `ServerMessage_VersionAhead{ServerVersion}`; equal -> nothing. It registers a `*subscriber{engineID, applied uint64, out chan *controlv1.ServerMessage (cap 1, latest wins)}` with the hub, runs a sender goroutine, and loops `Recv`: `Applied` -> `applied_version=$v, persist_error=$e, version_ahead=false, rejected_version=null, rejected_reason='' where rejected_version <= $v or rejected_version is null`; `Rejected` -> `rejected_version=$v, rejected_reason=$r`; `Stats` -> `last_seen_at=now()` and `OnStats` when non-nil. On exit it unregisters and sets `connected_instance=null where connected_instance=$instance`. `GetBlob` requires `EngineID`, selects `data` by sha256, and sends `BlobChunkSize` slices.
- [ ] Implement `control/hub.go`: `Run` acquires a dedicated connection, `LISTEN nexora_config`, and on each notification (and every 30 s as a safety net, and after reconnecting with 1 s backoff) loads `Latest` and offers the snapshot to every subscriber whose last sent/applied version is lower; `Connected()` returns the subscriber count.
- [ ] Implement `control/instances.go` and wire `serve` in `main.go`: `config.Load(os.Getenv)`, `store.Open`, `Migrate`, `pki.LoadCA`, `NewInstanceID`, `go RunInstanceHeartbeat`, `snapshot.EnsureInitial(BuildConfig{QueryLogToManagement: cfg.QueryLogBackend == "builtin", DefaultOTLPEndpoint: cfg.OTLPEndpoint})`, `NewHub` + `go hub.Run`, `grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)), grpc.KeepaliveParams(keepalive.ServerParameters{Time: 20 * time.Second, Timeout: 10 * time.Second}))`, register `EngineControl`, listen on `cfg.GRPCListen`, print `grpc listening on <addr>`, and stop gracefully on SIGTERM.
- [ ] Run `scripts/dev-exec.sh go test -count=1 ./mgmt/internal/snapshot/... ./mgmt/internal/control/...` — expect PASS: two `ok` lines.
- [ ] Commit: `git add mgmt && git commit -m "mgmt: snapshot builder, audited mutations, EngineControl server with pg_notify fan-out"`.

## Task 14: Authentication and authorization — passwords, sessions, API tokens, RBAC, setup token, OIDC

Files:

- `mgmt/internal/auth/password.go` (create) — argon2id hashing
- `mgmt/internal/auth/permissions.go` (create) — `Role`, `Permissions` keyed by operationId, `Public`, `Authorize`
- `mgmt/internal/auth/service.go` (create) — users, sessions, API tokens, setup token, request authentication
- `mgmt/internal/auth/oidc.go` (create) — authorization code + PKCE with go-oidc
- `mgmt/internal/auth/auth_test.go` (create) — unit tests (no database)
- `mgmt/internal/auth/service_test.go` (create) — database + OIDC fixture tests

Interfaces:

- Consumes `store.{Store, InTx, MapError, ErrNotFound, ErrConflict}`, `config.{Config, OIDCConfig}` (Task 12); `auth.Actor` (Task 13); harness `StartPostgres`, `StartOIDCFixture` (Task 10).
- `password.go`: `func HashPassword(pw string) (string, error)` producing `$argon2id$v=19$m=65536,t=3,p=2$<b64 salt>$<b64 hash>` (16-byte salt, 32-byte key); `func VerifyPassword(encoded, pw string) (bool, error)` (constant-time compare); `const MinPasswordLength = 12`.
- `permissions.go`: `type Role string`; `const RoleViewer Role = "viewer"; RoleOperator Role = "operator"; RoleAdmin Role = "admin"`; `func ParseRole(s string) (Role, error)`; `func (r Role) AtLeast(min Role) bool`; `var Public = map[string]bool{...}`; `var Permissions = map[string]Role{...}`; `func Authorize(p Principal, operationID string) error` (returns `ErrForbidden`; an operationId absent from both maps is forbidden).
- The exact map contents (Task 15's OpenAPI must use these operationIds and a test enforces the match):
  - `Public`: `getHealth`, `getSetupStatus`, `completeSetup`, `login`, `listAuthProviders`, `startOidcLogin`, `oidcCallback`.
  - `RoleViewer`: `logout`, `getCurrentUser`, `getDashboard`, `listUpstreams`, `getResolverSettings`, `getAccessControl`, `listFilterLists`, `getFilterList`, `getAllowlist`, `listEngines`, `getEngine`, `listConfigVersions`, `searchQueryLog`.
  - `RoleOperator`: `createUpstream`, `updateUpstream`, `deleteUpstream`, `updateResolverSettings`, `updateAccessControl`, `createFilterList`, `updateFilterList`, `deleteFilterList`, `refreshFilterList`, `updateAllowlist`.
  - `RoleAdmin`: `listUsers`, `createUser`, `updateUser`, `deleteUser`, `listApiTokens`, `createApiToken`, `revokeApiToken`, `listAuditEvents`, `listJoinTokens`, `createJoinToken`, `revokeJoinToken`, `deleteEngine`.
- `service.go`: `type Principal struct { UserID, Username string; Role Role; Kind string /* session|api_token */; TokenID string }`; `func (p Principal) Actor() Actor` (`Type` = `user` or `api_token`, `ID` = user or token id, `Name` = username or `username/tokenname`); `type User struct { ID, Username, Email string; Role Role; Source string; Disabled bool; Revision int64; CreatedAt time.Time }`; `type APIToken struct { ID, UserID, Name, Prefix string; Role Role; CreatedAt time.Time; ExpiresAt, LastUsedAt, RevokedAt *time.Time }`; `var ErrUnauthenticated, ErrForbidden, ErrInvalidCredentials, ErrSetupDone, ErrWeakPassword, ErrOIDCUnavailable error`; `const SessionCookieName = "nexora_session"; const SessionTTL = 12 * time.Hour`; `func NewService(st *store.Store, secureCookies bool) *Service`; `func (s *Service) EnsureSetupToken(ctx context.Context, instanceID string) (token string, created bool, err error)`; `func (s *Service) SetupRequired(ctx context.Context) (bool, error)`; `func (s *Service) CompleteSetup(ctx context.Context, token, username, email, password string) (User, error)`; `func (s *Service) Login(ctx context.Context, username, password string) (string, User, error)`; `func (s *Service) CreateSession(ctx context.Context, userID string) (string, error)`; `func (s *Service) Logout(ctx context.Context, sessionToken string) error`; `func (s *Service) Authenticate(ctx context.Context, r *http.Request) (Principal, error)`; `func (s *Service) SessionCookie(token string) *http.Cookie` (HttpOnly, `SameSite=Lax`, `Secure` = `secureCookies`, `Path=/`, `MaxAge` = SessionTTL); `func CreateUser(ctx context.Context, tx pgx.Tx, username, email, password string, role Role) (User, error)`; `func (s *Service) CreateAPIToken(ctx context.Context, tx pgx.Tx, owner Principal, name string, role Role, expiresAt *time.Time) (APIToken, string, error)` (token `nxt_<base32 of 32 random bytes>`, stored as SHA-256; `role` must not exceed the owner's role, else `ErrForbidden`).
- `oidc.go`: `func NewOIDC(cfg config.OIDCConfig, publicURL string, st *store.Store) *OIDC`; `func (o *OIDC) Enabled() bool`; `func (o *OIDC) Start(ctx context.Context, returnTo string) (string, error)`; `func (o *OIDC) Callback(ctx context.Context, state, code string) (User, string /* returnTo */, error)`; redirect URI `<publicURL>/api/v1/auth/oidc/callback`; provider discovery is lazy with a 5 s timeout and retried on the next login (a down provider yields `ErrOIDCUnavailable` and never affects local login); role from the `groups` claim: contains `AdminGroup` -> admin, `OperatorGroup` -> operator, otherwise viewer; the user row is upserted by `oidc_subject` with `source='oidc'` and its role refreshed at every login.
- Setup token: `EnsureSetupToken` runs in one transaction: `select count(*) from users` = 0, then `insert into setup_tokens(token_hash, created_by_instance) values (...) on conflict (singleton) do nothing`; `created` is true only for the instance whose insert took effect, which logs `setup token: <token>`; `CompleteSetup` deletes the row by hash and inserts the admin in the same transaction (`ErrSetupDone` when users exist or the hash does not match).

- [ ] Write the failing unit tests `mgmt/internal/auth/auth_test.go`:

```go
package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
)

func TestArgon2idHashAndVerify(t *testing.T) {
	h, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash format %q", h)
	}
	if ok, _ := auth.VerifyPassword(h, "correct horse battery"); !ok {
		t.Fatal("verify failed")
	}
	if ok, _ := auth.VerifyPassword(h, "wrong horse battery"); ok {
		t.Fatal("wrong password verified")
	}
	h2, _ := auth.HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salt not random")
	}
}

func TestRBACMatrix(t *testing.T) {
	viewer := auth.Principal{Username: "v", Role: auth.RoleViewer}
	operator := auth.Principal{Username: "o", Role: auth.RoleOperator}
	admin := auth.Principal{Username: "a", Role: auth.RoleAdmin}
	cases := []struct {
		p     auth.Principal
		op    string
		allow bool
	}{
		{viewer, "listUpstreams", true}, {viewer, "createUpstream", false}, {viewer, "listUsers", false},
		{viewer, "listApiTokens", false}, {viewer, "listAuditEvents", false}, {viewer, "searchQueryLog", true},
		{operator, "createUpstream", true}, {operator, "refreshFilterList", true}, {operator, "createUser", false},
		{operator, "listAuditEvents", false}, {operator, "createJoinToken", false},
		{admin, "createUser", true}, {admin, "listAuditEvents", true}, {admin, "deleteEngine", true},
		{admin, "noSuchOperation", false},
	}
	for _, c := range cases {
		err := auth.Authorize(c.p, c.op)
		if (err == nil) != c.allow {
			t.Errorf("%s %s: err=%v want allow=%v", c.p.Role, c.op, err, c.allow)
		}
		if err != nil && !errors.Is(err, auth.ErrForbidden) {
			t.Errorf("error must be ErrForbidden, got %v", err)
		}
	}
	for op := range auth.Public {
		if _, dup := auth.Permissions[op]; dup {
			t.Errorf("%s is both public and permissioned", op)
		}
	}
}
```

- [ ] Write the failing database/OIDC tests `mgmt/internal/auth/service_test.go`:

```go
package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func db(t *testing.T) (*harness.Env, *store.Store, context.Context) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return env, st, ctx
}

func TestSetupTokenIsSingleAndConsumedOnce(t *testing.T) {
	_, st, ctx := db(t)
	svc := auth.NewService(st, true)
	var mu sync.Mutex
	var tokens []string
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, created, err := svc.EnsureSetupToken(ctx, "instance-"+string(rune('a'+i)))
			if err != nil {
				t.Error(err)
				return
			}
			if created {
				mu.Lock()
				tokens = append(tokens, tok)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(tokens) != 1 {
		t.Fatalf("created %d setup tokens, want 1", len(tokens))
	}
	if _, err := svc.CompleteSetup(ctx, "wrong", "admin", "a@x", "a-long-password-123"); !errors.Is(err, auth.ErrSetupDone) {
		t.Fatalf("wrong token -> %v", err)
	}
	if _, err := svc.CompleteSetup(ctx, tokens[0], "admin", "a@x", "short"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Fatalf("weak password -> %v", err)
	}
	u, err := svc.CompleteSetup(ctx, tokens[0], "admin", "a@x", "a-long-password-123")
	if err != nil || u.Role != auth.RoleAdmin {
		t.Fatalf("setup: %v %v", u, err)
	}
	if _, err := svc.CompleteSetup(ctx, tokens[0], "admin2", "b@x", "a-long-password-123"); !errors.Is(err, auth.ErrSetupDone) {
		t.Fatalf("second setup -> %v", err)
	}
	if req, _ := svc.SetupRequired(ctx); req {
		t.Fatal("setup still required")
	}
}

func TestSessionsTokensAndDisabledUsers(t *testing.T) {
	_, st, ctx := db(t)
	svc := auth.NewService(st, true)
	var op auth.User
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		op, err = auth.CreateUser(ctx, tx, "olga", "o@x", "operator-password-1", auth.RoleOperator)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "olga", "nope-nope-nope"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("bad login -> %v", err)
	}
	sess, _, err := svc.Login(ctx, "olga", "operator-password-1")
	if err != nil {
		t.Fatal(err)
	}
	c := svc.SessionCookie(sess)
	if c.Name != "nexora_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie flags %+v", c)
	}
	r := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	r.AddCookie(c)
	p, err := svc.Authenticate(ctx, r)
	if err != nil || p.Username != "olga" || p.Role != auth.RoleOperator || p.Kind != "session" {
		t.Fatalf("session auth: %+v %v", p, err)
	}

	var tok string
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		if _, _, err := svc.CreateAPIToken(ctx, tx, p, "too-strong", auth.RoleAdmin, nil); !errors.Is(err, auth.ErrForbidden) {
			return errors.New("operator minted an admin token")
		}
		var e error
		_, tok, e = svc.CreateAPIToken(ctx, tx, p, "ci", auth.RoleViewer, nil)
		return e
	})
	if err != nil || !strings.HasPrefix(tok, "nxt_") {
		t.Fatalf("token: %q %v", tok, err)
	}
	r = httptest.NewRequest("GET", "/api/v1/upstreams", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	if p, err := svc.Authenticate(ctx, r); err != nil || p.Role != auth.RoleViewer || p.Kind != "api_token" {
		t.Fatalf("token auth: %+v %v", p, err)
	}

	_, _ = st.Pool.Exec(ctx, "update users set disabled=true where id=$1", op.ID)
	r = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	r.AddCookie(c)
	if _, err := svc.Authenticate(ctx, r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled user -> %v", err)
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	if _, err := svc.Authenticate(ctx, r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled user token -> %v", err)
	}
}

func TestOIDCLoginAndProviderDown(t *testing.T) {
	env, st, ctx := db(t)
	fx := env.StartOIDCFixture(harness.OIDCUser{Username: "ada", Email: "ada@x", Groups: []string{"nexora-admins"}})
	cfg := config.OIDCConfig{Issuer: fx.Issuer, ClientID: fx.ClientID, ClientSecretFile: fx.ClientSecretFile, AdminGroup: "nexora-admins", OperatorGroup: "nexora-operators"}
	o := auth.NewOIDC(cfg, "http://nexora.test", st)
	redirect, err := o.Start(ctx, "/upstreams")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(redirect)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
		t.Fatalf("authorize URL lacks PKCE/state: %s", redirect)
	}
	form := url.Values{"user": {"ada"}, "client_id": {q.Get("client_id")}, "redirect_uri": {q.Get("redirect_uri")}, "state": {q.Get("state")}, "nonce": {q.Get("nonce")}, "code_challenge": {q.Get("code_challenge")}}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.PostForm(fx.Issuer+"/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := url.Parse(resp.Header.Get("Location"))
	user, returnTo, err := o.Callback(ctx, cb.Query().Get("state"), cb.Query().Get("code"))
	if err != nil || user.Role != auth.RoleAdmin || user.Source != "oidc" || returnTo != "/upstreams" {
		t.Fatalf("callback: %+v %q %v", user, returnTo, err)
	}

	svc := auth.NewService(st, false)
	_ = st.InTx(ctx, func(tx pgx.Tx) error {
		_, err := auth.CreateUser(ctx, tx, "local", "l@x", "local-password-12", auth.RoleViewer)
		return err
	})
	fx.Proc.Kill()
	o2 := auth.NewOIDC(cfg, "http://nexora.test", st)
	if _, err := o2.Start(ctx, "/"); !errors.Is(err, auth.ErrOIDCUnavailable) {
		t.Fatalf("provider down -> %v", err)
	}
	if _, _, err := svc.Login(ctx, "local", "local-password-12"); err != nil {
		t.Fatalf("local login with OIDC down: %v", err)
	}
}
```

- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 ./mgmt/internal/auth/...'` — expect FAIL with `undefined: auth.HashPassword`.
- [ ] Implement `password.go` with `golang.org/x/crypto/argon2.IDKey(pw, salt, 3, 64*1024, 2, 32)` and `crypto/subtle.ConstantTimeCompare`; `VerifyPassword` parses parameters from the encoded string.
- [ ] Implement `permissions.go` with the exact maps above; `AtLeast` orders viewer < operator < admin.
- [ ] Implement `service.go`: session tokens are 32 random bytes base64url; `sessions.token_hash = sha256(token)`, `expires_at = now() + SessionTTL`, `last_seen_at` updated at most once per minute; `Authenticate` checks `Authorization: Bearer nxt_...` first (hash lookup, `revoked_at is null`, `expires_at is null or > now()`, owner not disabled, sets `last_used_at`), then the `nexora_session` cookie (not expired, user not disabled); `Login` runs `VerifyPassword` against a fixed dummy hash when the user does not exist (equal timing) and rejects `source='oidc'` users without a password hash; `CreateUser` enforces `MinPasswordLength` (`ErrWeakPassword`) and maps unique violations to `store.ErrConflict`.
- [ ] Implement `oidc.go` with `github.com/coreos/go-oidc/v3/oidc` and `golang.org/x/oauth2`: `Start` calls `oidc.NewProvider` under `context.WithTimeout(ctx, 5*time.Second)` (cached on success, error wrapped as `ErrOIDCUnavailable`), creates `state`, `nonce` and a PKCE verifier (`oauth2.GenerateVerifier`), stores them in `oidc_login_states` (expires 10 min), and returns `AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))`; `Callback` deletes and loads the state row (`ErrUnauthenticated` when missing/expired), exchanges with `oauth2.VerifierOption`, verifies the ID token (`Verifier(&oidc.Config{ClientID})`) and nonce, reads claims `sub`, `email`, `preferred_username`, `groups`, and upserts the user (username = `preferred_username`, suffixed with `-oidc` when it collides with a local user).
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 ./mgmt/internal/auth/...'` — expect PASS: `ok github.com/piwi3910/nexora/mgmt/internal/auth`.
- [ ] Commit: `git add mgmt/internal/auth go.mod go.sum && git commit -m "mgmt: argon2id users, sessions, API tokens, RBAC by operationId, setup token and OIDC"`.

## Task 15: OpenAPI spec, HTTP API handlers, embedded GUI serving and `serve`/`user create`

Files:

- `mgmt/api/openapi.yaml` (create) — HTTP API source of truth (OpenAPI 3.1)
- `mgmt/api/oapi-codegen.yaml` (create) — codegen config
- `mgmt/internal/api/gen.go` (create, generated by `make proto`, committed)
- `mgmt/internal/api/server.go` (create) — chi router, strict handler wiring, auth/CSRF/error middleware
- `mgmt/internal/api/handlers_auth.go` (create) — health, setup, login/logout/me, providers, OIDC start/callback
- `mgmt/internal/api/handlers_dns.go` (create) — upstreams, resolver settings, access control, filter lists, allowlist
- `mgmt/internal/api/handlers_fleet.go` (create) — engines, join tokens, config versions, dashboard
- `mgmt/internal/api/handlers_admin.go` (create) — users, API tokens, audit, query log
- `mgmt/internal/api/api_test.go` (create)
- `mgmt/internal/querylog/backend.go` (create) — `Backend` interface, `Query`, `Record`, `Page`, `ErrBackendUnavailable`
- `mgmt/internal/stats/stats.go` (create) — `Record`, `Dashboard`, pruning
- `mgmt/internal/webui/embed.go` (create) — `//go:embed all:dist` SPA handler with `index.html` fallback
- `mgmt/cmd/nexora-mgmt/main.go` (modify) — `serve` adds HTTP listener, setup token log, stats hook; `user create --admin --username U --email E --password-file F`

Interfaces:

- Consumes `auth.*` (Task 14), `snapshot.{Mutate, BuildConfig, Latest}`, `control.{CreateJoinToken, JoinToken}` (Task 13), `store.*`, `pki.CA`, `config.Config` (Task 12).
- `api.Deps{ Store *store.Store; Auth *auth.Service; OIDC *auth.OIDC; CA *pki.CA; Build snapshot.BuildConfig; QueryLog querylog.Backend; InstanceID string; PublicURL string; Metrics http.Handler; RefreshFilterList func(ctx context.Context, p auth.Principal, id string) error }`; `func api.NewHandler(d api.Deps) http.Handler` (routes: `/api/v1/*` strict server, `/metrics` -> `d.Metrics` when non-nil, everything else -> `webui.Handler()`).
- `querylog.Backend interface { Name() string; Search(ctx context.Context, q Query) (Page, error) }`; `type Query struct { From, To time.Time; Client, Name, QType, RCode, Cache, Filter string; Limit int; Cursor string }`; `type Record struct { Time time.Time; Client, Name, QType, RCode, Cache, Filter, Upstream, Transport, EngineID string; DurationUS int64 }`; `type Page struct { Records []Record; NextCursor string }`; `var ErrBackendUnavailable = errors.New("query log backend unavailable")`; `type Noop struct{}` returning an empty page (used until Task 18 wires real adapters and by tests).
- `stats.Record(ctx context.Context, st *store.Store, engineID string, s *controlv1.Stats) error` (inserts `engine_stats`, deletes rows older than 24 h for that engine once per 100 inserts); `stats.Dashboard(ctx context.Context, st *store.Store) (DashboardData, error)` with `type DashboardData struct { QueriesTotal, BlockedTotal uint64; QPS, CacheHitRatio float64; EnginesTotal, EnginesConnected int; Upstreams []UpstreamHealth; Series []Point }`, `type UpstreamHealth struct { Name string; UpEngines, TotalEngines int; RTTMs float64 }`, `type Point struct { At time.Time; QPS float64 }` (QPS from consecutive samples per engine over the last 5 minutes, 30 s buckets).
- `webui.Handler() http.Handler`.
- HTTP error codes (JSON `{"code","message"}`): 400 `invalid_request`, 401 `unauthenticated`, 403 `forbidden`, 404 `not_found`, 409 `conflict`, 415 `unsupported_media_type`, 503 `unavailable` (database) and `querylog_unavailable`.
- CLI: `nexora-mgmt user create --admin --username U --email E --password-file F` prints `user created: <id>`.

- [ ] Write `mgmt/api/openapi.yaml`:

```yaml
openapi: 3.1.0
info: { title: Nexora management API, version: 1.0.0 }
servers: [{ url: /api/v1 }]
security: [{ session: [] }, { bearer: [] }]
components:
  securitySchemes:
    session: { type: apiKey, in: cookie, name: nexora_session }
    bearer: { type: http, scheme: bearer }
  parameters:
    Id:
      {
        name: id,
        in: path,
        required: true,
        schema: { type: string, format: uuid },
      }
    Revision:
      {
        name: revision,
        in: query,
        required: true,
        schema: { type: integer, format: int64 },
      }
  responses:
    Error:
      {
        description: error,
        content:
          {
            application/json:
              { schema: { $ref: "#/components/schemas/Error" } },
          },
      }
  schemas:
    Error:
      type: object
      required: [code, message]
      properties: { code: { type: string }, message: { type: string } }
    Health:
      type: object
      required: [status, database, version]
      properties:
        {
          status: { type: string, enum: [ok, degraded] },
          database: { type: string, enum: [ok, unavailable] },
          version: { type: string },
        }
    SetupStatus:
      {
        type: object,
        required: [required],
        properties: { required: { type: boolean } },
      }
    SetupRequest:
      type: object
      required: [token, username, email, password]
      properties:
        {
          token: { type: string },
          username: { type: string },
          email: { type: string },
          password: { type: string, minLength: 12 },
        }
    LoginRequest:
      type: object
      required: [username, password]
      properties: { username: { type: string }, password: { type: string } }
    AuthProviders:
      {
        type: object,
        required: [local, oidc],
        properties: { local: { type: boolean }, oidc: { type: boolean } },
      }
    Role: { type: string, enum: [viewer, operator, admin] }
    User:
      type: object
      required:
        [id, username, email, role, source, disabled, revision, created_at]
      properties:
        id: { type: string, format: uuid }
        username: { type: string }
        email: { type: string }
        role: { $ref: "#/components/schemas/Role" }
        source: { type: string, enum: [local, oidc] }
        disabled: { type: boolean }
        revision: { type: integer, format: int64 }
        created_at: { type: string, format: date-time }
    UserCreate:
      type: object
      required: [username, email, password, role]
      properties:
        {
          username: { type: string },
          email: { type: string },
          password: { type: string, minLength: 12 },
          role: { $ref: "#/components/schemas/Role" },
        }
    UserUpdate:
      type: object
      required: [revision, email, role, disabled]
      properties:
        {
          revision: { type: integer, format: int64 },
          email: { type: string },
          role: { $ref: "#/components/schemas/Role" },
          disabled: { type: boolean },
          password: { type: string, minLength: 12 },
        }
    Upstream:
      type: object
      required:
        [
          id,
          name,
          protocol,
          address,
          tls_server_name,
          doh_url,
          timeout_ms,
          ca_certificate_pem,
          position,
          enabled,
          revision,
        ]
      properties:
        id: { type: string, format: uuid }
        name: { type: string }
        protocol: { type: string, enum: [udp, tcp, dot, doh] }
        address: { type: string }
        tls_server_name: { type: string }
        doh_url: { type: string }
        timeout_ms: { type: integer, minimum: 50, maximum: 5000 }
        ca_certificate_pem: { type: string }
        position: { type: integer }
        enabled: { type: boolean }
        revision: { type: integer, format: int64 }
    UpstreamInput:
      type: object
      required: [name, protocol, timeout_ms, enabled, position]
      properties:
        name: { type: string, minLength: 1, maxLength: 64 }
        protocol: { type: string, enum: [udp, tcp, dot, doh] }
        address: { type: string }
        tls_server_name: { type: string }
        doh_url: { type: string }
        timeout_ms: { type: integer, minimum: 50, maximum: 5000 }
        ca_certificate_pem: { type: string }
        position: { type: integer, minimum: 0 }
        enabled: { type: boolean }
        revision:
          { type: integer, format: int64, description: required on update }
    ResolverSettings:
      type: object
      required:
        [
          strategy,
          cache_max_bytes,
          cache_min_ttl,
          cache_max_ttl,
          cache_negative_max_ttl,
          cache_stale_window,
          block_mode,
          block_ttl,
          otlp_endpoint,
          trace_sample_one_in,
          trace_slow_threshold_us,
          revision,
        ]
      properties:
        strategy: { type: string, enum: [ordered, fastest] }
        cache_max_bytes: { type: integer, format: int64, minimum: 1048576 }
        cache_min_ttl: { type: integer, minimum: 0 }
        cache_max_ttl: { type: integer, minimum: 0 }
        cache_negative_max_ttl: { type: integer, minimum: 0 }
        cache_stale_window: { type: integer, minimum: 0 }
        block_mode: { type: string, enum: [null_ip, nxdomain, refused] }
        block_ttl: { type: integer, minimum: 0 }
        otlp_endpoint: { type: string }
        trace_sample_one_in: { type: integer, minimum: 0 }
        trace_slow_threshold_us: { type: integer, minimum: 0 }
        revision: { type: integer, format: int64 }
    AccessControl:
      type: object
      required: [allow_cidrs, revision]
      properties:
        {
          allow_cidrs: { type: array, items: { type: string } },
          revision: { type: integer, format: int64 },
        }
    FilterList:
      type: object
      required:
        [
          id,
          name,
          kind,
          url,
          refresh_interval_seconds,
          enabled,
          entry_count,
          invalid_line_count,
          last_error,
          stale,
          revision,
        ]
      properties:
        id: { type: string, format: uuid }
        name: { type: string }
        kind: { type: string, enum: [block, allow] }
        url: { type: string }
        refresh_interval_seconds: { type: integer, minimum: 300 }
        enabled: { type: boolean }
        current_blob_sha256: { type: [string, "null"] }
        entry_count: { type: integer }
        invalid_line_count: { type: integer }
        last_success_at: { type: [string, "null"], format: date-time }
        last_attempt_at: { type: [string, "null"], format: date-time }
        last_error: { type: string }
        stale:
          {
            type: boolean,
            description: last attempt failed or last success older than 2 x refresh interval,
          }
        revision: { type: integer, format: int64 }
    FilterListInput:
      type: object
      required: [name, kind, url, refresh_interval_seconds, enabled]
      properties:
        name: { type: string, minLength: 1, maxLength: 64 }
        kind: { type: string, enum: [block, allow] }
        url: { type: string, pattern: "^https?://" }
        refresh_interval_seconds: { type: integer, minimum: 300 }
        enabled: { type: boolean }
        revision: { type: integer, format: int64 }
    Allowlist:
      type: object
      required: [domains, revision]
      properties:
        {
          domains: { type: array, items: { type: string } },
          revision: { type: integer, format: int64 },
        }
    Engine:
      type: object
      required:
        [
          id,
          node_name,
          engine_version,
          enrolled_at,
          connected,
          applied_version,
          rejected_reason,
          persist_error,
          version_ahead,
          status,
        ]
      properties:
        id: { type: string, format: uuid }
        node_name: { type: string }
        engine_version: { type: string }
        enrolled_at: { type: string, format: date-time }
        last_seen_at: { type: [string, "null"], format: date-time }
        connected: { type: boolean }
        applied_version: { type: integer, format: int64 }
        rejected_version: { type: [integer, "null"], format: int64 }
        rejected_reason: { type: string }
        persist_error: { type: string }
        version_ahead: { type: boolean }
        status:
          {
            type: string,
            enum: [current, behind, rejected, ahead, disconnected],
          }
    JoinToken:
      type: object
      required: [id, name, created_by, created_at, expires_at, uses]
      properties:
        id: { type: string, format: uuid }
        name: { type: string }
        created_by: { type: string }
        created_at: { type: string, format: date-time }
        expires_at: { type: string, format: date-time }
        revoked_at: { type: [string, "null"], format: date-time }
        uses: { type: integer }
    JoinTokenCreate:
      type: object
      required: [name, ttl_seconds]
      properties:
        {
          name: { type: string },
          ttl_seconds: { type: integer, minimum: 60, maximum: 31536000 },
        }
    JoinTokenCreated:
      type: object
      required: [join_token, token]
      properties:
        {
          join_token: { $ref: "#/components/schemas/JoinToken" },
          token: { type: string },
        }
    ConfigVersion:
      type: object
      required: [version, created_at, created_by, summary]
      properties:
        {
          version: { type: integer, format: int64 },
          created_at: { type: string, format: date-time },
          created_by: { type: string },
          summary: { type: string },
        }
    ApiToken:
      type: object
      required: [id, user_id, name, prefix, role, created_at]
      properties:
        id: { type: string, format: uuid }
        user_id: { type: string, format: uuid }
        name: { type: string }
        prefix: { type: string }
        role: { $ref: "#/components/schemas/Role" }
        created_at: { type: string, format: date-time }
        expires_at: { type: [string, "null"], format: date-time }
        last_used_at: { type: [string, "null"], format: date-time }
        revoked_at: { type: [string, "null"], format: date-time }
    ApiTokenCreate:
      type: object
      required: [name, role]
      properties:
        {
          name: { type: string },
          role: { $ref: "#/components/schemas/Role" },
          expires_at: { type: [string, "null"], format: date-time },
        }
    ApiTokenCreated:
      type: object
      required: [api_token, token]
      properties:
        {
          api_token: { $ref: "#/components/schemas/ApiToken" },
          token: { type: string },
        }
    AuditEvent:
      type: object
      required:
        [
          id,
          at,
          actor_type,
          actor_id,
          actor_name,
          action,
          target_type,
          target_id,
          diff,
        ]
      properties:
        id: { type: integer, format: int64 }
        at: { type: string, format: date-time }
        actor_type: { type: string, enum: [user, api_token, system] }
        actor_id: { type: string }
        actor_name: { type: string }
        action: { type: string }
        target_type: { type: string }
        target_id: { type: string }
        diff: { type: object, additionalProperties: true }
        config_version: { type: [integer, "null"], format: int64 }
    QueryLogRecord:
      type: object
      required:
        [
          time,
          client,
          name,
          qtype,
          rcode,
          cache,
          filter,
          upstream,
          transport,
          engine_id,
          duration_us,
        ]
      properties:
        time: { type: string, format: date-time }
        client: { type: string }
        name: { type: string }
        qtype: { type: string }
        rcode: { type: string }
        cache: { type: string, enum: [hit, miss, stale, none] }
        filter: { type: string, enum: [none, blocked, allowed] }
        upstream: { type: string }
        transport: { type: string }
        engine_id: { type: string }
        duration_us: { type: integer, format: int64 }
    QueryLogPage:
      type: object
      required: [backend, records, next_cursor]
      properties:
        {
          backend: { type: string },
          records:
            {
              type: array,
              items: { $ref: "#/components/schemas/QueryLogRecord" },
            },
          next_cursor: { type: string },
        }
    Dashboard:
      type: object
      required:
        [
          queries_total,
          blocked_total,
          qps,
          cache_hit_ratio,
          engines_total,
          engines_connected,
          upstreams,
          series,
        ]
      properties:
        queries_total: { type: integer, format: int64 }
        blocked_total: { type: integer, format: int64 }
        qps: { type: number }
        cache_hit_ratio: { type: number }
        engines_total: { type: integer }
        engines_connected: { type: integer }
        upstreams:
          type: array
          items:
            {
              type: object,
              required: [name, up_engines, total_engines, rtt_ms],
              properties:
                {
                  name: { type: string },
                  up_engines: { type: integer },
                  total_engines: { type: integer },
                  rtt_ms: { type: number },
                },
            }
        series:
          type: array
          items:
            {
              type: object,
              required: [at, qps],
              properties:
                {
                  at: { type: string, format: date-time },
                  qps: { type: number },
                },
            }
paths:
  /health:
    get:
      {
        operationId: getHealth,
        security: [],
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      { schema: { $ref: "#/components/schemas/Health" } },
                  },
              },
            "503":
              {
                description: degraded,
                content:
                  {
                    application/json:
                      { schema: { $ref: "#/components/schemas/Health" } },
                  },
              },
          },
      }
  /setup:
    get:
      {
        operationId: getSetupStatus,
        security: [],
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      { schema: { $ref: "#/components/schemas/SetupStatus" } },
                  },
              },
          },
      }
    post:
      operationId: completeSetup
      security: []
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/SetupRequest" } },
            },
        }
      responses:
        {
          "201":
            {
              description: created; sets the session cookie,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/User" } },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
  /auth/login:
    post:
      operationId: login
      security: []
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/LoginRequest" } },
            },
        }
      responses:
        {
          "200":
            {
              description: logged in; sets the session cookie,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/User" } },
                },
            },
          "401": { $ref: "#/components/responses/Error" },
        }
  /auth/logout:
    post:
      { operationId: logout, responses: { "204": { description: logged out } } }
  /auth/me:
    get:
      {
        operationId: getCurrentUser,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      { schema: { $ref: "#/components/schemas/User" } },
                  },
              },
            "401": { $ref: "#/components/responses/Error" },
          },
      }
  /auth/providers:
    get:
      {
        operationId: listAuthProviders,
        security: [],
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      {
                        schema: { $ref: "#/components/schemas/AuthProviders" },
                      },
                  },
              },
          },
      }
  /auth/oidc/start:
    get:
      operationId: startOidcLogin
      security: []
      parameters: [{ name: return_to, in: query, schema: { type: string } }]
      responses:
        {
          "302": { description: redirect to the identity provider },
          "503": { $ref: "#/components/responses/Error" },
        }
  /auth/oidc/callback:
    get:
      operationId: oidcCallback
      security: []
      parameters:
        [
          { name: state, in: query, required: true, schema: { type: string } },
          { name: code, in: query, required: true, schema: { type: string } },
        ]
      responses:
        {
          "302":
            { description: redirect into the GUI with the session cookie set },
          "401": { $ref: "#/components/responses/Error" },
        }
  /dashboard:
    get:
      {
        operationId: getDashboard,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      { schema: { $ref: "#/components/schemas/Dashboard" } },
                  },
              },
          },
      }
  /upstreams:
    get:
      {
        operationId: listUpstreams,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      {
                        schema:
                          {
                            type: array,
                            items: { $ref: "#/components/schemas/Upstream" },
                          },
                      },
                  },
              },
          },
      }
    post:
      operationId: createUpstream
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/UpstreamInput" } },
            },
        }
      responses:
        {
          "201":
            {
              description: created,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/Upstream" } },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
  /upstreams/{id}:
    put:
      operationId: updateUpstream
      parameters: [{ $ref: "#/components/parameters/Id" }]
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/UpstreamInput" } },
            },
        }
      responses:
        {
          "200":
            {
              description: updated,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/Upstream" } },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "404": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
    delete:
      operationId: deleteUpstream
      parameters:
        [
          { $ref: "#/components/parameters/Id" },
          { $ref: "#/components/parameters/Revision" },
        ]
      responses:
        {
          "204": { description: deleted },
          "404": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
  /resolver-settings:
    get:
      {
        operationId: getResolverSettings,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      {
                        schema:
                          { $ref: "#/components/schemas/ResolverSettings" },
                      },
                  },
              },
          },
      }
    put:
      operationId: updateResolverSettings
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/ResolverSettings" } },
            },
        }
      responses:
        {
          "200":
            {
              description: updated,
              content:
                {
                  application/json:
                    {
                      schema: { $ref: "#/components/schemas/ResolverSettings" },
                    },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
  /access-control:
    get:
      {
        operationId: getAccessControl,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      {
                        schema: { $ref: "#/components/schemas/AccessControl" },
                      },
                  },
              },
          },
      }
    put:
      operationId: updateAccessControl
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/AccessControl" } },
            },
        }
      responses:
        {
          "200":
            {
              description: updated,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/AccessControl" } },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
  /filter-lists:
    get:
      {
        operationId: listFilterLists,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      {
                        schema:
                          {
                            type: array,
                            items: { $ref: "#/components/schemas/FilterList" },
                          },
                      },
                  },
              },
          },
      }
    post:
      operationId: createFilterList
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/FilterListInput" } },
            },
        }
      responses:
        {
          "201":
            {
              description: created,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/FilterList" } },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
  /filter-lists/{id}:
    get:
      operationId: getFilterList
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        {
          "200":
            {
              description: ok,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/FilterList" } },
                },
            },
          "404": { $ref: "#/components/responses/Error" },
        }
    put:
      operationId: updateFilterList
      parameters: [{ $ref: "#/components/parameters/Id" }]
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/FilterListInput" } },
            },
        }
      responses:
        {
          "200":
            {
              description: updated,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/FilterList" } },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "404": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
    delete:
      operationId: deleteFilterList
      parameters:
        [
          { $ref: "#/components/parameters/Id" },
          { $ref: "#/components/parameters/Revision" },
        ]
      responses:
        {
          "204": { description: deleted },
          "404": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
  /filter-lists/{id}/refresh:
    post:
      operationId: refreshFilterList
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        {
          "200":
            {
              description: refresh attempted; body is the list after the attempt,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/FilterList" } },
                },
            },
          "404": { $ref: "#/components/responses/Error" },
        }
  /allowlist:
    get:
      {
        operationId: getAllowlist,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      { schema: { $ref: "#/components/schemas/Allowlist" } },
                  },
              },
          },
      }
    put:
      operationId: updateAllowlist
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/Allowlist" } },
            },
        }
      responses:
        {
          "200":
            {
              description: updated,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/Allowlist" } },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
  /engines:
    get:
      {
        operationId: listEngines,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      {
                        schema:
                          {
                            type: array,
                            items: { $ref: "#/components/schemas/Engine" },
                          },
                      },
                  },
              },
          },
      }
  /engines/{id}:
    get:
      operationId: getEngine
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        {
          "200":
            {
              description: ok,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/Engine" } },
                },
            },
          "404": { $ref: "#/components/responses/Error" },
        }
    delete:
      operationId: deleteEngine
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        {
          "204": { description: deleted },
          "404": { $ref: "#/components/responses/Error" },
        }
  /join-tokens:
    get:
      {
        operationId: listJoinTokens,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      {
                        schema:
                          {
                            type: array,
                            items: { $ref: "#/components/schemas/JoinToken" },
                          },
                      },
                  },
              },
          },
      }
    post:
      operationId: createJoinToken
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/JoinTokenCreate" } },
            },
        }
      responses:
        {
          "201":
            {
              description: created; token shown once,
              content:
                {
                  application/json:
                    {
                      schema: { $ref: "#/components/schemas/JoinTokenCreated" },
                    },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
        }
  /join-tokens/{id}:
    delete:
      operationId: revokeJoinToken
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        {
          "204": { description: revoked },
          "404": { $ref: "#/components/responses/Error" },
        }
  /config-versions:
    get:
      operationId: listConfigVersions
      parameters:
        [
          {
            name: limit,
            in: query,
            schema: { type: integer, minimum: 1, maximum: 500, default: 50 },
          },
        ]
      responses:
        {
          "200":
            {
              description: ok,
              content:
                {
                  application/json:
                    {
                      schema:
                        {
                          type: array,
                          items: { $ref: "#/components/schemas/ConfigVersion" },
                        },
                    },
                },
            },
        }
  /users:
    get:
      {
        operationId: listUsers,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      {
                        schema:
                          {
                            type: array,
                            items: { $ref: "#/components/schemas/User" },
                          },
                      },
                  },
              },
          },
      }
    post:
      operationId: createUser
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/UserCreate" } },
            },
        }
      responses:
        {
          "201":
            {
              description: created,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/User" } },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
  /users/{id}:
    put:
      operationId: updateUser
      parameters: [{ $ref: "#/components/parameters/Id" }]
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/UserUpdate" } },
            },
        }
      responses:
        {
          "200":
            {
              description: updated,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/User" } },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "404": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
    delete:
      operationId: deleteUser
      parameters:
        [
          { $ref: "#/components/parameters/Id" },
          { $ref: "#/components/parameters/Revision" },
        ]
      responses:
        {
          "204": { description: deleted },
          "404": { $ref: "#/components/responses/Error" },
          "409": { $ref: "#/components/responses/Error" },
        }
  /api-tokens:
    get:
      {
        operationId: listApiTokens,
        responses:
          {
            "200":
              {
                description: ok,
                content:
                  {
                    application/json:
                      {
                        schema:
                          {
                            type: array,
                            items: { $ref: "#/components/schemas/ApiToken" },
                          },
                      },
                  },
              },
          },
      }
    post:
      operationId: createApiToken
      requestBody:
        {
          required: true,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/ApiTokenCreate" } },
            },
        }
      responses:
        {
          "201":
            {
              description: created; token shown once,
              content:
                {
                  application/json:
                    {
                      schema: { $ref: "#/components/schemas/ApiTokenCreated" },
                    },
                },
            },
          "400": { $ref: "#/components/responses/Error" },
          "403": { $ref: "#/components/responses/Error" },
        }
  /api-tokens/{id}:
    delete:
      operationId: revokeApiToken
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        {
          "204": { description: revoked },
          "404": { $ref: "#/components/responses/Error" },
        }
  /audit:
    get:
      operationId: listAuditEvents
      parameters:
        [
          {
            name: limit,
            in: query,
            schema: { type: integer, minimum: 1, maximum: 500, default: 100 },
          },
          {
            name: before_id,
            in: query,
            schema: { type: integer, format: int64 },
          },
        ]
      responses:
        {
          "200":
            {
              description: ok,
              content:
                {
                  application/json:
                    {
                      schema:
                        {
                          type: array,
                          items: { $ref: "#/components/schemas/AuditEvent" },
                        },
                    },
                },
            },
        }
  /query-log:
    get:
      operationId: searchQueryLog
      parameters:
        - { name: from, in: query, schema: { type: string, format: date-time } }
        - { name: to, in: query, schema: { type: string, format: date-time } }
        - { name: client, in: query, schema: { type: string } }
        - { name: name, in: query, schema: { type: string } }
        - { name: qtype, in: query, schema: { type: string } }
        - { name: rcode, in: query, schema: { type: string } }
        - { name: cache, in: query, schema: { type: string } }
        - { name: filter, in: query, schema: { type: string } }
        - {
            name: limit,
            in: query,
            schema: { type: integer, minimum: 1, maximum: 1000, default: 100 },
          }
        - { name: cursor, in: query, schema: { type: string } }
      responses:
        {
          "200":
            {
              description: ok,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/QueryLogPage" } },
                },
            },
          "503": { $ref: "#/components/responses/Error" },
        }
```

- [ ] Write `mgmt/api/oapi-codegen.yaml`:

```yaml
package: api
output: ../internal/api/gen.go
generate:
  chi-server: true
  strict-server: true
  models: true
  embedded-spec: true
output-options:
  skip-prune: true
```

- [ ] Write the failing test `mgmt/internal/api/api_test.go`:

```go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

type apiEnv struct {
	srv   *httptest.Server
	st    *store.Store
	svc   *auth.Service
	pg    *harness.Postgres
	ctx   context.Context
	setup string
}

func newAPI(t *testing.T) *apiEnv {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_ = pki.InitCA(dir)
	ca, _ := pki.LoadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(st, false)
	tok, _, err := svc.EnsureSetupToken(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	h := api.NewHandler(api.Deps{Store: st, Auth: svc, OIDC: auth.NewOIDC(auth.DisabledOIDC(), "http://x", st), CA: ca, QueryLog: querylog.Noop{}, InstanceID: "test", PublicURL: "http://x"})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &apiEnv{srv: srv, st: st, svc: svc, pg: pg, ctx: ctx, setup: tok}
}

type client struct {
	t    *testing.T
	base string
	hc   *http.Client
}

func (e *apiEnv) client(t *testing.T) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: e.srv.URL + "/api/v1", hc: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode
}

func TestPermissionsCoverEveryOperation(t *testing.T) {
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			seen[op.OperationID] = true
			_, perm := auth.Permissions[op.OperationID]
			if !perm && !auth.Public[op.OperationID] {
				t.Errorf("operation %s has no permission entry", op.OperationID)
			}
		}
	}
	for op := range auth.Permissions {
		if !seen[op] {
			t.Errorf("permission %s names no OpenAPI operation", op)
		}
	}
	for op := range auth.Public {
		if !seen[op] {
			t.Errorf("public %s names no OpenAPI operation", op)
		}
	}
}

func TestSetupCRUDConflictAuditAndRBAC(t *testing.T) {
	e := newAPI(t)
	admin := e.client(t)
	var status map[string]bool
	if admin.do("GET", "/setup", nil, &status); !status["required"] {
		t.Fatal("setup should be required")
	}
	if code := admin.do("GET", "/upstreams", nil, nil); code != 401 {
		t.Fatalf("unauthenticated list -> %d", code)
	}
	if code := admin.do("POST", "/setup", map[string]string{"token": e.setup, "username": "admin", "email": "a@x", "password": "admin-password-1"}, nil); code != 201 {
		t.Fatalf("setup -> %d", code)
	}
	var up map[string]any
	if code := admin.do("POST", "/upstreams", map[string]any{"name": "q9", "protocol": "udp", "address": "9.9.9.9:53", "timeout_ms": 250, "enabled": true, "position": 0}, &up); code != 201 {
		t.Fatalf("create upstream -> %d %v", code, up)
	}
	id := up["id"].(string)
	body := map[string]any{"name": "q9", "protocol": "udp", "address": "149.112.112.112:53", "timeout_ms": 300, "enabled": true, "position": 0, "revision": 1}
	if code := admin.do("PUT", "/upstreams/"+id, body, &up); code != 200 || up["revision"].(float64) != 2 {
		t.Fatalf("update -> %d %v", code, up)
	}
	var apiErr map[string]string
	if code := admin.do("PUT", "/upstreams/"+id, body, &apiErr); code != 409 || apiErr["code"] != "conflict" {
		t.Fatalf("stale revision -> %d %v", code, apiErr)
	}
	if code := admin.do("POST", "/upstreams", map[string]any{"name": "bad", "protocol": "doh", "doh_url": "http://x/dns-query", "timeout_ms": 250, "enabled": true, "position": 1}, &apiErr); code != 400 || apiErr["code"] != "invalid_request" {
		t.Fatalf("http DoH -> %d %v", code, apiErr)
	}

	var audit []map[string]any
	admin.do("GET", "/audit", nil, &audit)
	actions := map[string]bool{}
	for _, a := range audit {
		if a["actor_name"] == "admin" && a["diff"] != nil {
			actions[a["action"].(string)] = true
		}
	}
	if !actions["createUpstream"] || !actions["updateUpstream"] {
		t.Fatalf("audit missing changes: %v", actions)
	}
	var versions []map[string]any
	admin.do("GET", "/config-versions", nil, &versions)
	if len(versions) < 3 {
		t.Fatalf("config versions = %d", len(versions))
	}

	admin.do("POST", "/users", map[string]any{"username": "vic", "email": "v@x", "password": "viewer-password-1", "role": "viewer"}, nil)
	viewer := e.client(t)
	if code := viewer.do("POST", "/auth/login", map[string]string{"username": "vic", "password": "viewer-password-1"}, nil); code != 200 {
		t.Fatalf("viewer login -> %d", code)
	}
	if code := viewer.do("GET", "/upstreams", nil, nil); code != 200 {
		t.Fatalf("viewer read -> %d", code)
	}
	if code := viewer.do("POST", "/upstreams", map[string]any{"name": "v", "protocol": "udp", "address": "1.1.1.1:53", "timeout_ms": 250, "enabled": true, "position": 2}, &apiErr); code != 403 || apiErr["code"] != "forbidden" {
		t.Fatalf("viewer write -> %d %v", code, apiErr)
	}
	if code := viewer.do("GET", "/audit", nil, nil); code != 403 {
		t.Fatalf("viewer audit -> %d", code)
	}
	var jt map[string]any
	if code := admin.do("POST", "/join-tokens", map[string]any{"name": "fleet", "ttl_seconds": 3600}, &jt); code != 201 || len(jt["token"].(string)) < 10 {
		t.Fatalf("join token -> %d %v", code, jt)
	}
}

func TestCSRFAndDatabaseDown(t *testing.T) {
	e := newAPI(t)
	c := e.client(t)
	c.do("POST", "/setup", map[string]string{"token": e.setup, "username": "admin", "email": "a@x", "password": "admin-password-1"}, nil)
	req, _ := http.NewRequest("POST", c.base+"/upstreams", bytes.NewReader([]byte(`{"name":"x","protocol":"udp","address":"1.1.1.1:53","timeout_ms":250,"enabled":true,"position":0}`)))
	req.Header.Set("Content-Type", "text/plain")
	resp, _ := c.hc.Do(req)
	if resp.StatusCode != 415 {
		t.Fatalf("non-JSON mutation -> %d", resp.StatusCode)
	}
	if code := c.do("GET", "/health", nil, nil); code != 200 {
		t.Fatalf("health -> %d", code)
	}
	harness.StopPostgres(t, e.pg)
	var apiErr map[string]string
	if code := c.do("POST", "/upstreams", map[string]any{"name": "y", "protocol": "udp", "address": "1.1.1.1:53", "timeout_ms": 250, "enabled": true, "position": 0}, &apiErr); code != 503 || apiErr["code"] != "unavailable" {
		t.Fatalf("write with database down -> %d %v", code, apiErr)
	}
	var h map[string]string
	if code := c.do("GET", "/health", nil, &h); code != 503 || h["database"] != "unavailable" {
		t.Fatalf("health with database down -> %d %v", code, h)
	}
}
```

- [ ] Add `func DisabledOIDC() config.OIDCConfig { return config.OIDCConfig{} }` to `mgmt/internal/auth/oidc.go`, then run `scripts/dev-exec.sh go test -count=1 ./mgmt/internal/api/...` — expect FAIL with `no required module provides package github.com/piwi3910/nexora/mgmt/internal/api` (or `undefined: api.NewHandler` once `gen.go` exists).
- [ ] Generate: `scripts/dev-exec.sh bash -c 'cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml'` and copy `mgmt/internal/api/gen.go` back with `kubectl --context kw -n nexora-dev exec deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - mgmt/internal/api/gen.go | tar -xf -`.
- [ ] Implement `server.go`: chi router with `middleware.RequestID`, `middleware.Recoverer`; `/api/v1` mounts `HandlerWithOptions(NewStrictHandlerWithOptions(handlers, []StrictMiddlewareFunc{authz}, StrictHTTPServerOptions{RequestErrorHandlerFunc: writeError(400,"invalid_request"), ResponseErrorHandlerFunc: mapError}))`. A plain `http` middleware before the strict handler rejects non-GET/HEAD requests whose `Content-Type` media type is not `application/json` with 415 `unsupported_media_type`. The `authz` strict middleware receives `operationID`: `auth.Public[operationID]` passes; otherwise `Auth.Authenticate` (401) then `auth.Authorize` (403), storing the `Principal` in the context (`api.PrincipalFrom(ctx)`). `mapError` maps `store.ErrUnavailable` -> 503 `unavailable`, `store.ErrNotFound` -> 404, `store.ErrConflict` -> 409 `conflict`, `auth.ErrForbidden` -> 403, `auth.ErrUnauthenticated`/`ErrInvalidCredentials` -> 401, `querylog.ErrBackendUnavailable` -> 503 `querylog_unavailable`, `errValidation` (a local type carrying a message) -> 400 `invalid_request`, anything else -> 500 `internal` with a generic message.
- [ ] Implement `handlers_dns.go`. Every mutation calls `snapshot.Mutate(ctx, d.Store, d.Build, principal.Actor(), fn)` where `fn` performs the row change with optimistic concurrency (`update ... set ..., revision = revision + 1 where id=$1 and revision=$2 returning ...`; zero rows -> `select 1 ... where id=$1` distinguishes `ErrNotFound` from `ErrConflict`) and returns `auth.Change{Action: operationID, TargetType, TargetID, Before, After}` with the row before and after. Validation before `Mutate`: UDP/TCP/DoT `address` parses with `netip.ParseAddrPort`; DoT requires `tls_server_name`; DoH requires `doh_url` with scheme `https`; `ca_certificate_pem` parses with `x509` when non-empty; CIDRs parse with `netip.ParsePrefix`; allowlist domains lowercased and matched by `^[a-z0-9_-]+(\.[a-z0-9_-]+)*$`; resolver settings `cache_max_bytes >= 1048576`, `cache_max_ttl >= cache_min_ttl`. `refreshFilterList` calls `d.RefreshFilterList(ctx, principal, id)` (supplied by `blocklist.Fetcher.RefreshNow` in Task 17; a nil field yields 400 `invalid_request` with message `filter list fetching is not enabled on this instance`) and returns the list row read after the attempt.
- [ ] Implement `handlers_fleet.go`: `listEngines`/`getEngine` compute `connected` as `connected_instance is not null and instances.heartbeat_at > now() - interval '15 seconds'`, and `status`: `ahead` when `version_ahead`, `disconnected` when not connected, `rejected` when `rejected_version` > `applied_version`, `current` when `applied_version` = latest version, else `behind`; `deleteEngine` sets `deleted_at` (audit via `Mutate` with no snapshot content change); `createJoinToken` uses `control.CreateJoinToken` inside `Mutate` and returns the token once; `revokeJoinToken` sets `revoked_at`; `listConfigVersions` orders by version desc; `getDashboard` returns `stats.Dashboard`.
- [ ] Implement `handlers_auth.go`: `getHealth` pings the pool with a 2 s timeout (`200 {"status":"ok","database":"ok","version":<build version>}` or `503 {"status":"degraded","database":"unavailable",...}`); `completeSetup`/`login` set `Auth.SessionCookie`; `logout` deletes the session and expires the cookie; `listAuthProviders` returns `{local: true, oidc: d.OIDC.Enabled()}`; `startOidcLogin` 302 to `OIDC.Start` or 503 `unavailable` with message `identity provider unavailable`; `oidcCallback` creates a session and 302s to the stored `return_to` (only paths starting with `/` and not `//`).
- [ ] Implement `handlers_admin.go`: users CRUD with revision checks (admins cannot delete or demote the last enabled admin -> 409 `conflict`), API tokens (`createApiToken` owner = principal, token returned once; `listApiTokens` all tokens), `listAuditEvents` (`limit`, `before_id`, newest first), `searchQueryLog` -> `d.QueryLog.Search` mapped to `QueryLogPage{backend: d.QueryLog.Name()}`. User and token changes are audited with `auth.WriteAudit` in a plain `store.InTx` (they do not create config versions).
- [ ] Implement `querylog/backend.go`, `stats/stats.go` and `webui/embed.go` per the interfaces (`webui.Handler` serves files from `dist` with `index.html` fallback for paths without a file extension and `Cache-Control: no-cache` on `index.html`).
- [ ] Extend `serve` in `main.go`: `auth.NewService(st, cfg.SecureCookies)`; `EnsureSetupToken` and, when `created`, `log.Printf("setup token: %s", token)`; `server.OnStats = func(ctx, id, s) { _ = stats.Record(ctx, st, id, s) }`; `api.NewHandler(api.Deps{..., QueryLog: querylog.Noop{}})` on `cfg.HTTPListen` with `ReadHeaderTimeout: 10 * time.Second`; print `http listening on <addr>`. Add `user create --admin --username U --email E --password-file F` using `auth.CreateUser` + `auth.WriteAudit` with actor `{system, "cli", "cli"}`.
- [ ] Run `scripts/dev-exec.sh bash -c 'make mgmt-test'` — expect PASS: `ok github.com/piwi3910/nexora/mgmt/internal/api` among all packages.
- [ ] Commit: `git add mgmt go.mod go.sum && git commit -m "mgmt: OpenAPI 3.1 spec, strict HTTP API with RBAC, CSRF, optimistic concurrency and audit"`.

## Task 16: Engine management-plane client and control-plane acceptance tests

Files:

- `engine/src/control.rs` (create) — join token, pinned enrollment, identity storage, `Connect` loop, blob fetch, stats
- `engine/src/lib.rs` (modify) — add `pub mod control;`
- `engine/src/main.rs` (modify) — managed mode runs `control::run` on the `nexora-control` runtime
- `engine/tests/control_unit.rs` (create)
- `mgmt/internal/control/tls.go` (modify) — serve the CA certificate in the chain so engines can pin it during enrollment
- `e2e/harness/mgmt.go` (create) — CA init, management plane processes, API client, bootstrap, managed engines, raw snapshot publish
- `e2e/harness/lb.go` (create) — in-process TCP load balancer
- `e2e/control_test.go` (create) — `TestInvalidSnapshotRejected`, `TestMgmtStatelessHA`

Interfaces:

- Consumes `proto::engine_control_client::EngineControlClient`, `proto::{EnrollRequest, EngineMessage, engine_message::Msg, Hello, Applied, Rejected, ServerMessage, server_message::Msg as ServerMsg, GetBlobRequest}` (Task 2); `snapshot::{apply, ApplyOutcome, DirBlobs, verify_blob, SnapshotError}` (Task 7); `server::Shared` (Tasks 8–9); `Metrics::stats` (Task 9); `bootstrap::Bootstrap` (Task 8). Management side from Tasks 13 and 15: `POST /api/v1/join-tokens`, `GET /api/v1/engines`, `GET /api/v1/engines/{id}`, `POST /api/v1/upstreams`, `PUT /api/v1/upstreams/{id}`, `GET /api/v1/config-versions`, `POST /api/v1/setup`, `POST /api/v1/api-tokens`.
- `control.rs`: `#[derive(Debug, thiserror::Error)] pub enum ControlError { #[error("join token: {0}")] JoinToken(String), #[error("enroll: {0}")] Enroll(String), #[error("tls: {0}")] Tls(String), #[error("grpc: {0}")] Grpc(#[from] tonic::Status), #[error("transport: {0}")] Transport(#[from] tonic::transport::Error), #[error("io: {0}")] Io(#[from] std::io::Error) }`; `pub struct JoinToken { pub secret: String, pub ca_fingerprint: String }`; `pub fn parse_join_token(s: &str) -> Result<JoinToken, ControlError>`; `pub struct Identity { pub engine_id: String, pub cert_pem: String, pub key_pem: String, pub ca_pem: String }`; `pub fn load_identity(state_dir: &Path) -> std::io::Result<Option<Identity>>`; `pub fn save_identity(state_dir: &Path, id: &Identity) -> std::io::Result<()>` (files `identity/engine_id`, `identity/cert.pem`, `identity/key.pem` mode 0600, `identity/ca.pem`); `pub async fn fetch_pinned_ca(url: &str, fingerprint: &str) -> Result<String, ControlError>`; `pub async fn enroll(url: &str, token: &JoinToken, node_name: &str) -> Result<Identity, ControlError>`; `pub async fn channel(url: &str, id: &Identity) -> Result<tonic::transport::Channel, ControlError>`; `pub async fn fetch_blobs(client: &mut EngineControlClient<Channel>, snap: &ConfigSnapshot, blob_dir: &Path) -> Result<(), SnapshotError>`; `pub fn backoff(attempt: u32) -> Duration` (500 ms × 2^attempt capped at 30 s, ±20 % jitter); `pub async fn run(shared: Arc<Shared>, boot: Bootstrap)`; stderr lines `nexora-engine: enrolled as <id>`, `nexora-engine: control connected to <url>`, `nexora-engine: applied version <v>`, `nexora-engine: rejected version <v>: <reason>`, `nexora-engine: management plane at version <s> is behind engine version <a>`.
- `e2e/harness/mgmt.go`: `type CA struct { Dir, CertFile, KeyFile string }`; `func (e *Env) InitCA() *CA`; `type MgmtOptions struct { QueryLogBackend, OpenSearchURL, OTLPEndpoint string; OIDC *OIDCFixture; OIDCAdminGroup, OIDCOperatorGroup string; ExtraEnv []string }`; `type Mgmt struct { HTTPAddr, GRPCAddr, BaseURL, GRPCURL string; Proc *Proc }`; `func (e *Env) StartMgmt(pg *Postgres, ca *CA, o MgmtOptions) *Mgmt`; `func (m *Mgmt) SetupToken(t *testing.T) string` (from the log line `setup token: `, empty when another instance created it); `type API struct { T *testing.T; Base string; HC *http.Client; Bearer string }`; `func (e *Env) NewAPI(baseURL string) *API` (cookie jar, `DisableKeepAlives: true`); `func (a *API) Do(method, path string, body, out any) (int, error)`; `func (a *API) Must(method, path string, body, out any, want int)`; `func Bootstrap(t *testing.T, e *Env, setupToken, baseURL string) *API` (completes setup as `admin` / `admin-password-e2e`, creates an admin API token, returns a bearer client); `func (a *API) CreateJoinToken() string`; `func (a *API) LatestVersion() uint64`; `func (a *API) WaitEngine(nodeName string, timeout time.Duration, cond func(EngineView) bool) EngineView`; `type EngineView struct { ID, NodeName, Status, RejectedReason string; AppliedVersion uint64; RejectedVersion *uint64; Connected bool }`; `func (e *Env) StartManagedEngine(nodeName string, grpcURLs []string, joinToken string) *Engine`; `func PublishRawSnapshot(t *testing.T, pgURL string, snap *controlv1.ConfigSnapshot) uint64`.
- `e2e/harness/lb.go`: `type Balancer struct { Addr string }`; `func (e *Env) StartTCPBalancer(backends ...string) *Balancer` (each accepted connection dials backends in order with a 200 ms timeout and pipes bytes both ways; closed on cleanup).

- [ ] Write the failing Rust unit test `engine/tests/control_unit.rs`:

```rust
use nexora_engine::control::{backoff, load_identity, parse_join_token, save_identity, Identity};
use std::time::Duration;

#[test]
fn join_token_parsing() {
    let fp = "ab".repeat(32);
    let t = parse_join_token(&format!("nxj1.MFRGGZDFMZTWQ2LK.{fp}")).unwrap();
    assert_eq!(t.secret, "MFRGGZDFMZTWQ2LK");
    assert_eq!(t.ca_fingerprint, fp);
    for bad in ["nxj2.AAAA.".to_string() + &fp, "nxj1..".into(), format!("nxj1.AAAA.{}", "zz".repeat(32)), "nxj1.AAAA".into()] {
        assert!(parse_join_token(&bad).is_err(), "accepted {bad}");
    }
    assert!(parse_join_token(&format!("  nxj1.AAAA.{fp}\n")).is_ok(), "whitespace from files is trimmed");
}

#[test]
fn identity_round_trip_with_private_key_mode() {
    use std::os::unix::fs::PermissionsExt;
    let dir = tempfile::tempdir().unwrap();
    assert!(load_identity(dir.path()).unwrap().is_none());
    let id = Identity { engine_id: "e-1".into(), cert_pem: "CERT".into(), key_pem: "KEY".into(), ca_pem: "CA".into() };
    save_identity(dir.path(), &id).unwrap();
    let back = load_identity(dir.path()).unwrap().unwrap();
    assert_eq!(back.engine_id, "e-1");
    assert_eq!(back.key_pem, "KEY");
    let mode = std::fs::metadata(dir.path().join("identity/key.pem")).unwrap().permissions().mode() & 0o777;
    assert_eq!(mode, 0o600);
}

#[test]
fn backoff_grows_jitters_and_caps() {
    for _ in 0..50 {
        let b0 = backoff(0);
        assert!(b0 >= Duration::from_millis(400) && b0 <= Duration::from_millis(600), "{b0:?}");
        assert!(backoff(20) <= Duration::from_secs(36));
    }
    assert!(backoff(3) > backoff(0));
}
```

- [ ] Write the failing acceptance tests `e2e/control_test.go`:

```go
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/e2e/harness"
)

func createUDPUpstream(t *testing.T, api *harness.API, name, addr string) map[string]any {
	var up map[string]any
	api.Must("POST", "/upstreams", map[string]any{"name": name, "protocol": "udp", "address": addr, "timeout_ms": 250, "enabled": true, "position": 0}, &up, 201)
	return up
}

func TestInvalidSnapshotRejected(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	fx := env.StartDNSFixture()
	createUDPUpstream(t, api, "fixture", fx.UDP)
	eng := env.StartManagedEngine("engine-invalid", []string{mgmt.GRPCURL}, api.CreateJoinToken())
	good := api.LatestVersion()
	api.WaitEngine("engine-invalid", 15*time.Second, func(v harness.EngineView) bool { return v.AppliedVersion == good })

	before := harness.UniqueName("before")
	if r := harness.MustQuery(t, eng.DNS, before, dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
		t.Fatalf("engine not serving before the invalid snapshot: %v", r)
	}

	var current controlv1.ConfigSnapshot
	raw, err := os.ReadFile(filepath.Join(eng.StateDir, "snapshot.binpb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(raw, &current); err != nil || current.Version != good {
		t.Fatalf("persisted snapshot version %d (%v), want %d", current.Version, err, good)
	}
	bad := proto.Clone(&current).(*controlv1.ConfigSnapshot)
	bad.Upstreams[0].TimeoutMs = 10
	badVersion := harness.PublishRawSnapshot(t, pg.URL, bad)

	view := api.WaitEngine("engine-invalid", 10*time.Second, func(v harness.EngineView) bool {
		return v.RejectedVersion != nil && *v.RejectedVersion == badVersion
	})
	if view.Status != "rejected" || !strings.Contains(view.RejectedReason, "timeout") || view.AppliedVersion != good {
		t.Fatalf("API does not report the rejection: %+v", view)
	}
	if v := eng.Metric(t, "nexora_config_version", nil); uint64(v) != good {
		t.Fatalf("engine applied the invalid snapshot: version metric %v", v)
	}
	if r := harness.MustQuery(t, eng.DNS, harness.UniqueName("after"), dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
		t.Fatalf("engine stopped serving after rejection: %v", r)
	}
	raw, _ = os.ReadFile(filepath.Join(eng.StateDir, "snapshot.binpb"))
	_ = proto.Unmarshal(raw, &current)
	if current.Version != good {
		t.Fatalf("rejected snapshot was persisted: %d", current.Version)
	}

	var ups []map[string]any
	api.Must("GET", "/upstreams", nil, &ups, 200)
	ups[0]["timeout_ms"] = 300
	api.Must("PUT", "/upstreams/"+ups[0]["id"].(string), ups[0], nil, 200)
	next := api.LatestVersion()
	api.WaitEngine("engine-invalid", 10*time.Second, func(v harness.EngineView) bool { return v.AppliedVersion == next && v.Status == "current" })
}

func TestMgmtStatelessHA(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	a := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	token := a.SetupToken(t)
	b := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	httpLB := env.StartTCPBalancer(a.HTTPAddr, b.HTTPAddr)
	grpcLB := env.StartTCPBalancer(a.GRPCAddr, b.GRPCAddr)
	api := harness.Bootstrap(t, env, token, "http://"+httpLB.Addr)
	fx := env.StartDNSFixture()
	createUDPUpstream(t, api, "fixture", fx.UDP)
	eng := env.StartManagedEngine("engine-ha", []string{"https://" + grpcLB.Addr}, api.CreateJoinToken())
	v1 := api.LatestVersion()
	api.WaitEngine("engine-ha", 15*time.Second, func(v harness.EngineView) bool { return v.AppliedVersion == v1 && v.Connected })
	eng.Proc.WaitLog(regexpMust("control connected to"), 5*time.Second)

	a.Proc.Kill()
	killed := time.Now()

	var ok int
	deadline := killed.Add(10 * time.Second)
	for time.Now().Before(deadline) {
		code, err := api.Do("GET", "/upstreams", nil, nil)
		if err == nil && code == 200 {
			ok++
			if ok >= 3 {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if ok < 3 {
		t.Fatalf("API through the load balancer did not recover within 10s of killing an instance")
	}
	var up map[string]any
	api.Must("POST", "/upstreams", map[string]any{"name": "after-failover", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 1}, &up, 201)
	v2 := api.LatestVersion()
	api.WaitEngine("engine-ha", time.Until(killed.Add(10*time.Second))+time.Second, func(v harness.EngineView) bool { return v.AppliedVersion == v2 && v.Connected })
	if elapsed := time.Since(killed); elapsed > 11*time.Second {
		t.Fatalf("engine control stream recovered after %v", elapsed)
	}
	if r := harness.MustQuery(t, eng.DNS, harness.UniqueName("ha"), dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
		t.Fatalf("engine not answering: %v", r)
	}
}
```

with `func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }` added to `e2e/main_test.go` (import `regexp`).

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test control_unit` — expect FAIL with ``could not find `control` in `nexora_engine` ``; run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -run "TestInvalidSnapshotRejected|TestMgmtStatelessHA" ./e2e/'` — expect FAIL with `env.InitCA undefined`.
- [ ] Modify `mgmt/internal/control/tls.go` so the served `tls.Certificate.Certificate` is `[leafDER, ca.Cert.Raw]`.
- [ ] Implement `control.rs`:
  - `parse_join_token` trims, splits on `.` into exactly `nxj1`, a non-empty `[A-Z2-7]+` secret and 64 lowercase hex.
  - `fetch_pinned_ca` opens a `tokio_rustls` connection to the URL host:port with a custom `rustls::client::danger::ServerCertVerifier` that searches the presented chain (`end_entity` + `intermediates`) for a certificate whose SHA-256 equals `fingerprint`, then verifies the end entity against a `RootCertStore` containing only that CA using `rustls::client::WebPkiServerVerifier`; returns that CA as PEM or `ControlError::Tls("CA fingerprint mismatch")`.
  - `enroll`: `fetch_pinned_ca`, generate an ECDSA P-256 key and CSR with `rcgen` (`KeyPair::generate_for(&rcgen::PKCS_ECDSA_P256_SHA256)`, `CertificateParams::default().serialize_request(&key)`), call `Enroll` over a tonic channel whose `ClientTlsConfig` trusts only the pinned CA (`domain_name` = URL host), convert the returned DER certificates to PEM, and build `Identity`.
  - `channel`: `Endpoint::from_shared(url)?.connect_timeout(3s).http2_keep_alive_interval(10s).keep_alive_timeout(5s).tls_config(ClientTlsConfig::new().ca_certificate(Certificate::from_pem(ca)).identity(tonic::transport::Identity::from_pem(cert, key)).domain_name(host))?.connect().await`.
  - `fetch_blobs`: for each `BlobRef` in `filter.blocklists` and `filter.allowlists` missing from `blob_dir/<sha256>`, stream `GetBlob` into `blob_dir/<sha256>.tmp`, `verify_blob`, rename.
  - `run`: `identity = load_identity(state_dir)` or, when absent, read `join_token_file`, try `enroll` against each `management_urls` entry with `backoff(attempt)` between rounds, `save_identity`, set `shared.engine_id`. Then loop forever over `management_urls` (round-robin): `channel` -> store `shared.mgmt_channel`, create `EngineControlClient`, `tokio::sync::mpsc::channel::<EngineMessage>(16)` feeding `client.connect(ReceiverStream)`, send `Hello { engine_id, node_name, applied_version: shared.runtime.load().version, engine_version: env!("CARGO_PKG_VERSION") }`, set `metrics.control_connected = true`, reset the attempt counter, spawn a 10 s ticker sending `Stats` (`metrics.stats(&runtime)`), and process inbound messages: `Snapshot` -> `fetch_blobs` (errors become `Rejected{reason}`) -> `tokio::task::spawn_blocking(move || snapshot::apply(&shared.runtime, snap, &DirBlobs { dir: state_dir.join("blobs") }, Some(&state_dir)))` -> send `Applied{version, persist_error}` (and store `metrics.config_version`) or `Rejected{version, reason}`; `VersionAhead` -> stderr line. Any stream/transport error sets `control_connected = false`, clears `mgmt_channel`, sleeps `backoff(attempt)` and moves to the next URL.
- [ ] Modify `main.rs`: in managed mode, after serving the persisted snapshot (if any), spawn `control::run(shared.clone(), boot.clone())` on the `nexora-control` runtime.
- [ ] Implement `e2e/harness/mgmt.go`: `InitCA` runs `nexora-mgmt ca init --out <Dir>/ca`; `StartMgmt` picks HTTP/gRPC ports and runs `nexora-mgmt serve` with env `NEXORA_DATABASE_URL`, `NEXORA_HTTP_LISTEN=127.0.0.1:<p>`, `NEXORA_GRPC_LISTEN=127.0.0.1:<p>`, `NEXORA_CA_CERT_FILE`, `NEXORA_CA_KEY_FILE`, `NEXORA_GRPC_SERVER_NAMES=127.0.0.1`, `NEXORA_PUBLIC_URL=http://127.0.0.1:<p>`, `NEXORA_SECURE_COOKIES=false`, `NEXORA_QUERYLOG_BACKEND` (default `builtin`), OpenSearch/OTLP/OIDC variables from options (OIDC client secret file from the fixture), waits for `http listening on` and `grpc listening on`. `StartManagedEngine` writes the join token to `<dir>/join-token` and `engine.toml` with `management_urls`, `join_token_file`, loopback listeners, `workers = 2`, and waits for `control connected to`. `PublishRawSnapshot` connects with pgx, takes `pg_advisory_xact_lock(hashtext('nexora:config_version'))`, sets `snap.Version = max+1`, inserts into `config_versions(version, created_by, summary, snapshot)` with `created_by='e2e'`, and runs `select pg_notify('nexora_config', $1)`. `WaitEngine` polls `GET /engines` every 200 ms matching `node_name`.
- [ ] Implement `e2e/harness/lb.go` per the interface.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test control_unit` — expect PASS: `3 passed`.
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -v -run "TestInvalidSnapshotRejected|TestMgmtStatelessHA" ./e2e/'` — expect PASS: `--- PASS: TestInvalidSnapshotRejected` and `--- PASS: TestMgmtStatelessHA`.
- [ ] Commit: `git add engine mgmt/internal/control/tls.go e2e && git commit -m "engine: management-plane client with pinned enrollment; e2e invalid snapshot and HA tests"`.

## Task 17: Blocklist subscriptions — fetcher, parser, normalised blobs, and the subscription acceptance test

Files:

- `mgmt/internal/blocklist/parse.go` (create) — hosts / AdBlock / domain-list parser, normaliser, zstd blob
- `mgmt/internal/blocklist/fetcher.go` (create) — scheduled and on-demand refresh under advisory locks
- `mgmt/internal/blocklist/parse_test.go` (create)
- `mgmt/internal/api/handlers_dns.go` (modify) — `createFilterList` triggers an immediate background refresh
- `mgmt/cmd/nexora-mgmt/main.go` (modify) — start `Fetcher.Run`, pass `Fetcher.RefreshNow` as `Deps.RefreshFilterList`
- `e2e/blocklist_test.go` (create) — `TestBlocklistSubscription`

Interfaces:

- Consumes `snapshot.{Mutate, BuildConfig}`, `auth.{Actor, Change, Principal}` (Tasks 13–14), `store.Store` (Task 12), `api.Deps.RefreshFilterList` (Task 15); harness `StartHTTPFixture`, `HTTPFixture.{SetList, SetFailing, URL}`, `StartMgmt`, `Bootstrap`, `StartManagedEngine`, `API.WaitEngine`, `LatestVersion` (Tasks 10, 16); engine filter from Task 7.
- `blocklist.ParseStats{ Entries, Invalid int }`; `func Parse(r io.Reader) ([]string, ParseStats, error)`; `func Normalize(domains []string) []byte`; `func Compress(text []byte) (data []byte, sha256hex string, err error)`; `const MaxListBytes = 256 << 20`; `func NewFetcher(st *store.Store, build snapshot.BuildConfig, hc *http.Client) *Fetcher`; `func (f *Fetcher) Run(ctx context.Context)` (every 30 s selects `enabled` lists where `last_attempt_at is null or last_attempt_at < now() - refresh_interval_seconds * interval '1 second'`); `func (f *Fetcher) RefreshNow(ctx context.Context, p auth.Principal, id string) error`.
- Advisory lock key: `pg_try_advisory_lock(hashtext('filter_list:' || $1))` on a dedicated pooled connection, released with `pg_advisory_unlock` after the attempt; when not acquired the call returns nil (another instance is fetching).
- Line grammar: blank lines and lines starting with `#` or `!` and `[Adblock` headers are ignored (not counted); `0.0.0.0 d`, `127.0.0.1 d`, `:: d`, `::1 d` (extra hostnames on the line each count) -> `d`; `||d^` and `||d^$<options>` -> `d`; a bare `d` -> `d`; `localhost`, `localhost.localdomain`, `broadcasthost`, `0.0.0.0` hostnames are ignored; any other line (including `@@` exceptions, regexes, paths, cosmetic `##` filters) is counted in `Invalid`. Domains are converted with `golang.org/x/net/idna` `Lookup.ToASCII`, lowercased, trailing dot removed, and must match `^[a-z0-9_]{1}([a-z0-9_-]{0,61}[a-z0-9_])?(\.[a-z0-9_]{1}([a-z0-9_-]{0,61}[a-z0-9_])?)+$` or are counted invalid.

- [ ] Write the failing unit test `mgmt/internal/blocklist/parse_test.go`:

```go
package blocklist_test

import (
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/piwi3910/nexora/mgmt/internal/blocklist"
)

func TestParseFormats(t *testing.T) {
	in := strings.Join([]string{
		"# hosts style",
		"0.0.0.0 ads.example.com tracker.example.com",
		"127.0.0.1 localhost",
		"::1 Evil.Example.NET.",
		"[Adblock Plus 2.0]",
		"! adblock comment",
		"||adblock.example.org^",
		"||opts.example.org^$third-party",
		"@@||allowed.example.org^",
		"example.com##.banner",
		"plain.example.io",
		"bücher.example",
		"not a domain",
		"-bad-.example",
		"",
	}, "\n")
	domains, stats, err := blocklist.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ads.example.com", "tracker.example.com", "evil.example.net", "adblock.example.org", "opts.example.org", "plain.example.io", "xn--bcher-kva.example"}
	if strings.Join(domains, ",") != strings.Join(want, ",") {
		t.Fatalf("domains = %v", domains)
	}
	if stats.Entries != len(want) || stats.Invalid != 4 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestNormalizeAndCompressAreDeterministic(t *testing.T) {
	text := blocklist.Normalize([]string{"b.example", "a.example", "b.example"})
	if string(text) != "a.example\nb.example\n" {
		t.Fatalf("normalized %q", text)
	}
	d1, s1, err := blocklist.Compress(text)
	if err != nil {
		t.Fatal(err)
	}
	_, s2, _ := blocklist.Compress(text)
	if s1 != s2 || len(s1) != 64 {
		t.Fatalf("sha %s vs %s", s1, s2)
	}
	dec, _ := zstd.NewReader(nil)
	out, err := dec.DecodeAll(d1, nil)
	if err != nil || string(out) != string(text) {
		t.Fatalf("round trip: %q %v", out, err)
	}
}
```

- [ ] Write the failing acceptance test `e2e/blocklist_test.go`:

```go
package e2e

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func aRecord(t *testing.T, r *dns.Msg) net.IP {
	t.Helper()
	for _, rr := range r.Answer {
		if a, ok := rr.(*dns.A); ok {
			return a.A
		}
	}
	t.Fatalf("no A record in %v", r)
	return nil
}

func TestBlocklistSubscription(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	fx := env.StartDNSFixture()
	web := env.StartHTTPFixture()
	web.SetList(t, "hosts", "# hosts\n0.0.0.0 ads-hosts.test\n127.0.0.1 tracker-hosts.test\nthis line is invalid\n")
	web.SetList(t, "adblock", "[Adblock Plus 2.0]\n! c\n||ads-adblock.test^\n")
	web.SetList(t, "domains", "ads-domains.test\ntracker.blocked.test\n")

	api.Must("POST", "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, 201)
	ids := map[string]string{}
	for _, name := range []string{"hosts", "adblock", "domains"} {
		var fl map[string]any
		api.Must("POST", "/filter-lists", map[string]any{"name": name, "kind": "block", "url": web.URL(name), "refresh_interval_seconds": 3600, "enabled": true}, &fl, 201)
		ids[name] = fl["id"].(string)
		api.Must("POST", "/filter-lists/"+ids[name]+"/refresh", nil, &fl, 200)
		if fl["entry_count"].(float64) < 1 || fl["last_error"].(string) != "" {
			t.Fatalf("list %s not fetched: %v", name, fl)
		}
		if name == "hosts" && fl["invalid_line_count"].(float64) != 1 {
			t.Fatalf("invalid line not counted: %v", fl)
		}
	}
	var allow map[string]any
	api.Must("GET", "/allowlist", nil, &allow, 200)
	api.Must("PUT", "/allowlist", map[string]any{"domains": []string{"ok.ads-adblock.test"}, "revision": allow["revision"]}, nil, 200)

	eng := env.StartManagedEngine("engine-filter", []string{mgmt.GRPCURL}, api.CreateJoinToken())
	v := api.LatestVersion()
	api.WaitEngine("engine-filter", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })

	if ip := aRecord(t, harness.MustQuery(t, eng.DNS, "unlisted.test.", dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.ParseIP("192.0.2.1")) {
		t.Fatalf("unlisted name did not resolve normally: %v", ip)
	}
	for _, name := range []string{"ads-hosts.test.", "x.tracker-hosts.test.", "ads-adblock.test.", "deep.sub.ads-domains.test."} {
		if ip := aRecord(t, harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.IPv4zero) {
			t.Fatalf("listed name %s resolved to %v", name, ip)
		}
	}
	if ip := aRecord(t, harness.MustQuery(t, eng.DNS, "ok.ads-adblock.test.", dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.ParseIP("192.0.2.1")) {
		t.Fatalf("allowlisted name was blocked: %v", ip)
	}
	if ip := aRecord(t, harness.MustQuery(t, eng.DNS, harness.UniqueName("cloak"), dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.IPv4zero) {
		t.Fatalf("CNAME-cloaked tracker was not blocked: %v", ip)
	}
	if eng.Metric(t, "nexora_filter_blocked_total", nil) < 4 {
		t.Fatal("blocked counter did not increase")
	}

	web.SetList(t, "domains", "new-ads.test\n")
	web.SetFailing(t, "domains", true)
	var fl map[string]any
	api.Must("POST", "/filter-lists/"+ids["domains"]+"/refresh", nil, &fl, 200)
	if fl["last_error"].(string) == "" || fl["stale"] != true || fl["entry_count"].(float64) != 2 {
		t.Fatalf("failed refresh not surfaced or list emptied: %v", fl)
	}
	time.Sleep(time.Second)
	if ip := aRecord(t, harness.MustQuery(t, eng.DNS, "again.ads-domains.test.", dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.IPv4zero) {
		t.Fatalf("fetch error emptied the list: again.ads-domains.test resolved to %v", ip)
	}

	web.SetFailing(t, "domains", false)
	api.Must("POST", "/filter-lists/"+ids["domains"]+"/refresh", nil, &fl, 200)
	v2 := api.LatestVersion()
	api.WaitEngine("engine-filter", 10*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v2 })
	if ip := aRecord(t, harness.MustQuery(t, eng.DNS, "new-ads.test.", dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.IPv4zero) {
		t.Fatalf("successful refresh not applied: %v", ip)
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test -count=1 ./mgmt/internal/blocklist/...` — expect FAIL with `no required module provides package github.com/piwi3910/nexora/mgmt/internal/blocklist`; run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -run TestBlocklistSubscription ./e2e/'` — expect FAIL with `list hosts not fetched` (the refresh endpoint returns 400 `filter list fetching is not enabled on this instance`).
- [ ] Implement `parse.go` per the grammar above using a `bufio.Scanner` with a 1 MiB max token size; `Normalize` sorts and de-duplicates; `Compress` uses `zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))` `EncodeAll` and `hex(sha256(data))`.
- [ ] Implement `fetcher.go`. `refresh(ctx, id, actor)`: acquire the lock; load the row; `GET url` with a 60 s timeout, `User-Agent: nexora-mgmt`, reading at most `MaxListBytes+1` bytes (larger -> error `list exceeds 256 MiB`); non-200 -> error `http status <code>`. On error: `update filter_lists set last_attempt_at=now(), last_error=$err where id=$id` (no snapshot, previous blob kept). On success: `Parse`, `Normalize`, `Compress`; when `sha == current_blob_sha256` update `last_attempt_at`, `last_success_at`, counts, `last_error=''` without a new version; otherwise `snapshot.Mutate(ctx, st, build, actor, fn)` where `fn` inserts into `blobs ... on conflict do nothing`, updates `current_blob_sha256`, `entry_count`, `invalid_line_count`, `last_success_at`, `last_attempt_at`, `last_error=''`, `revision=revision+1`, and returns `auth.Change{Action: "refreshFilterList", TargetType: "filter_list", TargetID: id, Before: {"sha256", "entry_count"}, After: {...}}`. `RefreshNow` uses `p.Actor()`; `Run` uses `auth.Actor{Type: "system", ID: "blocklist-fetcher", Name: "system"}`. Blobs no longer referenced by any list or by the last 20 config versions are deleted hourly (`delete from blobs where sha256 not in (...)`) under `pg_try_advisory_lock(hashtext('nexora:blob-gc'))`.
- [ ] The API `FilterList.stale` field is computed in `handlers_dns.go` as `last_error <> '' or last_success_at is null or last_success_at < now() - 2 * refresh_interval_seconds * interval '1 second'`; `createFilterList` launches `go f.RefreshNow(context.WithoutCancel(ctx), principal, id)` after the mutation commits.
- [ ] Wire `serve`: `fetcher := blocklist.NewFetcher(st, build, &http.Client{})`, `go fetcher.Run(ctx)`, `Deps.RefreshFilterList = fetcher.RefreshNow`.
- [ ] Run `scripts/dev-exec.sh go test -count=1 ./mgmt/internal/blocklist/...` — expect PASS; run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -v -run TestBlocklistSubscription ./e2e/'` — expect PASS: `--- PASS: TestBlocklistSubscription`.
- [ ] Commit: `git add mgmt e2e/blocklist_test.go go.mod go.sum && git commit -m "mgmt: blocklist subscriptions with advisory-locked fetcher; e2e subscription test"`.

## Task 18: Query-log backends, fleet metrics, shared test services, and the observability acceptance tests

Files:

- `mgmt/internal/querylog/builtin.go` (create) — OTLP `LogsService` receiver on the gRPC port + in-memory ring
- `mgmt/internal/querylog/opensearch.go` (create) — OpenSearch adapter over `attributes.*`
- `mgmt/internal/querylog/builtin_test.go`, `mgmt/internal/querylog/opensearch_test.go` (create)
- `mgmt/internal/stats/collector.go` (create) — Prometheus collector for fleet stats and filter-list staleness
- `mgmt/internal/stats/collector_test.go` (create)
- `mgmt/internal/api/metrics.go` (create) — HTTP request metrics strict middleware and `/metrics` handler
- `mgmt/cmd/nexora-mgmt/main.go` (modify) — select backend, register `LogsService`, metrics registry
- `bench/dnsperf/dnsperf.go` (create) — run dnsperf and parse its output (shared by e2e and perfgate)
- `bench/dnsperf/dnsperf_test.go`, `bench/dnsperf/testdata/output.txt` (create)
- `e2e/harness/dnsperf.go` (create) — `RunDnsperf`
- `e2e/harness/external.go` (create) — `OpenSearchURL(t)`, `JaegerQueryURL(t)`, `JaegerOTLPEndpoint(t)`
- `e2e/observability_test.go` (create) — `TestObservabilityMetricsTraces`, `TestOTelSinkDownNoBackpressure`
- `deploy/kw/namespace.yaml`, `deploy/kw/opensearch.yaml` (create) — namespace `nexora` and single-node OpenSearch 3.8.0
- `deploy/dev/dev-pod.yaml` (modify) — env `NEXORA_E2E_OPENSEARCH_URL`, `NEXORA_E2E_JAEGER_QUERY_URL`

Interfaces:

- Consumes `querylog.{Backend, Query, Record, Page, ErrBackendUnavailable}`, `stats.Record`, `api.Deps` (Task 15); `control.EngineID` (Task 13); `config.{Config, OpenSearchConfig}` (Task 12); engine OTLP export (Task 9) and control client (Task 16); harness from Tasks 10 and 16.
- `querylog.NewBuiltin(capacity int) *Builtin`; `func (b *Builtin) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error)` (requires `control.EngineID(ctx)`; converts each LogRecord's attributes into a `Record`); `func (b *Builtin) Search(ctx context.Context, q Query) (Page, error)` (newest first; cursor = decimal sequence number); `Name() == "builtin"`.
- `querylog.NewOpenSearch(cfg config.OpenSearchConfig) (*OpenSearch, error)`; `Search` posts to `/<index>/_search` a `bool.filter` of `range @timestamp`, `term attributes.client.address`, `match_phrase attributes.dns.question.name`, `term attributes.dns.question.type`, `term attributes.dns.response.code`, `term attributes.nexora.cache`, `term attributes.nexora.filter`, `sort [{"@timestamp": "desc"}]`, `search_after` from the base64 JSON cursor; transport errors and HTTP >= 500 -> `ErrBackendUnavailable`; `Name() == "opensearch"`.
- `stats.NewCollector(st *store.Store) prometheus.Collector` emitting from each non-deleted engine's newest `engine_stats` row within 60 s: `nexora_fleet_qps{engine}` (from the two newest samples), `nexora_fleet_queries_total{engine}`, `nexora_fleet_query_duration_seconds{engine}` (const histogram from `duration_bucket_*`), `nexora_fleet_cache_hit_ratio{engine}`, `nexora_fleet_upstream_up{engine,upstream}`, `nexora_fleet_engines_connected`, `nexora_mgmt_config_version`, `nexora_mgmt_filter_list_stale{list}`, `nexora_mgmt_filter_list_last_success_timestamp_seconds{list}`; the `engine` label is the node name.
- `api.NewMetrics(reg *prometheus.Registry) *Metrics` with `nexora_mgmt_http_requests_total{operation,code}` and `nexora_mgmt_http_request_duration_seconds{operation}`; `func (m *Metrics) Middleware() StrictMiddlewareFunc`; `/metrics` is served unauthenticated by `promhttp.HandlerFor(reg, promhttp.HandlerOpts{})`.
- `dnsperf.Options{ Server string; Port int; Names []string; QType string; Seconds, Clients, Threads int; MaxQPS int }`; `dnsperf.Result{ QPS float64; Sent, Completed, Lost uint64; LatencyAvgSeconds float64; P99Seconds float64 }`; `func Run(ctx context.Context, o Options) (Result, error)` (writes a data file of `<name> <qtype>` lines, runs `dnsperf -s -p -d -l -c -T -O latency-histogram [-Q]`); `func Parse(out string) (Result, error)`.
- `harness.RunDnsperf(t *testing.T, server string, names []string, seconds int) dnsperf.Result`; `harness.OpenSearchURL(t) string`, `harness.JaegerQueryURL(t) string` (both `t.Fatal` with the variable name when unset); `harness.JaegerOTLPEndpoint(t) string` = host of the Jaeger query URL with port 4317.

- [ ] Capture real dnsperf 2.14 output as test data, querying the dev pod's cluster resolver at 50 QPS: `scripts/dev-exec.sh bash -c 'mkdir -p bench/dnsperf/testdata && printf "kubernetes.default.svc.cluster.local A\n" > /tmp/q.txt && dnsperf -s $(awk "/^nameserver/{print \$2; exit}" /etc/resolv.conf) -d /tmp/q.txt -l 3 -Q 50 -O latency-histogram > bench/dnsperf/testdata/output.txt'`, then copy it back with `kubectl --context kw -n nexora-dev exec deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - bench/dnsperf/testdata | tar -xf -`. The parser below is written against this file.
- [ ] Write the failing test `bench/dnsperf/dnsperf_test.go`:

```go
package dnsperf_test

import (
	"os"
	"testing"

	"github.com/piwi3910/nexora/bench/dnsperf"
)

func TestParseRealOutput(t *testing.T) {
	raw, err := os.ReadFile("testdata/output.txt")
	if err != nil {
		t.Fatal(err)
	}
	r, err := dnsperf.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if r.QPS <= 0 || r.Completed == 0 || r.Sent < r.Completed {
		t.Fatalf("parsed %+v", r)
	}
	if r.P99Seconds <= 0 || r.P99Seconds < r.LatencyAvgSeconds/10 {
		t.Fatalf("p99 not parsed from the latency histogram: %+v", r)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := dnsperf.Parse("nothing useful"); err == nil {
		t.Fatal("garbage accepted")
	}
}
```

- [ ] Write the failing tests `mgmt/internal/querylog/builtin_test.go` and `opensearch_test.go`:

```go
package querylog_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func request(names ...string) *collogspb.ExportLogsServiceRequest {
	var recs []*logspb.LogRecord
	for i, n := range names {
		recs = append(recs, &logspb.LogRecord{TimeUnixNano: uint64(1_700_000_000_000_000_000 + i), Attributes: []*commonpb.KeyValue{
			str("client.address", "10.0.0.1"), str("dns.question.name", n), str("dns.question.type", "A"), str("dns.response.code", "NOERROR"),
			str("nexora.cache", "miss"), str("nexora.filter", "none"), str("nexora.upstream", "fx"), str("nexora.transport", "udp"),
			str("nexora.engine.id", "e1"), {Key: "nexora.duration_us", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 420}}},
		}})
	}
	return &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}}}}}
}

func TestBuiltinRingSearchAndCapacity(t *testing.T) {
	b := querylog.NewBuiltin(3)
	b.Ingest("e1", request("a.example.", "b.example.", "c.example.", "d.example."))
	page, err := b.Search(context.Background(), querylog.Query{Limit: 10})
	if err != nil || len(page.Records) != 3 || page.Records[0].Name != "d.example." {
		t.Fatalf("ring: %+v %v", page, err)
	}
	page, _ = b.Search(context.Background(), querylog.Query{Name: "b.example", Limit: 10})
	if len(page.Records) != 1 || page.Records[0].DurationUS != 420 || page.Records[0].Client != "10.0.0.1" {
		t.Fatalf("filter by name: %+v", page)
	}
	first, _ := b.Search(context.Background(), querylog.Query{Limit: 2})
	second, _ := b.Search(context.Background(), querylog.Query{Limit: 2, Cursor: first.NextCursor})
	if len(first.Records) != 2 || len(second.Records) != 1 {
		t.Fatalf("paging: %d then %d", len(first.Records), len(second.Records))
	}
}

func TestOpenSearchQueriesAttributesAndReportsUnavailable(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if !strings.HasSuffix(r.URL.Path, "/_search") {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":{"hits":[{"_source":{"@timestamp":"2026-09-13T10:00:00Z","attributes":{"client.address":"10.0.0.9","dns.question.name":"q.example.","dns.question.type":"A","dns.response.code":"NOERROR","nexora.cache":"hit","nexora.filter":"none","nexora.upstream":"","nexora.transport":"udp","nexora.engine.id":"e1","nexora.duration_us":77}},"sort":[1]}]}}`)
	}))
	defer srv.Close()
	os, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: srv.URL, Index: "nexora-querylog-*"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.Search(context.Background(), querylog.Query{Name: "q.example", Limit: 5})
	if err != nil || len(page.Records) != 1 || page.Records[0].Cache != "hit" || page.Records[0].DurationUS != 77 {
		t.Fatalf("search: %+v %v", page, err)
	}
	if !strings.Contains(body, "attributes.dns.question.name") {
		t.Fatalf("query does not use attributes.*: %s", body)
	}
	srv.Close()
	if _, err := os.Search(context.Background(), querylog.Query{Limit: 5}); !errors.Is(err, querylog.ErrBackendUnavailable) {
		t.Fatalf("down backend -> %v", err)
	}
}
```

(`Ingest(engineID string, req *collogspb.ExportLogsServiceRequest)` is the method `Export` calls after authenticating.)

- [ ] Write the failing test `mgmt/internal/stats/collector_test.go` (Postgres via harness): insert a `users`-free engine row (`insert into engines(node_name, certificate_serial, connected_instance) ...` after inserting an `instances` row), call `stats.Record` twice 10 s apart (`Stats.UnixMs` values differing by 10 000, `QueriesTotal` 1000 then 3000, one upstream `{Name: "fx", Up: true}`, 15 buckets), then `prometheus.NewPedanticRegistry()`, `reg.MustRegister(stats.NewCollector(st))`, `reg.Gather()` and assert families `nexora_fleet_qps` (value 200 for engine label), `nexora_fleet_query_duration_seconds`, `nexora_fleet_cache_hit_ratio`, `nexora_fleet_upstream_up` exist:

```go
func TestCollectorExportsFleetMetrics(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ctx := context.Background()
	st, _ := store.Open(ctx, pg.URL)
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = st.Pool.Exec(ctx, "insert into instances(id) values ('i1')")
	var id string
	if err := st.Pool.QueryRow(ctx, "insert into engines(node_name, certificate_serial, connected_instance) values ('edge-1','1','i1') returning id").Scan(&id); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	bounds := []uint64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000, 250000, 500000, 1000000, 2000000}
	counts := make([]uint64, 15)
	for i := range counts {
		counts[i] = uint64(i+1) * 100
	}
	for i, total := range []uint64{1000, 3000} {
		s := &controlv1.Stats{UnixMs: now - int64(10000*(1-i)), QueriesTotal: total, CacheHitsTotal: total / 2, CacheMissesTotal: total / 2,
			DurationBucketBoundsUs: bounds, DurationBucketCounts: counts, DurationSumUs: 5000,
			Upstreams: []*controlv1.UpstreamStatus{{Id: "u", Name: "fx", Up: true, RttUs: 900}}}
		if err := stats.Record(ctx, st, id, s); err != nil {
			t.Fatal(err)
		}
	}
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(stats.NewCollector(st))
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*dto.MetricFamily{}
	for _, f := range fams {
		got[f.GetName()] = f
	}
	for _, n := range []string{"nexora_fleet_qps", "nexora_fleet_queries_total", "nexora_fleet_query_duration_seconds", "nexora_fleet_cache_hit_ratio", "nexora_fleet_upstream_up", "nexora_fleet_engines_connected"} {
		if got[n] == nil {
			t.Errorf("missing %s", n)
		}
	}
	if q := got["nexora_fleet_qps"].GetMetric()[0].GetGauge().GetValue(); q != 200 {
		t.Fatalf("qps = %v", q)
	}
}
```

(imports: `context`, `testing`, `time`, `github.com/prometheus/client_golang/prometheus`, `dto "github.com/prometheus/client_model/go"`, harness, controlv1, `stats`, `store`).

- [ ] Write the failing acceptance tests `e2e/observability_test.go`:

```go
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func scrape(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestObservabilityMetricsTraces(t *testing.T) {
	env := harness.New(t)
	col := env.StartOtelcol(harness.OtelcolConfig{JaegerOTLP: harness.JaegerOTLPEndpoint(t), DebugFile: env.Dir + "/otel.jsonl"})
	pg := env.StartPostgres()
	ca := env.InitCA()
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{OTLPEndpoint: "http://" + col.OTLPGRPC})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	fx := env.StartDNSFixture()
	api.Must("POST", "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, 201)
	eng := env.StartManagedEngine("engine-obs", []string{mgmt.GRPCURL}, api.CreateJoinToken())
	v := api.LatestVersion()
	api.WaitEngine("engine-obs", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })

	name := harness.UniqueName("obs")
	harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
	harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
	body := scrape(t, "http://"+eng.Metrics+"/metrics")
	for _, m := range []string{"nexora_queries_total{", "nexora_query_duration_seconds_bucket{", "nexora_cache_hits_total", "nexora_cache_misses_total", `nexora_upstream_up{upstream="fixture"} 1`} {
		if !strings.Contains(body, m) {
			t.Errorf("engine /metrics missing %s", m)
		}
	}
	harness.Eventually(t, 30*time.Second, func() error {
		b := scrape(t, mgmt.BaseURL+"/metrics")
		for _, m := range []string{`nexora_fleet_qps{engine="engine-obs"}`, "nexora_fleet_query_duration_seconds_bucket{", `nexora_fleet_cache_hit_ratio{engine="engine-obs"}`, `nexora_fleet_upstream_up{engine="engine-obs",upstream="fixture"} 1`} {
			if !strings.Contains(b, m) {
				return fmt.Errorf("management /metrics missing %s", m)
			}
		}
		return nil
	})

	fx.SetMode(t, "servfail")
	bad := harness.UniqueName("servfail")
	if r := harness.MustQuery(t, eng.DNS, bad, dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeServerFailure {
		t.Fatalf("expected SERVFAIL, got %s", dns.RcodeToString[r.Rcode])
	}
	jaeger := harness.JaegerQueryURL(t)
	tags, _ := json.Marshal(map[string]string{"dns.question.name": strings.ToLower(bad)})
	harness.Eventually(t, 45*time.Second, func() error {
		u := jaeger + "/api/traces?service=nexora-engine&lookback=1h&limit=20&tags=" + url.QueryEscape(string(tags))
		resp, err := http.Get(u)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var out struct {
			Data []struct {
				Spans []struct {
					OperationName string `json:"operationName"`
				} `json:"spans"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return err
		}
		for _, tr := range out.Data {
			for _, s := range tr.Spans {
				if s.OperationName == "dns.query" {
					return nil
				}
			}
		}
		return fmt.Errorf("no dns.query trace for %s yet", bad)
	})
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func TestOTelSinkDownNoBackpressure(t *testing.T) {
	env := harness.New(t)
	debug := env.Dir + "/otel.jsonl"
	col := env.StartOtelcol(harness.OtelcolConfig{DebugFile: debug})
	fx := env.StartDNSFixture()
	snap := harness.BaseSnapshot(1, harness.UDPUpstream("fx", fx.UDP))
	snap.Telemetry.OtlpEndpoint = "http://" + col.OTLPGRPC
	snap.Telemetry.TraceSampleOneIn = 1000
	eng := env.StartStandaloneEngine(snap, nil)

	names := make([]string, 200)
	for i := range names {
		names[i] = fmt.Sprintf("load-%03d.example.", i)
		harness.MustQuery(t, eng.DNS, names[i], dns.TypeA, harness.QueryOpts{})
	}
	harness.Eventually(t, 15*time.Second, func() error {
		if fi, err := os.Stat(debug); err != nil || fi.Size() == 0 {
			return fmt.Errorf("collector has not received logs yet")
		}
		return nil
	})

	var up, down []float64
	for round := 0; round < 3; round++ {
		up = append(up, harness.RunDnsperf(t, eng.DNS, names, 8).QPS)
		dropsBefore := eng.Metric(t, "nexora_export_dropped_total", map[string]string{"signal": "logs"})
		col.Stop()
		down = append(down, harness.RunDnsperf(t, eng.DNS, names, 8).QPS)
		harness.Eventually(t, 10*time.Second, func() error {
			if d := eng.Metric(t, "nexora_export_dropped_total", map[string]string{"signal": "logs"}); d <= dropsBefore {
				return fmt.Errorf("drop counter stayed at %v with the collector down", d)
			}
			return nil
		})
		col.Restart(env)
	}
	base, sinkDown := median(up), median(down)
	t.Logf("median QPS collector up=%.0f down=%.0f", base, sinkDown)
	if base <= 0 {
		t.Fatal("baseline QPS is zero")
	}
	if sinkDown < 0.95*base {
		t.Fatalf("QPS dropped %.1f%% with the collector stopped", 100*(1-sinkDown/base))
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test -count=1 ./bench/dnsperf/... ./mgmt/internal/querylog/... ./mgmt/internal/stats/...` — expect FAIL with `undefined: dnsperf.Parse`, `undefined: querylog.NewBuiltin`, `undefined: stats.NewCollector`.
- [ ] Write `deploy/kw/namespace.yaml` (`kind: Namespace`, name `nexora`) and `deploy/kw/opensearch.yaml`:

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  {
    name: opensearch,
    namespace: nexora,
    labels: { app.kubernetes.io/name: opensearch },
  }
spec:
  serviceName: opensearch
  replicas: 1
  selector: { matchLabels: { app.kubernetes.io/name: opensearch } }
  template:
    metadata: { labels: { app.kubernetes.io/name: opensearch } }
    spec:
      initContainers:
        - name: sysctl
          image: busybox:1.37
          command: ["sysctl", "-w", "vm.max_map_count=262144"]
          securityContext: { privileged: true }
      containers:
        - name: opensearch
          image: opensearchproject/opensearch:3.8.0
          env:
            - { name: discovery.type, value: single-node }
            - { name: DISABLE_SECURITY_PLUGIN, value: "true" }
            - { name: DISABLE_INSTALL_DEMO_CONFIG, value: "true" }
            - { name: OPENSEARCH_JAVA_OPTS, value: "-Xms1g -Xmx1g" }
          ports: [{ name: http, containerPort: 9200 }]
          readinessProbe:
            {
              httpGet: { path: /_cluster/health, port: 9200 },
              periodSeconds: 10,
            }
          resources:
            requests: { cpu: 500m, memory: 2Gi }
            limits: { cpu: "2", memory: 3Gi }
          volumeMounts: [{ name: data, mountPath: /usr/share/opensearch/data }]
  volumeClaimTemplates:
    - metadata: { name: data }
      spec:
        {
          accessModes: [ReadWriteOnce],
          storageClassName: longhorn-single,
          resources: { requests: { storage: 20Gi } },
        }
---
apiVersion: v1
kind: Service
metadata: { name: opensearch, namespace: nexora }
spec:
  selector: { app.kubernetes.io/name: opensearch }
  ports: [{ name: http, port: 9200, targetPort: 9200 }]
```

- [ ] Apply and wire the shared services: `kubectl --context kw apply -f deploy/kw/namespace.yaml -f deploy/kw/opensearch.yaml && kubectl --context kw -n nexora rollout status statefulset/opensearch --timeout=10m`; find the Jaeger query service with `kubectl --context kw -n observability get svc -o wide` and set, in `deploy/dev/dev-pod.yaml` container `env`, `NEXORA_E2E_OPENSEARCH_URL=http://opensearch.nexora.svc.cluster.local:9200` and `NEXORA_E2E_JAEGER_QUERY_URL=http://<jaeger query service>.observability.svc.cluster.local:16686` (the service exposing port 16686; `harness.JaegerOTLPEndpoint` uses the same host on port 4317, so pick the Jaeger service that exposes both ports, as the all-in-one and collector-with-query services do), then `kubectl --context kw apply -f deploy/dev/dev-pod.yaml && kubectl --context kw -n nexora-dev rollout status deploy/toolbox` and verify `scripts/dev-exec.sh bash -c 'curl -fsS $NEXORA_E2E_OPENSEARCH_URL/_cluster/health && curl -fsS $NEXORA_E2E_JAEGER_QUERY_URL/api/services && h=${NEXORA_E2E_JAEGER_QUERY_URL#http://}; bash -c "</dev/tcp/${h%%:*}/4317"'` — expect JSON from both and a successful TCP connect to port 4317.
- [ ] Implement `bench/dnsperf/dnsperf.go`: `Parse` reads `Queries sent:`, `Queries completed:`, `Queries lost:`, `Queries per second:`, `Average Latency (s):` and the latency histogram lines in the captured `testdata/output.txt` format, computing `P99Seconds` as the upper bound of the first bucket whose cumulative count reaches 99 % of completed queries; missing `Queries per second:` -> error `dnsperf output has no QPS line`. `Run` defaults `Clients=16`, `Threads=4`, `QType="A"`.
- [ ] Implement `builtin.go` (ring of `Record` with a monotonically increasing sequence, `sync.RWMutex`; `Export` -> `control.EngineID` then `Ingest`; filters are case-insensitive substring for `Name`, exact for others), `opensearch.go` (`opensearchapi.NewClient(opensearchapi.Config{Client: opensearch.Config{Addresses: []string{cfg.URL}, Username, Password (from PasswordFile)}})`, 5 s request timeout), `stats/collector.go` (`prometheus.MustNewConstMetric`, `prometheus.MustNewConstHistogram` with bucket bounds converted to seconds), and `api/metrics.go`.
- [ ] Wire `serve`: `reg := prometheus.NewRegistry()`, register `collectors.NewGoCollector()`, `collectors.NewProcessCollector(...)`, `stats.NewCollector(st)`, `api.NewMetrics(reg)`; query-log backend `builtin` -> `b := querylog.NewBuiltin(cfg.QueryLogBuiltinCapacity)` and `collogspb.RegisterLogsServiceServer(grpcServer, b)`; `opensearch` -> `querylog.NewOpenSearch(cfg.OpenSearch)`; `Deps.QueryLog` and `Deps.Metrics = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})`.
- [ ] Implement `e2e/harness/dnsperf.go` (calls `dnsperf.Run` with `Seconds`, splitting `server` into host and port, `t.Fatal` on error) and `e2e/harness/external.go`.
- [ ] Run `scripts/dev-exec.sh go test -count=1 ./bench/dnsperf/... ./mgmt/internal/querylog/... ./mgmt/internal/stats/...` — expect PASS: three `ok` lines.
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -v -run "TestObservabilityMetricsTraces|TestOTelSinkDownNoBackpressure" ./e2e/'` — expect PASS: `--- PASS: TestObservabilityMetricsTraces` and `--- PASS: TestOTelSinkDownNoBackpressure` (the log line shows both medians).
- [ ] Commit: `git add mgmt bench e2e deploy/kw/namespace.yaml deploy/kw/opensearch.yaml deploy/dev/dev-pod.yaml go.mod go.sum && git commit -m "mgmt: builtin/OpenSearch query-log backends and fleet metrics; e2e observability tests"`.

## Task 19: GUI foundation, auth screens, Playwright harness and `TestAuthRBACAuditOIDC`

Files:

- `web/package.json`, `web/pnpm-lock.yaml`, `web/tsconfig.json`, `web/vite.config.ts`, `web/index.html`, `web/eslint.config.js` (create)
- `web/src/main.tsx`, `web/src/app/router.tsx`, `web/src/index.css`, `web/src/lib/utils.ts` (create)
- `web/src/api/schema.d.ts` (create, generated by `pnpm run gen:api`, committed), `web/src/api/client.ts` (create) — openapi-fetch client and `ApiError`
- `web/src/auth/AuthProvider.tsx` (create) — current user query, `RequireAuth`, `useCan(operationId)`
- `web/src/auth/permissions.ts` (create) — operationId -> minimum role, mirroring `mgmt/internal/auth/permissions.go`
- `web/src/components/ui/{alert,badge,button,card,dialog,input,label,select,separator,switch,table,tabs,textarea,tooltip}.tsx` (create, ported from `~/Development/nexora-reference/Nexora/components/ui`)
- `web/src/components/layout/AppShell.tsx` (create) — sidebar navigation, health badge, user menu
- `web/src/pages/LoginPage.tsx`, `web/src/pages/SetupPage.tsx`, `web/src/pages/UpstreamsPage.tsx`, `web/src/pages/AuditPage.tsx` (create)
- `web/playwright.config.ts`, `web/e2e/fixtures.ts`, `web/e2e/auth.spec.ts`, `web/e2e/auth-oidc-down.spec.ts` (create)
- `web/scripts/check-permissions.mjs` (create) — fails when `permissions.ts` and `permissions.go` differ
- `e2e/harness/playwright.go` (create) — `RunPlaywright`
- `e2e/auth_test.go` (create) — `TestAuthRBACAuditOIDC`

Interfaces:

- Consumes the HTTP API from Task 15 (operationIds listed in Task 14), session cookie `nexora_session`, OIDC routes `/api/v1/auth/oidc/start` and `/api/v1/auth/oidc/callback`; harness `StartMgmt`, `MgmtOptions.OIDC`, `StartOIDCFixture`, `Bootstrap`, `NewAPI` (Tasks 10, 16).
- `web/src/api/client.ts`: `export const api = createClient<paths>({ baseUrl: "/api/v1", credentials: "same-origin", headers: { "Content-Type": "application/json" } })`; `export class ApiError extends Error { status: number; code: string }`; `export function unwrap<T>(r: { data?: T; error?: { code: string; message: string }; response: Response }): T`.
- `web/src/auth/AuthProvider.tsx`: `export function useCurrentUser(): { user: components["schemas"]["User"] | null; loading: boolean }`; `export function RequireAuth({ children }: { children: React.ReactNode })` (redirects to `/setup` when `getSetupStatus.required`, else `/login?return_to=<path>` on 401); `export function useCan(operationId: OperationId): boolean`.
- Routes (react-router 7 data router): `/login`, `/setup`, and inside `AppShell`: `/` (dashboard), `/query-log`, `/upstreams`, `/access-control`, `/filtering`, `/engines`, `/users`, `/api-tokens`, `/audit`, `/settings`. This task implements `/login`, `/setup`, `/upstreams`, `/audit`; Task 20 implements the rest (until then `router.tsx` lists only the four routes plus `/` redirecting to `/upstreams`).
- Test ids used by Playwright specs (must exist exactly): `nav-<route>` links (`nav-upstreams`, `nav-audit`, `nav-dashboard`, `nav-query-log`, `nav-access-control`, `nav-filtering`, `nav-engines`, `nav-users`, `nav-api-tokens`, `nav-settings`), `user-menu`, `logout`, `login-username`, `login-password`, `login-submit`, `login-oidc`, `login-error`, `setup-token`, `setup-username`, `setup-email`, `setup-password`, `setup-submit`, `upstream-add`, `upstream-name`, `upstream-protocol`, `upstream-address`, `upstream-doh-url`, `upstream-tls-name`, `upstream-timeout`, `upstream-save`, `upstream-row-<name>`, `upstream-edit-<name>`, `upstream-delete-<name>`, `confirm-delete`, `audit-row` (one per event, with `data-action` and `data-actor` attributes), `audit-diff-<id>`, `health-badge`.
- `harness.RunPlaywright(t *testing.T, specs []string, env map[string]string) string` — runs `pnpm exec playwright test <specs...>` in `<repo>/web` with `env` plus `NEXORA_E2E_COVERAGE_DIR` (taken from `env` when present, otherwise a new temp dir), streams output to `<Dir>/playwright.log`, `t.Fatal`s on non-zero exit, returns the coverage directory.

- [ ] Write `web/package.json`:

```json
{
  "name": "nexora-web",
  "private": true,
  "type": "module",
  "engines": { "node": ">=26" },
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "typecheck": "tsc -b --noEmit",
    "lint": "eslint . && node scripts/check-permissions.mjs",
    "gen:api": "openapi-typescript ../mgmt/api/openapi.yaml -o src/api/schema.d.ts",
    "e2e": "playwright test"
  },
  "dependencies": {
    "@radix-ui/react-dialog": "^1.1.14",
    "@radix-ui/react-label": "^2.1.7",
    "@radix-ui/react-select": "^2.2.5",
    "@radix-ui/react-separator": "^1.1.7",
    "@radix-ui/react-switch": "^1.2.5",
    "@radix-ui/react-tabs": "^1.1.12",
    "@radix-ui/react-tooltip": "^1.2.7",
    "@tanstack/react-query": "~5.102.0",
    "class-variance-authority": "^0.7.1",
    "clsx": "^2.1.1",
    "lucide-react": "~1.45.0",
    "openapi-fetch": "~0.17.0",
    "react": "~19.3.0",
    "react-dom": "~19.3.0",
    "react-router": "~7.13.0",
    "recharts": "~3.10.0",
    "tailwind-merge": "^3.3.1"
  },
  "devDependencies": {
    "@eslint/js": "^9.0.0",
    "@playwright/test": "~1.63.0",
    "@tailwindcss/vite": "~4.3.0",
    "@types/node": "^24.0.0",
    "@types/react": "~19.3.0",
    "@types/react-dom": "~19.3.0",
    "@vitejs/plugin-react": "^5.0.0",
    "eslint": "^9.0.0",
    "eslint-plugin-react-hooks": "^6.0.0",
    "openapi-typescript": "~7.13.0",
    "tailwindcss": "~4.3.0",
    "typescript": "^5.9.0",
    "typescript-eslint": "^8.0.0",
    "vite": "~8.3.0",
    "yaml": "^2.8.0"
  }
}
```

- [ ] Write `web/vite.config.ts` and `web/playwright.config.ts`:

```ts
// web/vite.config.ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath } from "node:url";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: { proxy: { "/api": "http://127.0.0.1:8080" } },
  build: { outDir: "dist", sourcemap: false },
});
```

```ts
// web/playwright.config.ts
import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: [["list"]],
  use: {
    baseURL: process.env.NEXORA_E2E_BASE_URL,
    trace: "retain-on-failure",
    ...devices["Desktop Chrome"],
  },
});
```

- [ ] Write `web/e2e/fixtures.ts` (request recorder used for coverage, and login helpers):

```ts
import { test as base, expect, type Page } from "@playwright/test";
import { appendFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";

const coverageDir = process.env.NEXORA_E2E_COVERAGE_DIR;

export const test = base.extend<{ page: Page }>({
  page: async ({ page }, use, testInfo) => {
    if (coverageDir) {
      mkdirSync(coverageDir, { recursive: true });
      const file = join(coverageDir, `requests-${testInfo.workerIndex}.jsonl`);
      page.on("request", (req) => {
        const url = new URL(req.url());
        if (url.pathname.startsWith("/api/v1/")) {
          appendFileSync(
            file,
            JSON.stringify({
              method: req.method(),
              path: url.pathname.slice("/api/v1".length),
              test: testInfo.titlePath.join(" > "),
            }) + "\n",
          );
        }
      });
    }
    await use(page);
  },
});

export { expect };

export function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`missing environment variable ${name}`);
  return v;
}

export async function login(page: Page, username: string, password: string) {
  await page.goto("/login");
  await page.getByTestId("login-username").fill(username);
  await page.getByTestId("login-password").fill(password);
  await page.getByTestId("login-submit").click();
  await expect(page.getByTestId("user-menu")).toContainText(username);
}

export async function logout(page: Page) {
  await page.getByTestId("user-menu").click();
  await page.getByTestId("logout").click();
  await expect(page).toHaveURL(/\/login/);
}
```

- [ ] Write the failing spec `web/e2e/auth.spec.ts`:

```ts
import { test, expect, env, login, logout } from "./fixtures";

test("viewer cannot change config, operator can, audit shows actor and diff", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.getByTestId("nav-upstreams").click();
  await expect(page.getByTestId("upstream-row-seed")).toBeVisible();
  await expect(page.getByTestId("upstream-add")).toHaveCount(0);
  await expect(page.getByTestId("upstream-edit-seed")).toHaveCount(0);
  await expect(page.getByTestId("nav-audit")).toHaveCount(0);
  const denied = await page.evaluate(async () => {
    const r = await fetch("/api/v1/upstreams", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: "viewer-write",
        protocol: "udp",
        address: "192.0.2.53:53",
        timeout_ms: 250,
        enabled: true,
        position: 9,
      }),
    });
    return r.status;
  });
  expect(denied).toBe(403);
  await logout(page);

  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-upstreams").click();
  await page.getByTestId("upstream-add").click();
  await page.getByTestId("upstream-name").fill("operator-added");
  await page.getByTestId("upstream-address").fill("192.0.2.54:53");
  await page.getByTestId("upstream-save").click();
  await expect(page.getByTestId("upstream-row-operator-added")).toBeVisible();
  await page.getByTestId("upstream-edit-operator-added").click();
  await page.getByTestId("upstream-timeout").fill("400");
  await page.getByTestId("upstream-save").click();
  await expect(page.getByTestId("upstream-row-operator-added")).toContainText(
    "400",
  );
  await logout(page);

  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-audit").click();
  const row = page
    .locator('[data-testid="audit-row"][data-action="updateUpstream"]')
    .filter({ hasText: env("NEXORA_E2E_OPERATOR_USER") })
    .first();
  await expect(row).toBeVisible();
  await row.click();
  await expect(
    page.locator('[data-testid^="audit-diff-"]').first(),
  ).toContainText("400");
  await logout(page);
});

test("OIDC login works", async ({ page }) => {
  await page.goto("/login");
  await page.getByTestId("login-oidc").click();
  await page
    .getByRole("button", { name: `Sign in as ${env("NEXORA_E2E_OIDC_USER")}` })
    .click();
  await expect(page.getByTestId("user-menu")).toContainText(
    env("NEXORA_E2E_OIDC_USER"),
  );
  await page.getByTestId("nav-audit").click();
  await expect(page.getByTestId("audit-row").first()).toBeVisible();
  await logout(page);
});
```

- [ ] Write the failing spec `web/e2e/auth-oidc-down.spec.ts`:

```ts
import { test, expect, env, login, logout } from "./fixtures";

test("local login still works while the OIDC provider is down", async ({
  page,
}) => {
  await page.goto("/login");
  await page.getByTestId("login-oidc").click();
  await expect(page.getByTestId("login-error")).toContainText(
    "identity provider unavailable",
  );
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await logout(page);
});
```

- [ ] Write the failing Go wrapper `e2e/auth_test.go`:

```go
package e2e

import (
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestAuthRBACAuditOIDC(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	oidc := env.StartOIDCFixture(harness.OIDCUser{Username: "ada", Email: "ada@example.test", Groups: []string{"nexora-admins"}})
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{OIDC: oidc, OIDCAdminGroup: "nexora-admins", OIDCOperatorGroup: "nexora-operators"})
	admin := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)

	for _, u := range []map[string]any{
		{"username": "vera", "email": "vera@example.test", "password": "viewer-password-e2e", "role": "viewer"},
		{"username": "otto", "email": "otto@example.test", "password": "operator-password-e2e", "role": "operator"},
	} {
		admin.Must("POST", "/users", u, nil, 201)
	}
	admin.Must("POST", "/upstreams", map[string]any{"name": "seed", "protocol": "udp", "address": "192.0.2.1:53", "timeout_ms": 250, "enabled": true, "position": 0}, nil, 201)

	// Go API checks: viewer write is 403, operator write succeeds, every change is audited.
	viewer := env.NewAPI(mgmt.BaseURL)
	viewer.Must("POST", "/auth/login", map[string]string{"username": "vera", "password": "viewer-password-e2e"}, nil, 200)
	viewer.Must("GET", "/upstreams", nil, nil, 200)
	viewer.Must("POST", "/upstreams", map[string]any{"name": "nope", "protocol": "udp", "address": "192.0.2.2:53", "timeout_ms": 250, "enabled": true, "position": 1}, nil, 403)
	operator := env.NewAPI(mgmt.BaseURL)
	operator.Must("POST", "/auth/login", map[string]string{"username": "otto", "password": "operator-password-e2e"}, nil, 200)
	var created map[string]any
	operator.Must("POST", "/upstreams", map[string]any{"name": "api-op", "protocol": "udp", "address": "192.0.2.3:53", "timeout_ms": 250, "enabled": true, "position": 2}, &created, 201)
	var audit []map[string]any
	admin.Must("GET", "/audit?limit=500", nil, &audit, 200)
	found := false
	for _, a := range audit {
		if a["action"] == "createUpstream" && a["actor_name"] == "otto" && a["diff"] != nil {
			found = true
		}
		if a["actor_name"] == "vera" {
			t.Fatalf("viewer produced an audit entry: %v", a)
		}
	}
	if !found {
		t.Fatal("operator change has no audit entry with actor and diff")
	}
	var versions []map[string]any
	admin.Must("GET", "/config-versions", nil, &versions, 200)
	audited := map[float64]bool{}
	for _, a := range audit {
		if v, ok := a["config_version"].(float64); ok {
			audited[v] = true
		}
	}
	for _, v := range versions {
		if !audited[v["version"].(float64)] {
			t.Fatalf("config version %v has no audit entry", v["version"])
		}
	}

	pw := map[string]string{
		"NEXORA_E2E_BASE_URL":          mgmt.BaseURL,
		"NEXORA_E2E_ADMIN_USER":        "admin",
		"NEXORA_E2E_ADMIN_PASSWORD":    "admin-password-e2e",
		"NEXORA_E2E_VIEWER_USER":       "vera",
		"NEXORA_E2E_VIEWER_PASSWORD":   "viewer-password-e2e",
		"NEXORA_E2E_OPERATOR_USER":     "otto",
		"NEXORA_E2E_OPERATOR_PASSWORD": "operator-password-e2e",
		"NEXORA_E2E_OIDC_USER":         "ada",
	}
	harness.RunPlaywright(t, []string{"e2e/auth.spec.ts"}, pw)

	oidc.Proc.Kill()
	time.Sleep(500 * time.Millisecond)
	harness.RunPlaywright(t, []string{"e2e/auth-oidc-down.spec.ts"}, pw)
}
```

- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -run TestAuthRBACAuditOIDC ./e2e/'` — expect FAIL with `undefined: harness.RunPlaywright`.
- [ ] Scaffold and install: write `web/tsconfig.json` (`"strict": true`, `"jsx": "react-jsx"`, `"moduleResolution": "bundler"`, `"paths": {"@/*": ["./src/*"]}`, `"types": ["node"]`), `web/index.html` (`<div id="root">`, `<script type="module" src="/src/main.tsx">`), `web/src/index.css` (`@import "tailwindcss";` plus the CSS variables `--background`, `--foreground`, `--primary`, `--primary-foreground`, `--destructive`, `--destructive-foreground`, `--border`, `--input`, `--ring`, `--accent`, `--accent-foreground`, `--muted`, `--muted-foreground` mapped via `@theme inline` so the ported component classes such as `bg-primary` resolve), `web/eslint.config.js` (`@eslint/js` recommended + `typescript-eslint` recommended + `react-hooks`), then run `scripts/dev-exec.sh bash -c 'cd web && pnpm install && pnpm run gen:api'` and copy `web/pnpm-lock.yaml` and `web/src/api/schema.d.ts` back with `kubectl --context kw -n nexora-dev exec deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - web/pnpm-lock.yaml web/src/api/schema.d.ts | tar -xf -`.
- [ ] Port the UI components: copy `alert, badge, button, card, dialog, input, label, select, separator, switch, table, tabs, textarea, tooltip` from `~/Development/nexora-reference/Nexora/components/ui/*.tsx` into `web/src/components/ui/`, removing any `"use client"` directives and keeping the `@/lib/utils` import; write `web/src/lib/utils.ts` as `export function cn(...inputs: ClassValue[]) { return twMerge(clsx(inputs)); }`.
- [ ] Write `web/src/auth/permissions.ts` exporting `export type Role = "viewer" | "operator" | "admin"` and `export const permissions: Record<string, Role | "public">` with exactly the operationIds and roles listed in Task 14, and `web/scripts/check-permissions.mjs` that parses `mgmt/internal/auth/permissions.go` with the regex `/"(\w+)":\s*Role(Viewer|Operator|Admin)/g` and the `Public` map with `/"(\w+)":\s*true/g`, reads `web/src/auth/permissions.ts` as text with `/(\w+):\s*"(viewer|operator|admin|public)"/g`, compares the two maps in both directions, and exits 1 listing every mismatch.
- [ ] Implement `client.ts`, `AuthProvider.tsx` (`useQuery({ queryKey: ["me"], queryFn: () => api.GET("/auth/me") })`; `useCan(op)` compares the user's role with `permissions[op]` using viewer < operator < admin), `router.tsx` (`createBrowserRouter`), `main.tsx` (`QueryClientProvider` with `retry: (n, err) => !(err instanceof ApiError && err.status < 500) && n < 2`), `AppShell.tsx` (sidebar links carrying `data-testid="nav-<route>"` rendered only when `useCan` of that screen's list operation holds — `listUsers`, `listApiTokens`, `listAuditEvents`, `listJoinTokens` gate users, API tokens, audit and the join-token panel; a `health-badge` fed by `getHealth` every 15 s showing `ok` or `degraded`; `user-menu` showing the username with a `logout` item calling `POST /auth/logout`).
- [ ] Implement `LoginPage.tsx`: username/password form (`login-*` test ids) posting `/auth/login` then navigating to `return_to` or `/`; `listAuthProviders` decides whether `login-oidc` renders; clicking it first calls `fetch("/api/v1/auth/oidc/start?return_to=/", { redirect: "manual" })` — an `opaqueredirect` response navigates `window.location` to the same URL, a 503 shows `login-error` with the API message (`identity provider unavailable`); errors from login show `login-error` with `Invalid username or password`.
- [ ] Implement `SetupPage.tsx`: `setup-token`, `setup-username`, `setup-email`, `setup-password` fields posting `completeSetup`, then navigating to `/`; `/setup` redirects to `/login` when `getSetupStatus.required` is false.
- [ ] Implement `UpstreamsPage.tsx`: table of `listUpstreams` ordered by position with rows `upstream-row-<name>` showing protocol, address/URL, timeout and enabled; `upstream-add` and per-row `upstream-edit-<name>`/`upstream-delete-<name>` rendered only when `useCan("createUpstream")`/`useCan("updateUpstream")`/`useCan("deleteUpstream")`; a dialog form with `upstream-name`, `upstream-protocol` (select udp/tcp/dot/doh), `upstream-address` (udp/tcp/dot), `upstream-tls-name` (dot), `upstream-doh-url` (doh), `upstream-timeout` (default 250), `upstream-save`; create posts `UpstreamInput` with `position = rows.length`, edit PUTs with the row's `revision`; a 409 shows `This upstream was changed by someone else — reload to see the latest version`; delete opens a confirm dialog with `confirm-delete` and sends `revision`.
- [ ] Implement `AuditPage.tsx`: `listAuditEvents` newest first, each row `data-testid="audit-row"` with `data-action` and `data-actor` attributes showing time, actor, action, target; clicking a row expands `audit-diff-<id>` containing `JSON.stringify(diff, null, 2)` in a `<pre>`; a "Load older" button pages with `before_id`.
- [ ] Implement `e2e/harness/playwright.go` per the interface (working directory `<repo>/web`, `exec.CommandContext` with a 15-minute timeout, `CI=1`).
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -v -run TestAuthRBACAuditOIDC ./e2e/'` — expect PASS: Playwright prints `3 passed` across the two runs and Go prints `--- PASS: TestAuthRBACAuditOIDC`.
- [ ] Run `scripts/dev-exec.sh make web-test` — expect PASS (typecheck, eslint, permission parity, build).
- [ ] Commit: `git add web e2e/harness/playwright.go e2e/auth_test.go && git commit -m "web: GUI foundation, login/setup/upstreams/audit screens; e2e auth, RBAC, audit and OIDC test"`.

## Task 20: Remaining GUI screens, `TestGUICoverage` and `TestQueryLogBackends`

Files:

- `web/src/pages/DashboardPage.tsx`, `QueryLogPage.tsx`, `AccessControlPage.tsx`, `FilteringPage.tsx`, `EnginesPage.tsx`, `UsersPage.tsx`, `ApiTokensPage.tsx`, `SettingsPage.tsx` (create)
- `web/src/app/router.tsx` (modify) — all M1 routes
- `web/e2e/screens/00-setup.spec.ts`, `01-dashboard.spec.ts`, `02-upstreams.spec.ts`, `03-access-control.spec.ts`, `04-filtering.spec.ts`, `05-engines.spec.ts`, `06-users.spec.ts`, `07-api-tokens.spec.ts`, `08-audit.spec.ts`, `09-settings.spec.ts`, `10-query-log.spec.ts`, `11-oidc.spec.ts` (create)
- `web/e2e/querylog.spec.ts` (create)
- `e2e/harness/openapi.go` (create) — operation matcher over `mgmt/api/openapi.yaml`
- `e2e/gui_test.go` (create) — `TestGUICoverage`, `TestQueryLogBackends`

Interfaces:

- Consumes `web/e2e/fixtures.ts` (`test`, `expect`, `env`, `login`, `logout`), `AppShell`, `useCan`, `api` client (Task 19); every operationId from Task 15; harness `RunPlaywright`, `StartMgmt`, `StartManagedEngine`, `StartOtelcol`, `OpenSearchURL`, `NewAPI`, `MustQuery` (Tasks 10, 16, 18, 19).
- `harness.Operation{ ID, Method, Path string }`; `func LoadOperations(t *testing.T) []Operation` (reads `<repo>/mgmt/api/openapi.yaml` with `gopkg.in/yaml.v3`); `func MatchOperation(ops []Operation, method, path string) (string, bool)` (path templates `{x}` match one non-empty segment; the query string is ignored); `func CoveredOperations(t *testing.T, ops []Operation, coverageDir string) map[string]bool` (reads every `requests-*.jsonl`).
- Test ids (exact): dashboard `dashboard-qps`, `dashboard-cache-hit-ratio`, `dashboard-engines`, `dashboard-chart`; access control `acl-cidr-input`, `acl-add`, `acl-remove-<cidr>`, `acl-save`, `acl-row-<cidr>`; filtering `list-add`, `list-name`, `list-kind`, `list-url`, `list-interval`, `list-save`, `list-row-<name>`, `list-open-<name>`, `list-detail`, `list-refresh`, `list-edit`, `list-delete`, `list-stale-<name>`, `allowlist-input`, `allowlist-add`, `allowlist-save`, `allowlist-row-<domain>`; engines `engine-row-<node>`, `engine-open-<node>`, `engine-detail`, `engine-delete`, `jointoken-add`, `jointoken-name`, `jointoken-save`, `jointoken-value`, `jointoken-row-<name>`, `jointoken-revoke-<name>`; users `user-add`, `user-username`, `user-email`, `user-password`, `user-role`, `user-save`, `user-row-<username>`, `user-edit-<username>`, `user-disabled`, `user-delete-<username>`; API tokens `token-add`, `token-name`, `token-role`, `token-save`, `token-value`, `token-row-<name>`, `token-revoke-<name>`; settings `settings-strategy`, `settings-cache-max-bytes`, `settings-block-mode`, `settings-block-ttl`, `settings-otlp-endpoint`, `settings-sample-one-in`, `settings-save`, `version-row` (one per config version); query log `querylog-name`, `querylog-client`, `querylog-search`, `querylog-row`, `querylog-backend`, `querylog-unavailable`; shared `confirm-delete`.

- [ ] Write the failing screen specs. `web/e2e/screens/00-setup.spec.ts`:

```ts
import { test, expect, env } from "../fixtures";

test("first-run setup creates the admin", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/setup/);
  await page.getByTestId("setup-token").fill(env("NEXORA_E2E_SETUP_TOKEN"));
  await page.getByTestId("setup-username").fill(env("NEXORA_E2E_ADMIN_USER"));
  await page.getByTestId("setup-email").fill("admin@example.test");
  await page
    .getByTestId("setup-password")
    .fill(env("NEXORA_E2E_ADMIN_PASSWORD"));
  await page.getByTestId("setup-submit").click();
  await expect(page.getByTestId("user-menu")).toContainText(
    env("NEXORA_E2E_ADMIN_USER"),
  );
  await expect(page.getByTestId("health-badge")).toContainText("ok");
});
```

`web/e2e/screens/01-dashboard.spec.ts`:

```ts
import { test, expect, env, login, logout } from "../fixtures";

test("dashboard shows fleet stats", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-dashboard").click();
  await expect(page.getByTestId("dashboard-engines")).toContainText(
    /\d+ \/ \d+/,
  );
  await expect(page.getByTestId("dashboard-qps")).toBeVisible();
  await expect(page.getByTestId("dashboard-cache-hit-ratio")).toBeVisible();
  await expect(page.getByTestId("dashboard-chart")).toBeVisible();
  await logout(page);
});
```

`web/e2e/screens/02-upstreams.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("upstreams create, edit, delete", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-upstreams").click();
  await page.getByTestId("upstream-add").click();
  await page.getByTestId("upstream-name").fill("gui-doh");
  await page.getByTestId("upstream-protocol").click();
  await page.getByRole("option", { name: "doh" }).click();
  await page
    .getByTestId("upstream-doh-url")
    .fill("https://dns.example.test/dns-query");
  await page.getByTestId("upstream-save").click();
  await expect(page.getByTestId("upstream-row-gui-doh")).toContainText(
    "https://dns.example.test/dns-query",
  );
  await page.getByTestId("upstream-edit-gui-doh").click();
  await page.getByTestId("upstream-timeout").fill("900");
  await page.getByTestId("upstream-save").click();
  await expect(page.getByTestId("upstream-row-gui-doh")).toContainText("900");
  await page.getByTestId("upstream-delete-gui-doh").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("upstream-row-gui-doh")).toHaveCount(0);
});
```

`web/e2e/screens/03-access-control.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("access control list edit", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-access-control").click();
  await expect(page.getByTestId("acl-row-127.0.0.0/8")).toBeVisible();
  await page.getByTestId("acl-cidr-input").fill("198.51.100.0/24");
  await page.getByTestId("acl-add").click();
  await page.getByTestId("acl-save").click();
  await page.reload();
  await expect(page.getByTestId("acl-row-198.51.100.0/24")).toBeVisible();
  await page.getByTestId("acl-cidr-input").fill("not-a-cidr");
  await page.getByTestId("acl-add").click();
  await expect(page.getByText("Invalid CIDR")).toBeVisible();
});
```

`web/e2e/screens/04-filtering.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("filter lists and allowlist", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-filtering").click();
  await page.getByTestId("list-add").click();
  await page.getByTestId("list-name").fill("gui-list");
  await page.getByTestId("list-url").fill(env("NEXORA_E2E_LIST_URL"));
  await page.getByTestId("list-interval").fill("3600");
  await page.getByTestId("list-save").click();
  await expect(page.getByTestId("list-row-gui-list")).toBeVisible();
  await page.getByTestId("list-open-gui-list").click();
  await expect(page.getByTestId("list-detail")).toContainText("gui-list");
  await page.getByTestId("list-refresh").click();
  await expect(page.getByTestId("list-detail")).toContainText(/2 entries/);
  await page.getByTestId("list-edit").click();
  await page.getByTestId("list-interval").fill("7200");
  await page.getByTestId("list-save").click();
  await expect(page.getByTestId("list-row-gui-list")).toContainText("7200");
  await page.getByTestId("allowlist-input").fill("ok.gui.test");
  await page.getByTestId("allowlist-add").click();
  await page.getByTestId("allowlist-save").click();
  await page.reload();
  await expect(page.getByTestId("allowlist-row-ok.gui.test")).toBeVisible();
  await page.getByTestId("list-open-gui-list").click();
  await page.getByTestId("list-delete").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("list-row-gui-list")).toHaveCount(0);
});
```

`web/e2e/screens/05-engines.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("engines and join tokens", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-engines").click();
  await expect(page.getByTestId("engine-row-gui-engine")).toContainText(
    "current",
  );
  await page.getByTestId("engine-open-gui-engine-2").click();
  await expect(page.getByTestId("engine-detail")).toContainText("gui-engine-2");
  await page.getByTestId("engine-delete").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("engine-row-gui-engine-2")).toHaveCount(0);
  await page.getByTestId("jointoken-add").click();
  await page.getByTestId("jointoken-name").fill("gui-token");
  await page.getByTestId("jointoken-save").click();
  await expect(page.getByTestId("jointoken-value")).toContainText("nxj1.");
  await page.keyboard.press("Escape");
  await page.getByTestId("jointoken-revoke-gui-token").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("jointoken-row-gui-token")).toContainText(
    "revoked",
  );
});
```

`web/e2e/screens/06-users.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("users create, edit, delete", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-users").click();
  await page.getByTestId("user-add").click();
  await page.getByTestId("user-username").fill("gui-user");
  await page.getByTestId("user-email").fill("gui@example.test");
  await page.getByTestId("user-password").fill("gui-user-password-1");
  await page.getByTestId("user-save").click();
  await expect(page.getByTestId("user-row-gui-user")).toContainText("viewer");
  await page.getByTestId("user-edit-gui-user").click();
  await page.getByTestId("user-role").click();
  await page.getByRole("option", { name: "operator" }).click();
  await page.getByTestId("user-save").click();
  await expect(page.getByTestId("user-row-gui-user")).toContainText("operator");
  await page.getByTestId("user-delete-gui-user").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("user-row-gui-user")).toHaveCount(0);
});
```

`web/e2e/screens/07-api-tokens.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("api tokens create and revoke", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-api-tokens").click();
  await page.getByTestId("token-add").click();
  await page.getByTestId("token-name").fill("gui-ci");
  await page.getByTestId("token-save").click();
  await expect(page.getByTestId("token-value")).toContainText("nxt_");
  await page.keyboard.press("Escape");
  await page.getByTestId("token-revoke-gui-ci").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("token-row-gui-ci")).toContainText("revoked");
});
```

`web/e2e/screens/08-audit.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("audit log lists changes with diffs", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-audit").click();
  const row = page
    .locator('[data-testid="audit-row"][data-action="createUser"]')
    .first();
  await expect(row).toBeVisible();
  await row.click();
  await expect(
    page.locator('[data-testid^="audit-diff-"]').first(),
  ).toContainText("gui-user");
});
```

`web/e2e/screens/09-settings.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("resolver settings and config versions", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-settings").click();
  const before = await page.getByTestId("version-row").count();
  await page.getByTestId("settings-block-ttl").fill("120");
  await page.getByTestId("settings-save").click();
  await page.reload();
  await expect(page.getByTestId("settings-block-ttl")).toHaveValue("120");
  await expect(page.getByTestId("version-row")).toHaveCount(before + 1);
});
```

`web/e2e/screens/10-query-log.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("query log search", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-query-log").click();
  await page.getByTestId("querylog-name").fill(env("NEXORA_E2E_QUERY_NAME"));
  await page.getByTestId("querylog-search").click();
  await expect(
    page
      .getByTestId("querylog-row")
      .filter({ hasText: env("NEXORA_E2E_QUERY_NAME") })
      .first(),
  ).toBeVisible();
});
```

`web/e2e/screens/11-oidc.spec.ts`:

```ts
import { test, expect, env, logout } from "../fixtures";

test("OIDC sign-in round trip", async ({ page }) => {
  await page.goto("/login");
  await page.getByTestId("login-oidc").click();
  await page
    .getByRole("button", { name: `Sign in as ${env("NEXORA_E2E_OIDC_USER")}` })
    .click();
  await expect(page.getByTestId("user-menu")).toContainText(
    env("NEXORA_E2E_OIDC_USER"),
  );
  await logout(page);
});
```

- [ ] Write the failing spec `web/e2e/querylog.spec.ts`:

```ts
import { test, expect, env, login } from "./fixtures";

test("a query made against the engine appears in the query log within 10 s", async ({
  page,
}) => {
  const queryAt = Number(env("NEXORA_E2E_QUERY_AT"));
  const name = env("NEXORA_E2E_QUERY_NAME");
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-query-log").click();
  await expect(page.getByTestId("querylog-backend")).toHaveText(
    env("NEXORA_E2E_QUERYLOG_BACKEND"),
  );
  await page.getByTestId("querylog-name").fill(name);
  const row = page
    .getByTestId("querylog-row")
    .filter({ hasText: name })
    .first();
  while (Date.now() < queryAt + 10_000) {
    await page.getByTestId("querylog-search").click();
    if (await row.isVisible()) break;
    await page.waitForTimeout(500);
  }
  await expect(row).toBeVisible({ timeout: 1 });
  expect(Date.now() - queryAt).toBeLessThanOrEqual(12_000);
  await expect(row).toContainText("NOERROR");
});
```

- [ ] Write the failing Go tests `e2e/gui_test.go`:

```go
package e2e

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestGUICoverage(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	oidc := env.StartOIDCFixture(harness.OIDCUser{Username: "ada", Email: "ada@example.test", Groups: []string{"nexora-admins"}})
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{OIDC: oidc, OIDCAdminGroup: "nexora-admins"})
	web := env.StartHTTPFixture()
	web.SetList(t, "gui", "a.gui.test\nb.gui.test\n")
	fx := env.StartDNSFixture()

	vars := map[string]string{
		"NEXORA_E2E_BASE_URL":       mgmt.BaseURL,
		"NEXORA_E2E_SETUP_TOKEN":    mgmt.SetupToken(t),
		"NEXORA_E2E_ADMIN_USER":     "admin",
		"NEXORA_E2E_ADMIN_PASSWORD": "admin-password-e2e",
		"NEXORA_E2E_OIDC_USER":      "ada",
		"NEXORA_E2E_LIST_URL":       web.URL("gui"),
	}
	dir := harness.RunPlaywright(t, []string{"e2e/screens/00-setup.spec.ts"}, vars)

	admin := env.NewAPI(mgmt.BaseURL)
	admin.Must("POST", "/auth/login", map[string]string{"username": "admin", "password": "admin-password-e2e"}, nil, 200)
	admin.Must("POST", "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, 201)
	eng := env.StartManagedEngine("gui-engine", []string{mgmt.GRPCURL}, admin.CreateJoinToken())
	env.StartManagedEngine("gui-engine-2", []string{mgmt.GRPCURL}, admin.CreateJoinToken())
	v := admin.LatestVersion()
	admin.WaitEngine("gui-engine", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
	admin.WaitEngine("gui-engine-2", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
	name := harness.UniqueName("gui")
	harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{})
	vars["NEXORA_E2E_QUERY_NAME"] = strings.TrimSuffix(name, ".")
	time.Sleep(12 * time.Second) // one engine Stats interval so the dashboard has samples

	specs, _ := filepath.Glob(filepath.Join(harness.RepoRoot(t), "web/e2e/screens/[01][0-9]-*.spec.ts"))
	var rel []string
	for _, s := range specs {
		if !strings.HasSuffix(s, "00-setup.spec.ts") {
			rel = append(rel, strings.TrimPrefix(s, filepath.Join(harness.RepoRoot(t), "web")+"/"))
		}
	}
	vars["NEXORA_E2E_COVERAGE_DIR"] = dir
	harness.RunPlaywright(t, rel, vars)

	ops := harness.LoadOperations(t)
	covered := harness.CoveredOperations(t, ops, dir)
	if len(covered) == 0 {
		t.Fatal("no API requests were recorded; the coverage recorder is broken")
	}
	var missing []string
	for _, op := range ops {
		if !covered[op.ID] {
			missing = append(missing, fmt.Sprintf("%s (%s %s)", op.ID, op.Method, op.Path))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d OpenAPI operations have no covering Playwright test:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

func TestQueryLogBackends(t *testing.T) {
	for _, backend := range []string{"builtin", "opensearch"} {
		t.Run(backend, func(t *testing.T) {
			env := harness.New(t)
			pg := env.StartPostgres()
			ca := env.InitCA()
			opts := harness.MgmtOptions{QueryLogBackend: backend}
			if backend == "opensearch" {
				col := env.StartOtelcol(harness.OtelcolConfig{OpenSearchURL: harness.OpenSearchURL(t), DebugFile: env.Dir + "/otel.jsonl"})
				opts.OpenSearchURL = harness.OpenSearchURL(t)
				opts.OTLPEndpoint = "http://" + col.OTLPGRPC
			}
			mgmt := env.StartMgmt(pg, ca, opts)
			api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
			fx := env.StartDNSFixture()
			api.Must("POST", "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, 201)
			eng := env.StartManagedEngine("engine-ql-"+backend, []string{mgmt.GRPCURL}, api.CreateJoinToken())
			v := api.LatestVersion()
			api.WaitEngine("engine-ql-"+backend, 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })

			name := harness.UniqueName("ql-" + backend)
			queryAt := time.Now()
			if r := harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
				t.Fatalf("query failed: %v", r)
			}
			harness.RunPlaywright(t, []string{"e2e/querylog.spec.ts"}, map[string]string{
				"NEXORA_E2E_BASE_URL":          mgmt.BaseURL,
				"NEXORA_E2E_ADMIN_USER":        "admin",
				"NEXORA_E2E_ADMIN_PASSWORD":    "admin-password-e2e",
				"NEXORA_E2E_QUERY_NAME":        strings.TrimSuffix(name, "."),
				"NEXORA_E2E_QUERY_AT":          strconv.FormatInt(queryAt.UnixMilli(), 10),
				"NEXORA_E2E_QUERYLOG_BACKEND":  backend,
			})
		})
	}
}
```

and add `func RepoRoot(t *testing.T) string` (walks up from the working directory to the directory containing `go.mod`) to `e2e/harness/harness.go`.

- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -run "TestGUICoverage|TestQueryLogBackends" ./e2e/'` — expect FAIL with `undefined: harness.LoadOperations`; after adding `openapi.go`, the next run is expected to FAIL with `OpenAPI operations have no covering Playwright test` listing the screens not yet built (for example `getDashboard (GET /dashboard)`), which proves the coverage comparison bites before the screens exist.
- [ ] Implement `e2e/harness/openapi.go` per the interface.
- [ ] Implement `DashboardPage.tsx`: `getDashboard` every 10 s; `dashboard-qps` (QPS rounded), `dashboard-cache-hit-ratio` (percent), `dashboard-engines` (`<connected> / <total>`), a Recharts `LineChart` of `series` inside `data-testid="dashboard-chart"`, and an upstream health table.
- [ ] Implement `QueryLogPage.tsx`: filter inputs `querylog-name`, `querylog-client` (plus qtype/rcode/cache/filter selects), `querylog-search` runs `searchQueryLog` with `limit=100`; results table rows `querylog-row` showing time, client, name, type, rcode, cache, filter, upstream, duration; `querylog-backend` shows `page.backend`; a 503 `querylog_unavailable` renders `querylog-unavailable` with `Query log backend unavailable` while the rest of the app keeps working; "Next page" uses `next_cursor`.
- [ ] Implement `AccessControlPage.tsx` (`getAccessControl`; rows `acl-row-<cidr>` with `acl-remove-<cidr>`; `acl-cidr-input` validated client-side with an IPv4/IPv6 prefix regex showing `Invalid CIDR`; `acl-save` PUTs with `revision`), `FilteringPage.tsx` (lists table with `list-row-<name>` showing kind, URL, interval, `<entry_count> entries`, last success, and `list-stale-<name>` badge when `stale`; `list-open-<name>` opens `list-detail` via `getFilterList` with `list-refresh` (`refreshFilterList`), `list-edit`, `list-delete`; form fields `list-name`, `list-kind`, `list-url`, `list-interval`, `list-save`; allowlist editor `allowlist-input`, `allowlist-add`, rows `allowlist-row-<domain>`, `allowlist-save`), `EnginesPage.tsx` (engines table `engine-row-<node>` with status badge text from `Engine.status`, applied version, rejected reason; `engine-open-<node>` -> `getEngine` detail `engine-detail` with `engine-delete`; join tokens panel shown when `useCan("listJoinTokens")`: `jointoken-add` dialog with `jointoken-name`, TTL select, `jointoken-save`, one-time `jointoken-value` display, rows `jointoken-row-<name>` showing `revoked` when revoked, `jointoken-revoke-<name>`), `UsersPage.tsx` (`user-*` ids; the edit dialog includes `user-role` select and `user-disabled` switch and sends `revision`), `ApiTokensPage.tsx` (`token-*` ids; `token-role` select limited to roles at or below the current user's; one-time `token-value`), `SettingsPage.tsx` (resolver settings form with `settings-*` ids sending `revision`, and a config version history table from `listConfigVersions` with rows `version-row`).
- [ ] Update `router.tsx` with every M1 route and make `/` render `DashboardPage`.
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -v -run "TestGUICoverage|TestQueryLogBackends" ./e2e/'` — expect PASS: `--- PASS: TestGUICoverage`, `--- PASS: TestQueryLogBackends/builtin`, `--- PASS: TestQueryLogBackends/opensearch`.
- [ ] Run `scripts/dev-exec.sh make web-test` — expect PASS.
- [ ] Commit: `git add web e2e/gui_test.go e2e/harness/openapi.go e2e/harness/harness.go go.mod go.sum && git commit -m "web: all M1 screens; e2e GUI coverage and query-log backend tests"`.

## Task 21: Two-tier dnsperf performance gate

Files:

- `bench/cmd/perfgate/main.go` (create) — `run`, `serve`, `load`, `compare`, `absolute` subcommands
- `bench/cmd/perfgate/gate.go` (create) — threshold logic
- `bench/cmd/perfgate/gate_test.go` (create)
- `bench/corpus/names.go` (create) — deterministic cache-hit corpus generator
- `.github/workflows/perf-gate.yml` (create)

Interfaces:

- Consumes `bench/dnsperf.{Run, Options, Result}` (Task 18), `e2e/fixtures/cmd/nexora-fixture` binary (Task 10), `nexora-engine` standalone mode (Task 8).
- `corpus.Names(n int) []string` returns `perf-<i>.example.` for `i` in `[0, n)`.
- `func Compare(base, head []dnsperf.Result, maxDrop float64) (Verdict, error)` where `type Verdict struct { BaseQPS, HeadQPS, Drop float64; Pass bool }` uses the median QPS of each side; `Pass` is `head >= base*(1-maxDrop)`.
- `func Absolute(r dnsperf.Result, minQPS float64, maxP99 time.Duration) (Verdict2, error)` with `type Verdict2 struct { QPS float64; P99 time.Duration; Pass bool; Reasons []string }`.
- CLI:
  - `perfgate run --engine PATH --fixture PATH --names 10000 --seconds 20 --workers N --out FILE` — starts the fixture and a standalone engine on loopback (engine `workers = N`), warms every name once, runs dnsperf, writes the `dnsperf.Result` JSON.
  - `perfgate serve --engine PATH --fixture PATH --listen ADDR --workers N` — same setup bound to `ADDR`, prints `perfgate ready`, runs until SIGTERM (reference box).
  - `perfgate load --target ADDR --names 10000 --seconds 60 --clients 64 --threads 16 --out FILE` — warm + dnsperf against a remote engine (load host).
  - `perfgate compare --base a.json,b.json,c.json --head d.json,e.json,f.json --max-drop 0.05` — prints the verdict, exit 1 when failing.
  - `perfgate absolute --result FILE --min-qps 1000000 --max-p99 500us` — prints reasons, exit 1 when failing.

- [ ] Write the failing test `bench/cmd/perfgate/gate_test.go`:

```go
package main

import (
	"testing"
	"time"

	"github.com/piwi3910/nexora/bench/dnsperf"
)

func qps(v ...float64) []dnsperf.Result {
	out := make([]dnsperf.Result, len(v))
	for i, q := range v {
		out[i] = dnsperf.Result{QPS: q, Completed: 1}
	}
	return out
}

func TestCompareUsesMedianAndFivePercentBoundary(t *testing.T) {
	v, err := Compare(qps(100000, 10, 101000), qps(95000, 95100, 1), 0.05)
	if err != nil || !v.Pass || v.BaseQPS != 100000 || v.HeadQPS != 95000 {
		t.Fatalf("exactly 5%% drop must pass: %+v %v", v, err)
	}
	v, _ = Compare(qps(100000, 100000, 100000), qps(94900, 94900, 94900), 0.05)
	if v.Pass {
		t.Fatalf("5.1%% drop must fail: %+v", v)
	}
	if _, err := Compare(nil, qps(1), 0.05); err == nil {
		t.Fatal("empty base accepted")
	}
}

func TestAbsoluteThresholds(t *testing.T) {
	ok, _ := Absolute(dnsperf.Result{QPS: 1_000_001, P99Seconds: 0.000499, Completed: 1}, 1_000_000, 500*time.Microsecond)
	if !ok.Pass {
		t.Fatalf("should pass: %+v", ok)
	}
	bad, _ := Absolute(dnsperf.Result{QPS: 999_999, P99Seconds: 0.000501, Completed: 1}, 1_000_000, 500*time.Microsecond)
	if bad.Pass || len(bad.Reasons) != 2 {
		t.Fatalf("should fail twice: %+v", bad)
	}
	if _, err := Absolute(dnsperf.Result{QPS: 2_000_000}, 1_000_000, 500*time.Microsecond); err == nil {
		t.Fatal("result with zero completed queries accepted")
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./bench/cmd/perfgate/...` — expect FAIL with `undefined: Compare`.
- [ ] Implement `gate.go` (median of sorted QPS values; `Drop = 1 - head/base`; errors on empty inputs or `Completed == 0`), `corpus/names.go`, and `main.go` (the fixture is started with `nexora-fixture dns` on free loopback ports; the snapshot is `version 1`, one UDP upstream to the fixture, cache `max_bytes` 512 MiB, `stale_window` 0, ACL `0.0.0.0/0` and `::/0`, telemetry empty; warm-up uses 64 goroutines of `miekg/dns` UDP queries; `serve` writes the snapshot with the listen address from `--listen`).
- [ ] Run `scripts/dev-exec.sh bash -c 'go test ./bench/... && make e2e-build && bin/perfgate run --engine bin/nexora-engine --fixture bin/nexora-fixture --names 2000 --seconds 5 --workers 2 --out /tmp/perf.json && cat /tmp/perf.json'` — expect PASS: tests `ok` and a JSON result with `"QPS"` greater than 0.
- [ ] Write `.github/workflows/perf-gate.yml`:

```yaml
name: perf-gate
on:
  pull_request:
    paths:
      [
        "engine/**",
        "proto/**",
        "Cargo.lock",
        "bench/**",
        ".github/workflows/perf-gate.yml",
      ]
  schedule:
    - cron: "0 3 * * *"
  push:
    tags: ["v*"]
  workflow_dispatch: {}
permissions:
  contents: read
concurrency:
  group: perf-gate-${{ github.ref }}
  cancel-in-progress: true
jobs:
  relative:
    name: relative regression vs main (PR)
    if: github.event_name == 'pull_request'
    runs-on: ubuntu-24.04
    timeout-minutes: 75
    steps:
      - uses: actions/checkout@08eba0b27e820071cde6df949e0beb9ba4906955 # v4.3.0
        with: { path: head }
      - uses: actions/checkout@08eba0b27e820071cde6df949e0beb9ba4906955 # v4.3.0
        with: { path: base, ref: "${{ github.event.pull_request.base.sha }}" }
      - name: Toolchains
        run: |
          sudo apt-get update && sudo apt-get install -y protobuf-compiler dnsperf
          rustup toolchain install 1.97 --profile minimal
          curl -fsSL https://go.dev/dl/go1.27.1.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
          echo /usr/local/go/bin >> "$GITHUB_PATH"
      - name: Build base and head engines, fixture and perfgate
        run: |
          (cd base && cargo build --locked --release -p nexora-engine)
          (cd head && cargo build --locked --release -p nexora-engine)
          (cd head && go build -o ../perfgate ./bench/cmd/perfgate && go build -o ../nexora-fixture ./e2e/fixtures/cmd/nexora-fixture)
      - name: Interleaved A/B benchmark (3 rounds)
        run: |
          for i in 1 2 3; do
            ./perfgate run --engine base/target/release/nexora-engine --fixture ./nexora-fixture --names 10000 --seconds 20 --workers 2 --out base-$i.json
            ./perfgate run --engine head/target/release/nexora-engine --fixture ./nexora-fixture --names 10000 --seconds 20 --workers 2 --out head-$i.json
          done
      - name: Fail on more than 5% QPS drop
        run: ./perfgate compare --base base-1.json,base-2.json,base-3.json --head head-1.json,head-2.json,head-3.json --max-drop 0.05
      - uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2
        if: always()
        with: { name: perf-relative, path: "*.json" }

  absolute:
    name: absolute gate on the reference box (nightly, release tags)
    if: github.event_name != 'pull_request'
    runs-on: [self-hosted, linux, x64, nexora-load]
    timeout-minutes: 90
    env:
      REF_HOST: ${{ vars.NEXORA_REFERENCE_HOST }}
      REF_ADDR: ${{ vars.NEXORA_REFERENCE_DNS_ADDR }}
    steps:
      - uses: actions/checkout@08eba0b27e820071cde6df949e0beb9ba4906955 # v4.3.0
      - name: Build release binaries
        run: |
          cargo build --locked --release -p nexora-engine
          go build -o perfgate ./bench/cmd/perfgate
          go build -o nexora-fixture ./e2e/fixtures/cmd/nexora-fixture
      - name: Start the engine on the reference box
        env:
          SSH_KEY: ${{ secrets.NEXORA_REFERENCE_SSH_KEY }}
        run: |
          install -m 600 /dev/null ~/.ssh/nexora-ref && printf '%s\n' "$SSH_KEY" > ~/.ssh/nexora-ref
          scp -i ~/.ssh/nexora-ref target/release/nexora-engine perfgate nexora-fixture "$REF_HOST:/tmp/"
          ssh -i ~/.ssh/nexora-ref "$REF_HOST" "nohup /tmp/perfgate serve --engine /tmp/nexora-engine --fixture /tmp/nexora-fixture --listen $REF_ADDR --workers 8 > /tmp/perfgate.log 2>&1 & echo \$! > /tmp/perfgate.pid"
          for i in $(seq 1 60); do ssh -i ~/.ssh/nexora-ref "$REF_HOST" grep -q 'perfgate ready' /tmp/perfgate.log && break; sleep 1; done
      - name: Load from this host
        run: ./perfgate load --target "$REF_ADDR" --names 100000 --seconds 60 --clients 64 --threads 16 --out absolute.json
      - name: Enforce >= 1,000,000 QPS and p99 < 500us
        run: ./perfgate absolute --result absolute.json --min-qps 1000000 --max-p99 500us
      - name: Stop the engine
        if: always()
        run: ssh -i ~/.ssh/nexora-ref "$REF_HOST" 'kill $(cat /tmp/perfgate.pid) || true'
      - uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2
        if: always()
        with: { name: perf-absolute, path: absolute.json }
```

- [ ] Validate the workflow syntax: `scripts/dev-exec.sh bash -c 'go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/perf-gate.yml .github/workflows/fuzz.yml'` — expect PASS with no output.
- [ ] Commit: `git add bench .github/workflows/perf-gate.yml && git commit -m "bench: perfgate tool and two-tier dnsperf performance gate workflow"`.

## Task 22: Container images and CI workflows

Files:

- `deploy/docker/engine.Dockerfile` (create)
- `deploy/docker/mgmt.Dockerfile` (create)
- `.dockerignore` (create)
- `engine/src/main.rs` (modify) — `#[command(version)]` so `nexora-engine --version` works
- `mgmt/cmd/nexora-mgmt/main.go` (modify) — `version` subcommand printing `nexora-mgmt <version>` (`-ldflags -X main.version`)
- `.github/workflows/images.yml` (create) — multi-arch (amd64, arm64) images to `ghcr.io/piwi3910/nexora-engine` and `ghcr.io/piwi3910/nexora-mgmt`
- `.github/workflows/ci.yml` (create) — engine, management plane and GUI unit tests and lint

Interfaces:

- Consumes `make build` outputs (Tasks 1, 8, 15, 20) and `scripts/build-image.sh -f <dockerfile relative to context> -n <name> -t <tag> <context>`.
- Produces images `192.168.10.131:5000/azrtydxb/nexora-engine:<tag>` and `.../nexora-mgmt:<tag>` (pulled as `192.168.10.131/azrtydxb/<name>:<tag>`), consumed by Task 23. Engine image: entrypoint `/usr/local/bin/nexora-engine`, default args `--config /etc/nexora/engine.toml`, user 10001, ports 53/udp, 53/tcp, 9153/tcp. Management image: entrypoint `/nexora-mgmt`, default args `serve`, user nonroot, ports 8080, 9443.

- [ ] Run the image build before the Dockerfile exists: `scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine -t m1-check .` — expect FAIL with `failed to read dockerfile`.
- [ ] Write `.dockerignore`:

```
.git
target
engine/target
**/node_modules
web/dist
web/test-results
web/playwright-report
bin
.procoder
```

- [ ] Write `deploy/docker/engine.Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1.10
FROM rust:1.97-trixie AS build
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends protobuf-compiler \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY proto proto
COPY engine engine
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/target \
    cargo build --locked --release -p nexora-engine \
 && install -m 0755 target/release/nexora-engine /nexora-engine

FROM debian:trixie-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home-dir /var/lib/nexora nexora \
 && mkdir -p /var/lib/nexora /etc/nexora && chown nexora:nexora /var/lib/nexora
COPY --from=build /nexora-engine /usr/local/bin/nexora-engine
USER 10001
EXPOSE 53/udp 53/tcp 9153/tcp
ENTRYPOINT ["/usr/local/bin/nexora-engine"]
CMD ["--config", "/etc/nexora/engine.toml"]
```

- [ ] Write `deploy/docker/mgmt.Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1.10
FROM node:26-trixie-slim AS web
RUN npm install -g pnpm@10
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web ./
COPY mgmt/api/openapi.yaml /src/mgmt/api/openapi.yaml
RUN pnpm run build

FROM golang:1.27-trixie AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY gen gen
COPY mgmt mgmt
COPY --from=web /src/web/dist mgmt/internal/webui/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /nexora-mgmt ./mgmt/cmd/nexora-mgmt

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /nexora-mgmt /nexora-mgmt
EXPOSE 8080 9443
ENTRYPOINT ["/nexora-mgmt"]
CMD ["serve"]
```

- [ ] Add `#[command(version)]` to the engine `Args` and the `version` subcommand to `nexora-mgmt`, then build both images on kw BuildKit: `tag=m1-$(git rev-parse --short HEAD) && scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine -t "$tag" . && scripts/build-image.sh -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t "$tag" .` — expect PASS: both print `pull as 192.168.10.131/azrtydxb/<name>:m1-<sha>`.
- [ ] Verify the images run on arm64 kw nodes: `kubectl --context kw -n nexora-dev run engine-version --rm -i --restart=Never --image=192.168.10.131/azrtydxb/nexora-engine:$tag --overrides='{"spec":{"imagePullSecrets":[{"name":"nexus-pull"}]}}' -- --version` — expect `nexora-engine 0.1.0`; and the same with `nexora-mgmt:$tag` and args `version` — expect `nexora-mgmt m1-...` or `nexora-mgmt dev`.
- [ ] Write `.github/workflows/ci.yml`:

```yaml
name: ci
on:
  push: { branches: [main] }
  pull_request: {}
permissions:
  contents: read
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true
jobs:
  engine:
    runs-on: ubuntu-24.04
    timeout-minutes: 45
    steps:
      - uses: actions/checkout@08eba0b27e820071cde6df949e0beb9ba4906955 # v4.3.0
      - run: sudo apt-get update && sudo apt-get install -y protobuf-compiler
      - run: rustup toolchain install 1.97 --profile minimal --component clippy,rustfmt
      - run: cargo fmt --all -- --check
      - run: cargo clippy --locked -p nexora-engine --all-targets -- -D warnings
      - run: cargo test --locked -p nexora-engine --all-targets
  mgmt:
    runs-on: ubuntu-24.04
    timeout-minutes: 45
    steps:
      - uses: actions/checkout@08eba0b27e820071cde6df949e0beb9ba4906955 # v4.3.0
      - name: Toolchain and PostgreSQL binaries
        run: |
          curl -fsSL https://go.dev/dl/go1.27.1.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
          echo /usr/local/go/bin >> "$GITHUB_PATH"
          echo /usr/lib/postgresql/16/bin >> "$GITHUB_PATH"
          sudo apt-get update && sudo apt-get install -y postgresql
      - run: make webui-placeholder
      - run: go vet ./...
      - run: go test -race -count=1 -skip 'TestOIDCLoginAndProviderDown' ./mgmt/... ./gen/... ./bench/...
  web:
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    steps:
      - uses: actions/checkout@08eba0b27e820071cde6df949e0beb9ba4906955 # v4.3.0
      - run: curl -fsSL https://nodejs.org/dist/v26.7.0/node-v26.7.0-linux-x64.tar.xz | sudo tar -C /usr/local --strip-components=1 -xJ && sudo npm install -g pnpm@10
      - run: cd web && pnpm install --frozen-lockfile && pnpm run typecheck && pnpm run lint && pnpm run build
```

`TestOIDCLoginAndProviderDown` is skipped in CI because it needs the `nexora-fixture` binary; it runs in the dev pod through `make mgmt-test` after `make e2e-build`. The end-to-end suite runs in the dev pod (`make e2e`), not on GitHub runners.

- [ ] Write `.github/workflows/images.yml` (multi-arch via QEMU/buildx; tags `sha-<short>` on main, `vX.Y.Z` on tags):

```yaml
name: images
on:
  push:
    branches: [main]
    tags: ["v*"]
permissions:
  contents: read
  packages: write
concurrency:
  group: images-${{ github.ref }}
  cancel-in-progress: false
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 120
    strategy:
      matrix:
        image:
          - { name: nexora-engine, file: deploy/docker/engine.Dockerfile }
          - { name: nexora-mgmt, file: deploy/docker/mgmt.Dockerfile }
    steps:
      - uses: actions/checkout@08eba0b27e820071cde6df949e0beb9ba4906955 # v4.3.0
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          {
            registry: ghcr.io,
            username: "${{ github.actor }}",
            password: "${{ secrets.GITHUB_TOKEN }}",
          }
      - id: meta
        run: |
          if [[ "$GITHUB_REF" == refs/tags/v* ]]; then echo "tag=${GITHUB_REF#refs/tags/}" >> "$GITHUB_OUTPUT"; else echo "tag=sha-${GITHUB_SHA::7}" >> "$GITHUB_OUTPUT"; fi
      - uses: docker/build-push-action@v6
        with:
          context: .
          file: ${{ matrix.image.file }}
          platforms: linux/amd64,linux/arm64
          push: true
          build-args: VERSION=${{ steps.meta.outputs.tag }}
          tags: ghcr.io/piwi3910/${{ matrix.image.name }}:${{ steps.meta.outputs.tag }}
```

- [ ] Pin the four `docker/*` actions to commit SHAs: for each `<repo>@<tag>` run `gh api repos/<repo>/commits/<tag> --jq .sha` (for example `gh api repos/docker/build-push-action/commits/v6 --jq .sha`) and replace `@<tag>` with `@<sha> # <tag>`; then run `scripts/dev-exec.sh bash -c 'go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7'` — expect PASS with no output.
- [ ] Commit: `git add .dockerignore deploy/docker .github/workflows/images.yml .github/workflows/ci.yml engine/src/main.rs mgmt/cmd/nexora-mgmt/main.go && git commit -m "deploy: engine and management plane images; CI and multi-arch image workflows"`.

## Task 23: First deployment to kw and `TestKwSmoke`

Files:

- `deploy/kw/cnpg-cluster.yaml` (create) — CNPG cluster `nexora-db`
- `deploy/kw/mgmt-grpc-lb.yaml` (create) — LoadBalancer Service for gRPC 9443 (applied first so its IP can go into the server certificate SANs)
- `deploy/kw/mgmt.yaml` (create) — `nexora-mgmt` Deployment (2 replicas), ClusterIP Services, Ingress `nexora.kw.local`
- `deploy/kw/otelcol.yaml` (create) — OpenTelemetry Collector (OpenSearch logs, Jaeger traces)
- `deploy/kw/engine.yaml` (create) — `nexora-engine` Deployment (3 replicas, one per node), config template, DNS LoadBalancer
- `scripts/kw-deploy.sh` (create) — build, apply, bootstrap (CA, setup, upstreams, join token), roll out
- `e2e/kw_smoke_test.go` (create) — `TestKwSmoke`
- `e2e/main_test.go` (modify) — remove the binary check from `TestMain` (the harness `Bin` already fails tests that need binaries) so `TestKwSmoke` runs without local builds

Interfaces:

- Consumes images from Task 22, `deploy/kw/namespace.yaml` and `deploy/kw/opensearch.yaml` from Task 18, env contract of `nexora-mgmt serve` (Tasks 12–18), `engine.toml` bootstrap keys (Task 8), API operations `completeSetup`, `login`, `createApiToken`, `listUpstreams`, `createUpstream`, `createJoinToken` (Task 15).
- `scripts/kw-deploy.sh [--tag TAG] --admin-password-file FILE` — prints `NEXORA_KW_DNS_ADDR=<ip>:53` and `NEXORA_KW_API_URL=http://nexora.kw.local` at the end.
- Kubernetes objects in namespace `nexora`: `Cluster nexora-db` (app secret `nexora-db-app`, key `uri`), `Secret nexora-ca` (`ca.crt`, `ca.key`), `Secret nexora-join-token` (`join-token`), `Deployment nexora-mgmt`, `Service nexora-mgmt` (8080), `Service nexora-mgmt-grpc` (ClusterIP 9443), `Service nexora-mgmt-grpc-lb` (LoadBalancer 9443), `Ingress nexora` (`nexora.kw.local`, class `nginx`), `Deployment nexora-otelcol` + `Service nexora-otelcol` (4317), `ConfigMap nexora-engine-config`, `Deployment nexora-engine`, `Service nexora-dns` (LoadBalancer, 53/UDP and 53/TCP, `externalTrafficPolicy: Local`), `Service nexora-engine-metrics` (9153).
- `TestKwSmoke` reads `NEXORA_KW_DNS_ADDR` and `NEXORA_KW_API_URL` and skips when either is unset.

- [ ] Write the failing test `e2e/kw_smoke_test.go`:

```go
package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestKwSmoke(t *testing.T) {
	dnsAddr, apiURL := os.Getenv("NEXORA_KW_DNS_ADDR"), strings.TrimSuffix(os.Getenv("NEXORA_KW_API_URL"), "/")
	if dnsAddr == "" || apiURL == "" {
		t.Skip("NEXORA_KW_DNS_ADDR and NEXORA_KW_API_URL are not set")
	}
	hc := &http.Client{Timeout: 10 * time.Second}

	resp, err := hc.Get(apiURL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if resp.StatusCode != 200 || health["status"] != "ok" || health["database"] != "ok" {
		t.Fatalf("health: %d %v", resp.StatusCode, health)
	}

	resp, err = hc.Get(apiURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), `id="root"`) {
		t.Fatalf("GUI not served: %d", resp.StatusCode)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err = hc.Get(apiURL + "/metrics")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if strings.Contains(string(body), "nexora_fleet_engines_connected 3") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("three engines are not connected to the management plane")
		}
		time.Sleep(2 * time.Second)
	}

	for _, network := range []string{"udp", "tcp"} {
		c := &dns.Client{Net: network, Timeout: 3 * time.Second}
		m := new(dns.Msg)
		m.SetQuestion("example.com.", dns.TypeA)
		first, _, err := c.Exchange(m, dnsAddr)
		if err != nil || first.Rcode != dns.RcodeSuccess || len(first.Answer) == 0 {
			t.Fatalf("%s query: %v %v", network, first, err)
		}
		time.Sleep(1100 * time.Millisecond)
		second, _, err := c.Exchange(m, dnsAddr)
		if err != nil || second.Rcode != dns.RcodeSuccess || len(second.Answer) == 0 {
			t.Fatalf("%s second query: %v %v", network, second, err)
		}
		if second.Answer[0].Header().Ttl > first.Answer[0].Header().Ttl {
			t.Fatalf("%s TTL grew between queries: %d -> %d", network, first.Answer[0].Header().Ttl, second.Answer[0].Header().Ttl)
		}
	}
}
```

- [ ] Run `scripts/dev-exec.sh env NEXORA_KW_DNS_ADDR=192.0.2.1:53 NEXORA_KW_API_URL=http://nexora.kw.local go test -count=1 -run TestKwSmoke ./e2e/` — expect FAIL with `health:` or a connection error (nothing is deployed yet).
- [ ] Modify `e2e/main_test.go` to contain only the helpers:

```go
package e2e

import (
	"encoding/hex"
	"regexp"
)

func hexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }
```

- [ ] Write `deploy/kw/cnpg-cluster.yaml`:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata: { name: nexora-db, namespace: nexora }
spec:
  instances: 2
  imageName: ghcr.io/cloudnative-pg/postgresql:17
  storage: { size: 10Gi, storageClass: longhorn-single }
  bootstrap:
    initdb: { database: nexora, owner: nexora }
  resources:
    requests: { cpu: 250m, memory: 512Mi }
    limits: { cpu: "1", memory: 1Gi }
```

- [ ] Write `deploy/kw/mgmt-grpc-lb.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata: { name: nexora-mgmt-grpc-lb, namespace: nexora }
spec:
  type: LoadBalancer
  selector: { app.kubernetes.io/name: nexora-mgmt }
  ports: [{ name: grpc, port: 9443, targetPort: 9443 }]
```

- [ ] Write `deploy/kw/mgmt.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  {
    name: nexora-mgmt,
    namespace: nexora,
    labels: { app.kubernetes.io/name: nexora-mgmt },
  }
spec:
  replicas: 2
  selector: { matchLabels: { app.kubernetes.io/name: nexora-mgmt } }
  template:
    metadata: { labels: { app.kubernetes.io/name: nexora-mgmt } }
    spec:
      imagePullSecrets: [{ name: nexus-pull }]
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                {
                  topologyKey: kubernetes.io/hostname,
                  labelSelector:
                    { matchLabels: { app.kubernetes.io/name: nexora-mgmt } },
                }
      containers:
        - name: mgmt
          image: 192.168.10.131/azrtydxb/nexora-mgmt:NEXORA_TAG
          args: ["serve"]
          env:
            - name: NEXORA_DATABASE_URL
              valueFrom: { secretKeyRef: { name: nexora-db-app, key: uri } }
            - { name: NEXORA_HTTP_LISTEN, value: ":8080" }
            - { name: NEXORA_GRPC_LISTEN, value: ":9443" }
            - { name: NEXORA_CA_CERT_FILE, value: /etc/nexora/ca/ca.crt }
            - { name: NEXORA_CA_KEY_FILE, value: /etc/nexora/ca/ca.key }
            - {
                name: NEXORA_GRPC_SERVER_NAMES,
                value: "nexora-mgmt-grpc.nexora.svc,nexora-mgmt-grpc.nexora.svc.cluster.local,NEXORA_GRPC_LB_IP",
              }
            - { name: NEXORA_PUBLIC_URL, value: "http://nexora.kw.local" }
            - { name: NEXORA_SECURE_COOKIES, value: "false" }
            - { name: NEXORA_QUERYLOG_BACKEND, value: opensearch }
            - {
                name: NEXORA_OPENSEARCH_URL,
                value: "http://opensearch.nexora.svc.cluster.local:9200",
              }
            - { name: NEXORA_OPENSEARCH_INDEX, value: "nexora-querylog-*" }
            - {
                name: NEXORA_OTLP_ENDPOINT,
                value: "http://nexora-otelcol.nexora.svc.cluster.local:4317",
              }
          ports:
            - { name: http, containerPort: 8080 }
            - { name: grpc, containerPort: 9443 }
          readinessProbe:
            { httpGet: { path: /api/v1/health, port: http }, periodSeconds: 5 }
          livenessProbe: { tcpSocket: { port: grpc }, periodSeconds: 10 }
          resources:
            requests: { cpu: 100m, memory: 128Mi }
            limits: { cpu: "1", memory: 512Mi }
          volumeMounts:
            [{ name: ca, mountPath: /etc/nexora/ca, readOnly: true }]
      volumes:
        - name: ca
          secret: { secretName: nexora-ca, defaultMode: 0400 }
---
apiVersion: v1
kind: Service
metadata: { name: nexora-mgmt, namespace: nexora }
spec:
  selector: { app.kubernetes.io/name: nexora-mgmt }
  ports: [{ name: http, port: 8080, targetPort: http }]
---
apiVersion: v1
kind: Service
metadata: { name: nexora-mgmt-grpc, namespace: nexora }
spec:
  selector: { app.kubernetes.io/name: nexora-mgmt }
  ports: [{ name: grpc, port: 9443, targetPort: grpc }]
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: { name: nexora, namespace: nexora }
spec:
  ingressClassName: nginx
  rules:
    - host: nexora.kw.local
      http:
        paths:
          - {
              path: /,
              pathType: Prefix,
              backend: { service: { name: nexora-mgmt, port: { name: http } } },
            }
```

- [ ] Write `deploy/kw/otelcol.yaml` (`NEXORA_JAEGER_OTLP` is substituted by `scripts/kw-deploy.sh`):

```yaml
apiVersion: v1
kind: ConfigMap
metadata: { name: nexora-otelcol, namespace: nexora }
data:
  config.yaml: |
    receivers:
      otlp:
        protocols:
          grpc: {endpoint: "0.0.0.0:4317"}
    processors:
      batch: {timeout: 1s}
    exporters:
      debug: {verbosity: basic}
      opensearch:
        http: {endpoint: "http://opensearch.nexora.svc.cluster.local:9200"}
        logs_index: nexora-querylog
        logs_index_time_format: "yyyy.MM.dd"
      otlp/jaeger:
        endpoint: "NEXORA_JAEGER_OTLP"
        tls: {insecure: true}
    service:
      pipelines:
        logs: {receivers: [otlp], processors: [batch], exporters: [opensearch]}
        traces: {receivers: [otlp], processors: [batch], exporters: [otlp/jaeger]}
        metrics: {receivers: [otlp], processors: [batch], exporters: [debug]}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  {
    name: nexora-otelcol,
    namespace: nexora,
    labels: { app.kubernetes.io/name: nexora-otelcol },
  }
spec:
  replicas: 1
  selector: { matchLabels: { app.kubernetes.io/name: nexora-otelcol } }
  template:
    metadata: { labels: { app.kubernetes.io/name: nexora-otelcol } }
    spec:
      containers:
        - name: otelcol
          image: otel/opentelemetry-collector-contrib:0.160.0
          args: ["--config=/conf/config.yaml"]
          ports: [{ name: otlp-grpc, containerPort: 4317 }]
          resources:
            requests: { cpu: 100m, memory: 256Mi }
            limits: { cpu: "1", memory: 512Mi }
          volumeMounts: [{ name: conf, mountPath: /conf }]
      volumes:
        - name: conf
          configMap: { name: nexora-otelcol }
---
apiVersion: v1
kind: Service
metadata: { name: nexora-otelcol, namespace: nexora }
spec:
  selector: { app.kubernetes.io/name: nexora-otelcol }
  ports: [{ name: otlp-grpc, port: 4317, targetPort: otlp-grpc }]
```

- [ ] Write `deploy/kw/engine.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata: { name: nexora-engine-config, namespace: nexora }
data:
  engine.toml.tmpl: |
    node_name = "NODE_NAME"
    state_dir = "/var/lib/nexora"
    management_urls = ["https://nexora-mgmt-grpc.nexora.svc.cluster.local:9443"]
    join_token_file = "/etc/nexora/join/join-token"
    listen_udp = ["0.0.0.0:53"]
    listen_tcp = ["0.0.0.0:53"]
    metrics_listen = "0.0.0.0:9153"
    workers = 2
---
apiVersion: apps/v1
kind: Deployment
metadata:
  {
    name: nexora-engine,
    namespace: nexora,
    labels: { app.kubernetes.io/name: nexora-engine },
  }
spec:
  replicas: 3
  selector: { matchLabels: { app.kubernetes.io/name: nexora-engine } }
  template:
    metadata: { labels: { app.kubernetes.io/name: nexora-engine } }
    spec:
      imagePullSecrets: [{ name: nexus-pull }]
      securityContext:
        sysctls: [{ name: net.ipv4.ip_unprivileged_port_start, value: "0" }]
      affinity:
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - topologyKey: kubernetes.io/hostname
              labelSelector:
                { matchLabels: { app.kubernetes.io/name: nexora-engine } }
      initContainers:
        - name: render-config
          image: busybox:1.37
          command:
            [
              "sh",
              "-c",
              'sed "s/NODE_NAME/$(echo "$POD_NAME" | cut -c1-63)/" /tmpl/engine.toml.tmpl > /etc/nexora/engine.toml',
            ]
          env:
            [
              {
                name: POD_NAME,
                valueFrom: { fieldRef: { fieldPath: metadata.name } },
              },
            ]
          volumeMounts:
            - { name: tmpl, mountPath: /tmpl }
            - { name: config, mountPath: /etc/nexora }
      containers:
        - name: engine
          image: 192.168.10.131/azrtydxb/nexora-engine:NEXORA_TAG
          args: ["--config", "/etc/nexora/engine.toml"]
          ports:
            - { name: dns-udp, containerPort: 53, protocol: UDP }
            - { name: dns-tcp, containerPort: 53, protocol: TCP }
            - { name: metrics, containerPort: 9153 }
          readinessProbe: { tcpSocket: { port: dns-tcp }, periodSeconds: 5 }
          livenessProbe:
            { httpGet: { path: /metrics, port: metrics }, periodSeconds: 10 }
          resources:
            requests: { cpu: 500m, memory: 256Mi }
            limits: { cpu: "2", memory: 1Gi }
          securityContext:
            {
              runAsNonRoot: true,
              runAsUser: 10001,
              allowPrivilegeEscalation: false,
              readOnlyRootFilesystem: true,
              capabilities: { drop: [ALL] },
            }
          volumeMounts:
            - { name: config, mountPath: /etc/nexora, readOnly: true }
            - { name: join, mountPath: /etc/nexora/join, readOnly: true }
            - { name: state, mountPath: /var/lib/nexora }
      volumes:
        - name: tmpl
          configMap: { name: nexora-engine-config }
        - name: config
          emptyDir: {}
        - name: join
          secret: { secretName: nexora-join-token }
        - name: state
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata: { name: nexora-dns, namespace: nexora }
spec:
  type: LoadBalancer
  externalTrafficPolicy: Local
  selector: { app.kubernetes.io/name: nexora-engine }
  ports:
    - { name: dns-udp, port: 53, targetPort: dns-udp, protocol: UDP }
    - { name: dns-tcp, port: 53, targetPort: dns-tcp, protocol: TCP }
---
apiVersion: v1
kind: Service
metadata: { name: nexora-engine-metrics, namespace: nexora }
spec:
  selector: { app.kubernetes.io/name: nexora-engine }
  ports: [{ name: metrics, port: 9153, targetPort: metrics }]
```

- [ ] Write `scripts/kw-deploy.sh`:

```bash
#!/usr/bin/env bash
# Deploy Nexora M1 to the kw cluster (namespace nexora) and bootstrap it.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
ctx=kw; ns=nexora
tag="m1-$(git -C "$root" rev-parse --short HEAD)"
pwfile=""
while [ $# -gt 0 ]; do
	case "$1" in
	--tag) tag="$2"; shift 2 ;;
	--admin-password-file) pwfile="$2"; shift 2 ;;
	*) echo "usage: $0 [--tag TAG] --admin-password-file FILE" >&2; exit 2 ;;
	esac
done
[ -n "$pwfile" ] && [ -r "$pwfile" ] || { echo "--admin-password-file is required" >&2; exit 2; }
k() { kubectl --context "$ctx" -n "$ns" "$@"; }
api="http://nexora.kw.local"
jaeger_otlp="${NEXORA_KW_JAEGER_OTLP:?set NEXORA_KW_JAEGER_OTLP to the Jaeger OTLP gRPC host:port in namespace observability (the host found in Task 18)}"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

"$root/scripts/build-image.sh" -f deploy/docker/engine.Dockerfile -n nexora-engine -t "$tag" "$root"
"$root/scripts/build-image.sh" -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t "$tag" "$root"

kubectl --context "$ctx" apply -f "$root/deploy/kw/namespace.yaml"
kubectl --context "$ctx" -n novaforge-dev get secret nexus-pull -o json \
	| jq 'del(.metadata.namespace,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.metadata.ownerReferences)' \
	| k apply -f -
k apply -f "$root/deploy/kw/opensearch.yaml" -f "$root/deploy/kw/cnpg-cluster.yaml"
k wait --for=condition=Ready cluster/nexora-db --timeout=15m

if ! k get secret nexora-ca >/dev/null 2>&1; then
	"$root/scripts/dev-exec.sh" bash -c 'rm -rf /tmp/nexora-ca && go run ./mgmt/cmd/nexora-mgmt ca init --out /tmp/nexora-ca'
	kubectl --context "$ctx" -n nexora-dev exec deploy/toolbox -c toolbox -- tar -C /tmp/nexora-ca -cf - ca.crt ca.key | tar -C "$tmp" -xf -
	k create secret generic nexora-ca --from-file=ca.crt="$tmp/ca.crt" --from-file=ca.key="$tmp/ca.key"
fi

k apply -f "$root/deploy/kw/mgmt-grpc-lb.yaml"
k wait --for=jsonpath='{.status.loadBalancer.ingress[0].ip}' service/nexora-mgmt-grpc-lb --timeout=5m
grpc_ip=$(k get service nexora-mgmt-grpc-lb -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
sed -e "s/NEXORA_TAG/$tag/" -e "s/NEXORA_GRPC_LB_IP/$grpc_ip/" "$root/deploy/kw/mgmt.yaml" | k apply -f -
k rollout status deployment/nexora-mgmt --timeout=10m

jar="$tmp/cookies"
if curl -fsS "$api/api/v1/setup" | jq -e '.required' >/dev/null; then
	token=$(k logs -l app.kubernetes.io/name=nexora-mgmt --tail=-1 | sed -n 's/.*setup token: \([^ ]*\).*/\1/p' | head -1)
	[ -n "$token" ] || { echo "setup token not found in nexora-mgmt logs" >&2; exit 1; }
	jq -n --arg t "$token" --rawfile p "$pwfile" '{token:$t, username:"admin", email:"admin@kw.local", password:($p|rtrimstr("\n"))}' \
		| curl -fsS -c "$jar" -H 'Content-Type: application/json' -d @- "$api/api/v1/setup" >/dev/null
else
	jq -n --rawfile p "$pwfile" '{username:"admin", password:($p|rtrimstr("\n"))}' \
		| curl -fsS -c "$jar" -H 'Content-Type: application/json' -d @- "$api/api/v1/auth/login" >/dev/null
fi
call() { curl -fsS -b "$jar" -H 'Content-Type: application/json' "$@"; }

if [ "$(call "$api/api/v1/upstreams" | jq length)" = "0" ]; then
	call -d '{"name":"cloudflare","protocol":"udp","address":"1.1.1.1:53","timeout_ms":250,"enabled":true,"position":0}' "$api/api/v1/upstreams" >/dev/null
	call -d '{"name":"quad9","protocol":"udp","address":"9.9.9.9:53","timeout_ms":250,"enabled":true,"position":1}' "$api/api/v1/upstreams" >/dev/null
fi
if ! k get secret nexora-join-token >/dev/null 2>&1; then
	join=$(call -d '{"name":"kw-engines","ttl_seconds":31536000}' "$api/api/v1/join-tokens" | jq -r .token)
	k create secret generic nexora-join-token --from-literal=join-token="$join"
fi

sed "s|NEXORA_JAEGER_OTLP|$jaeger_otlp|" "$root/deploy/kw/otelcol.yaml" | k apply -f -
sed "s/NEXORA_TAG/$tag/" "$root/deploy/kw/engine.yaml" | k apply -f -
k rollout status deployment/nexora-otelcol --timeout=5m
k rollout status deployment/nexora-engine --timeout=10m
k wait --for=jsonpath='{.status.loadBalancer.ingress[0].ip}' service/nexora-dns --timeout=5m
dns_ip=$(k get service nexora-dns -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
echo "NEXORA_KW_DNS_ADDR=${dns_ip}:53"
echo "NEXORA_KW_API_URL=${api}"
```

- [ ] Deploy: `chmod +x scripts/kw-deploy.sh && NEXORA_KW_JAEGER_OTLP=<jaeger host>:4317 scripts/kw-deploy.sh --admin-password-file ~/.config/nexora/kw-admin-password` — expect the final two lines `NEXORA_KW_DNS_ADDR=<ip>:53` and `NEXORA_KW_API_URL=http://nexora.kw.local`, and `kubectl --context kw -n nexora get pods` shows 2 `nexora-mgmt`, 3 `nexora-engine`, 1 `nexora-otelcol`, 1 `opensearch-0` and 2 `nexora-db-*` pods `Running`.
- [ ] Run the smoke test from the dev pod with the printed values: `scripts/dev-exec.sh env NEXORA_KW_DNS_ADDR=<ip>:53 NEXORA_KW_API_URL=http://nexora.kw.local go test -count=1 -v -run TestKwSmoke ./e2e/` — expect PASS: `--- PASS: TestKwSmoke`.
- [ ] Commit: `git add deploy/kw scripts/kw-deploy.sh e2e/kw_smoke_test.go e2e/main_test.go && git commit -m "deploy: first Nexora deployment on kw with smoke test"`.
