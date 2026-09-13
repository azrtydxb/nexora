# nexora-v1-m2 — implementation plan

Status: draft
Spec: .procoder/specs/nexora-v1.md

## Goal

Ship M2 "Client transports + policy": DoT, DoH and DoQ listeners with certificates delivered in memory over the control stream and rotated without restart, per-client policy groups selected by most-specific CIDR, safe-search enforcement and custom DNS rewrites, with API, GUI, kw deployment and the acceptance tests `TestEncryptedTransports`, `TestPerClientPolicy` and `TestSafeSearchRewrites` green.

## Architecture

The engine gains three encrypted listeners (`engine/src/server/{dot,doh,doq}.rs`) that run on the existing per-core workers and feed every decoded DNS message through one transport-agnostic `Answerer` (the same path M1's TCP listener uses), with a shared `CertStore` implementing rustls `ResolvesServerCert` so a `TlsMaterial` message received on the mTLS control stream swaps the certificate for all new handshakes atomically and never touches disk. Policy lives in `engine/src/filter.rs` as a `PolicyTable` (longest-prefix CIDR match to an `EffectivePolicy` of blocklists, allowlist and a merged `RewriteTable`); the management plane stores groups, rewrites and safe-search settings in PostgreSQL and expands safe-search into shared, deduplicated `RewriteSet`s in the snapshot, so the engine only knows rewrites. The management plane loads the DNS serving certificate from files, pushes it to engines, records per-engine acceptance, and exposes everything through new OpenAPI operations rendered on `/policies`, `/rewrites` and a Settings TLS section.

## Constraints

Copied verbatim from `.procoder/specs/nexora-v1.md`:

- DNS engine: Rust, hickory-proto used as wire codec only; server loop, cache and resolver are Nexora code.
- Platform: engine targets Linux only (free to use SO_REUSEPORT, recvmmsg, io_uring and other Linux-specific APIs). Management plane and GUI ship as Linux builds/containers.
- Management plane: Go.
- GUI: React on Vite (not Next.js).
- Performance gate is two-tier: every PR runs a relative dnsperf regression benchmark on a CI runner (fails on > 5% drop vs main's baseline); the absolute 1M QPS / p99 < 500 us gate runs on a self-hosted reference box nightly and before every release tag.
- Management plane is stateless: all state in PostgreSQL, any number of instances behind a load balancer, engines may connect to any instance.
- The query path never logs synchronously, never touches a database, and never performs per-packet heap allocation on the cache-hit path.
- Engine local state: last applied config snapshot on disk (without key material); in-memory cache; DNSSEC trust anchors.
- Filtering: blocklists with millions of entries; wildcard/subdomain matches; allowlist beats blocklist; per-client policy beats global; invalid lines in a list are skipped and counted, not fatal; CNAME-cloaked trackers (blocked name reached via CNAME) are blocked.
- Encrypted transports: DoH GET and POST, HTTP/2 multiplexed streams; DoQ one stream per query; TLS certificate rotation without restart.
- Concurrent edits: two operators editing the same zone or policy get optimistic-concurrency conflicts, not lost writes.
- [S-6] The same query succeeds over DoT, DoH (GET and POST) and DoQ — `TestEncryptedTransports`; fails if any transport returns a different answer than UDP.
- [S-10] Two clients in different CIDR groups get different filtering results for the same name — `TestPerClientPolicy`; fails if both get the same answer.
- [S-11] With safe search on, `www.google.com` resolves to the SafeSearch address, and a custom rewrite returns its configured record — `TestSafeSearchRewrites`; fails if the original address is returned.
- [S-15] Every API resource in the milestone being shipped has a GUI screen exercised by a Playwright test — `TestGUICoverage` compares OpenAPI operations to Playwright-covered routes; fails if an operation has no covering test.

Copied verbatim from `docs/architecture.md`:

- No logging, no allocation, no locks held across packets on the cache-hit path. Enforced by `cache_hit_path_does_not_allocate` (counting allocator).
- Config is read through `ArcSwap<Runtime>::load()` once per packet.
- Listen addresses are host concerns and are not part of the snapshot.
- Secrets come from files, never from the database in plaintext.
- HTTP API: `/api/v1`, OpenAPI 3.1 at `mgmt/api/openapi.yaml`, JSON errors `{"code": "...", "message": "..."}`. Editable resources carry `revision`; a stale revision returns 409 `conflict`.
- Every config mutation runs in one transaction: change rows -> write audit row -> build snapshot -> insert `config_versions` -> `pg_notify('nexora_config', version)`.
- Mutating requests must send `Content-Type: application/json` (CSRF defence with SameSite=Lax).
- Roles: `viewer` (read everything except users, tokens, audit), `operator` (+ DNS configuration mutations), `admin` (everything). Permissions keyed by OpenAPI operationId in `mgmt/internal/auth/permissions.go`.
- Every test that asserts "does not happen" first asserts the positive path in the same run, so a harness failure cannot pass a negative check.
- All builds and tests run in the dev pod on kw (`deploy/dev/`); source is synced with `scripts/dev-sync.sh`, commands run with `scripts/dev-exec.sh <cmd>`.

Pinned versions for M2: Rust 1.97, tokio 1.53, rustls 0.23.44, tokio-rustls 0.26.5, hyper 1.11 (http2 server), hyper-util 0.1.20, quinn =0.11.11, hickory-proto 0.26.3, Go 1.27, github.com/miekg/dns v1.1.73, github.com/quic-go/quic-go v0.62.0, @playwright/test 1.63.0.

Plan-wide rules:

- Every command below runs after `scripts/dev-sync.sh`, as `scripts/dev-exec.sh <cmd>` from `/work/nexora` in the dev pod.
- Names consumed from M1 (listed under each task's "Consumed from M1") are the names in `.procoder/plans/nexora-v1-m1.md`; names M1 had not yet published when this plan was written are marked "(M1, later task)". Where M1's merged code spells one differently, the M1 code's name is used and the M2 behaviour written here stays unchanged.
- M2 owns protobuf field numbers 300-399 in every M1 message it extends (`ConfigSnapshot`, `Hello`, and both `Connect` envelope `oneof msg`s); M3 uses 100-199 in `ConfigSnapshot`/`Stats`, M4 200-299, so parallel milestones cannot collide. New M2 messages number from 1.
- Engine commands use the workspace form `cargo test --locked -p nexora-engine ...`.
- The DNS serving private key exists only in management-plane files (`NEXORA_DNS_TLS_KEY_FILE`), in memory on management instances, in the mTLS control stream, and in engine memory. It is never written to PostgreSQL, to `state_dir`, or to logs.

## Task 1: Control contract additions and architecture update

Files:
- `proto/nexora/control/v1/control.proto` — M2 messages and fields.
- `gen/go/nexora/control/v1/control.pb.go`, `gen/go/nexora/control/v1/control_grpc.pb.go` — regenerated by `make proto`.
- `mgmt/internal/control/contract_m2_test.go` — field-number and key-isolation guard test.
- `docs/architecture.md` — M2 decisions recorded (engine.toml keys, env vars, modules, proto numbering).

Interfaces:
- Consumed from M1 (Task 2): messages `ConfigSnapshot`, `Hello`, `ServerMessage` (oneof `msg`), `EngineMessage` (oneof `msg`), `BlobRef`, `FilterConfig` (its `blocklists`/`allowlists` stay the global selection).
- Produced (Go, package `controlv1` at `github.com/piwi3910/nexora/gen/go/nexora/control/v1`): `PolicyGroup`, `RewriteSet`, `RewriteRule`, `RewriteType` (`RewriteType_REWRITE_TYPE_A`, `_AAAA`, `_CNAME`), `TlsMaterial`, `TlsMaterialResult`; `ConfigSnapshot.PolicyGroups`, `.RewriteSets`, `.GlobalRewriteSetIds`; `Hello.TlsFingerprintSha256`; `ServerMessage_TlsMaterial`; `EngineMessage_TlsMaterialResult`.
- Produced (Rust, prost module `crate::proto` as generated by M1's `build.rs`): the same types with snake_case fields; `server_message::Msg::TlsMaterial(TlsMaterial)`, `engine_message::Msg::TlsMaterialResult(TlsMaterialResult)`.

- [ ] Write the failing guard test `mgmt/internal/control/contract_m2_test.go`:

```go
package control_test

import (
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestM2ContractFieldNumbers(t *testing.T) {
	snap := (&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor()
	hello := (&controlv1.Hello{}).ProtoReflect().Descriptor()
	srv := (&controlv1.ServerMessage{}).ProtoReflect().Descriptor()
	eng := (&controlv1.EngineMessage{}).ProtoReflect().Descriptor()
	cases := []struct {
		msg   protoreflect.MessageDescriptor
		field protoreflect.Name
		num   protoreflect.FieldNumber
	}{
		{snap, "policy_groups", 300},
		{snap, "rewrite_sets", 301},
		{snap, "global_rewrite_set_ids", 302},
		{hello, "tls_fingerprint_sha256", 300},
		{srv, "tls_material", 300},
		{eng, "tls_material_result", 300},
	}
	for _, c := range cases {
		f := c.msg.Fields().ByName(c.field)
		if f == nil {
			t.Fatalf("%s.%s missing", c.msg.FullName(), c.field)
		}
		if f.Number() != c.num {
			t.Errorf("%s.%s = %d, want %d", c.msg.FullName(), c.field, f.Number(), c.num)
		}
	}
}

// The snapshot is persisted on engine disk; it must never be able to carry key material.
func TestSnapshotCannotReachTlsMaterial(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var walk func(m protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if seen[m.FullName()] {
			return
		}
		seen[m.FullName()] = true
		if m.FullName() == "nexora.control.v1.TlsMaterial" {
			t.Fatalf("ConfigSnapshot reaches TlsMaterial")
		}
		fields := m.Fields()
		for i := 0; i < fields.Len(); i++ {
			if sub := fields.Get(i).Message(); sub != nil {
				walk(sub)
			}
		}
	}
	walk((&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor())
	if seen["nexora.control.v1.PolicyGroup"] != true {
		t.Fatalf("walk did not reach PolicyGroup; the positive path is broken")
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run 'TestM2ContractFieldNumbers|TestSnapshotCannotReachTlsMaterial'` — expect FAIL with `undefined` build errors naming `policy_groups` or with "nexora.control.v1.ConfigSnapshot.policy_groups missing".
- [ ] Append the M2 messages to `proto/nexora/control/v1/control.proto`:

```proto
// ---------------------------------------------------------------------------
// M2: client transports + policy. Fields added to M1 messages use numbers 300-399.
// ---------------------------------------------------------------------------

// A client group selected by source CIDR. Most specific CIDR across all groups wins;
// a client in a group gets only the group's blocklists, allowlist and rewrite sets
// (the global FilterConfig and global rewrite sets do not apply to it).
message PolicyGroup {
  string id = 1;                        // policy_groups.id (UUID)
  string name = 2;
  repeated string cidrs = 3;            // canonical prefixes, host bits zero, e.g. "10.1.0.0/16"
  repeated BlobRef blocklists = 4;      // normalised list blobs (same format as FilterConfig.blocklists), fetched via GetBlob
  repeated string allowlist = 5;        // lowercase punycode domains, no trailing dot; matches subdomains
  repeated string rewrite_set_ids = 6;  // RewriteSet.id values, in precedence order
}

enum RewriteType {
  REWRITE_TYPE_UNSPECIFIED = 0;
  REWRITE_TYPE_A = 1;
  REWRITE_TYPE_AAAA = 2;
  REWRITE_TYPE_CNAME = 3;
}

message RewriteRule {
  string name = 1;       // lowercase punycode, no trailing dot; "*." prefix = strict subdomains of the rest
  RewriteType type = 2;
  string value = 3;      // IPv4 text, IPv6 text, or CNAME target domain (no trailing dot)
  uint32 ttl = 4;        // 0..=86400
}

// Ids: "custom:global", "custom:group:<uuid>", "safesearch:google", "safesearch:bing",
// "safesearch:duckduckgo", "safesearch:youtube-strict", "safesearch:youtube-moderate".
message RewriteSet {
  string id = 1;
  string label = 2;
  repeated RewriteRule rules = 3;
}

// DNS serving certificate for DoT/DoH/DoQ. Sent only on the Connect stream; held in
// engine memory only; never part of ConfigSnapshot.
message TlsMaterial {
  bytes certificate_chain_pem = 1;  // leaf first
  bytes private_key_pem = 2;        // PKCS#8, SEC1 or PKCS#1 PEM
  string fingerprint_sha256 = 3;    // lowercase hex SHA-256 of the leaf DER
}

message TlsMaterialResult {
  string fingerprint_sha256 = 1;    // echoes TlsMaterial.fingerprint_sha256
  bool applied = 2;
  string error = 3;                 // empty when applied
}
```

- [ ] Add these fields inside the existing M1 messages in the same file: in `message ConfigSnapshot` add `repeated PolicyGroup policy_groups = 300;`, `repeated RewriteSet rewrite_sets = 301;`, `repeated string global_rewrite_set_ids = 302;` (rewrite sets for clients in no group; M1's `FilterConfig` stays the global filter selection); in `message Hello` add `string tls_fingerprint_sha256 = 300;` (empty when the engine holds no certificate); inside `oneof msg` of `ServerMessage` add `TlsMaterial tls_material = 300;`; inside `oneof msg` of `EngineMessage` add `TlsMaterialResult tls_material_result = 300;`.
- [ ] Run `scripts/dev-exec.sh make proto` — expect exit 0 and changes in `gen/go/nexora/control/v1/control.pb.go`; blob fetching for `PolicyGroup.blocklists` reuses M1's `BlobSource`/GetBlob path (Task 6 wires it).
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run 'TestM2ContractFieldNumbers|TestSnapshotCannotReachTlsMaterial'` — expect PASS.
- [ ] Run `scripts/dev-exec.sh cargo build --locked -p nexora-engine` — expect exit 0 (prost regenerates via `build.rs`; existing matches on `server_message::Msg` gain a `TlsMaterial(_) => {}` arm so the build stays exhaustive until Task 3 handles it).
- [ ] Edit `docs/architecture.md`: under "Bootstrap file" add the keys `listen_dot`, `listen_doh`, `listen_doq`, `doh_path`, `proxy_protocol_dot`, `proxy_protocol_doh`, `proxy_protocol_trusted_cidrs`, `tls_cert_file`, `tls_key_file` exactly as written in Task 2; in the repository layout add `src/server/{stream,proxy,tls,rewrite}.rs`; under "Management plane" add `NEXORA_DNS_TLS_CERT_FILE`, `NEXORA_DNS_TLS_KEY_FILE`, `NEXORA_DNS_TLS_RELOAD_INTERVAL` (`30s`) and the CLI `nexora-mgmt ca issue-dns`; under "Contract" add the sentence "M2 fields added to M1 messages use numbers 300-399; `TlsMaterial` travels only on `Connect` and is held in engine memory"; add the M2 metric names listed in Task 4 and Task 6 to the Telemetry list.
- [ ] Commit: `git add proto gen/go mgmt/internal/control/contract_m2_test.go docs/architecture.md engine/src && git commit -m "feat(proto): M2 policy, rewrite and TLS material contract"`.

## Task 2: Engine listener config, PROXY v2 parser and the shared stream server

Files:
- `engine/Cargo.toml` — add `ipnet = "2.12.2"`.
- `engine/src/bootstrap.rs` — M1 `Bootstrap` gains the M2 listener keys and their validation.
- `engine/src/edns.rs` — M1 `Transport` gains `Dot`, `Doh`, `Doq`.
- `engine/src/telemetry/metrics.rs` — `TRANSPORT_SLOTS` becomes 5 and the transport label list gains `dot`, `doh`, `doq`.
- `engine/src/server/mod.rs` — `ClientInfo`; trait `Answerer`; `WorkerAnswerer` (M1 pipeline behind `Answerer`); module declarations `stream`, `proxy`, `testutil`.
- `engine/src/server/proxy.rs` — PROXY protocol v2 header parser and trusted-peer policy.
- `engine/src/server/stream.rs` — length-prefixed pipelined DNS stream server generic over the I/O type.
- `engine/src/server/tcp.rs` — M1 `run_tcp` connection loop replaced by a call to `stream::serve_dns_stream`.
- `engine/src/server/testutil.rs` — `#[cfg(test)]` `EchoAnswerer` and `test_query` shared by Tasks 2, 4, 5.

Interfaces:
- Consumed from M1 (Task 3): `edns::Transport { Udp, Tcp }`, `edns::reply_limit`. (Task 8): `bootstrap::{Bootstrap, load}` (serde, `deny_unknown_fields`); `server::{Shared, WorkerCtx, handle_packet, resolve_miss, FastOutcome, MissJob}` with `handle_packet(ctx: &WorkerCtx, rt: &Runtime, packet: &[u8], client: SocketAddr, transport: Transport, out: &mut [u8]) -> FastOutcome` and `resolve_miss(ctx: Rc<WorkerCtx>, rt: Arc<Runtime>, job: MissJob) -> Vec<u8>`; `tcp::{bind_tcp, run_tcp, IDLE_TIMEOUT}`; `telemetry::metrics::{TRANSPORT_SLOTS, WorkerCounters::observe}`.
- Produced:
  - `edns::Transport { Udp, Tcp, Dot, Doh, Doq }` with `pub const fn label(self) -> &'static str` returning `udp|tcp|dot|doh|doq` and `pub const fn slot(self) -> usize` (0..=4). `reply_limit` returns 65535 for every non-`Udp` transport.
  - `#[derive(Clone, Copy, Debug)] pub struct ClientInfo { pub addr: SocketAddr, pub transport: Transport }`.
  - `pub trait Answerer { async fn answer(&self, client: ClientInfo, query: &[u8], out: &mut Vec<u8>); }` — clears `out`, writes the full response; leaves `out` empty when the message must be dropped.
  - `pub struct WorkerAnswerer(pub Rc<WorkerCtx>);` with `impl Answerer for WorkerAnswerer`.
  - `pub async fn read_proxy_v2<S: AsyncRead + Unpin>(io: &mut S) -> Result<ProxyHeader, ProxyError>`; `pub enum ProxyHeader { Local, Proxied { source: SocketAddr, destination: SocketAddr } }`; `pub enum ProxyError { BadSignature, Version, Command, Family, TooLong, Io }`.
  - `pub struct ProxyPolicy` with `pub fn new(cidrs: &[String]) -> Result<Self, String>`; `pub async fn resolve_client<S: AsyncRead + Unpin>(policy: Option<&ProxyPolicy>, io: &mut S, peer: SocketAddr) -> Result<SocketAddr, ProxyReject>`; `pub enum ProxyReject { UntrustedPeer, InvalidHeader, Timeout }`.
  - `pub async fn serve_dns_stream<A, S>(answerer: Rc<A>, io: S, client: ClientInfo, idle: Duration) where A: Answerer + 'static, S: AsyncRead + AsyncWrite + 'static`.
  - `pub fn normalize_peer(addr: SocketAddr) -> SocketAddr` — IPv4-mapped IPv6 becomes IPv4.
  - `Bootstrap` fields `listen_dot`, `listen_doh`, `listen_doq`, `doh_path`, `proxy_protocol_dot`, `proxy_protocol_doh`, `proxy_protocol_trusted_cidrs`, `tls_cert_file`, `tls_key_file`.

- [ ] Write `engine/src/server/testutil.rs`:

```rust
#![cfg(test)]
use super::{Answerer, ClientInfo};
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{rdata::A, Name, RData, Record, RecordType};
use std::net::{IpAddr, Ipv4Addr};

/// Answers every query with `<qname> 300 A 192.0.2.1` and, for IPv4 clients,
/// `<qname> 60 A <client ip>` so tests can observe the client address the transport resolved.
pub struct EchoAnswerer;

impl Answerer for EchoAnswerer {
    async fn answer(&self, client: ClientInfo, query: &[u8], out: &mut Vec<u8>) {
        out.clear();
        if query.len() < 12 {
            return;
        }
        let q = Message::from_vec(query).expect("test query parses");
        let mut m = Message::response(q.metadata.id, OpCode::Query);
        m.metadata.recursion_desired = q.metadata.recursion_desired;
        m.metadata.recursion_available = true;
        m.queries = q.queries.clone();
        let name = q.queries[0].name().clone();
        m.answers.push(Record::from_rdata(name.clone(), 300, RData::A(A(Ipv4Addr::new(192, 0, 2, 1)))));
        if let IpAddr::V4(v4) = client.addr.ip() {
            m.answers.push(Record::from_rdata(name, 60, RData::A(A(v4))));
        }
        out.extend_from_slice(&m.to_vec().expect("encode"));
    }
}

pub fn test_query(id: u16, name: &str) -> Vec<u8> {
    let mut m = Message::new(id, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.queries.push(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    m.to_vec().unwrap()
}
```

- [ ] Write the failing tests at the bottom of `engine/src/server/proxy.rs` (create the file with only `use` lines and this module first):

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::AsyncReadExt;

    fn header(cmd_ver: u8, fam: u8, body: &[u8]) -> Vec<u8> {
        let mut h = SIGNATURE.to_vec();
        h.push(cmd_ver);
        h.push(fam);
        h.extend_from_slice(&(body.len() as u16).to_be_bytes());
        h.extend_from_slice(body);
        h
    }

    #[tokio::test]
    async fn parses_tcp4_and_leaves_payload_unread() {
        let mut data = header(0x21, 0x11, &[192, 0, 2, 7, 10, 0, 0, 1, 0xd4, 0x31, 0x03, 0x55]);
        data.extend_from_slice(b"\x00\x1d");
        let mut cur = std::io::Cursor::new(data);
        let h = read_proxy_v2(&mut cur).await.unwrap();
        assert_eq!(h, ProxyHeader::Proxied {
            source: "192.0.2.7:54321".parse().unwrap(),
            destination: "10.0.0.1:853".parse().unwrap(),
        });
        let mut rest = Vec::new();
        cur.read_to_end(&mut rest).await.unwrap();
        assert_eq!(rest, b"\x00\x1d");
    }

    #[tokio::test]
    async fn parses_tcp6_with_tlvs() {
        let mut body = Vec::new();
        body.extend_from_slice(&"2001:db8::7".parse::<std::net::Ipv6Addr>().unwrap().octets());
        body.extend_from_slice(&"2001:db8::1".parse::<std::net::Ipv6Addr>().unwrap().octets());
        body.extend_from_slice(&[0x13, 0x88, 0x01, 0xbb]);
        body.extend_from_slice(&[0x04, 0x00, 0x01, 0xaa]); // NOOP TLV, ignored
        let mut cur = std::io::Cursor::new(header(0x21, 0x21, &body));
        assert_eq!(read_proxy_v2(&mut cur).await.unwrap(), ProxyHeader::Proxied {
            source: "[2001:db8::7]:5000".parse().unwrap(),
            destination: "[2001:db8::1]:443".parse().unwrap(),
        });
    }

    #[tokio::test]
    async fn local_command_and_errors() {
        let mut cur = std::io::Cursor::new(header(0x20, 0x00, &[]));
        assert_eq!(read_proxy_v2(&mut cur).await.unwrap(), ProxyHeader::Local);
        let mut bad = header(0x21, 0x11, &[0; 12]);
        bad[0] = b'X';
        assert_eq!(read_proxy_v2(&mut std::io::Cursor::new(bad)).await, Err(ProxyError::BadSignature));
        assert_eq!(read_proxy_v2(&mut std::io::Cursor::new(header(0x11, 0x11, &[0; 12]))).await, Err(ProxyError::Version));
        assert_eq!(read_proxy_v2(&mut std::io::Cursor::new(header(0x22, 0x11, &[0; 12]))).await, Err(ProxyError::Command));
        assert_eq!(read_proxy_v2(&mut std::io::Cursor::new(header(0x21, 0x12, &[0; 12]))).await, Err(ProxyError::Family));
        assert_eq!(read_proxy_v2(&mut std::io::Cursor::new(header(0x21, 0x11, &[0; 4]))).await, Err(ProxyError::Family));
        let truncated = header(0x21, 0x11, &[0; 12])[..20].to_vec();
        assert_eq!(read_proxy_v2(&mut std::io::Cursor::new(truncated)).await, Err(ProxyError::Io));
    }

    #[tokio::test]
    async fn policy_rejects_untrusted_peer_and_uses_header_source() {
        let policy = ProxyPolicy::new(&["10.0.0.0/8".to_string()]).unwrap();
        let data = header(0x21, 0x11, &[192, 0, 2, 7, 10, 0, 0, 1, 0xd4, 0x31, 0x03, 0x55]);
        let got = resolve_client(Some(&policy), &mut std::io::Cursor::new(data.clone()), "10.9.9.9:4000".parse().unwrap()).await;
        assert_eq!(got, Ok("192.0.2.7:54321".parse().unwrap()));
        let got = resolve_client(Some(&policy), &mut std::io::Cursor::new(data), "198.51.100.1:4000".parse().unwrap()).await;
        assert_eq!(got, Err(ProxyReject::UntrustedPeer));
        let got = resolve_client(None, &mut std::io::Cursor::new(Vec::new()), "[::ffff:198.51.100.1]:4000".parse().unwrap()).await;
        assert_eq!(got, Ok("198.51.100.1:4000".parse().unwrap()));
        assert!(ProxyPolicy::new(&[]).is_err());
    }
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::proxy` — expect FAIL with "cannot find function `read_proxy_v2` in this scope".
- [ ] Implement `engine/src/server/proxy.rs`:

```rust
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
use std::time::Duration;
use tokio::io::{AsyncRead, AsyncReadExt};

pub const SIGNATURE: [u8; 12] = *b"\r\n\r\n\0\r\nQUIT\n";
pub const HEADER_TIMEOUT: Duration = Duration::from_secs(5);
const MAX_BODY: usize = 536;

#[derive(Debug, PartialEq, Eq)]
pub enum ProxyHeader { Local, Proxied { source: SocketAddr, destination: SocketAddr } }

#[derive(Debug, PartialEq, Eq)]
pub enum ProxyError { BadSignature, Version, Command, Family, TooLong, Io }

#[derive(Debug, PartialEq, Eq)]
pub enum ProxyReject { UntrustedPeer, InvalidHeader, Timeout }

pub fn normalize_peer(addr: SocketAddr) -> SocketAddr {
    match addr.ip() {
        IpAddr::V6(v6) => match v6.to_ipv4_mapped() {
            Some(v4) => SocketAddr::new(IpAddr::V4(v4), addr.port()),
            None => addr,
        },
        IpAddr::V4(_) => addr,
    }
}

pub async fn read_proxy_v2<S: AsyncRead + Unpin>(io: &mut S) -> Result<ProxyHeader, ProxyError> {
    let mut fixed = [0u8; 16];
    io.read_exact(&mut fixed).await.map_err(|_| ProxyError::Io)?;
    if fixed[..12] != SIGNATURE { return Err(ProxyError::BadSignature); }
    if fixed[12] >> 4 != 2 { return Err(ProxyError::Version); }
    let cmd = fixed[12] & 0x0f;
    let fam = fixed[13];
    let len = u16::from_be_bytes([fixed[14], fixed[15]]) as usize;
    if len > MAX_BODY { return Err(ProxyError::TooLong); }
    let mut body = [0u8; MAX_BODY];
    io.read_exact(&mut body[..len]).await.map_err(|_| ProxyError::Io)?;
    match cmd {
        0x0 => return Ok(ProxyHeader::Local),
        0x1 => {}
        _ => return Err(ProxyError::Command),
    }
    let b = &body[..len];
    match fam {
        0x00 => Ok(ProxyHeader::Local),
        0x11 if len >= 12 => Ok(ProxyHeader::Proxied {
            source: SocketAddr::new(Ipv4Addr::new(b[0], b[1], b[2], b[3]).into(), u16::from_be_bytes([b[8], b[9]])),
            destination: SocketAddr::new(Ipv4Addr::new(b[4], b[5], b[6], b[7]).into(), u16::from_be_bytes([b[10], b[11]])),
        }),
        0x21 if len >= 36 => {
            let src: [u8; 16] = b[0..16].try_into().expect("16 bytes");
            let dst: [u8; 16] = b[16..32].try_into().expect("16 bytes");
            Ok(ProxyHeader::Proxied {
                source: SocketAddr::new(Ipv6Addr::from(src).into(), u16::from_be_bytes([b[32], b[33]])),
                destination: SocketAddr::new(Ipv6Addr::from(dst).into(), u16::from_be_bytes([b[34], b[35]])),
            })
        }
        _ => Err(ProxyError::Family),
    }
}

pub struct ProxyPolicy { trusted: Vec<ipnet::IpNet> }

impl ProxyPolicy {
    pub fn new(cidrs: &[String]) -> Result<Self, String> {
        if cidrs.is_empty() {
            return Err("proxy_protocol_trusted_cidrs must not be empty when PROXY protocol is enabled".into());
        }
        let trusted = cidrs.iter()
            .map(|c| c.parse::<ipnet::IpNet>().map_err(|_| format!("invalid proxy_protocol_trusted_cidrs entry {c}")))
            .collect::<Result<Vec<_>, _>>()?;
        Ok(Self { trusted })
    }
    fn trusts(&self, ip: IpAddr) -> bool { self.trusted.iter().any(|n| n.contains(&ip)) }
}

pub async fn resolve_client<S: AsyncRead + Unpin>(policy: Option<&ProxyPolicy>, io: &mut S, peer: SocketAddr) -> Result<SocketAddr, ProxyReject> {
    let peer = normalize_peer(peer);
    let Some(policy) = policy else { return Ok(peer) };
    if !policy.trusts(peer.ip()) { return Err(ProxyReject::UntrustedPeer); }
    match tokio::time::timeout(HEADER_TIMEOUT, read_proxy_v2(io)).await {
        Err(_) => Err(ProxyReject::Timeout),
        Ok(Err(_)) => Err(ProxyReject::InvalidHeader),
        Ok(Ok(ProxyHeader::Local)) => Ok(peer),
        Ok(Ok(ProxyHeader::Proxied { source, .. })) => Ok(normalize_peer(source)),
    }
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::proxy` — expect PASS (4 tests).
- [ ] Write the failing stream test at the bottom of `engine/src/server/stream.rs`:

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use crate::server::testutil::{test_query, EchoAnswerer};
    use crate::edns::Transport;
    use crate::server::ClientInfo;
    use hickory_proto::op::Message;
    use hickory_proto::rr::{rdata::A, RData};
    use std::net::Ipv4Addr;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[tokio::test(flavor = "current_thread")]
    async fn pipelined_queries_all_answered_on_one_stream() {
        tokio::task::LocalSet::new().run_until(async {
            let (client_io, server_io) = tokio::io::duplex(64 * 1024);
            let client = ClientInfo { addr: "192.0.2.9:5555".parse().unwrap(), transport: Transport::Dot };
            let server = tokio::task::spawn_local(serve_dns_stream(Rc::new(EchoAnswerer), server_io, client, Duration::from_secs(5)));
            let (mut rd, mut wr) = tokio::io::split(client_io);
            let mut want = Vec::new();
            for i in 0..10u16 {
                let q = test_query(0x1000 + i, "example.com.");
                wr.write_all(&(q.len() as u16).to_be_bytes()).await.unwrap();
                wr.write_all(&q).await.unwrap();
                want.push(0x1000 + i);
            }
            let mut got = Vec::new();
            for _ in 0..10 {
                let mut l = [0u8; 2];
                rd.read_exact(&mut l).await.unwrap();
                let mut b = vec![0u8; u16::from_be_bytes(l) as usize];
                rd.read_exact(&mut b).await.unwrap();
                let m = Message::from_vec(&b).unwrap();
                assert_eq!(m.answers[1].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 9))));
                got.push(m.metadata.id);
            }
            got.sort();
            assert_eq!(got, want);
            // a short message is dropped without a reply and a zero length closes the stream
            wr.write_all(&[0, 3, 1, 2, 3, 0, 0]).await.unwrap();
            server.await.unwrap();
            let mut rest = Vec::new();
            rd.read_to_end(&mut rest).await.unwrap();
            assert!(rest.is_empty());
        }).await;
    }

    #[tokio::test(flavor = "current_thread", start_paused = true)]
    async fn idle_stream_is_closed() {
        tokio::task::LocalSet::new().run_until(async {
            let (_client_io, server_io) = tokio::io::duplex(1024);
            let client = ClientInfo { addr: "192.0.2.9:5555".parse().unwrap(), transport: Transport::Dot };
            let server = tokio::task::spawn_local(serve_dns_stream(Rc::new(EchoAnswerer), server_io, client, Duration::from_secs(30)));
            tokio::time::advance(Duration::from_secs(31)).await;
            server.await.unwrap();
        }).await;
    }
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::stream` — expect FAIL with "cannot find function `serve_dns_stream` in this scope".
- [ ] In `engine/src/edns.rs` add `Dot`, `Doh`, `Doq` to `Transport` with `label()`/`slot()`; in `engine/src/telemetry/metrics.rs` set `TRANSPORT_SLOTS = 5` and render the `transport` label from `Transport::label`. In `engine/src/server/mod.rs` add `ClientInfo`, `Answerer`, and:

```rust
pub struct WorkerAnswerer(pub Rc<WorkerCtx>);

impl Answerer for WorkerAnswerer {
    async fn answer(&self, client: ClientInfo, query: &[u8], out: &mut Vec<u8>) {
        let rt = self.0.shared.runtime.load_full();
        out.clear();
        out.resize(65535, 0);
        match handle_packet(&self.0, &rt, query, client.addr, client.transport, out) {
            FastOutcome::Reply(n) => out.truncate(n),
            FastOutcome::Drop => out.clear(),
            FastOutcome::Miss(job) => {
                let reply = resolve_miss(self.0.clone(), rt, job).await;
                out.clear();
                out.extend_from_slice(&reply);
            }
        }
    }
}
```

and declare `pub mod stream; pub mod proxy; #[cfg(test)] pub mod testutil;`. `handle_packet` already records metrics and the query-log entry with the transport it is given.
- [ ] Implement `engine/src/server/stream.rs`:

```rust
use crate::server::{Answerer, ClientInfo};
use std::rc::Rc;
use std::time::Duration;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};

pub const MAX_PIPELINED: usize = 32;
const BODY_TIMEOUT: Duration = Duration::from_secs(5);

pub async fn serve_dns_stream<A, S>(answerer: Rc<A>, io: S, client: ClientInfo, idle: Duration)
where
    A: Answerer + 'static,
    S: AsyncRead + AsyncWrite + 'static,
{
    let (mut rd, mut wr) = tokio::io::split(io);
    let (tx, mut rx) = tokio::sync::mpsc::channel::<Vec<u8>>(MAX_PIPELINED);
    let writer = tokio::task::spawn_local(async move {
        while let Some(frame) = rx.recv().await {
            if wr.write_all(&frame).await.is_err() { break; }
            if rx.is_empty() && wr.flush().await.is_err() { break; }
        }
        let _ = wr.shutdown().await;
    });
    let permits = Rc::new(tokio::sync::Semaphore::new(MAX_PIPELINED));
    loop {
        let mut len = [0u8; 2];
        match tokio::time::timeout(idle, rd.read_exact(&mut len)).await {
            Ok(Ok(_)) => {}
            _ => break,
        }
        let n = u16::from_be_bytes(len) as usize;
        if n == 0 { break; }
        let mut msg = vec![0u8; n];
        match tokio::time::timeout(BODY_TIMEOUT, rd.read_exact(&mut msg)).await {
            Ok(Ok(_)) => {}
            _ => break,
        }
        let Ok(permit) = permits.clone().acquire_owned().await else { break };
        let answerer = answerer.clone();
        let tx = tx.clone();
        tokio::task::spawn_local(async move {
            let mut out = Vec::with_capacity(512);
            answerer.answer(client, &msg, &mut out).await;
            if !out.is_empty() && out.len() <= 65535 {
                let mut frame = Vec::with_capacity(out.len() + 2);
                frame.extend_from_slice(&(out.len() as u16).to_be_bytes());
                frame.extend_from_slice(&out);
                let _ = tx.send(frame).await;
            }
            drop(permit);
        });
    }
    drop(tx);
    let _ = writer.await;
}
```

- [ ] Replace the per-connection loop inside M1's `run_tcp` in `engine/src/server/tcp.rs` with `tokio::task::spawn_local(stream::serve_dns_stream(Rc::new(WorkerAnswerer(ctx.clone())), tcp_stream, ClientInfo { addr: proxy::normalize_peer(peer), transport: Transport::Tcp }, IDLE_TIMEOUT))`.
- [ ] Add to `Bootstrap` in `engine/src/bootstrap.rs`:

```rust
#[serde(default)] pub listen_dot: Vec<std::net::SocketAddr>,       // e.g. ["0.0.0.0:853"]
#[serde(default)] pub listen_doh: Vec<std::net::SocketAddr>,       // e.g. ["0.0.0.0:443"]
#[serde(default)] pub listen_doq: Vec<std::net::SocketAddr>,       // e.g. ["0.0.0.0:853"] (UDP)
#[serde(default = "default_doh_path")] pub doh_path: String,       // "/dns-query"
#[serde(default)] pub proxy_protocol_dot: bool,
#[serde(default)] pub proxy_protocol_doh: bool,
#[serde(default)] pub proxy_protocol_trusted_cidrs: Vec<String>,
#[serde(default)] pub tls_cert_file: String,                       // standalone mode only
#[serde(default)] pub tls_key_file: String,                        // standalone mode only
```

with `fn default_doh_path() -> String { "/dns-query".into() }`, and extend `bootstrap::load` validation so that it returns these exact errors: `doh_path must start with '/'`; `proxy_protocol_trusted_cidrs must not be empty when PROXY protocol is enabled` (when either flag is true and the list is empty, via `ProxyPolicy::new`); `tls_cert_file and tls_key_file must be set together`; `tls_cert_file is only valid with standalone_snapshot` (when `is_standalone()` is false).
- [ ] Add a unit test `bootstrap_rejects_proxy_without_trusted_cidrs` in `engine/src/bootstrap.rs` that writes to a temp file and loads with `bootstrap::load` `node_name = "e"\nstate_dir = "/tmp/x"\nstandalone_snapshot = "/tmp/s"\nlisten_dot = ["127.0.0.1:853"]\nproxy_protocol_dot = true\n` and asserts the error text contains `proxy_protocol_trusted_cidrs must not be empty`, then asserts the same text with `proxy_protocol_trusted_cidrs = ["10.0.0.0/8"]` added loads successfully.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine` — expect PASS for `server::stream`, `server::proxy`, `bootstrap_rejects_proxy_without_trusted_cidrs` and every M1 test including `cache_hit_path_does_not_allocate`.
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 ./e2e/ -run TestEDNSTruncationTCP'` — expect PASS (TCP behaviour unchanged by the refactor).
- [ ] Commit: `git add engine && git commit -m "feat(engine): transport-agnostic answerer, stream server, PROXY v2"`.

## Task 3: Engine certificate store and TlsMaterial over the control stream

Files:
- `engine/Cargo.toml` — add `x509-parser = "0.18.1"`, `zeroize = "1.9.0"`, `aws-lc-rs = "1.18.1"`; dev-dependency `rcgen = "0.14.10"`.
- `engine/src/server/tls.rs` — `CertStore` (in-memory certificate, `ResolvesServerCert`), rustls server config builders, PEM installation.
- `engine/src/server/mod.rs` — `pub mod tls;`; `spawn_workers` gains a `cert_store: Arc<CertStore>` parameter.
- `engine/src/control.rs` — handle `TlsMaterial`, reply `TlsMaterialResult`, send `Hello.tls_fingerprint_sha256`.
- `engine/src/main.rs` — create one `Arc<CertStore>` per process and pass it to `spawn_workers` and the control client; in standalone mode load `tls_cert_file`/`tls_key_file` at start and on `SIGHUP`.
- `engine/src/telemetry/metrics.rs` — `nexora_tls_certificate_not_after_seconds`, `nexora_tls_material_updates_total{result}`.

Interfaces:
- Consumed from M1: `control.rs` Connect stream loop that sends `EngineMessage`, builds `Hello` and matches `server_message::Msg` (M1, later task); the SIGHUP reload in standalone mode in `main.rs` and `bootstrap::Bootstrap::is_standalone` (Task 8); the Prometheus text renderer in `telemetry/metrics.rs` (Task 9).
- Consumed from Task 1: `proto::TlsMaterial`, `proto::TlsMaterialResult`, `engine_message::Msg::TlsMaterialResult`, `Hello.tls_fingerprint_sha256`.
- Produced:
  - `pub struct CertStore` with `pub fn new() -> Self`, `pub fn is_empty(&self) -> bool`, `pub fn fingerprint(&self) -> Option<String>`, `pub fn install_pem(&self, chain_pem: &[u8], key_pem: &[u8], now_unix: i64) -> Result<InstalledInfo, String>`, `pub fn install_material(&self, m: proto::TlsMaterial, now_unix: i64) -> proto::TlsMaterialResult`; `impl rustls::server::ResolvesServerCert for CertStore`.
  - `pub struct InstalledInfo { pub fingerprint_sha256: String, pub not_after_unix: i64 }`.
  - `pub fn provider() -> Arc<rustls::crypto::CryptoProvider>` (aws-lc-rs).
  - `pub fn stream_server_config(store: Arc<CertStore>, alpn: &[&[u8]]) -> Arc<rustls::ServerConfig>` (TLS 1.2 + 1.3).
  - `pub fn quic_server_config(store: Arc<CertStore>) -> quinn::ServerConfig` (added in Task 5).

- [ ] Write the failing tests at the bottom of `engine/src/server/tls.rs`:

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use rustls::pki_types::{CertificateDer, ServerName};
    use std::sync::Arc;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    fn self_signed(name: &str) -> (Vec<u8>, Vec<u8>, CertificateDer<'static>) {
        let ck = rcgen::generate_simple_self_signed(vec![name.to_string()]).unwrap();
        (ck.cert.pem().into_bytes(), ck.signing_key.serialize_pem().into_bytes(), ck.cert.der().clone())
    }

    fn now() -> i64 { std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() as i64 }

    #[test]
    fn install_rejects_mismatch_expired_and_bad_fingerprint() {
        let store = CertStore::new();
        assert!(store.is_empty());
        let (c1, k1, _) = self_signed("dns.test");
        let (_, k2, _) = self_signed("dns.test");
        assert_eq!(store.install_pem(&c1, &k2, now()).unwrap_err(), "private key does not match certificate");
        assert!(store.is_empty());
        let info = store.install_pem(&c1, &k1, now()).unwrap();
        assert_eq!(info.fingerprint_sha256.len(), 64);
        assert_eq!(store.fingerprint().as_deref(), Some(info.fingerprint_sha256.as_str()));

        let key = rcgen::KeyPair::generate().unwrap();
        let mut params = rcgen::CertificateParams::new(vec!["dns.test".to_string()]).unwrap();
        params.not_before = rcgen::date_time_ymd(2019, 1, 1);
        params.not_after = rcgen::date_time_ymd(2020, 1, 1);
        let old = params.self_signed(&key).unwrap();
        let err = store.install_pem(old.pem().as_bytes(), key.serialize_pem().as_bytes(), now()).unwrap_err();
        assert!(err.starts_with("certificate expired at "), "{err}");
        assert_eq!(store.fingerprint().as_deref(), Some(info.fingerprint_sha256.as_str()), "failed install keeps previous");

        let (c3, k3, _) = self_signed("dns.test");
        let r = store.install_material(proto::TlsMaterial {
            certificate_chain_pem: c3, private_key_pem: k3, fingerprint_sha256: "00".repeat(32),
        }, now());
        assert!(!r.applied);
        assert_eq!(r.error, "fingerprint mismatch");
    }

    #[tokio::test]
    async fn rotation_applies_to_new_handshakes_without_rebuilding_config() {
        let store = Arc::new(CertStore::new());
        let acceptor = tokio_rustls::TlsAcceptor::from(stream_server_config(store.clone(), &[b"dot"]));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            loop {
                let (tcp, _) = listener.accept().await.unwrap();
                let acceptor = acceptor.clone();
                tokio::spawn(async move {
                    if let Ok(mut s) = acceptor.accept(tcp).await {
                        let mut b = [0u8; 1];
                        let _ = s.read_exact(&mut b).await;
                        let _ = s.write_all(&b).await;
                    }
                });
            }
        });
        async fn handshake(addr: std::net::SocketAddr, root: &CertificateDer<'static>) -> Result<CertificateDer<'static>, std::io::Error> {
            let mut roots = rustls::RootCertStore::empty();
            roots.add(root.clone()).unwrap();
            let cfg = rustls::ClientConfig::builder_with_provider(provider()).with_safe_default_protocol_versions().unwrap()
                .with_root_certificates(roots).with_no_client_auth();
            let tcp = tokio::net::TcpStream::connect(addr).await?;
            let s = tokio_rustls::TlsConnector::from(Arc::new(cfg)).connect(ServerName::try_from("dns.test").unwrap(), tcp).await?;
            Ok(s.get_ref().1.peer_certificates().unwrap()[0].clone())
        }
        let (c1, k1, d1) = self_signed("dns.test");
        assert!(handshake(addr, &d1).await.is_err(), "no certificate installed: handshake must fail");
        store.install_pem(&c1, &k1, now()).unwrap();
        assert_eq!(handshake(addr, &d1).await.unwrap(), d1);
        let (c2, k2, d2) = self_signed("dns.test");
        store.install_pem(&c2, &k2, now()).unwrap();
        assert_eq!(handshake(addr, &d2).await.unwrap(), d2);
    }
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::tls` — expect FAIL with "cannot find type `CertStore` in this scope".
- [ ] Implement `engine/src/server/tls.rs`:

```rust
use crate::proto;
use arc_swap::ArcSwapOption;
use rustls::pki_types::{pem::PemObject, CertificateDer, PrivateKeyDer};
use rustls::server::{ClientHello, ResolvesServerCert};
use rustls::sign::CertifiedKey;
use std::sync::Arc;
use zeroize::Zeroize;

struct Installed { key: Arc<CertifiedKey>, fingerprint: String, not_after_unix: i64 }

pub struct CertStore { current: ArcSwapOption<Installed> }

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct InstalledInfo { pub fingerprint_sha256: String, pub not_after_unix: i64 }

impl std::fmt::Debug for CertStore {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("CertStore").field("fingerprint", &self.fingerprint()).finish()
    }
}

pub fn provider() -> Arc<rustls::crypto::CryptoProvider> {
    Arc::new(rustls::crypto::aws_lc_rs::default_provider())
}

impl CertStore {
    pub fn new() -> Self { Self { current: ArcSwapOption::empty() } }
    pub fn is_empty(&self) -> bool { self.current.load().is_none() }
    pub fn fingerprint(&self) -> Option<String> { self.current.load().as_ref().map(|i| i.fingerprint.clone()) }

    pub fn install_pem(&self, chain_pem: &[u8], key_pem: &[u8], now_unix: i64) -> Result<InstalledInfo, String> {
        let chain: Vec<CertificateDer<'static>> = CertificateDer::pem_slice_iter(chain_pem)
            .collect::<Result<_, _>>().map_err(|_| "invalid certificate PEM".to_string())?;
        let leaf = chain.first().ok_or_else(|| "no certificate in chain".to_string())?;
        let (_, parsed) = x509_parser::parse_x509_certificate(leaf.as_ref()).map_err(|_| "invalid certificate".to_string())?;
        let not_after_unix = parsed.validity().not_after.timestamp();
        if not_after_unix <= now_unix {
            return Err(format!("certificate expired at {not_after_unix}"));
        }
        let fingerprint = hex_lower(aws_lc_rs::digest::digest(&aws_lc_rs::digest::SHA256, leaf.as_ref()).as_ref());
        let key = PrivateKeyDer::from_pem_slice(key_pem).map_err(|_| "invalid private key".to_string())?;
        let certified = CertifiedKey::from_der(chain, key, &provider()).map_err(|e| match e {
            rustls::Error::InconsistentKeys(_) => "private key does not match certificate".to_string(),
            _ => "invalid private key".to_string(),
        })?;
        self.current.store(Some(Arc::new(Installed { key: Arc::new(certified), fingerprint: fingerprint.clone(), not_after_unix })));
        crate::telemetry::metrics::ENCRYPTED.set_tls_not_after(not_after_unix);
        Ok(InstalledInfo { fingerprint_sha256: fingerprint, not_after_unix })
    }

    pub fn install_material(&self, mut m: proto::TlsMaterial, now_unix: i64) -> proto::TlsMaterialResult {
        let result = match self.check_fingerprint(&m) {
            Err(e) => Err(e),
            Ok(()) => self.install_pem(&m.certificate_chain_pem, &m.private_key_pem, now_unix),
        };
        m.private_key_pem.zeroize();
        let applied = result.is_ok();
        crate::telemetry::metrics::ENCRYPTED.tls_update(applied);
        proto::TlsMaterialResult { fingerprint_sha256: m.fingerprint_sha256, applied, error: result.err().unwrap_or_default() }
    }

    fn check_fingerprint(&self, m: &proto::TlsMaterial) -> Result<(), String> {
        let leaf = CertificateDer::pem_slice_iter(&m.certificate_chain_pem).next()
            .ok_or_else(|| "no certificate in chain".to_string())?.map_err(|_| "invalid certificate PEM".to_string())?;
        let fp = hex_lower(aws_lc_rs::digest::digest(&aws_lc_rs::digest::SHA256, leaf.as_ref()).as_ref());
        if fp != m.fingerprint_sha256 { return Err("fingerprint mismatch".into()); }
        Ok(())
    }
}

impl ResolvesServerCert for CertStore {
    fn resolve(&self, _hello: ClientHello<'_>) -> Option<Arc<CertifiedKey>> {
        self.current.load().as_ref().map(|i| i.key.clone())
    }
}

pub fn stream_server_config(store: Arc<CertStore>, alpn: &[&[u8]]) -> Arc<rustls::ServerConfig> {
    let mut cfg = rustls::ServerConfig::builder_with_provider(provider())
        .with_protocol_versions(&[&rustls::version::TLS13, &rustls::version::TLS12])
        .expect("aws-lc-rs supports TLS 1.2 and 1.3")
        .with_no_client_auth()
        .with_cert_resolver(store);
    cfg.alpn_protocols = alpn.iter().map(|p| p.to_vec()).collect();
    Arc::new(cfg)
}

fn hex_lower(bytes: &[u8]) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes { s.push(HEX[(b >> 4) as usize] as char); s.push(HEX[(b & 0x0f) as usize] as char); }
    s
}
```

- [ ] Add to `engine/src/telemetry/metrics.rs` the static `ENCRYPTED: EncryptedMetrics` (full struct written in Task 4) with, for this task, `tls_not_after: AtomicI64`, `tls_updates: [AtomicU64; 2]` (index 0 `applied`, 1 `rejected`), `pub fn set_tls_not_after(&self, v: i64)`, `pub fn tls_update(&self, applied: bool)`, rendered as `nexora_tls_certificate_not_after_seconds <v>` (omitted while 0) and `nexora_tls_material_updates_total{result="applied"} <n>` / `{result="rejected"} <n>`.
- [ ] In `engine/src/control.rs`: the `Hello` sent on every (re)connect sets `tls_fingerprint_sha256: cert_store.fingerprint().unwrap_or_default()`; the `server_message::Msg::TlsMaterial(m)` arm calls `cert_store.install_material(m, now_unix)` on the control runtime and sends `EngineMessage { msg: Some(engine_message::Msg::TlsMaterialResult(result)) }`; the key bytes are never logged (log only `fingerprint_sha256`, `applied`, `error`) and never passed to `snapshot.rs`.
- [ ] In `engine/src/main.rs`: construct `let cert_store = Arc::new(server::tls::CertStore::new());` before workers start and hand a clone to each worker and to the control client; in standalone mode with `tls_cert_file` set, read both files, call `install_pem`, and on failure log `standalone TLS certificate rejected: <error>` and continue serving UDP/TCP; repeat on each `SIGHUP` after the snapshot reload.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::tls` — expect PASS (2 tests).
- [ ] Run `scripts/dev-exec.sh sh -c 'cargo test --locked -p nexora-engine && grep -rn "private_key_pem" engine/src/snapshot.rs; test $? -eq 1'` — expect all tests PASS and the grep to find nothing (exit status check succeeds).
- [ ] Commit: `git add engine && git commit -m "feat(engine): in-memory DNS TLS certificate store with rotation"`.

## Task 4: DoT and DoH listeners

Files:
- `engine/Cargo.toml` — add `hyper = { version = "1.11", features = ["http1", "http2", "server"] }`, `hyper-util = { version = "0.1.20", features = ["tokio", "server", "server-auto", "http1", "http2"] }`, `http = "1.5.0"`, `http-body-util = "0.1.5"`, `bytes = "1.12.1"`, `base64 = "0.23.1"`.
- `engine/src/server/dot.rs` — DoT accept loop (RFC 7858) on 853.
- `engine/src/server/doh.rs` — DoH request handler (RFC 8484) and HTTP/2 connection loop on 443.
- `engine/src/server/mod.rs` — `pub mod dot; pub mod doh;`; per-worker spawn of DoT/DoH listeners.
- `engine/src/telemetry/metrics.rs` — `EncryptedMetrics` complete.

Interfaces:
- Consumed from Task 2: `Answerer`, `ClientInfo`, `Transport`, `stream::serve_dns_stream`, `proxy::{ProxyPolicy, resolve_client, ProxyReject, normalize_peer}`, `testutil::{EchoAnswerer, test_query}`, `Bootstrap.{listen_dot, listen_doh, doh_path, proxy_protocol_dot, proxy_protocol_doh, proxy_protocol_trusted_cidrs}`.
- Consumed from Task 3: `tls::{CertStore, stream_server_config}`.
- Consumed from M1 (Task 8): `tcp::bind_tcp(addr) -> std::io::Result<std::net::TcpListener>` (SO_REUSEPORT) and `server::spawn_workers(shared, boot)`, which starts each worker's listeners; `WorkerCtx`.
- Produced:
  - `pub async fn run_dot<A: Answerer + 'static>(listener: tokio::net::TcpListener, acceptor: tokio_rustls::TlsAcceptor, certs: Arc<CertStore>, answerer: Rc<A>, proxy: Option<Rc<ProxyPolicy>>)`.
  - `pub async fn run_doh<A: Answerer + 'static>(listener: tokio::net::TcpListener, acceptor: tokio_rustls::TlsAcceptor, certs: Arc<CertStore>, answerer: Rc<A>, proxy: Option<Rc<ProxyPolicy>>, path: Rc<str>)`.
  - `pub async fn handle<A, B>(answerer: &A, client: ClientInfo, doh_path: &str, req: http::Request<B>) -> http::Response<http_body_util::Full<bytes::Bytes>>` where `B: http_body::Body<Data = bytes::Bytes>`, `B::Error: Into<Box<dyn std::error::Error + Send + Sync>>`.
  - `pub fn min_ttl(response: &[u8]) -> u32`.
  - `#[derive(Clone, Copy)] pub struct LocalExec;` implementing `hyper::rt::Executor<F>` via `tokio::task::spawn_local`.
  - Metrics: `nexora_tls_handshakes_total{transport,result="ok|failed|no_certificate"}`, `nexora_encrypted_connections{transport}`, `nexora_doh_requests_total{method="GET|POST|other",status="200|400|404|405|413|415"}`, `nexora_doq_protocol_errors_total`, `nexora_proxy_protocol_rejected_total{transport="dot|doh",reason="untrusted_peer|invalid_header|timeout"}`, `nexora_filter_rewritten_total`; `nexora_queries_total{transport="dot|doh|doq",rcode}` comes from M1's counters via the extended `Transport`.

- [ ] Write the failing DoH handler tests at the bottom of `engine/src/server/doh.rs`:

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use crate::server::testutil::{test_query, EchoAnswerer};
    use base64::Engine as _;
    use http_body_util::BodyExt;

    fn client() -> ClientInfo { ClientInfo { addr: "192.0.2.44:40000".parse().unwrap(), transport: Transport::Doh } }

    async fn body(resp: Response<Full<Bytes>>) -> Vec<u8> { resp.into_body().collect().await.unwrap().to_bytes().to_vec() }

    #[tokio::test]
    async fn get_and_post_answer_with_dns_message_and_max_age() {
        let q = test_query(0, "example.com.");
        let url = format!("/dns-query?ct&dns={}", base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(&q));
        let get = Request::get(url).body(Full::new(Bytes::new())).unwrap();
        let resp = handle(&EchoAnswerer, client(), "/dns-query", get).await;
        assert_eq!(resp.status(), StatusCode::OK);
        assert_eq!(resp.headers()[CONTENT_TYPE], "application/dns-message");
        assert_eq!(resp.headers()[CACHE_CONTROL], "max-age=60");
        let m = hickory_proto::op::Message::from_vec(&body(resp).await).unwrap();
        assert_eq!(m.answers.len(), 2);

        let post = Request::post("/dns-query").header(CONTENT_TYPE, "application/dns-message").body(Full::new(Bytes::from(q.clone()))).unwrap();
        let resp = handle(&EchoAnswerer, client(), "/dns-query", post).await;
        assert_eq!(resp.status(), StatusCode::OK);
        assert_eq!(hickory_proto::op::Message::from_vec(&body(resp).await).unwrap().answers.len(), 2);
    }

    #[tokio::test]
    async fn error_statuses() {
        let q = test_query(0, "example.com.");
        let cases: Vec<(Request<Full<Bytes>>, StatusCode)> = vec![
            (Request::post("/dns-query").header(CONTENT_TYPE, "application/json").body(Full::new(Bytes::from(q.clone()))).unwrap(), StatusCode::UNSUPPORTED_MEDIA_TYPE),
            (Request::get("/dns-query").body(Full::new(Bytes::new())).unwrap(), StatusCode::BAD_REQUEST),
            (Request::get("/dns-query?dns=***").body(Full::new(Bytes::new())).unwrap(), StatusCode::BAD_REQUEST),
            (Request::get("/dns-query?dns=AAAA").body(Full::new(Bytes::new())).unwrap(), StatusCode::BAD_REQUEST),
            (Request::put("/dns-query").body(Full::new(Bytes::from(q.clone()))).unwrap(), StatusCode::METHOD_NOT_ALLOWED),
            (Request::get("/other").body(Full::new(Bytes::new())).unwrap(), StatusCode::NOT_FOUND),
            (Request::post("/dns-query").header(CONTENT_TYPE, "application/dns-message").body(Full::new(Bytes::from(vec![0u8; 65536]))).unwrap(), StatusCode::PAYLOAD_TOO_LARGE),
        ];
        for (req, want) in cases {
            let method = req.method().clone();
            let resp = handle(&EchoAnswerer, client(), "/dns-query", req).await;
            assert_eq!(resp.status(), want, "{method}");
            if want == StatusCode::METHOD_NOT_ALLOWED { assert_eq!(resp.headers()[ALLOW], "GET, POST"); }
        }
    }

    #[test]
    fn min_ttl_of_empty_answer_is_zero() {
        let q = test_query(7, "example.com.");
        let mut m = hickory_proto::op::Message::from_vec(&q).unwrap();
        m.metadata.message_type = hickory_proto::op::MessageType::Response;
        assert_eq!(min_ttl(&m.to_vec().unwrap()), 0);
    }
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::doh` — expect FAIL with "cannot find function `handle` in this scope".
- [ ] Implement the handler in `engine/src/server/doh.rs`:

```rust
use crate::edns::Transport;
use crate::server::{proxy, tls::CertStore, Answerer, ClientInfo};
use crate::telemetry::metrics::{HandshakeResult, ENCRYPTED};
use base64::Engine as _;
use bytes::Bytes;
use http::header::{ALLOW, CACHE_CONTROL, CONTENT_TYPE};
use http::{Method, Request, Response, StatusCode};
use http_body_util::{BodyExt, Full, LengthLimitError, Limited};
use std::rc::Rc;
use std::sync::Arc;
use std::time::Duration;

pub const MAX_DNS_MESSAGE: usize = 65535;
const HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(10);

fn status(code: StatusCode) -> Response<Full<Bytes>> {
    let mut b = Response::builder().status(code);
    if code == StatusCode::METHOD_NOT_ALLOWED { b = b.header(ALLOW, "GET, POST"); }
    b.body(Full::new(Bytes::new())).expect("static response")
}

pub fn min_ttl(response: &[u8]) -> u32 {
    match hickory_proto::op::Message::from_vec(response) {
        Ok(m) => m.answers.iter().chain(m.authorities.iter()).map(|r| r.ttl).min().unwrap_or(0),
        Err(_) => 0,
    }
}

pub async fn handle<A, B>(answerer: &A, client: ClientInfo, doh_path: &str, req: Request<B>) -> Response<Full<Bytes>>
where
    A: Answerer,
    B: http_body::Body<Data = Bytes>,
    B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
{
    let method = req.method().clone();
    let resp = handle_inner(answerer, client, doh_path, req).await;
    ENCRYPTED.doh_request(&method, resp.status());
    resp
}

async fn handle_inner<A, B>(answerer: &A, client: ClientInfo, doh_path: &str, req: Request<B>) -> Response<Full<Bytes>>
where
    A: Answerer,
    B: http_body::Body<Data = Bytes>,
    B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
{
    if req.uri().path() != doh_path { return status(StatusCode::NOT_FOUND); }
    let query: Vec<u8> = match *req.method() {
        Method::GET => {
            let Some(qs) = req.uri().query() else { return status(StatusCode::BAD_REQUEST) };
            let Some(param) = qs.split('&').find_map(|kv| kv.strip_prefix("dns=")) else { return status(StatusCode::BAD_REQUEST) };
            match base64::engine::general_purpose::URL_SAFE_NO_PAD_INDIFFERENT.decode(param) {
                Ok(b) if b.len() <= MAX_DNS_MESSAGE => b,
                Ok(_) => return status(StatusCode::PAYLOAD_TOO_LARGE),
                Err(_) => return status(StatusCode::BAD_REQUEST),
            }
        }
        Method::POST => {
            let ct = req.headers().get(CONTENT_TYPE).and_then(|v| v.to_str().ok()).unwrap_or("");
            if !ct.eq_ignore_ascii_case("application/dns-message") { return status(StatusCode::UNSUPPORTED_MEDIA_TYPE); }
            match Limited::new(req.into_body(), MAX_DNS_MESSAGE).collect().await {
                Ok(c) => c.to_bytes().to_vec(),
                Err(e) if e.downcast_ref::<LengthLimitError>().is_some() => return status(StatusCode::PAYLOAD_TOO_LARGE),
                Err(_) => return status(StatusCode::BAD_REQUEST),
            }
        }
        _ => return status(StatusCode::METHOD_NOT_ALLOWED),
    };
    if query.len() < 12 { return status(StatusCode::BAD_REQUEST); }
    let mut out = Vec::with_capacity(512);
    answerer.answer(client, &query, &mut out).await;
    if out.is_empty() { return status(StatusCode::BAD_REQUEST); }
    let max_age = min_ttl(&out);
    Response::builder()
        .status(StatusCode::OK)
        .header(CONTENT_TYPE, "application/dns-message")
        .header(CACHE_CONTROL, format!("max-age={max_age}"))
        .body(Full::new(Bytes::from(out)))
        .expect("static headers")
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::doh` — expect PASS (3 tests). The `?dns=AAAA` case decodes to 3 bytes and must hit the `< 12` branch.
- [ ] Complete `EncryptedMetrics` in `engine/src/telemetry/metrics.rs` (these counters are off the cache-hit path, so process-wide atomics are acceptable):

```rust
use std::sync::atomic::{AtomicI64, AtomicU64, Ordering::Relaxed};

#[derive(Clone, Copy)]
pub enum HandshakeResult { Ok = 0, Failed = 1, NoCertificate = 2 }

pub struct EncryptedMetrics {
    handshakes: [[AtomicU64; 3]; 3],      // [dot, doh, doq] x [ok, failed, no_certificate]
    connections: [AtomicI64; 3],
    doh_requests: [[AtomicU64; 6]; 3],    // [GET, POST, other] x [200, 400, 404, 405, 413, 415]
    doq_protocol_errors: AtomicU64,
    proxy_rejected: [[AtomicU64; 3]; 2],  // [dot, doh] x [untrusted_peer, invalid_header, timeout]
    tls_not_after: AtomicI64,
    tls_updates: [AtomicU64; 2],          // [applied, rejected]
    rewritten: AtomicU64,
}

pub static ENCRYPTED: EncryptedMetrics = EncryptedMetrics {
    handshakes: [const { [const { AtomicU64::new(0) }; 3] }; 3],
    connections: [const { AtomicI64::new(0) }; 3],
    doh_requests: [const { [const { AtomicU64::new(0) }; 6] }; 3],
    doq_protocol_errors: AtomicU64::new(0),
    proxy_rejected: [const { [const { AtomicU64::new(0) }; 3] }; 2],
    tls_not_after: AtomicI64::new(0),
    tls_updates: [const { AtomicU64::new(0) }; 2],
    rewritten: AtomicU64::new(0),
};

fn tidx(t: crate::edns::Transport) -> usize {
    match t { crate::edns::Transport::Dot => 0, crate::edns::Transport::Doh => 1, _ => 2 }
}

pub struct ConnectionGuard(crate::edns::Transport);
impl ConnectionGuard {
    pub fn new(t: crate::edns::Transport) -> Self { ENCRYPTED.connections[tidx(t)].fetch_add(1, Relaxed); Self(t) }
}
impl Drop for ConnectionGuard {
    fn drop(&mut self) { ENCRYPTED.connections[tidx(self.0)].fetch_sub(1, Relaxed); }
}

impl EncryptedMetrics {
    pub fn handshake(&self, t: crate::edns::Transport, r: HandshakeResult) { self.handshakes[tidx(t)][r as usize].fetch_add(1, Relaxed); }
    pub fn doh_request(&self, m: &http::Method, s: http::StatusCode) {
        let mi = if m == http::Method::GET { 0 } else if m == http::Method::POST { 1 } else { 2 };
        let si = match s.as_u16() { 200 => 0, 400 => 1, 404 => 2, 405 => 3, 413 => 4, _ => 5 };
        self.doh_requests[mi][si].fetch_add(1, Relaxed);
    }
    pub fn doq_protocol_error(&self) { self.doq_protocol_errors.fetch_add(1, Relaxed); }
    pub fn proxy_rejected(&self, t: crate::edns::Transport, r: &crate::server::proxy::ProxyReject) {
        let ri = match r { crate::server::proxy::ProxyReject::UntrustedPeer => 0, crate::server::proxy::ProxyReject::InvalidHeader => 1, crate::server::proxy::ProxyReject::Timeout => 2 };
        self.proxy_rejected[if tidx(t) == 0 { 0 } else { 1 }][ri].fetch_add(1, Relaxed);
    }
    pub fn set_tls_not_after(&self, v: i64) { self.tls_not_after.store(v, Relaxed); }
    pub fn tls_update(&self, applied: bool) { self.tls_updates[if applied { 0 } else { 1 }].fetch_add(1, Relaxed); }
    pub fn rewritten(&self) { self.rewritten.fetch_add(1, Relaxed); }
}
```

Render in M1's `/metrics` output, one sample per array cell, labels in the order of the comments (`transport` values `dot`, `doh`, `doq`; `method` values `GET`, `POST`, `other`; `status` values `200`, `400`, `404`, `405`, `413`, `415`), with `# TYPE` lines `counter` for `_total` names and `gauge` for `nexora_encrypted_connections` and `nexora_tls_certificate_not_after_seconds`.
- [ ] Add a unit test `encrypted_metrics_render` in `engine/src/telemetry/metrics.rs` that calls `ENCRYPTED.handshake(Transport::Doh, HandshakeResult::NoCertificate)` and `ENCRYPTED.doh_request(&http::Method::POST, http::StatusCode::UNSUPPORTED_MEDIA_TYPE)`, renders, and asserts the output contains the lines `nexora_tls_handshakes_total{transport="doh",result="no_certificate"} 1` and `nexora_doh_requests_total{method="POST",status="415"} 1`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib encrypted_metrics_render` — expect FAIL with "assertion failed" before rendering is wired, then wire rendering and expect PASS.
- [ ] Implement `engine/src/server/dot.rs`:

```rust
use crate::edns::Transport;
use crate::server::{proxy::{self, ProxyPolicy}, stream, tls::CertStore, Answerer, ClientInfo};
use crate::telemetry::metrics::{ConnectionGuard, HandshakeResult, ENCRYPTED};
use std::rc::Rc;
use std::sync::Arc;
use std::time::Duration;

pub const DOT_IDLE: Duration = Duration::from_secs(30);
const HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(10);

pub async fn run_dot<A: Answerer + 'static>(
    listener: tokio::net::TcpListener,
    acceptor: tokio_rustls::TlsAcceptor,
    certs: Arc<CertStore>,
    answerer: Rc<A>,
    proxy_policy: Option<Rc<ProxyPolicy>>,
) {
    loop {
        let (mut tcp, peer) = match listener.accept().await {
            Ok(v) => v,
            Err(_) => { tokio::time::sleep(Duration::from_millis(50)).await; continue; }
        };
        let _ = tcp.set_nodelay(true);
        let (acceptor, certs, answerer, proxy_policy) = (acceptor.clone(), certs.clone(), answerer.clone(), proxy_policy.clone());
        tokio::task::spawn_local(async move {
            let addr = match proxy::resolve_client(proxy_policy.as_deref(), &mut tcp, peer).await {
                Ok(a) => a,
                Err(r) => { ENCRYPTED.proxy_rejected(Transport::Dot, &r); return; }
            };
            if certs.is_empty() { ENCRYPTED.handshake(Transport::Dot, HandshakeResult::NoCertificate); return; }
            let tls = match tokio::time::timeout(HANDSHAKE_TIMEOUT, acceptor.accept(tcp)).await {
                Ok(Ok(s)) => { ENCRYPTED.handshake(Transport::Dot, HandshakeResult::Ok); s }
                _ => { ENCRYPTED.handshake(Transport::Dot, HandshakeResult::Failed); return; }
            };
            let _guard = ConnectionGuard::new(Transport::Dot);
            stream::serve_dns_stream(answerer, tls, ClientInfo { addr, transport: Transport::Dot }, DOT_IDLE).await;
        });
    }
}
```

- [ ] Implement `run_doh` and `LocalExec` in `engine/src/server/doh.rs`: same accept, PROXY (`Transport::Doh`), empty-store and handshake handling as `run_dot` with ALPN from the acceptor, then:

```rust
#[derive(Clone, Copy)]
pub struct LocalExec;

impl<F> hyper::rt::Executor<F> for LocalExec
where
    F: std::future::Future + 'static,
{
    fn execute(&self, fut: F) { tokio::task::spawn_local(fut); }
}

// inside the per-connection task, after the TLS handshake:
let _guard = crate::telemetry::metrics::ConnectionGuard::new(Transport::Doh);
let client = ClientInfo { addr, transport: Transport::Doh };
let service = hyper::service::service_fn(move |req: Request<hyper::body::Incoming>| {
    let answerer = answerer.clone();
    let path = path.clone();
    async move { Ok::<_, std::convert::Infallible>(handle(&*answerer, client, &path, req).await) }
});
let mut builder = hyper_util::server::conn::auto::Builder::new(LocalExec);
builder.http1().timer(hyper_util::rt::TokioTimer::new()).header_read_timeout(Duration::from_secs(10));
builder.http2().timer(hyper_util::rt::TokioTimer::new()).max_concurrent_streams(100).keep_alive_interval(Some(Duration::from_secs(30)));
let _ = builder.serve_connection(hyper_util::rt::TokioIo::new(tls), service).await;
```

The client address is the TCP peer (or the PROXY v2 source); `X-Forwarded-For` and `Forwarded` headers are ignored.
- [ ] In `server::spawn_workers`: before the worker threads start, build once `dot_acceptor = TlsAcceptor::from(tls::stream_server_config(cert_store.clone(), &[b"dot"]))` and `doh_acceptor = TlsAcceptor::from(tls::stream_server_config(cert_store.clone(), &[b"h2", b"http/1.1"]))`; then inside each worker's runtime, for each address in `listen_dot` and `listen_doh`, create the listener with `tcp::bind_tcp(addr)` converted via `tokio::net::TcpListener::from_std`, and `spawn_local(dot::run_dot(listener, dot_acceptor.clone(), cert_store.clone(), Rc::new(WorkerAnswerer(ctx.clone())), proxy))` / `spawn_local(doh::run_doh(listener, doh_acceptor.clone(), cert_store.clone(), Rc::new(WorkerAnswerer(ctx.clone())), proxy, Rc::from(boot.doh_path.as_str())))`, passing `Some(Rc::new(ProxyPolicy::new(&boot.proxy_protocol_trusted_cidrs)?))` only when the matching `proxy_protocol_*` flag is true. A bind failure is a startup error `bind dot <addr>: <io error>` / `bind doh <addr>: <io error>`.
- [ ] Add a `#[tokio::test(flavor = "current_thread")]` test `dot_end_to_end_with_proxy_header` in `engine/src/server/dot.rs` that runs inside a `LocalSet`: installs an rcgen self-signed cert for `dns.test` in a `CertStore`, binds `127.0.0.1:0`, spawns `run_dot` with `EchoAnswerer` and `ProxyPolicy::new(&["127.0.0.0/8".into()])`, connects with a tokio-rustls client trusting that cert, first writes the PROXY v2 bytes `SIGNATURE ++ [0x21, 0x11, 0x00, 0x0c, 198, 51, 100, 23, 127, 0, 0, 1, 0x9c, 0x40, 0x03, 0x55]` on the raw TCP stream before the TLS handshake, sends `test_query(0x4242, "example.com.")` length-prefixed, and asserts the reply has id `0x4242` and `answers[1].data == RData::A(A(Ipv4Addr::new(198, 51, 100, 23)))`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::` — expect PASS for `server::dot`, `server::doh`, `server::stream`, `server::proxy`, `server::tls`.
- [ ] Commit: `git add engine && git commit -m "feat(engine): DoT and DoH (HTTP/2, GET+POST) listeners"`.

## Task 5: DoQ listener (RFC 9250)

Files:
- `engine/Cargo.toml` — add `quinn = { version = "=0.11.11", default-features = false, features = ["runtime-tokio", "rustls-aws-lc-rs"] }`.
- `engine/src/server/doq.rs` — DoQ endpoint per worker, stream handling, framing checks.
- `engine/src/server/tls.rs` — `quic_server_config`.
- `engine/src/server/mod.rs` — `pub mod doq;`; per-worker spawn of DoQ endpoints.

Interfaces:
- Consumed from Task 2: `Answerer`, `ClientInfo`, `Transport::Doq`, `proxy::normalize_peer`, `testutil::{EchoAnswerer, test_query}`, `Bootstrap.listen_doq`.
- Consumed from Task 3: `tls::{CertStore, provider}`.
- Consumed from Task 4: `telemetry::metrics::{ENCRYPTED, ConnectionGuard, HandshakeResult}`.
- Produced:
  - `pub const DOQ_NO_ERROR: u32 = 0x0; pub const DOQ_INTERNAL_ERROR: u32 = 0x1; pub const DOQ_PROTOCOL_ERROR: u32 = 0x2; pub const DOQ_REQUEST_CANCELLED: u32 = 0x3; pub const DOQ_EXCESSIVE_LOAD: u32 = 0x4;`
  - `pub fn decode_query(buf: &[u8]) -> Result<&[u8], DoqFrameError>`; `pub enum DoqFrameError { Short, LengthMismatch, NonZeroId }`.
  - `pub fn quic_server_config(store: Arc<CertStore>) -> quinn::ServerConfig`.
  - `pub fn endpoint_config(reset_key: &[u8; 64]) -> quinn::EndpointConfig`.
  - `pub fn bind_doq(addr: SocketAddr, server: quinn::ServerConfig, endpoint: quinn::EndpointConfig) -> std::io::Result<quinn::Endpoint>`.
  - `pub async fn run_doq<A: Answerer + 'static>(endpoint: quinn::Endpoint, answerer: Rc<A>, certs: Arc<CertStore>)`.

- [ ] Write the failing tests at the bottom of `engine/src/server/doq.rs`:

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use crate::server::testutil::{test_query, EchoAnswerer};
    use rustls::pki_types::CertificateDer;

    fn framed(q: &[u8]) -> Vec<u8> { let mut f = (q.len() as u16).to_be_bytes().to_vec(); f.extend_from_slice(q); f }

    #[test]
    fn framing_rules() {
        let q = test_query(0, "example.com.");
        assert_eq!(decode_query(&framed(&q)).unwrap(), &q[..]);
        assert_eq!(decode_query(&[0]), Err(DoqFrameError::Short));
        assert_eq!(decode_query(&framed(&q)[..10]), Err(DoqFrameError::LengthMismatch));
        assert_eq!(decode_query(&framed(&test_query(9, "example.com."))), Err(DoqFrameError::NonZeroId));
    }

    fn client_endpoint(root: CertificateDer<'static>) -> quinn::Endpoint {
        let mut roots = rustls::RootCertStore::empty();
        roots.add(root).unwrap();
        let mut tls = rustls::ClientConfig::builder_with_provider(crate::server::tls::provider())
            .with_protocol_versions(&[&rustls::version::TLS13]).unwrap()
            .with_root_certificates(roots).with_no_client_auth();
        tls.alpn_protocols = vec![b"doq".to_vec()];
        let crypto = quinn::crypto::rustls::QuicClientConfig::try_from(tls).unwrap();
        let mut ep = quinn::Endpoint::client("127.0.0.1:0".parse().unwrap()).unwrap();
        ep.set_default_client_config(quinn::ClientConfig::new(Arc::new(crypto)));
        ep
    }

    #[tokio::test(flavor = "current_thread")]
    async fn one_stream_per_query_and_nonzero_id_closes_connection() {
        tokio::task::LocalSet::new().run_until(async {
            let ck = rcgen::generate_simple_self_signed(vec!["dns.test".to_string()]).unwrap();
            let store = Arc::new(CertStore::new());
            let store_for_run = store.clone();
            let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() as i64;
            store.install_pem(ck.cert.pem().as_bytes(), ck.signing_key.serialize_pem().as_bytes(), now).unwrap();
            let server = bind_doq("127.0.0.1:0".parse().unwrap(), quic_server_config(store), endpoint_config(&[7u8; 64])).unwrap();
            let addr = server.local_addr().unwrap();
            tokio::task::spawn_local(run_doq(server, Rc::new(EchoAnswerer), store_for_run));

            let client = client_endpoint(ck.cert.der().clone());
            let conn = client.connect(addr, "dns.test").unwrap().await.unwrap();
            let mut tasks = Vec::new();
            for _ in 0..20 {
                let conn = conn.clone();
                tasks.push(tokio::task::spawn_local(async move {
                    let (mut send, mut recv) = conn.open_bi().await.unwrap();
                    send.write_all(&framed(&test_query(0, "example.com."))).await.unwrap();
                    send.finish().unwrap();
                    let resp = recv.read_to_end(65537).await.unwrap();
                    let len = u16::from_be_bytes([resp[0], resp[1]]) as usize;
                    assert_eq!(len, resp.len() - 2);
                    let m = hickory_proto::op::Message::from_vec(&resp[2..]).unwrap();
                    assert_eq!(m.metadata.id, 0);
                    assert_eq!(m.answers.len(), 2);
                }));
            }
            for t in tasks { t.await.unwrap(); }

            let (mut send, mut recv) = conn.open_bi().await.unwrap();
            send.write_all(&framed(&test_query(5, "example.com."))).await.unwrap();
            send.finish().unwrap();
            let _ = recv.read_to_end(65537).await;
            match conn.closed().await {
                quinn::ConnectionError::ApplicationClosed(c) => assert_eq!(c.error_code, quinn::VarInt::from_u32(DOQ_PROTOCOL_ERROR)),
                other => panic!("unexpected close: {other:?}"),
            }
        }).await;
    }
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::doq` — expect FAIL with "cannot find function `decode_query` in this scope".
- [ ] Add to `engine/src/server/tls.rs`:

```rust
pub fn quic_server_config(store: Arc<CertStore>) -> quinn::ServerConfig {
    let mut tls = rustls::ServerConfig::builder_with_provider(provider())
        .with_protocol_versions(&[&rustls::version::TLS13])
        .expect("aws-lc-rs supports TLS 1.3")
        .with_no_client_auth()
        .with_cert_resolver(store);
    tls.alpn_protocols = vec![b"doq".to_vec()];
    tls.max_early_data_size = 0; // no 0-RTT: DNS queries must not be replayable
    let crypto = quinn::crypto::rustls::QuicServerConfig::try_from(tls).expect("aws-lc-rs has TLS13_AES_128_GCM_SHA256");
    let mut cfg = quinn::ServerConfig::with_crypto(Arc::new(crypto));
    let mut transport = quinn::TransportConfig::default();
    transport
        .max_concurrent_bidi_streams(100u32.into())
        .max_concurrent_uni_streams(0u32.into())
        .max_idle_timeout(Some(std::time::Duration::from_secs(30).try_into().expect("30 s fits a VarInt")));
    cfg.transport_config(Arc::new(transport));
    cfg.migration(false); // per-worker SO_REUSEPORT endpoints cannot follow a migrated 4-tuple
    cfg
}
```

- [ ] Implement `engine/src/server/doq.rs`:

```rust
use crate::edns::Transport;
use crate::server::{proxy::normalize_peer, tls::CertStore, Answerer, ClientInfo};
use crate::telemetry::metrics::{ConnectionGuard, HandshakeResult, ENCRYPTED};
use quinn::VarInt;
use std::net::SocketAddr;
use std::rc::Rc;
use std::sync::Arc;

pub const DOQ_NO_ERROR: u32 = 0x0;
pub const DOQ_INTERNAL_ERROR: u32 = 0x1;
pub const DOQ_PROTOCOL_ERROR: u32 = 0x2;
pub const DOQ_REQUEST_CANCELLED: u32 = 0x3;
pub const DOQ_EXCESSIVE_LOAD: u32 = 0x4;

#[derive(Debug, PartialEq, Eq)]
pub enum DoqFrameError { Short, LengthMismatch, NonZeroId }

pub fn decode_query(buf: &[u8]) -> Result<&[u8], DoqFrameError> {
    if buf.len() < 2 + 12 { return if buf.len() < 2 { Err(DoqFrameError::Short) } else { Err(DoqFrameError::LengthMismatch) }; }
    let len = u16::from_be_bytes([buf[0], buf[1]]) as usize;
    if len != buf.len() - 2 { return Err(DoqFrameError::LengthMismatch); }
    let msg = &buf[2..];
    if msg[0] != 0 || msg[1] != 0 { return Err(DoqFrameError::NonZeroId); }
    Ok(msg)
}

pub fn endpoint_config(reset_key: &[u8; 64]) -> quinn::EndpointConfig {
    let key = aws_lc_rs::hmac::Key::new(aws_lc_rs::hmac::HMAC_SHA256, reset_key);
    quinn::EndpointConfig::new(Arc::new(key))
}

pub fn bind_doq(addr: SocketAddr, server: quinn::ServerConfig, endpoint: quinn::EndpointConfig) -> std::io::Result<quinn::Endpoint> {
    use socket2::{Domain, Protocol, Socket, Type};
    let socket = Socket::new(Domain::for_address(addr), Type::DGRAM, Some(Protocol::UDP))?;
    if addr.is_ipv6() { socket.set_only_v6(true)?; }
    socket.set_reuse_port(true)?;
    socket.set_nonblocking(true)?;
    socket.bind(&addr.into())?;
    quinn::Endpoint::new(endpoint, Some(server), socket.into(), Arc::new(quinn::TokioRuntime))
}

pub async fn run_doq<A: Answerer + 'static>(endpoint: quinn::Endpoint, answerer: Rc<A>, certs: Arc<CertStore>) {
    while let Some(incoming) = endpoint.accept().await {
        let answerer = answerer.clone();
        let no_cert = certs.is_empty();
        tokio::task::spawn_local(async move {
            let conn = match incoming.await {
                Ok(c) => { ENCRYPTED.handshake(Transport::Doq, HandshakeResult::Ok); c }
                Err(_) => {
                    let r = if no_cert { HandshakeResult::NoCertificate } else { HandshakeResult::Failed };
                    ENCRYPTED.handshake(Transport::Doq, r);
                    return;
                }
            };
            let _guard = ConnectionGuard::new(Transport::Doq);
            let client = ClientInfo { addr: normalize_peer(conn.remote_address()), transport: Transport::Doq };
            let uni = conn.clone();
            tokio::task::spawn_local(async move {
                if uni.accept_uni().await.is_ok() {
                    ENCRYPTED.doq_protocol_error();
                    uni.close(VarInt::from_u32(DOQ_PROTOCOL_ERROR), b"unidirectional stream");
                }
            });
            while let Ok((mut send, mut recv)) = conn.accept_bi().await {
                let (answerer, conn) = (answerer.clone(), conn.clone());
                tokio::task::spawn_local(async move {
                    let buf = match recv.read_to_end(2 + 65535).await {
                        Ok(b) => b,
                        Err(quinn::ReadToEndError::TooLong) => {
                            ENCRYPTED.doq_protocol_error();
                            conn.close(VarInt::from_u32(DOQ_PROTOCOL_ERROR), b"message too long");
                            return;
                        }
                        Err(_) => { let _ = send.reset(VarInt::from_u32(DOQ_REQUEST_CANCELLED)); return; }
                    };
                    let query = match decode_query(&buf) {
                        Ok(q) => q,
                        Err(_) => {
                            ENCRYPTED.doq_protocol_error();
                            conn.close(VarInt::from_u32(DOQ_PROTOCOL_ERROR), b"malformed query");
                            return;
                        }
                    };
                    let mut out = Vec::with_capacity(512);
                    answerer.answer(client, query, &mut out).await;
                    if out.is_empty() || out.len() > 65535 { let _ = send.reset(VarInt::from_u32(DOQ_INTERNAL_ERROR)); return; }
                    let mut frame = Vec::with_capacity(out.len() + 2);
                    frame.extend_from_slice(&(out.len() as u16).to_be_bytes());
                    frame.extend_from_slice(&out);
                    if send.write_all(&frame).await.is_ok() { let _ = send.finish(); }
                });
            }
        });
    }
}
```

With no certificate installed, `CertStore::resolve` returns `None`, the QUIC handshake fails, and the failure is counted as `result="no_certificate"`.
- [ ] In `server::spawn_workers`: generate one 64-byte reset key per process with `aws_lc_rs::rand::fill`, build `quic_server_config(cert_store.clone())` once, and on every worker `bind_doq(addr, cfg.clone(), endpoint_config(&reset_key))` for each `listen_doq` address inside that worker's tokio runtime, then `spawn_local(doq::run_doq(endpoint, Rc::new(WorkerAnswerer(ctx.clone())), cert_store.clone()))`. A bind failure is a startup error `bind doq <addr>: <io error>`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::doq` — expect PASS (2 tests).
- [ ] Run `scripts/dev-exec.sh cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` — expect exit 0.
- [ ] Commit: `git add engine && git commit -m "feat(engine): DoQ listener per RFC 9250"`.

## Task 6: Engine per-client policy, rewrites and safe-search answers

Files:
- `engine/src/filter.rs` — `PolicyTable`, `EffectivePolicy`, `RewriteTable`, `RewriteAnswer`, `Verdict`; build + validation from the snapshot.
- `engine/src/server/rewrite.rs` — rewrite response synthesis (A/AAAA/NODATA, CNAME with bounded chase) and `WorkerRewriteCtx`.
- `engine/src/server/mod.rs` — `pub mod rewrite;`; `handle_packet` selects the client's `EffectivePolicy` and applies its verdict; `FastOutcome::Rewrite(RewriteJob)`.
- `engine/src/server/udp.rs` — spawn `FastOutcome::Rewrite` jobs like misses.
- `engine/src/runtime.rs` — `Runtime.filter` becomes `Arc<FilterSet>` (global selection); new `Runtime.policy: filter::PolicyTable`.
- `engine/src/snapshot.rs` — `apply` rejects with the `PolicyTable::build` reason (through `Runtime::build`).
- `engine/src/telemetry/querylog.rs` — `FilterOutcome::Rewritten` (`rewritten`); `QueryRecord.policy_group: u16`; OTLP attribute `nexora.policy.group`.
- `engine/tests/hot_path_alloc.rs` — the `cache_hit_path_does_not_allocate` snapshot gains policy groups and rewrites.

Interfaces:
- Consumed from M1 (Task 7): `filter::{FilterSet, FilterDecision, BlockMode, decode_blob, domain_to_wire}` with `FilterSet::build(blocklists: &[Vec<u8>], allowlists: &[Vec<u8>], mode: BlockMode, ttl: u32) -> (FilterSet, ListStats)`, `FilterSet::decide(&self, name_wire: &[u8]) -> FilterDecision`, `FilterSet::cloaked`, `FilterSet::write_block_reply`, public fields `mode`, `ttl`; `snapshot::{BlobSource, SnapshotError}`; `runtime::Runtime::build(s, blobs, previous)`. (Task 3): `wire::{parse_query, QueryView, NameKey}`. (Task 8): `server::{handle_packet, resolve_miss, FastOutcome, WorkerCtx}`, `telemetry::querylog::{QueryRecord, FilterOutcome}`.
- Consumed from Task 1: `proto::{ConfigSnapshot, PolicyGroup, RewriteSet, RewriteRule, RewriteType, BlobRef}`.
- Consumed from Task 2: `edns::Transport`, `server::{Answerer, ClientInfo, WorkerAnswerer}`.
- Consumed from Task 4: `ENCRYPTED.rewritten()`.
- Produced:
  - `pub struct PolicyTable` with `pub fn build(snap: &proto::ConfigSnapshot, global_filter: Arc<FilterSet>, blobs: &dyn BlobSource) -> Result<PolicyTable, String>` and `pub fn select(&self, ip: IpAddr) -> (&EffectivePolicy, Option<u16>)` (group index, `None` = global).
  - `pub struct EffectivePolicy` with `pub fn check(&self, wire_name: &[u8]) -> Verdict<'_>`, `pub fn filter(&self) -> &FilterSet`, `pub fn group_id(&self) -> &str` (empty for global).
  - `pub enum Verdict<'a> { Pass, Allowed, Blocked, Rewrite(&'a RewriteAnswer) }`.
  - `#[derive(Clone)] pub enum RewriteAnswer { Addrs { a: Box<[(Ipv4Addr, u32)]>, aaaa: Box<[(Ipv6Addr, u32)]> }, Cname { target: hickory_proto::rr::Name, ttl: u32 } }`.
  - `#[derive(Default)] pub struct RewriteTable` with `pub fn lookup(&self, wire_name: &[u8]) -> Option<&RewriteAnswer>`.
  - `pub trait RewriteContext { fn policy(&self) -> &EffectivePolicy; async fn resolve(&self, name: &Name, qtype: RecordType) -> Result<Message, ()>; fn block(&self, owner: &Name, qtype: RecordType) -> (ResponseCode, Vec<Record>); }`.
  - `pub async fn rewrite_response<C: RewriteContext>(ctx: &C, query: &[u8], first: &RewriteAnswer) -> Vec<u8>`; `pub const MAX_REWRITE_DEPTH: usize = 8`.
  - `pub struct RewriteJob { pub query: Box<[u8]>, pub client: SocketAddr, pub transport: Transport, pub limit: usize }`; `pub struct WorkerRewriteCtx { pub ctx: Rc<WorkerCtx>, pub rt: Arc<Runtime>, pub client: SocketAddr }` implementing `RewriteContext`; `pub async fn run_rewrite_job(ctx: Rc<WorkerCtx>, rt: Arc<Runtime>, job: RewriteJob) -> Vec<u8>`.

Semantics (binding for this task):
- Selection: longest matching prefix over all groups' CIDRs; IPv4-mapped IPv6 clients match IPv4 prefixes; no match -> global. A client in a group gets only that group's blocklists, allowlist and rewrite sets; global clients use M1's `Runtime.filter` plus `global_rewrite_set_ids`.
- Verdict order: rewrite match -> `Rewrite`; else the policy's `FilterSet::decide`: `Blocked` -> `Blocked`, `Allowed` (blocklisted but allowlisted) -> `Allowed`, `None` -> `Pass`. Group filter sets use the global `mode` and `ttl`; groups with the same blocklist hashes and allowlist share one `Arc<FilterSet>`.
- Rewrite matching: exact name first; else wildcard `*.base` matches strict subdomains of `base`, longest base first. Sets merge in the order of `rewrite_set_ids`; for an identical key (same exact name, or same wildcard base) the first set wins.
- A/AAAA rules for one key merge into one `Addrs`; qtype A answers the A records, AAAA the AAAA records, any other qtype (or an empty family) answers NOERROR with no records. A key with a CNAME rule and any other rule in the same set is invalid.
- CNAME rewrite: answer the CNAME (owner = client's question name, TTL = rule TTL); for qtype CNAME stop there; otherwise check the target against the same `EffectivePolicy`: another rewrite -> follow (at most `MAX_REWRITE_DEPTH` hops, a repeated name -> SERVFAIL); blocked -> block records/rcode from M1's block mode; otherwise resolve `(target, qtype)` through the M1 miss pipeline and append its answers and authorities with its rcode; resolution failure -> SERVFAIL.
- Rewrite answers are not written to the response cache. CNAME-cloaking checks on upstream answers use the client's `EffectivePolicy`.
- Validation reasons (exact strings): `policy group <id>: invalid cidr <cidr>`; `policy group <id>: cidr <cidr> also in group <other id>`; `policy group <id>: blob <sha256>: <reason>` (from `SnapshotError::Blob`); `policy group <id>: unknown rewrite set <set id>`; `unknown global rewrite set <set id>`; `rewrite set <set id>: invalid rule name <name>`; `rewrite set <set id>: <name> A value <v> is not an IPv4 address`; `rewrite set <set id>: <name> AAAA value <v> is not an IPv6 address`; `rewrite set <set id>: <name> CNAME target <v> is invalid`; `rewrite set <set id>: <name> has CNAME and other records`; `rewrite set <set id>: <name> ttl <t> exceeds 86400`; `rewrite set <set id>: duplicate id`.

- [ ] Write the failing tests at the bottom of `engine/src/filter.rs`:

```rust
#[cfg(test)]
mod policy_tests {
    use super::*;
    use crate::proto::{BlobRef, ConfigSnapshot, PolicyGroup, RewriteRule, RewriteSet, RewriteType};
    use crate::snapshot::{BlobSource, SnapshotError};
    use std::collections::HashMap;
    use std::net::IpAddr;
    use std::sync::Arc;

    fn wire(name: &str) -> Vec<u8> {
        let mut w = Vec::new();
        for l in name.trim_end_matches('.').split('.') { w.push(l.len() as u8); w.extend_from_slice(l.as_bytes()); }
        w.push(0);
        w
    }
    fn rule(name: &str, t: RewriteType, v: &str) -> RewriteRule { RewriteRule { name: name.into(), r#type: t as i32, value: v.into(), ttl: 120 } }
    struct MapBlobs(HashMap<String, Vec<u8>>);
    impl BlobSource for MapBlobs {
        fn read(&self, r: &BlobRef) -> Result<Vec<u8>, SnapshotError> {
            self.0.get(&r.sha256).cloned().ok_or_else(|| SnapshotError::Blob { sha256: r.sha256.clone(), reason: "missing".into() })
        }
    }
    fn ads_blob() -> (BlobRef, Vec<u8>) {
        let z = zstd::encode_all(&b"ads.example.test\ntracker.test\n"[..], 3).unwrap();
        (BlobRef { sha256: "a".repeat(64), size: z.len() as u64, name: "ads".into() }, z)
    }
    fn blobs() -> MapBlobs { let (r, z) = ads_blob(); MapBlobs(HashMap::from([(r.sha256, z)])) }
    fn global() -> Arc<FilterSet> {
        Arc::new(FilterSet::build(&[b"ads.example.test\n".to_vec()], &[], BlockMode::NullIp, 60).0)
    }
    fn group(id: &str, cidrs: &[&str], lists: &[BlobRef], allow: &[&str], sets: &[&str]) -> PolicyGroup {
        PolicyGroup {
            id: id.into(), name: id.into(),
            cidrs: cidrs.iter().map(|s| s.to_string()).collect(),
            blocklists: lists.to_vec(),
            allowlist: allow.iter().map(|s| s.to_string()).collect(),
            rewrite_set_ids: sets.iter().map(|s| s.to_string()).collect(),
        }
    }
    fn snapshot() -> ConfigSnapshot {
        ConfigSnapshot {
            policy_groups: vec![
                group("wide", &["10.0.0.0/8"], &[ads_blob().0], &["ok.ads.example.test"], &["custom:group:wide"]),
                group("narrow", &["10.1.0.0/16", "2001:db8::/32"], &[], &[], &["custom:global"]),
            ],
            rewrite_sets: vec![
                RewriteSet { id: "custom:global".into(), label: "custom".into(), rules: vec![
                    rule("nas.home.test", RewriteType::A, "192.168.1.50"),
                    rule("nas.home.test", RewriteType::Aaaa, "fd00::50"),
                    rule("*.lab.home.test", RewriteType::Cname, "nas.home.test"),
                    rule("*.home.test", RewriteType::A, "192.168.1.1"),
                    rule("special.lab.home.test", RewriteType::A, "192.168.1.60"),
                ]},
                RewriteSet { id: "custom:group:wide".into(), label: "custom".into(), rules: vec![
                    rule("nas.home.test", RewriteType::A, "10.9.9.9"),
                ]},
            ],
            global_rewrite_set_ids: vec!["custom:global".into()],
            ..Default::default()
        }
    }

    #[test]
    fn most_specific_cidr_wins_and_group_replaces_global() {
        let t = PolicyTable::build(&snapshot(), global(), &blobs()).unwrap();
        let ads = wire("x.ads.example.test");
        let (p, g) = t.select("10.2.3.4".parse::<IpAddr>().unwrap());
        assert_eq!((p.group_id(), g), ("wide", Some(0)));
        assert!(matches!(p.check(&ads), Verdict::Blocked));
        assert!(matches!(p.check(&wire("ok.ads.example.test")), Verdict::Allowed));
        let (p, _) = t.select("10.1.3.4".parse::<IpAddr>().unwrap());
        assert_eq!(p.group_id(), "narrow");
        assert!(matches!(p.check(&ads), Verdict::Pass), "narrow selects no lists");
        let (p, _) = t.select("::ffff:10.1.0.9".parse::<IpAddr>().unwrap());
        assert_eq!(p.group_id(), "narrow");
        let (p, _) = t.select("2001:db8::1".parse::<IpAddr>().unwrap());
        assert_eq!(p.group_id(), "narrow");
        let (p, g) = t.select("192.0.2.1".parse::<IpAddr>().unwrap());
        assert_eq!((p.group_id(), g), ("", None));
        assert!(matches!(p.check(&ads), Verdict::Blocked));
    }

    #[test]
    fn rewrite_precedence_exact_wildcard_and_set_order() {
        let t = PolicyTable::build(&snapshot(), global(), &blobs()).unwrap();
        let (global, _) = t.select("192.0.2.1".parse::<IpAddr>().unwrap());
        match global.check(&wire("nas.home.test")) {
            Verdict::Rewrite(RewriteAnswer::Addrs { a, aaaa }) => { assert_eq!(a.len(), 1); assert_eq!(aaaa.len(), 1); }
            _ => panic!("expected addrs"),
        }
        assert!(matches!(global.check(&wire("x.lab.home.test")), Verdict::Rewrite(RewriteAnswer::Cname { .. })));
        assert!(matches!(global.check(&wire("a.b.lab.home.test")), Verdict::Rewrite(RewriteAnswer::Cname { .. })));
        assert!(matches!(global.check(&wire("special.lab.home.test")), Verdict::Rewrite(RewriteAnswer::Addrs { .. })));
        assert!(matches!(global.check(&wire("printer.home.test")), Verdict::Rewrite(RewriteAnswer::Addrs { .. })));
        assert!(matches!(global.check(&wire("lab.home.test")), Verdict::Rewrite(RewriteAnswer::Addrs { .. })), "*.home.test covers lab.home.test");
        assert!(matches!(global.check(&wire("home.test")), Verdict::Pass), "wildcard never matches its base");
        let (wide, _) = t.select("10.2.3.4".parse::<IpAddr>().unwrap());
        match wide.check(&wire("nas.home.test")) {
            Verdict::Rewrite(RewriteAnswer::Addrs { a, .. }) => assert_eq!(a[0].0.to_string(), "10.9.9.9"),
            _ => panic!("expected group rewrite"),
        }
    }

    #[test]
    fn invalid_snapshots_are_rejected_with_reason() {
        let mut s = snapshot();
        s.policy_groups[1].cidrs.push("10.0.0.0/8".into());
        assert_eq!(PolicyTable::build(&s, global(), &blobs()).err().unwrap(), "policy group narrow: cidr 10.0.0.0/8 also in group wide");
        let mut s = snapshot();
        s.policy_groups[0].cidrs = vec!["10.0.0.1/8".into()];
        assert_eq!(PolicyTable::build(&s, global(), &blobs()).err().unwrap(), "policy group wide: invalid cidr 10.0.0.1/8");
        let mut s = snapshot();
        s.policy_groups[0].blocklists.push(BlobRef { sha256: "nope".into(), size: 1, name: "x".into() });
        assert_eq!(PolicyTable::build(&s, global(), &blobs()).err().unwrap(), "policy group wide: blob nope: missing");
        let mut s = snapshot();
        s.rewrite_sets[0].rules.push(rule("nas.home.test", RewriteType::Cname, "other.test"));
        assert_eq!(PolicyTable::build(&s, global(), &blobs()).err().unwrap(), "rewrite set custom:global: nas.home.test has CNAME and other records");
        let mut s = snapshot();
        s.rewrite_sets[0].rules.push(rule("bad.test", RewriteType::A, "fd00::1"));
        assert_eq!(PolicyTable::build(&s, global(), &blobs()).err().unwrap(), "rewrite set custom:global: bad.test A value fd00::1 is not an IPv4 address");
    }
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib filter::policy_tests` — expect FAIL with "cannot find type `PolicyTable` in this scope".
- [ ] Implement in `engine/src/filter.rs` (core code; validation produces exactly the reasons listed under Semantics):

```rust
use crate::snapshot::BlobSource;
use hickory_proto::rr::Name;
use rustc_hash::FxHashMap;
use std::collections::HashMap;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
use std::sync::Arc;

#[derive(Clone)]
pub enum RewriteAnswer {
    Addrs { a: Box<[(Ipv4Addr, u32)]>, aaaa: Box<[(Ipv6Addr, u32)]> },
    Cname { target: Name, ttl: u32 },
}

pub enum Verdict<'a> { Pass, Allowed, Blocked, Rewrite(&'a RewriteAnswer) }

#[derive(Default)]
pub struct RewriteTable {
    exact: FxHashMap<Box<[u8]>, RewriteAnswer>,
    wildcard: FxHashMap<Box<[u8]>, RewriteAnswer>,
}

impl RewriteTable {
    pub fn lookup(&self, wire_name: &[u8]) -> Option<&RewriteAnswer> {
        if self.exact.is_empty() && self.wildcard.is_empty() { return None; }
        if let Some(a) = self.exact.get(wire_name) { return Some(a); }
        if self.wildcard.is_empty() { return None; }
        let mut pos = 0usize;
        loop {
            let len = *wire_name.get(pos)? as usize;
            if len == 0 { return None; }
            pos += 1 + len;
            if *wire_name.get(pos)? == 0 { return None; }
            if let Some(a) = self.wildcard.get(&wire_name[pos..]) { return Some(a); }
        }
    }
}

pub struct EffectivePolicy {
    group_id: Box<str>,
    filter: Arc<FilterSet>,
    rewrites: RewriteTable,
}

impl EffectivePolicy {
    pub fn group_id(&self) -> &str { &self.group_id }
    pub fn filter(&self) -> &FilterSet { &self.filter }
    pub fn check(&self, wire_name: &[u8]) -> Verdict<'_> {
        if let Some(r) = self.rewrites.lookup(wire_name) { return Verdict::Rewrite(r); }
        match self.filter.decide(wire_name) {
            FilterDecision::Blocked => Verdict::Blocked,
            FilterDecision::Allowed => Verdict::Allowed,
            FilterDecision::None => Verdict::Pass,
        }
    }
}

pub struct PolicyTable {
    groups: Box<[EffectivePolicy]>,
    global: EffectivePolicy,
    v4: Box<[(u8, FxHashMap<u32, u16>)]>,   // prefix length descending
    v6: Box<[(u8, FxHashMap<u128, u16>)]>,  // prefix length descending
}

impl PolicyTable {
    pub fn select(&self, ip: IpAddr) -> (&EffectivePolicy, Option<u16>) {
        let ip = match ip { IpAddr::V6(v6) => v6.to_ipv4_mapped().map(IpAddr::V4).unwrap_or(IpAddr::V6(v6)), v4 => v4 };
        match ip {
            IpAddr::V4(v4) => {
                let bits = u32::from(v4);
                for (len, map) in self.v4.iter() {
                    let mask = if *len == 0 { 0 } else { u32::MAX << (32 - *len as u32) };
                    if let Some(&i) = map.get(&(bits & mask)) { return (&self.groups[i as usize], Some(i)); }
                }
            }
            IpAddr::V6(v6) => {
                let bits = u128::from(v6);
                for (len, map) in self.v6.iter() {
                    let mask = if *len == 0 { 0 } else { u128::MAX << (128 - *len as u32) };
                    if let Some(&i) = map.get(&(bits & mask)) { return (&self.groups[i as usize], Some(i)); }
                }
            }
        }
        (&self.global, None)
    }
}
```

`PolicyTable::build` steps: (1) index `rewrite_sets` by id (duplicate id -> reason), converting each set into a per-set `RewriteTable` after validating every rule (name: optional `*.` prefix followed by 1..=127 labels of `[a-z0-9_-]{1,63}`, total <= 253, lowercase; value parse per type with `Ipv4Addr`/`Ipv6Addr`/`Name::from_ascii(format!("{v}."))`; ttl <= 86400) and grouping A/AAAA per key; (2) for global and for each group, merge its sets in listed order with `entry(key).or_insert_with(|| clone of that set's answer)` into one `RewriteTable`; (3) for each group, read every `blocklists` blob with `blobs.read` and `decode_blob` (errors become `policy group <id>: <SnapshotError>`), and build `FilterSet::build(&decoded, &[allowlist.join("\n").into_bytes()], global_filter.mode, global_filter.ttl).0`, caching the `Arc<FilterSet>` by the key `sorted sha256 list + "|" + allowlist joined by ","`; (4) the global policy uses `global_filter` unchanged; (5) parse each CIDR with `ipnet::IpNet`, require `net == net.trunc()`, map IPv4 into `v4` and IPv6 into `v6` keyed by `(prefix_len, network bits)`, and reject a key already present with the "also in group" reason naming the earlier group; (6) sort the per-length maps by length descending. Rewrite rule names are converted with `filter::domain_to_wire` (wildcards: the part after `*.`).
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib filter::policy_tests` — expect PASS (3 tests).
- [ ] Write the failing tests at the bottom of `engine/src/server/rewrite.rs`:

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use crate::filter::{BlockMode, FilterSet, PolicyTable};
    use crate::proto::{BlobRef, ConfigSnapshot, RewriteRule, RewriteSet, RewriteType};
    use crate::snapshot::{BlobSource, SnapshotError};
    use hickory_proto::op::{MessageType, Query};
    use hickory_proto::rr::rdata::{A, CNAME};
    use std::cell::Cell;
    use std::net::Ipv4Addr;
    use std::sync::Arc;

    struct Ctx { table: PolicyTable, upstream_calls: Cell<u32> }
    impl RewriteContext for Ctx {
        fn policy(&self) -> &EffectivePolicy { self.table.select("192.0.2.1".parse().unwrap()).0 }
        async fn resolve(&self, name: &Name, qtype: RecordType) -> Result<Message, ()> {
            self.upstream_calls.set(self.upstream_calls.get() + 1);
            let mut m = Message::response(1, OpCode::Query);
            if name.to_ascii() == "forcesafesearch.google.com." && qtype == RecordType::A {
                m.answers.push(Record::from_rdata(name.clone(), 300, RData::A(A(Ipv4Addr::new(216, 239, 38, 120)))));
                Ok(m)
            } else { Err(()) }
        }
        fn block(&self, owner: &Name, _q: RecordType) -> (ResponseCode, Vec<Record>) {
            (ResponseCode::NoError, vec![Record::from_rdata(owner.clone(), 10, RData::A(A(Ipv4Addr::UNSPECIFIED)))])
        }
    }
    struct NoBlobs;
    impl BlobSource for NoBlobs {
        fn read(&self, r: &BlobRef) -> Result<Vec<u8>, SnapshotError> {
            Err(SnapshotError::Blob { sha256: r.sha256.clone(), reason: "unused".into() })
        }
    }
    fn ctx(rules: Vec<(&str, RewriteType, &str)>, blocked: &str) -> Ctx {
        let snap = ConfigSnapshot {
            rewrite_sets: vec![RewriteSet { id: "s".into(), label: "s".into(), rules: rules.into_iter()
                .map(|(n, t, v)| RewriteRule { name: n.into(), r#type: t as i32, value: v.into(), ttl: 300 }).collect() }],
            global_rewrite_set_ids: vec!["s".into()],
            ..Default::default()
        };
        let global = Arc::new(FilterSet::build(&[blocked.as_bytes().to_vec()], &[], BlockMode::NullIp, 10).0);
        Ctx { table: PolicyTable::build(&snap, global, &NoBlobs).unwrap(), upstream_calls: Cell::new(0) }
    }
    fn query(name: &str, t: RecordType) -> Vec<u8> {
        let mut m = Message::new(0xbeef, MessageType::Query, OpCode::Query);
        m.metadata.recursion_desired = true;
        m.queries.push(Query::query(Name::from_ascii(name).unwrap(), t));
        m.to_vec().unwrap()
    }
    async fn run(c: &Ctx, name: &str, t: RecordType) -> Message {
        let wire = Name::from_ascii(name).unwrap().to_lowercase().to_bytes().unwrap();
        let crate::filter::Verdict::Rewrite(first) = c.policy().check(&wire) else { panic!("no rewrite for {name}") };
        Message::from_vec(&rewrite_response(c, &query(name, t), first).await).unwrap()
    }

    #[tokio::test]
    async fn safe_search_cname_is_chased_and_keeps_client_casing() {
        let c = ctx(vec![("www.google.com", RewriteType::Cname, "forcesafesearch.google.com")], "");
        let m = run(&c, "WWW.Google.com.", RecordType::A).await;
        assert_eq!(m.metadata.id, 0xbeef);
        assert_eq!(m.metadata.response_code, ResponseCode::NoError);
        assert_eq!(m.answers[0].name.to_ascii(), "WWW.Google.com.");
        assert_eq!(m.answers[0].data, RData::CNAME(CNAME(Name::from_ascii("forcesafesearch.google.com.").unwrap())));
        assert_eq!(m.answers[1].data, RData::A(A(Ipv4Addr::new(216, 239, 38, 120))));
        let only = run(&c, "www.google.com.", RecordType::CNAME).await;
        assert_eq!(only.answers.len(), 1);
        assert_eq!(c.upstream_calls.get(), 1, "qtype CNAME does not chase");
    }

    #[tokio::test]
    async fn addrs_nodata_chains_loops_and_blocked_targets() {
        let c = ctx(vec![
            ("nas.home.test", RewriteType::A, "192.168.1.50"),
            ("x.home.test", RewriteType::Cname, "nas.home.test"),
            ("loop1.test", RewriteType::Cname, "loop2.test"),
            ("loop2.test", RewriteType::Cname, "loop1.test"),
            ("tracked.test", RewriteType::Cname, "ads.bad.test"),
            ("dead.test", RewriteType::Cname, "nowhere.test"),
        ], "bad.test\n");
        let m = run(&c, "nas.home.test.", RecordType::A).await;
        assert_eq!(m.answers[0].data, RData::A(A(Ipv4Addr::new(192, 168, 1, 50))));
        assert_eq!(m.answers[0].ttl, 300);
        let m = run(&c, "nas.home.test.", RecordType::MX).await;
        assert_eq!((m.metadata.response_code, m.answers.len()), (ResponseCode::NoError, 0));
        let m = run(&c, "x.home.test.", RecordType::A).await;
        assert_eq!(m.answers.len(), 2);
        assert_eq!(m.answers[1].data, RData::A(A(Ipv4Addr::new(192, 168, 1, 50))));
        let m = run(&c, "loop1.test.", RecordType::A).await;
        assert_eq!((m.metadata.response_code, m.answers.len()), (ResponseCode::ServFail, 0));
        let m = run(&c, "tracked.test.", RecordType::A).await;
        assert_eq!(m.answers[1].data, RData::A(A(Ipv4Addr::UNSPECIFIED)));
        let m = run(&c, "dead.test.", RecordType::A).await;
        assert_eq!(m.metadata.response_code, ResponseCode::ServFail);
    }
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::rewrite` — expect FAIL with "cannot find function `rewrite_response` in this scope".
- [ ] Implement `engine/src/server/rewrite.rs`:

```rust
use crate::filter::{EffectivePolicy, RewriteAnswer, Verdict};
use hickory_proto::op::{Edns, Message, OpCode, ResponseCode};
use hickory_proto::rr::rdata::{A, AAAA, CNAME};
use hickory_proto::rr::{Name, RData, Record, RecordType};
use hickory_proto::serialize::binary::BinEncodable;

pub const MAX_REWRITE_DEPTH: usize = 8;

pub trait RewriteContext {
    fn policy(&self) -> &EffectivePolicy;
    async fn resolve(&self, name: &Name, qtype: RecordType) -> Result<Message, ()>;
    fn block(&self, owner: &Name, qtype: RecordType) -> (ResponseCode, Vec<Record>);
}

fn skeleton(q: &Message) -> Message {
    let mut m = Message::response(q.metadata.id, OpCode::Query);
    m.metadata.recursion_desired = q.metadata.recursion_desired;
    m.metadata.recursion_available = true;
    m.metadata.checking_disabled = q.metadata.checking_disabled;
    m.queries = q.queries.clone();
    if q.edns.is_some() {
        let mut e = Edns::new();
        e.set_max_payload(1232);
        m.edns = Some(e);
    }
    m
}

fn addr_records(owner: &Name, a: &[(std::net::Ipv4Addr, u32)], aaaa: &[(std::net::Ipv6Addr, u32)], qtype: RecordType) -> Vec<Record> {
    match qtype {
        RecordType::A => a.iter().map(|(ip, ttl)| Record::from_rdata(owner.clone(), *ttl, RData::A(A(*ip)))).collect(),
        RecordType::AAAA => aaaa.iter().map(|(ip, ttl)| Record::from_rdata(owner.clone(), *ttl, RData::AAAA(AAAA(*ip)))).collect(),
        _ => Vec::new(),
    }
}

pub async fn rewrite_response<C: RewriteContext>(ctx: &C, query: &[u8], first: &RewriteAnswer) -> Vec<u8> {
    let Ok(q) = Message::from_vec(query) else { return Vec::new() };
    let Some(question) = q.queries.first() else { return Vec::new() };
    let qtype = question.query_type();
    let mut resp = skeleton(&q);
    let mut owner = question.name().clone();
    let mut seen = vec![owner.to_lowercase()];
    let mut current = first;
    crate::telemetry::metrics::ENCRYPTED.rewritten();
    for hop in 0..=MAX_REWRITE_DEPTH {
        match current {
            RewriteAnswer::Addrs { a, aaaa } => {
                resp.answers.extend(addr_records(&owner, a, aaaa, qtype));
                break;
            }
            RewriteAnswer::Cname { target, ttl } => {
                resp.answers.push(Record::from_rdata(owner.clone(), *ttl, RData::CNAME(CNAME(target.clone()))));
                if qtype == RecordType::CNAME { break; }
                let lower = target.to_lowercase();
                if seen.contains(&lower) || hop == MAX_REWRITE_DEPTH {
                    resp.answers.clear();
                    resp.metadata.response_code = ResponseCode::ServFail;
                    break;
                }
                seen.push(lower.clone());
                let wire = lower.to_bytes().unwrap_or_default();
                match ctx.policy().check(&wire) {
                    Verdict::Rewrite(next) => { owner = target.clone(); current = next; }
                    Verdict::Blocked => {
                        let (rcode, records) = ctx.block(target, qtype);
                        resp.answers.extend(records);
                        resp.metadata.response_code = rcode;
                        break;
                    }
                    Verdict::Pass | Verdict::Allowed => {
                        match ctx.resolve(target, qtype).await {
                            Ok(up) => {
                                resp.metadata.response_code = up.metadata.response_code;
                                resp.answers.extend(up.answers);
                                resp.authorities = up.authorities;
                            }
                            Err(()) => { resp.answers.clear(); resp.metadata.response_code = ResponseCode::ServFail; }
                        }
                        break;
                    }
                }
            }
        }
    }
    resp.to_vec().unwrap_or_default()
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib server::rewrite` — expect PASS (2 tests).
- [ ] Wire policy into the pipeline. In `engine/src/runtime.rs`: `Runtime.filter` becomes `Arc<FilterSet>` (call sites deref unchanged) and `Runtime::build` sets `policy: PolicyTable::build(s, filter.clone(), blobs).map_err(SnapshotError::Invalid)?`. In `engine/src/server/mod.rs` `handle_packet`, after the ACL check and before the cache lookup: `let (policy, group) = rt.policy.select(client.ip());` then `match policy.check(q.name.as_wire())`: `Verdict::Rewrite(RewriteAnswer::Addrs { .. })` -> build the reply synchronously with `rewrite_response` driven by `futures_util::FutureExt::now_or_never` (an `Addrs` answer never awaits), copy it into `out` applying `reply_limit` (over the limit: re-encode with TC=1 and no answer/authority records) and return `FastOutcome::Reply(n)`; `Verdict::Rewrite(RewriteAnswer::Cname { .. })` -> `FastOutcome::Rewrite(RewriteJob { query: packet.into(), client, transport, limit })`; `Verdict::Blocked` -> `policy.filter().write_block_reply(..)` (M1's blocked branch with `rt.filter` replaced by `policy.filter()`); `Allowed`/`Pass` -> M1's cache/miss path, with M1's CNAME-cloaking check calling `policy.filter().cloaked(..)`. Rewrite replies are never inserted into the cache. The query-log record gets `filter: FilterOutcome::Rewritten` for rewrites and `policy_group: group.unwrap_or(u16::MAX)`; `nexora.policy.group` is exported as the group id looked up in the runtime current at export time (empty for `u16::MAX`), and `nexora.filter` values are `none|blocked|allowed|rewritten`.
- [ ] Implement `WorkerRewriteCtx` and `run_rewrite_job` in `engine/src/server/rewrite.rs`: `policy()` returns `self.rt.policy.select(self.client.ip()).0`; `resolve(name, qtype)` builds a query (`Message::query()`, RD=1, one question) and runs it through `handle_packet(&self.ctx, &self.rt, &wire, self.client, Transport::Tcp, &mut buf)` with a 65535-byte `buf`, awaiting `resolve_miss(self.ctx.clone(), self.rt.clone(), job)` on `FastOutcome::Miss`, returning `Err(())` on `Drop` or a decode failure; `block(owner, qtype)` returns `(NXDOMAIN, [])` for `BlockMode::NxDomain`, `(REFUSED, [])` for `Refused`, and for `NullIp` `(NOERROR, [A 0.0.0.0 | AAAA ::])` with TTL `filter.ttl` (no records for other qtypes); `run_rewrite_job` re-selects the verdict for the job's name, calls `rewrite_response`, and applies `job.limit` as above. In `engine/src/server/udp.rs` spawn `FastOutcome::Rewrite(job)` with `spawn_local` exactly like `FastOutcome::Miss`, sending the returned bytes; in `WorkerAnswerer::answer` (Task 2) add the arm `FastOutcome::Rewrite(job) => { let reply = rewrite::run_rewrite_job(self.0.clone(), rt, job).await; out.clear(); out.extend_from_slice(&reply); }`. The internal target lookup of a CNAME rewrite is recorded in the query log as its own record with the same client.
- [ ] Extend the snapshot in `engine/tests/hot_path_alloc.rs` (`cache_hit_path_does_not_allocate`) with `policy_groups: vec![PolicyGroup { id: "g1".into(), name: "g1".into(), cidrs: vec!["10.0.0.0/8".into(), "10.1.0.0/16".into(), "2001:db8::/32".into()], rewrite_set_ids: vec!["r".into()], ..Default::default() }]` and `rewrite_sets: vec![RewriteSet { id: "r".into(), label: "r".into(), rules: vec![RewriteRule { name: "*.home.test".into(), r#type: RewriteType::A as i32, value: "192.168.1.1".into(), ttl: 60 }] }]`, extend its ACL with `10.0.0.0/8`, and send the measured cache-hit query from client `10.1.2.3:5353`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine` — expect PASS including `cache_hit_path_does_not_allocate`, `filter::policy_tests`, `server::rewrite`.
- [ ] Commit: `git add engine && git commit -m "feat(engine): per-client policy groups, rewrites and safe-search answers"`.

## Task 7: Management plane schema, store and snapshot policy section

Files:
- `mgmt/migrations/00200_policy_rewrites_tls.sql` — tables for groups, rewrites, global safe search, engine TLS state.
- `mgmt/internal/store/policies.go` — policy group + global safe search queries.
- `mgmt/internal/store/rewrites.go` — rewrite queries.
- `mgmt/internal/store/enginetls.go` — per-engine TLS state queries.
- `mgmt/internal/store/policies_test.go` — store tests against the test database.
- `mgmt/internal/snapshot/policy.go` — pure builder of the snapshot policy section.
- `mgmt/internal/snapshot/safesearch.go` — provider rewrite tables.
- `mgmt/internal/snapshot/policy_test.go` — builder tests.
- `mgmt/internal/snapshot/snapshot.go` (M1 `Build`) — calls `BuildPolicySection`.
- `mgmt/internal/blocklist` fetch scheduler (M1, later task) — also fetches disabled lists referenced by any policy group.

Interfaces:
- Consumed from M1 (Task 12): migration `00001_init.sql` tables `filter_lists` (`id uuid`, `enabled`, `kind`, `current_blob_sha256`), `blobs` (`sha256`, `size`), `engines` (`id uuid`, `node_name`); `store.Open(ctx, url) (*store.Store, error)`, `type Store struct { Pool *pgxpool.Pool }`, `(*Store).Migrate`, `(*Store).InTx(ctx, fn func(pgx.Tx) error) error`, `store.ErrNotFound`, `store.ErrConflict`, `store.MapError`. (Task 13): `snapshot.Build(ctx, tx, version, cfg)`. (Task 10): `harness.New(t).StartPostgres()` with `URL`. `github.com/google/uuid`.
- Consumed from Task 1: `controlv1.PolicyGroup`, `RewriteSet`, `RewriteRule`, `RewriteType_*`, `ConfigSnapshot.{PolicyGroups, RewriteSets, GlobalRewriteSetIds}`, `controlv1.BlobRef`.
- Produced (package `store`):
  - `type SafeSearch struct { Google, Bing, DuckDuckGo bool; YouTube string }` (`"off"|"moderate"|"strict"`).
  - `type PolicyGroup struct { ID uuid.UUID; Name, Description string; CIDRs []netip.Prefix; FilterListIDs []uuid.UUID; Allowlist []string; SafeSearch SafeSearch; Revision int64; CreatedAt, UpdatedAt time.Time }`.
  - `type Rewrite struct { ID uuid.UUID; GroupID *uuid.UUID; Name, Type, Value string; TTL int32; Revision int64; CreatedAt, UpdatedAt time.Time }`.
  - `type GlobalSafeSearch struct { SafeSearch; Revision int64 }`.
  - `type EngineTLSState struct { EngineID uuid.UUID; Fingerprint string; Applied bool; Error string; UpdatedAt time.Time }`.
  - `type Querier interface { Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error); Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error); QueryRow(ctx context.Context, sql string, args ...any) pgx.Row }` named `PolicyQuerier`.
  - `var ErrCIDRInUse, ErrUnknownFilterList, ErrUnknownGroup, ErrRewriteCNAMEConflict error` (M1's `ErrNotFound`/`ErrConflict` are reused).
  - `func ListPolicyGroups(ctx context.Context, q PolicyQuerier) ([]PolicyGroup, error)`; `GetPolicyGroup(ctx, q, id uuid.UUID) (PolicyGroup, error)`; `CreatePolicyGroup(ctx, tx pgx.Tx, g PolicyGroup) (PolicyGroup, error)`; `UpdatePolicyGroup(ctx, tx pgx.Tx, g PolicyGroup) (PolicyGroup, error)` (uses `g.Revision` as the expected revision); `DeletePolicyGroup(ctx, tx pgx.Tx, id uuid.UUID, revision int64) error`; `GetGlobalSafeSearch(ctx, q) (GlobalSafeSearch, error)`; `UpdateGlobalSafeSearch(ctx, tx pgx.Tx, s GlobalSafeSearch) (GlobalSafeSearch, error)`.
  - `func ListRewrites(ctx, q PolicyQuerier, groupID *uuid.UUID, allScopes bool) ([]Rewrite, error)`; `CreateRewrite(ctx, tx, r Rewrite) (Rewrite, error)`; `UpdateRewrite(ctx, tx, r Rewrite) (Rewrite, error)`; `DeleteRewrite(ctx, tx, id uuid.UUID, revision int64) error`.
  - `func UpsertEngineTLSState(ctx, q PolicyQuerier, s EngineTLSState) error`; `ListEngineTLSState(ctx, q PolicyQuerier) ([]EngineTLSState, error)`.
- Produced (package `snapshot`): `func BuildPolicySection(groups []store.PolicyGroup, listBlobs map[uuid.UUID]*controlv1.BlobRef, rewrites []store.Rewrite, global store.SafeSearch) PolicySection` (`listBlobs` holds the current blob of every fetched blocklist, enabled or not); `type PolicySection struct { Groups []*controlv1.PolicyGroup; RewriteSets []*controlv1.RewriteSet; GlobalRewriteSetIDs []string }`; `func SafeSearchSet(id string) *controlv1.RewriteSet`; constants `SafeSearchGoogle = "safesearch:google"`, `SafeSearchBing = "safesearch:bing"`, `SafeSearchDuckDuckGo = "safesearch:duckduckgo"`, `SafeSearchYouTubeStrict = "safesearch:youtube-strict"`, `SafeSearchYouTubeModerate = "safesearch:youtube-moderate"`.

- [ ] Write the migration `mgmt/migrations/00200_policy_rewrites_tls.sql`:

```sql
-- +goose Up
CREATE TABLE policy_groups (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                   text NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9 _.-]{0,62}$'),
    description            text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    safe_search_google     boolean NOT NULL DEFAULT false,
    safe_search_bing       boolean NOT NULL DEFAULT false,
    safe_search_duckduckgo boolean NOT NULL DEFAULT false,
    safe_search_youtube    text NOT NULL DEFAULT 'off' CHECK (safe_search_youtube IN ('off', 'moderate', 'strict')),
    revision               bigint NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

-- An exact prefix belongs to at most one group; overlapping prefixes are allowed (most specific wins).
CREATE TABLE policy_group_cidrs (
    cidr     cidr PRIMARY KEY,
    group_id uuid NOT NULL REFERENCES policy_groups (id) ON DELETE CASCADE
);
CREATE INDEX policy_group_cidrs_group ON policy_group_cidrs (group_id);

CREATE TABLE policy_group_filter_lists (
    group_id       uuid NOT NULL REFERENCES policy_groups (id) ON DELETE CASCADE,
    filter_list_id uuid NOT NULL REFERENCES filter_lists (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, filter_list_id)
);

CREATE TABLE policy_group_allowlist (
    group_id uuid NOT NULL REFERENCES policy_groups (id) ON DELETE CASCADE,
    domain   text NOT NULL CHECK (domain ~ '^([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?\.)*[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$' AND length(domain) <= 253),
    PRIMARY KEY (group_id, domain)
);

CREATE TABLE rewrites (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id   uuid REFERENCES policy_groups (id) ON DELETE CASCADE, -- NULL = global
    name       text NOT NULL CHECK (name ~ '^(\*\.)?([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?\.)*[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$' AND length(name) <= 253),
    type       text NOT NULL CHECK (type IN ('A', 'AAAA', 'CNAME')),
    value      text NOT NULL CHECK (length(value) BETWEEN 1 AND 253),
    ttl        integer NOT NULL DEFAULT 300 CHECK (ttl BETWEEN 0 AND 86400),
    revision   bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX rewrites_unique_record
    ON rewrites (COALESCE(group_id, '00000000-0000-0000-0000-000000000000'::uuid), name, type, value);
CREATE INDEX rewrites_scope_name ON rewrites (group_id, name);

CREATE TABLE global_safe_search (
    singleton  boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    google     boolean NOT NULL DEFAULT false,
    bing       boolean NOT NULL DEFAULT false,
    duckduckgo boolean NOT NULL DEFAULT false,
    youtube    text NOT NULL DEFAULT 'off' CHECK (youtube IN ('off', 'moderate', 'strict')),
    revision   bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO global_safe_search DEFAULT VALUES;

-- Which DNS serving certificate each engine accepted. No key material is stored.
CREATE TABLE engine_tls_state (
    engine_id   uuid PRIMARY KEY REFERENCES engines (id) ON DELETE CASCADE,
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    applied     boolean NOT NULL,
    error       text NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE engine_tls_state;
DROP TABLE global_safe_search;
DROP TABLE rewrites;
DROP TABLE policy_group_allowlist;
DROP TABLE policy_group_filter_lists;
DROP TABLE policy_group_cidrs;
DROP TABLE policy_groups;
```

- [ ] Write the failing store test `mgmt/internal/store/policies_test.go`:

```go
package store_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	pg := harness.New(t).StartPostgres()
	ctx := context.Background()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func inTx(t *testing.T, s *store.Store, fn func(tx pgx.Tx) error) error {
	t.Helper()
	return s.InTx(context.Background(), fn)
}

func TestPolicyGroupsRevisionAndCIDRUniqueness(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	var kids store.PolicyGroup
	err := inTx(t, s, func(tx pgx.Tx) error {
		var err error
		kids, err = store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{
			Name:       "kids",
			CIDRs:      []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
			Allowlist:  []string{"school.example"},
			SafeSearch: store.SafeSearch{Google: true, YouTube: "strict"},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if kids.Revision != 1 || len(kids.CIDRs) != 1 {
		t.Fatalf("created = %+v", kids)
	}
	err = inTx(t, s, func(tx pgx.Tx) error {
		_, err := store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "narrow", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}})
		return err
	})
	if err != nil {
		t.Fatalf("overlapping narrower prefix must be allowed: %v", err)
	}
	err = inTx(t, s, func(tx pgx.Tx) error {
		_, err := store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "dup", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
		return err
	})
	if !errors.Is(err, store.ErrCIDRInUse) {
		t.Fatalf("duplicate prefix: got %v, want ErrCIDRInUse", err)
	}
	kids.Description = "first edit"
	err = inTx(t, s, func(tx pgx.Tx) error {
		updated, err := store.UpdatePolicyGroup(ctx, tx, kids)
		if err == nil && updated.Revision != 2 {
			t.Errorf("revision after update = %d", updated.Revision)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	kids.Description = "stale edit"
	err = inTx(t, s, func(tx pgx.Tx) error { _, err := store.UpdatePolicyGroup(ctx, tx, kids); return err })
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale revision: got %v, want ErrConflict", err)
	}
	got, err := store.GetPolicyGroup(ctx, s.Pool, kids.ID)
	if err != nil || got.Description != "first edit" || got.SafeSearch.YouTube != "strict" || got.Allowlist[0] != "school.example" {
		t.Fatalf("get = %+v, %v", got, err)
	}
}

func TestRewritesScopesAndGlobalSafeSearch(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	err := inTx(t, s, func(tx pgx.Tx) error {
		if _, err := store.CreateRewrite(ctx, tx, store.Rewrite{Name: "nas.home.test", Type: "A", Value: "192.168.1.50", TTL: 120}); err != nil {
			return err
		}
		_, err := store.CreateRewrite(ctx, tx, store.Rewrite{Name: "nas.home.test", Type: "CNAME", Value: "other.home.test", TTL: 120})
		return err
	})
	if !errors.Is(err, store.ErrRewriteCNAMEConflict) {
		t.Fatalf("CNAME beside A: got %v", err)
	}
	err = inTx(t, s, func(tx pgx.Tx) error {
		_, err := store.CreateRewrite(ctx, tx, store.Rewrite{Name: "*.lab.home.test", Type: "CNAME", Value: "nas.home.test", TTL: 60})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := store.ListRewrites(ctx, s.Pool, nil, true)
	if err != nil || len(all) != 1 || all[0].GroupID != nil {
		t.Fatalf("list = %+v, %v", all, err)
	}
	g, err := store.GetGlobalSafeSearch(ctx, s.Pool)
	if err != nil || g.Revision != 1 || g.YouTube != "off" {
		t.Fatalf("global = %+v, %v", g, err)
	}
	g.Bing = true
	err = inTx(t, s, func(tx pgx.Tx) error { _, err := store.UpdateGlobalSafeSearch(ctx, tx, g); return err })
	if err != nil {
		t.Fatal(err)
	}
	err = inTx(t, s, func(tx pgx.Tx) error { _, err := store.UpdateGlobalSafeSearch(ctx, tx, g); return err })
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale global safe search: got %v", err)
	}
}
```

The first transaction in `TestRewritesScopesAndGlobalSafeSearch` rolls back as a whole, which is why the list afterwards holds only the wildcard rewrite.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/store/ -run 'TestPolicyGroups|TestRewritesScopes'` — expect FAIL with "undefined: store.CreatePolicyGroup".
- [ ] Implement `mgmt/internal/store/policies.go`, `rewrites.go`, `enginetls.go` with pgx: `CreatePolicyGroup` inserts the row then child rows (`policy_group_cidrs`, `policy_group_filter_lists`, `policy_group_allowlist`) in the same transaction; `UpdatePolicyGroup` runs `UPDATE policy_groups SET name=$2, description=$3, safe_search_google=$4, safe_search_bing=$5, safe_search_duckduckgo=$6, safe_search_youtube=$7, revision = revision + 1, updated_at = now() WHERE id = $1 AND revision = $8 RETURNING revision, created_at, updated_at`, and on zero rows returns `ErrNotFound` when `SELECT 1 FROM policy_groups WHERE id=$1` finds nothing, else `ErrConflict`; it then deletes and re-inserts child rows; `DeletePolicyGroup` uses `DELETE ... WHERE id=$1 AND revision=$2` with the same not-found/conflict split. Map `*pgconn.PgError` code `23505` with `ConstraintName == "policy_group_cidrs_pkey"` to `ErrCIDRInUse`, `23505` on `policy_groups_name_key` to `ErrConflict` wrapped as `fmt.Errorf("%w: name already used", ErrConflict)`, and `23503` on `policy_group_filter_lists_filter_list_id_fkey` to `ErrUnknownFilterList`. `CreateRewrite`/`UpdateRewrite` first run `SELECT type FROM rewrites WHERE group_id IS NOT DISTINCT FROM $1 AND name = $2 AND id <> $3 FOR UPDATE` and return `ErrRewriteCNAMEConflict` when the new type is `CNAME` and any row exists, or when any existing row is `CNAME`; `23503` on `rewrites_group_id_fkey` -> `ErrUnknownGroup`. `ListRewrites(ctx, q, nil, true)` returns every rewrite ordered by `group_id NULLS FIRST, name, type, value`; `(ctx, q, nil, false)` only global; `(ctx, q, &id, false)` only that group. `UpsertEngineTLSState` uses `INSERT ... ON CONFLICT (engine_id) DO UPDATE SET fingerprint=EXCLUDED.fingerprint, applied=EXCLUDED.applied, error=EXCLUDED.error, updated_at=now()`.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/store/ -run 'TestPolicyGroups|TestRewritesScopes'` — expect PASS.
- [ ] Write the failing builder test `mgmt/internal/snapshot/policy_test.go`:

```go
package snapshot_test

import (
	"net/netip"
	"testing"

	"github.com/google/uuid"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func TestBuildPolicySection(t *testing.T) {
	groupOnly := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	unfetched := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	listBlobs := map[uuid.UUID]*controlv1.BlobRef{groupOnly: {Sha256: "ab12", Size: 42, Name: "ads"}}
	kidsID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	kids := store.PolicyGroup{
		ID: kidsID, Name: "kids",
		CIDRs:         []netip.Prefix{netip.MustParsePrefix("192.168.50.0/24")},
		FilterListIDs: []uuid.UUID{groupOnly, unfetched},
		Allowlist:     []string{"school.example"},
		SafeSearch:    store.SafeSearch{Google: true, YouTube: "strict"},
	}
	rewrites := []store.Rewrite{
		{Name: "nas.home.test", Type: "A", Value: "192.168.1.50", TTL: 120},
		{GroupID: &kidsID, Name: "www.google.com", Type: "A", Value: "192.0.2.99", TTL: 60},
	}
	sec := snapshot.BuildPolicySection([]store.PolicyGroup{kids}, listBlobs, rewrites, store.SafeSearch{Bing: true, YouTube: "moderate"})

	if bl := sec.Groups[0].Blocklists; len(bl) != 1 || bl[0].Sha256 != "ab12" || bl[0].Size != 42 {
		t.Fatalf("group blocklists = %v (a list without a fetched blob is skipped)", bl)
	}
	wantGlobal := []string{"custom:global", snapshot.SafeSearchBing, snapshot.SafeSearchYouTubeModerate}
	if !equal(sec.GlobalRewriteSetIDs, wantGlobal) {
		t.Fatalf("global sets = %v, want %v", sec.GlobalRewriteSetIDs, wantGlobal)
	}
	g := sec.Groups[0]
	wantGroup := []string{"custom:group:" + kidsID.String(), snapshot.SafeSearchGoogle, snapshot.SafeSearchYouTubeStrict}
	if g.Id != kidsID.String() || !equal(g.Cidrs, []string{"192.168.50.0/24"}) || !equal(g.RewriteSetIds, wantGroup) || !equal(g.Allowlist, []string{"school.example"}) {
		t.Fatalf("group = %v", g)
	}
	ids := map[string]*controlv1.RewriteSet{}
	for _, s := range sec.RewriteSets {
		if ids[s.Id] != nil {
			t.Fatalf("duplicate set %s", s.Id)
		}
		ids[s.Id] = s
	}
	if len(ids) != 6 {
		t.Fatalf("sets = %d, want 6 (2 custom + google, bing, youtube strict, youtube moderate)", len(ids))
	}
	google := ids[snapshot.SafeSearchGoogle]
	found := false
	for _, r := range google.Rules {
		if r.Name == "www.google.co.uk" && r.Type == controlv1.RewriteType_REWRITE_TYPE_CNAME && r.Value == "forcesafesearch.google.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("google set lacks www.google.co.uk -> forcesafesearch.google.com")
	}
	if ids[snapshot.SafeSearchYouTubeStrict].Rules[0].Value != "restrict.youtube.com" {
		t.Fatalf("youtube strict target = %s", ids[snapshot.SafeSearchYouTubeStrict].Rules[0].Value)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/snapshot/ -run TestBuildPolicySection` — expect FAIL with "undefined: snapshot.BuildPolicySection".
- [ ] Implement `mgmt/internal/snapshot/safesearch.go`:

```go
package snapshot

import controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"

const (
	SafeSearchGoogle          = "safesearch:google"
	SafeSearchBing            = "safesearch:bing"
	SafeSearchDuckDuckGo      = "safesearch:duckduckgo"
	SafeSearchYouTubeStrict   = "safesearch:youtube-strict"
	SafeSearchYouTubeModerate = "safesearch:youtube-moderate"
	safeSearchTTL             = 300
)

// googleDomains is https://www.google.com/supported_domains (fetched 2026-09-13).
var googleDomains = []string{
	"google.com", "google.ad", "google.ae", "google.com.af", "google.com.ag", "google.al", "google.am", "google.co.ao",
	"google.com.ar", "google.as", "google.at", "google.com.au", "google.az", "google.ba", "google.com.bd", "google.be",
	"google.bf", "google.bg", "google.com.bh", "google.bi", "google.bj", "google.com.bn", "google.com.bo", "google.com.br",
	"google.bs", "google.bt", "google.co.bw", "google.by", "google.com.bz", "google.ca", "google.cd", "google.cf",
	"google.cg", "google.ch", "google.ci", "google.co.ck", "google.cl", "google.cm", "google.cn", "google.com.co",
	"google.co.cr", "google.com.cu", "google.cv", "google.com.cy", "google.cz", "google.de", "google.dj", "google.dk",
	"google.dm", "google.com.do", "google.dz", "google.com.ec", "google.ee", "google.com.eg", "google.es", "google.com.et",
	"google.fi", "google.com.fj", "google.fm", "google.fr", "google.ga", "google.ge", "google.gg", "google.com.gh",
	"google.com.gi", "google.gl", "google.gm", "google.gr", "google.com.gt", "google.gy", "google.com.hk", "google.hn",
	"google.hr", "google.ht", "google.hu", "google.co.id", "google.ie", "google.co.il", "google.im", "google.co.in",
	"google.iq", "google.is", "google.it", "google.je", "google.com.jm", "google.jo", "google.co.jp", "google.co.ke",
	"google.com.kh", "google.ki", "google.kg", "google.co.kr", "google.com.kw", "google.kz", "google.la", "google.com.lb",
	"google.li", "google.lk", "google.co.ls", "google.lt", "google.lu", "google.lv", "google.com.ly", "google.co.ma",
	"google.md", "google.me", "google.mg", "google.mk", "google.ml", "google.com.mm", "google.mn", "google.com.mt",
	"google.mu", "google.mv", "google.mw", "google.com.mx", "google.com.my", "google.co.mz", "google.com.na", "google.com.ng",
	"google.com.ni", "google.ne", "google.nl", "google.no", "google.com.np", "google.nr", "google.nu", "google.co.nz",
	"google.com.om", "google.com.pa", "google.com.pe", "google.com.pg", "google.com.ph", "google.com.pk", "google.pl", "google.pn",
	"google.com.pr", "google.ps", "google.pt", "google.com.py", "google.com.qa", "google.ro", "google.ru", "google.rw",
	"google.com.sa", "google.com.sb", "google.sc", "google.se", "google.com.sg", "google.sh", "google.si", "google.sk",
	"google.com.sl", "google.sn", "google.so", "google.sm", "google.sr", "google.st", "google.com.sv", "google.td",
	"google.tg", "google.co.th", "google.com.tj", "google.tl", "google.tm", "google.tn", "google.to", "google.com.tr",
	"google.tt", "google.com.tw", "google.co.tz", "google.com.ua", "google.co.ug", "google.co.uk", "google.com.uy", "google.co.uz",
	"google.com.vc", "google.co.ve", "google.co.vi", "google.com.vn", "google.vu", "google.ws", "google.rs", "google.co.za",
	"google.co.zm", "google.co.zw", "google.cat",
}

var youtubeNames = []string{"www.youtube.com", "m.youtube.com", "youtubei.googleapis.com", "youtube.googleapis.com", "www.youtube-nocookie.com"}

func cnameSet(id, label, target string, names []string) *controlv1.RewriteSet {
	s := &controlv1.RewriteSet{Id: id, Label: label}
	for _, n := range names {
		s.Rules = append(s.Rules, &controlv1.RewriteRule{Name: n, Type: controlv1.RewriteType_REWRITE_TYPE_CNAME, Value: target, Ttl: safeSearchTTL})
	}
	return s
}

// SafeSearchSet returns the provider rewrite set for id, or nil for an unknown id.
func SafeSearchSet(id string) *controlv1.RewriteSet {
	switch id {
	case SafeSearchGoogle:
		names := make([]string, 0, 2*len(googleDomains))
		for _, d := range googleDomains {
			names = append(names, "www."+d, d)
		}
		return cnameSet(id, "Google SafeSearch", "forcesafesearch.google.com", names)
	case SafeSearchBing:
		return cnameSet(id, "Bing SafeSearch", "strict.bing.com", []string{"www.bing.com", "bing.com"})
	case SafeSearchDuckDuckGo:
		return cnameSet(id, "DuckDuckGo safe search", "safe.duckduckgo.com", []string{"duckduckgo.com", "www.duckduckgo.com", "start.duckduckgo.com"})
	case SafeSearchYouTubeStrict:
		return cnameSet(id, "YouTube Restricted (strict)", "restrict.youtube.com", youtubeNames)
	case SafeSearchYouTubeModerate:
		return cnameSet(id, "YouTube Restricted (moderate)", "restrictmoderate.youtube.com", youtubeNames)
	}
	return nil
}

func safeSearchIDs(s store.SafeSearch) []string {
	var ids []string
	if s.Google {
		ids = append(ids, SafeSearchGoogle)
	}
	if s.Bing {
		ids = append(ids, SafeSearchBing)
	}
	if s.DuckDuckGo {
		ids = append(ids, SafeSearchDuckDuckGo)
	}
	switch s.YouTube {
	case "strict":
		ids = append(ids, SafeSearchYouTubeStrict)
	case "moderate":
		ids = append(ids, SafeSearchYouTubeModerate)
	}
	return ids
}
```

(add `"github.com/piwi3910/nexora/mgmt/internal/store"` to the imports.)
- [ ] Implement `mgmt/internal/snapshot/policy.go`: `BuildPolicySection` sorts groups by name and rewrites by `(name, type, value)`; builds the custom set `custom:global` (label `Custom rewrites`) from global rewrites and `custom:group:<id>` (label `Custom rewrites: <group name>`) per group, omitting a custom set that has no rules; global set ids = `[custom:global if present] + safeSearchIDs(global)`; each group's `RewriteSetIds` = `[its custom set if present] + safeSearchIDs(group.SafeSearch)`; `RewriteSets` = custom sets followed by `SafeSearchSet(id)` for each distinct safe-search id referenced anywhere, in first-reference order; each group's `Blocklists` = `listBlobs[id]` for its `FilterListIDs` in order, skipping ids without a blob; rewrite `Type` maps `A`/`AAAA`/`CNAME` to the enum; `Cidrs` use `netip.Prefix.String()`.
- [ ] In M1's `snapshot.Build` (`mgmt/internal/snapshot/snapshot.go`), inside the same transaction: load groups (`ListPolicyGroups(ctx, tx)`), rewrites (`ListRewrites(ctx, tx, nil, true)`), global safe search, and `listBlobs` with `SELECT f.id, f.name, b.sha256, b.size FROM filter_lists f JOIN blobs b ON b.sha256 = f.current_blob_sha256 WHERE f.kind = 'blocklist'` (M1's kind value for block lists); call `BuildPolicySection`; set `PolicyGroups`, `RewriteSets`, `GlobalRewriteSetIds`. `FilterConfig` keeps M1's global selection (enabled lists) unchanged. In M1's blocklist fetch scheduler, the "lists due for fetch" query selects `WHERE enabled OR id IN (SELECT filter_list_id FROM policy_group_filter_lists)`. Engines fetch group blobs through M1's `GetBlob`, which serves any row in `blobs`.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/snapshot/ ./mgmt/internal/store/ ./mgmt/internal/blocklist/` — expect PASS including `TestBuildPolicySection` and M1's `snapshot_test.go`.
- [ ] Commit: `git add mgmt && git commit -m "feat(mgmt): policy groups, rewrites, safe search storage and snapshot section"`.

## Task 8: OpenAPI operations and handlers for policies, rewrites and safe search

Files:
- `mgmt/api/openapi.yaml` — paths and schemas below.
- `mgmt/internal/api/gen.go` (M1 oapi-codegen output) — regenerated.
- `web/src/api/schema.d.ts` — regenerated.
- `mgmt/internal/api/policies.go` — policy group and global safe-search handlers.
- `mgmt/internal/api/rewrites.go` — rewrite handlers.
- `mgmt/internal/api/policyvalidate.go` — input normalisation and validation.
- `mgmt/internal/api/policies_test.go` — handler tests.
- `mgmt/internal/auth/permissions.go` — operationId roles.

Interfaces:
- Consumed from M1 (Task 13): `snapshot.Mutate(ctx context.Context, st *store.Store, cfg snapshot.BuildConfig, a auth.Actor, fn func(tx pgx.Tx) (auth.Change, error)) (uint64, error)` (change -> audit -> snapshot -> `config_versions` -> `pg_notify` in one transaction); `auth.Change{Action, TargetType, TargetID string; Before, After any}`; `auth.Actor`. (Task 2 `Makefile`): `make proto` runs protoc, oapi-codegen (`mgmt/api/oapi-codegen.yaml`) and `pnpm run gen:api`. (M1, later task): the oapi-codegen strict server interface `StrictServerInterface` in `mgmt/internal/api`, the request actor accessor `auth.ActorFrom(ctx) auth.Actor`, `auth.RoleViewer`/`auth.RoleOperator`, the JSON error writer, and the handler test helper `apitest.New(t *testing.T) *apitest.Server` with `(*apitest.Server).As(role string) *apitest.Client` and `(*apitest.Client).JSON(method, path string, body any, out any) int`.
- Consumed from Task 7: `store.PolicyGroup`, `store.Rewrite`, `store.SafeSearch`, `store.GlobalSafeSearch`, all store functions and sentinel errors.
- Produced operationIds: `listPolicyGroups`, `createPolicyGroup`, `getPolicyGroup`, `updatePolicyGroup`, `deletePolicyGroup`, `getGlobalSafeSearch`, `updateGlobalSafeSearch`, `listRewrites`, `createRewrite`, `updateRewrite`, `deleteRewrite`.
- Error mapping: stale revision -> 409 `conflict`; `ErrCIDRInUse` -> 409 `cidr_in_use`; `ErrRewriteCNAMEConflict` -> 409 `rewrite_conflict`; `ErrUnknownFilterList`, `ErrUnknownGroup` and validation failures -> 422 `validation_failed`; `ErrNotFound` -> 404 `not_found`.

- [ ] Add to `mgmt/api/openapi.yaml` under `paths`:

```yaml
  /api/v1/policy-groups:
    get:
      operationId: listPolicyGroups
      tags: [policies]
      responses:
        '200':
          description: All client policy groups ordered by name.
          content:
            application/json:
              schema: {type: array, items: {$ref: '#/components/schemas/PolicyGroup'}}
    post:
      operationId: createPolicyGroup
      tags: [policies]
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/PolicyGroupInput'}
      responses:
        '201': {description: Created., content: {application/json: {schema: {$ref: '#/components/schemas/PolicyGroup'}}}}
        '409': {$ref: '#/components/responses/Error'}
        '422': {$ref: '#/components/responses/Error'}
  /api/v1/policy-groups/{id}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: string, format: uuid}}
    get:
      operationId: getPolicyGroup
      tags: [policies]
      responses:
        '200': {description: The group., content: {application/json: {schema: {$ref: '#/components/schemas/PolicyGroup'}}}}
        '404': {$ref: '#/components/responses/Error'}
    put:
      operationId: updatePolicyGroup
      tags: [policies]
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/PolicyGroupUpdate'}
      responses:
        '200': {description: Updated., content: {application/json: {schema: {$ref: '#/components/schemas/PolicyGroup'}}}}
        '404': {$ref: '#/components/responses/Error'}
        '409': {$ref: '#/components/responses/Error'}
        '422': {$ref: '#/components/responses/Error'}
    delete:
      operationId: deletePolicyGroup
      tags: [policies]
      parameters:
        - {name: revision, in: query, required: true, schema: {type: integer, format: int64}}
      responses:
        '204': {description: Deleted; the group's rewrites are deleted with it.}
        '404': {$ref: '#/components/responses/Error'}
        '409': {$ref: '#/components/responses/Error'}
  /api/v1/safe-search:
    get:
      operationId: getGlobalSafeSearch
      tags: [policies]
      responses:
        '200': {description: Safe search for clients in no group., content: {application/json: {schema: {$ref: '#/components/schemas/GlobalSafeSearch'}}}}
    put:
      operationId: updateGlobalSafeSearch
      tags: [policies]
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/GlobalSafeSearch'}
      responses:
        '200': {description: Updated., content: {application/json: {schema: {$ref: '#/components/schemas/GlobalSafeSearch'}}}}
        '409': {$ref: '#/components/responses/Error'}
  /api/v1/rewrites:
    get:
      operationId: listRewrites
      tags: [rewrites]
      parameters:
        - name: scope
          in: query
          required: false
          description: "`all` (default), `global`, or a policy group id."
          schema: {type: string}
      responses:
        '200':
          description: Rewrites ordered by scope, name, type, value.
          content:
            application/json:
              schema: {type: array, items: {$ref: '#/components/schemas/Rewrite'}}
        '422': {$ref: '#/components/responses/Error'}
    post:
      operationId: createRewrite
      tags: [rewrites]
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/RewriteInput'}
      responses:
        '201': {description: Created., content: {application/json: {schema: {$ref: '#/components/schemas/Rewrite'}}}}
        '409': {$ref: '#/components/responses/Error'}
        '422': {$ref: '#/components/responses/Error'}
  /api/v1/rewrites/{id}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: string, format: uuid}}
    put:
      operationId: updateRewrite
      tags: [rewrites]
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/RewriteUpdate'}
      responses:
        '200': {description: Updated., content: {application/json: {schema: {$ref: '#/components/schemas/Rewrite'}}}}
        '404': {$ref: '#/components/responses/Error'}
        '409': {$ref: '#/components/responses/Error'}
        '422': {$ref: '#/components/responses/Error'}
    delete:
      operationId: deleteRewrite
      tags: [rewrites]
      parameters:
        - {name: revision, in: query, required: true, schema: {type: integer, format: int64}}
      responses:
        '204': {description: Deleted.}
        '404': {$ref: '#/components/responses/Error'}
        '409': {$ref: '#/components/responses/Error'}
```

and under `components.schemas`:

```yaml
    SafeSearch:
      type: object
      required: [google, bing, duckduckgo, youtube]
      properties:
        google: {type: boolean}
        bing: {type: boolean}
        duckduckgo: {type: boolean}
        youtube: {type: string, enum: ['off', moderate, strict]}
    GlobalSafeSearch:
      allOf:
        - {$ref: '#/components/schemas/SafeSearch'}
        - type: object
          required: [revision]
          properties:
            revision: {type: integer, format: int64}
    PolicyGroupInput:
      type: object
      required: [name, cidrs]
      properties:
        name: {type: string, pattern: '^[A-Za-z0-9][A-Za-z0-9 _.-]{0,62}$'}
        description: {type: string, maxLength: 500, default: ''}
        cidrs: {type: array, minItems: 1, maxItems: 1024, items: {type: string}}
        filter_list_ids: {type: array, maxItems: 256, items: {type: string, format: uuid}, default: []}
        allowlist: {type: array, maxItems: 10000, items: {type: string}, default: []}
        safe_search: {$ref: '#/components/schemas/SafeSearch'}
    PolicyGroupUpdate:
      allOf:
        - {$ref: '#/components/schemas/PolicyGroupInput'}
        - type: object
          required: [revision]
          properties:
            revision: {type: integer, format: int64}
    PolicyGroup:
      type: object
      required: [id, name, description, cidrs, filter_list_ids, allowlist, safe_search, revision, created_at, updated_at]
      properties:
        id: {type: string, format: uuid}
        name: {type: string}
        description: {type: string}
        cidrs: {type: array, items: {type: string}}
        filter_list_ids: {type: array, items: {type: string, format: uuid}}
        allowlist: {type: array, items: {type: string}}
        safe_search: {$ref: '#/components/schemas/SafeSearch'}
        revision: {type: integer, format: int64}
        created_at: {type: string, format: date-time}
        updated_at: {type: string, format: date-time}
    RewriteInput:
      type: object
      required: [name, type, value]
      properties:
        group_id: {type: [string, 'null'], format: uuid, description: null means global}
        name: {type: string, maxLength: 255, description: 'Domain or *.domain'}
        type: {type: string, enum: [A, AAAA, CNAME]}
        value: {type: string, maxLength: 255}
        ttl: {type: integer, minimum: 0, maximum: 86400, default: 300}
    RewriteUpdate:
      allOf:
        - {$ref: '#/components/schemas/RewriteInput'}
        - type: object
          required: [revision]
          properties:
            revision: {type: integer, format: int64}
    Rewrite:
      type: object
      required: [id, group_id, name, type, value, ttl, revision, created_at, updated_at]
      properties:
        id: {type: string, format: uuid}
        group_id: {type: [string, 'null'], format: uuid}
        name: {type: string}
        type: {type: string, enum: [A, AAAA, CNAME]}
        value: {type: string}
        ttl: {type: integer}
        revision: {type: integer, format: int64}
        created_at: {type: string, format: date-time}
        updated_at: {type: string, format: date-time}
```

When `components.responses.Error` does not exist under that name in M1's spec, use M1's shared error response reference instead.
- [ ] Add to `mgmt/internal/auth/permissions.go`: `"listPolicyGroups": RoleViewer, "getPolicyGroup": RoleViewer, "getGlobalSafeSearch": RoleViewer, "listRewrites": RoleViewer, "createPolicyGroup": RoleOperator, "updatePolicyGroup": RoleOperator, "deletePolicyGroup": RoleOperator, "updateGlobalSafeSearch": RoleOperator, "createRewrite": RoleOperator, "updateRewrite": RoleOperator, "deleteRewrite": RoleOperator`.
- [ ] Run the M1 generation (`scripts/dev-exec.sh make proto`, which also runs oapi-codegen and openapi-typescript) — expect the build of `./mgmt/...` to FAIL with "does not implement StrictServerInterface (missing method CreatePolicyGroup)".
- [ ] Write the failing handler test `mgmt/internal/api/policies_test.go`:

```go
package api_test

import (
	"net/http"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/api/apitest"
)

type group struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	CIDRs      []string `json:"cidrs"`
	Allowlist  []string `json:"allowlist"`
	Revision   int64    `json:"revision"`
	SafeSearch struct {
		Google  bool   `json:"google"`
		YouTube string `json:"youtube"`
	} `json:"safe_search"`
}

type apiErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func TestPolicyGroupAPI(t *testing.T) {
	srv := apitest.New(t)
	op, viewer := srv.As("operator"), srv.As("viewer")
	body := map[string]any{
		"name": "kids", "cidrs": []string{"192.168.50.0/24"},
		"allowlist":   []string{"School.Example."},
		"safe_search": map[string]any{"google": true, "bing": false, "duckduckgo": false, "youtube": "strict"},
	}
	var g group
	if code := op.JSON(http.MethodPost, "/api/v1/policy-groups", body, &g); code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	if g.Allowlist[0] != "school.example" || g.Revision != 1 || !g.SafeSearch.Google {
		t.Fatalf("created = %+v", g)
	}
	var e apiErr
	if code := viewer.JSON(http.MethodPost, "/api/v1/policy-groups", body, &e); code != http.StatusForbidden {
		t.Fatalf("viewer create = %d", code)
	}
	body["name"] = "dup"
	if code := op.JSON(http.MethodPost, "/api/v1/policy-groups", body, &e); code != http.StatusConflict || e.Code != "cidr_in_use" {
		t.Fatalf("duplicate cidr = %d %+v", code, e)
	}
	body["cidrs"] = []string{"192.168.60.1/24"}
	if code := op.JSON(http.MethodPost, "/api/v1/policy-groups", body, &e); code != http.StatusUnprocessableEntity || e.Message != "cidr 192.168.60.1/24 has host bits set" {
		t.Fatalf("host bits = %d %+v", code, e)
	}
	upd := map[string]any{"name": "kids", "cidrs": []string{"192.168.50.0/24", "fd00:50::/64"}, "revision": g.Revision,
		"safe_search": map[string]any{"google": true, "bing": true, "duckduckgo": false, "youtube": "moderate"}}
	var g2 group
	if code := op.JSON(http.MethodPut, "/api/v1/policy-groups/"+g.ID, upd, &g2); code != http.StatusOK || g2.Revision != 2 {
		t.Fatalf("update = %d %+v", code, g2)
	}
	if code := op.JSON(http.MethodPut, "/api/v1/policy-groups/"+g.ID, upd, &e); code != http.StatusConflict || e.Code != "conflict" {
		t.Fatalf("stale update = %d %+v", code, e)
	}
	var list []group
	if code := viewer.JSON(http.MethodGet, "/api/v1/policy-groups", nil, &list); code != http.StatusOK || len(list) != 1 {
		t.Fatalf("list = %d %d", code, len(list))
	}
	if code := op.JSON(http.MethodDelete, "/api/v1/policy-groups/"+g.ID+"?revision=1", nil, &e); code != http.StatusConflict {
		t.Fatalf("stale delete = %d", code)
	}
	if code := op.JSON(http.MethodDelete, "/api/v1/policy-groups/"+g.ID+"?revision=2", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
}

func TestRewriteAPIValidation(t *testing.T) {
	srv := apitest.New(t)
	op := srv.As("operator")
	var e apiErr
	cases := []struct {
		body map[string]any
		code int
		msg  string
	}{
		{map[string]any{"name": "nas.home.test", "type": "A", "value": "fd00::1"}, 422, "value fd00::1 is not an IPv4 address"},
		{map[string]any{"name": "nas.home.test", "type": "AAAA", "value": "::ffff:1.2.3.4"}, 422, "value ::ffff:1.2.3.4 is not an IPv6 address"},
		{map[string]any{"name": "a.*.test", "type": "A", "value": "1.2.3.4"}, 422, "name a.*.test is not a domain or *.domain"},
		{map[string]any{"name": "loop.test", "type": "CNAME", "value": "loop.test"}, 422, "CNAME target must differ from name"},
		{map[string]any{"name": "x.test", "type": "A", "value": "1.2.3.4", "ttl": 90000}, 422, "ttl must be between 0 and 86400"},
	}
	for _, c := range cases {
		e = apiErr{}
		if code := op.JSON(http.MethodPost, "/api/v1/rewrites", c.body, &e); code != c.code || e.Message != c.msg {
			t.Errorf("%v: got %d %q, want %d %q", c.body, code, e.Message, c.code, c.msg)
		}
	}
	var created struct {
		ID  string `json:"id"`
		TTL int    `json:"ttl"`
	}
	if code := op.JSON(http.MethodPost, "/api/v1/rewrites", map[string]any{"name": "NAS.home.test.", "type": "A", "value": "192.168.1.50"}, &created); code != 201 || created.TTL != 300 {
		t.Fatalf("create = %d %+v", code, created)
	}
	if code := op.JSON(http.MethodPost, "/api/v1/rewrites", map[string]any{"name": "nas.home.test", "type": "CNAME", "value": "other.test"}, &e); code != 409 || e.Code != "rewrite_conflict" {
		t.Fatalf("cname conflict = %d %+v", code, e)
	}
	var list []map[string]any
	if code := op.JSON(http.MethodGet, "/api/v1/rewrites?scope=global", nil, &list); code != 200 || len(list) != 1 || list[0]["name"] != "nas.home.test" {
		t.Fatalf("list = %d %v", code, list)
	}
	if code := op.JSON(http.MethodGet, "/api/v1/rewrites?scope=bogus", nil, &e); code != 422 {
		t.Fatalf("bad scope = %d", code)
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/api/ -run 'TestPolicyGroupAPI|TestRewriteAPIValidation'` — expect FAIL with "missing method CreatePolicyGroup".
- [ ] Implement `mgmt/internal/api/policyvalidate.go` with these exact 422 messages: domain normalisation = trim, strip one trailing dot, `idna.Lookup.ToASCII`, lowercase, then match `^([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?\.)*[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$` and length <= 253, failure `domain <input> is invalid`; rewrite name allows one leading `*.`, failure `name <input> is not a domain or *.domain`; CIDR parse with `netip.ParsePrefix`, failure `cidr <input> is invalid`, `p != p.Masked()` -> `cidr <input> has host bits set`; duplicate CIDR within one request -> `cidr <c> listed twice`; group name -> `name must match ^[A-Za-z0-9][A-Za-z0-9 _.-]{0,62}$`; A value `netip.ParseAddr` must be `Is4()` -> `value <v> is not an IPv4 address`; AAAA must be `Is6() && !Is4In6()` -> `value <v> is not an IPv6 address`; CNAME target normalised as a domain, equal to the name -> `CNAME target must differ from name`; ttl omitted -> 300, outside 0..86400 -> `ttl must be between 0 and 86400`; `scope` query other than `all`, `global` or a UUID -> `scope must be all, global or a policy group id`; safe_search omitted -> all false and `youtube` `off`.
- [ ] Implement `mgmt/internal/api/policies.go` and `rewrites.go`: reads call store functions on the pool; every mutation calls `snapshot.Mutate(ctx, h.store, h.buildCfg, auth.ActorFrom(ctx), fn)` where `fn` loads the before-state (nil on create), calls the store mutation, and returns `auth.Change{Action: "<operationId>", TargetType: "policy_group" | "rewrite" | "safe_search", TargetID: <id or "global">, Before: before, After: after}`; map sentinel errors per the Interfaces error mapping; `createPolicyGroup` and `createRewrite` return 201; deletes return 204.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/... ` — expect PASS including `TestPolicyGroupAPI`, `TestRewriteAPIValidation`, and M1's audit/permission tests (every new operationId is present in `permissions.go`).
- [ ] Commit: `git add mgmt/api mgmt/internal web/src/api/schema.d.ts && git commit -m "feat(api): policy groups, rewrites and safe search operations"`.

## Task 9: Management plane DNS TLS certificate loading, push and status

Files:
- `mgmt/internal/config/config.go` — `NEXORA_DNS_TLS_CERT_FILE`, `NEXORA_DNS_TLS_KEY_FILE`, `NEXORA_DNS_TLS_RELOAD_INTERVAL`.
- `mgmt/internal/pki/dnstls.go` — issue (from M1's `pki.CA`), load and watch the DNS serving certificate.
- `mgmt/internal/pki/dnstls_test.go` — tests.
- `mgmt/cmd/nexora-mgmt/main.go` — `ca issue-dns` subcommand; start the watcher in `serve`.
- `mgmt/internal/control/dnstls.go` — `DNSTLSFanout`: which engines need `TlsMaterial`, latest-wins delivery, result recording.
- `mgmt/internal/control/dnstls_test.go` — fan-out behaviour test.
- `mgmt/internal/control/server.go` (M1) — `Connect` registers with the fan-out and sends its messages; handles `TlsMaterialResult`.
- `mgmt/api/openapi.yaml`, `mgmt/internal/api/dnstls.go`, `mgmt/internal/auth/permissions.go` — `getDnsTlsStatus`.

Interfaces:
- Consumed from M1 (Task 12): `config.Load(getenv)`/`config.Config`; `pki.LoadCA(certFile, keyFile string) (*pki.CA, error)`, `type CA struct { Cert *x509.Certificate; Key *ecdsa.PrivateKey; CertPEM []byte }`; CLI dispatch `serve | migrate | ca init --out <dir>` in `mgmt/cmd/nexora-mgmt/main.go`. (Task 13): `control.NewServer(st, ca, hub, instanceID)` whose `Connect` requires `Hello` first, registers a subscriber with a capacity-1 latest-wins snapshot channel and runs a sender goroutine; `control.EngineID(ctx) (string, error)`. `engines.node_name` for the status join.
- Consumed from Task 1: `controlv1.TlsMaterial`, `TlsMaterialResult`, `ServerMessage_TlsMaterial`, `EngineMessage_TlsMaterialResult`, `Hello.TlsFingerprintSha256`.
- Consumed from Task 7: `store.UpsertEngineTLSState`, `store.ListEngineTLSState`, `store.EngineTLSState`.
- Produced:
  - `type DNSTLSMaterial struct { ChainPEM, KeyPEM []byte; FingerprintSHA256, Subject string; DNSNames, IPAddresses []string; NotBefore, NotAfter time.Time }`.
  - `func (ca *CA) IssueDNSServerCert(names []string, validity time.Duration, now time.Time) (chainPEM, keyPEM []byte, err error)`.
  - `func LoadDNSTLS(certFile, keyFile string, now time.Time) (*DNSTLSMaterial, error)`.
  - `type DNSTLSWatcher` with `func NewDNSTLSWatcher(certFile, keyFile string, interval time.Duration) *DNSTLSWatcher`, `func (w *DNSTLSWatcher) Current() *DNSTLSMaterial`, `func (w *DNSTLSWatcher) Run(ctx context.Context, onChange func(*DNSTLSMaterial))`.
  - CLI: `nexora-mgmt ca issue-dns --ca-cert <file> --ca-key <file> --names <comma list> [--days 90] --out <dir>` writing `<dir>/tls.crt` (leaf then CA, 0644) and `<dir>/tls.key` (PKCS#8 ECDSA P-256, 0600).
  - `type DNSTLSFanout` with `func NewDNSTLSFanout() *DNSTLSFanout`, `func (f *DNSTLSFanout) Register(engineID, helloFingerprint string) <-chan *controlv1.TlsMaterial` (capacity 1, latest wins; the current material is queued when its fingerprint differs), `func (f *DNSTLSFanout) Unregister(engineID string)`, `func (f *DNSTLSFanout) Set(m *pki.DNSTLSMaterial)` (queues to every registered engine whose fingerprint differs), `func (f *DNSTLSFanout) Result(engineID string, r *controlv1.TlsMaterialResult)` (records the fingerprint when applied), `func (f *DNSTLSFanout) Current() *pki.DNSTLSMaterial`; `control.NewServer` gains a `*DNSTLSFanout` argument.
  - operationId `getDnsTlsStatus` at `GET /api/v1/settings/dns-tls`, role `viewer`.
  - Metrics: `nexora_mgmt_dns_tls_reload_errors_total`, `nexora_mgmt_dns_tls_not_after_seconds`.

- [ ] Write the failing test `mgmt/internal/pki/dnstls_test.go`:

```go
package pki_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

func testCA(t *testing.T) *pki.CA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &pki.CA{Cert: cert, Key: key}
}

func write(t *testing.T, dir string, chain, key []byte) (string, string) {
	t.Helper()
	c, k := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(c, chain, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(k, key, 0o600); err != nil {
		t.Fatal(err)
	}
	return c, k
}

func TestIssueAndLoadDNSTLS(t *testing.T) {
	ca := testCA(t)
	now := time.Now()
	chain, key, err := ca.IssueDNSServerCert([]string{"dns.nexora.test", "127.0.0.1"}, 90*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	c, k := write(t, t.TempDir(), chain, key)
	m, err := pki.LoadDNSTLS(c, k, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.FingerprintSHA256) != 64 || m.DNSNames[0] != "dns.nexora.test" || m.IPAddresses[0] != "127.0.0.1" {
		t.Fatalf("material = %+v", m)
	}
	if !m.NotAfter.After(now.Add(89 * 24 * time.Hour)) {
		t.Fatalf("not after = %v", m.NotAfter)
	}
	_, other, _ := ca.IssueDNSServerCert([]string{"dns.nexora.test"}, time.Hour, now)
	c2, k2 := write(t, t.TempDir(), chain, other)
	if _, err := pki.LoadDNSTLS(c2, k2, now); err == nil || !strings.Contains(err.Error(), "private key does not match certificate") {
		t.Fatalf("mismatch err = %v", err)
	}
	if _, err := pki.LoadDNSTLS(c, k, now.Add(91*24*time.Hour)); err == nil || !strings.Contains(err.Error(), "certificate expired") {
		t.Fatalf("expired err = %v", err)
	}
}

func TestDNSTLSWatcherRotationKeepsLastGood(t *testing.T) {
	ca := testCA(t)
	dir := t.TempDir()
	chain, key, _ := ca.IssueDNSServerCert([]string{"dns.nexora.test"}, time.Hour, time.Now())
	c, k := write(t, dir, chain, key)
	w := pki.NewDNSTLSWatcher(c, k, 20*time.Millisecond)
	var changes atomic.Int32
	var last atomic.Value
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, func(m *pki.DNSTLSMaterial) { changes.Add(1); last.Store(m.FingerprintSHA256) })
	waitFor(t, func() bool { return changes.Load() == 1 })
	first := w.Current().FingerprintSHA256

	if err := os.WriteFile(k, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if w.Current().FingerprintSHA256 != first || changes.Load() != 1 {
		t.Fatalf("broken files must keep the last good material")
	}
	chain2, key2, _ := ca.IssueDNSServerCert([]string{"dns.nexora.test"}, time.Hour, time.Now())
	write(t, dir, chain2, key2)
	waitFor(t, func() bool { return changes.Load() == 2 })
	if last.Load().(string) == first {
		t.Fatalf("rotation did not change fingerprint")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 3s")
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/pki/ -run 'TestIssueAndLoadDNSTLS|TestDNSTLSWatcher'` — expect FAIL with "ca.IssueDNSServerCert undefined (type *pki.CA has no field or method IssueDNSServerCert)".
- [ ] Implement `mgmt/internal/pki/dnstls.go`:
  - `(*CA).IssueDNSServerCert`: new ECDSA P-256 key signed by `ca.Cert`/`ca.Key`; template with random 128-bit serial, `Subject.CommonName = names[0]`, `NotBefore = now.Add(-5*time.Minute)`, `NotAfter = now.Add(validity)`, `KeyUsage = x509.KeyUsageDigitalSignature`, `ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}`; each name that parses with `net.ParseIP` goes to `IPAddresses`, others to `DNSNames`; chain PEM = leaf `CERTIFICATE` block followed by `ca.CertPEM`; key PEM = `PRIVATE KEY` block from `x509.MarshalPKCS8PrivateKey`; empty `names` -> error `at least one name is required`.
  - `LoadDNSTLS`: read both files; decode all `CERTIFICATE` blocks (none -> `dns tls: no certificate in <file>`); parse leaf; parse key with PKCS#8, then `ParseECPrivateKey`, then `ParsePKCS1PrivateKey` (all fail -> `dns tls: invalid private key in <file>`); compare `key.Public().(interface{ Equal(crypto.PublicKey) bool }).Equal(leaf.PublicKey)` -> false gives `dns tls: private key does not match certificate`; `now.After(leaf.NotAfter)` -> `dns tls: certificate expired at <RFC3339>`; `FingerprintSHA256 = hex.EncodeToString(sha256.Sum256(leaf.Raw))`; IP SANs as strings.
  - `DNSTLSWatcher.Run`: loads immediately, then every `interval` compares `(size, mtime)` of both files and reloads on change; a successful load with a new fingerprint replaces `Current()` (an `atomic.Pointer[DNSTLSMaterial]`), sets `nexora_mgmt_dns_tls_not_after_seconds`, and calls `onChange`; a failed load increments `nexora_mgmt_dns_tls_reload_errors_total`, logs `dns tls reload failed: <err>` (never key bytes), and keeps the previous material. Returns when `ctx` is done.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/pki/ -run 'TestIssueAndLoadDNSTLS|TestDNSTLSWatcher'` — expect PASS.
- [ ] In `mgmt/internal/config/config.go` add `DNSTLSCertFile string` (`NEXORA_DNS_TLS_CERT_FILE`), `DNSTLSKeyFile string` (`NEXORA_DNS_TLS_KEY_FILE`), `DNSTLSReloadInterval time.Duration` (`NEXORA_DNS_TLS_RELOAD_INTERVAL`, default `30s`, minimum `1s`); exactly one of the two files set -> config error `NEXORA_DNS_TLS_CERT_FILE and NEXORA_DNS_TLS_KEY_FILE must be set together`.
- [ ] In `mgmt/cmd/nexora-mgmt/main.go`: add `ca issue-dns` (flags as in Interfaces; `--out` created with 0700; prints `wrote <dir>/tls.crt and <dir>/tls.key (fingerprint <hex>)`); in `serve`, when both files are configured start `NewDNSTLSWatcher(...).Run(ctx, fanout.Set)`; `ca issue-dns` loads the CA with `pki.LoadCA(caCert, caKey)`.
- [ ] Write the failing fan-out test `mgmt/internal/control/dnstls_test.go`:

```go
package control_test

import (
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

func pending(ch <-chan *controlv1.TlsMaterial) *controlv1.TlsMaterial {
	select {
	case m := <-ch:
		return m
	default:
		return nil
	}
}

func TestDNSTLSFanoutPushesOnlyWhenFingerprintDiffers(t *testing.T) {
	f := control.NewDNSTLSFanout()
	a := f.Register("engine-a", "")
	if pending(a) != nil {
		t.Fatal("nothing to push before material exists")
	}
	f.Set(&pki.DNSTLSMaterial{ChainPEM: []byte("chain-1"), KeyPEM: []byte("key-1"), FingerprintSHA256: "aa"})
	if m := pending(a); m == nil || m.FingerprintSha256 != "aa" || string(m.PrivateKeyPem) != "key-1" {
		t.Fatalf("engine a got %v", m)
	}
	f.Result("engine-a", &controlv1.TlsMaterialResult{FingerprintSha256: "aa", Applied: true})

	b := f.Register("engine-b", "aa")
	c := f.Register("engine-c", "old")
	if pending(b) != nil {
		t.Fatal("engine already holding the certificate must not receive it again")
	}
	if m := pending(c); m == nil || m.FingerprintSha256 != "aa" {
		t.Fatal("engine with an old certificate must receive the current one on registration")
	}
	f.Result("engine-c", &controlv1.TlsMaterialResult{FingerprintSha256: "aa", Applied: false, Error: "certificate expired at 1"})

	// two rotations before the sender drains: only the latest is delivered
	f.Set(&pki.DNSTLSMaterial{ChainPEM: []byte("chain-2"), KeyPEM: []byte("key-2"), FingerprintSHA256: "bb"})
	f.Set(&pki.DNSTLSMaterial{ChainPEM: []byte("chain-3"), KeyPEM: []byte("key-3"), FingerprintSHA256: "cc"})
	for name, ch := range map[string]<-chan *controlv1.TlsMaterial{"a": a, "b": b, "c": c} {
		if m := pending(ch); m == nil || m.FingerprintSha256 != "cc" {
			t.Fatalf("engine %s got %v, want latest cc", name, m)
		}
		if pending(ch) != nil {
			t.Fatalf("engine %s received a superseded certificate", name)
		}
	}
	f.Unregister("engine-a")
	f.Set(&pki.DNSTLSMaterial{FingerprintSHA256: "dd"})
	if pending(a) != nil {
		t.Fatal("unregistered engine must receive nothing")
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestDNSTLSFanout` — expect FAIL with "undefined: control.NewDNSTLSFanout".
- [ ] Implement `mgmt/internal/control/dnstls.go`: a mutex-protected `current *pki.DNSTLSMaterial` and `map[string]*tlsEngine{fingerprint string; ch chan *controlv1.TlsMaterial}`; queuing drains a stale value from the capacity-1 channel before sending the new one (never blocks); `Set` updates `current` and queues to engines whose fingerprint differs; `Result` sets the engine's fingerprint when `Applied`. In M1's `Server.Connect` (`mgmt/internal/control/server.go`): after `Hello`, `tlsCh := s.dnsTLS.Register(engineID, hello.TlsFingerprintSha256)` with `defer s.dnsTLS.Unregister(engineID)`; the sender goroutine selects on the snapshot channel and `tlsCh`, sending `&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_TlsMaterial{TlsMaterial: m}}`; the receive loop handles `EngineMessage_TlsMaterialResult` by calling `s.dnsTLS.Result(engineID, r)` and `store.UpsertEngineTLSState(ctx, s.st.Pool, store.EngineTLSState{EngineID: uuid.MustParse(engineID), Fingerprint: r.FingerprintSha256, Applied: r.Applied, Error: r.Error})`; logs carry engine id, fingerprint, applied and error only. In `serve`, create one fan-out, pass it to `control.NewServer`, and run the watcher with `onChange = fanout.Set`.
- [ ] Add `GET /api/v1/settings/dns-tls` to `mgmt/api/openapi.yaml`:

```yaml
  /api/v1/settings/dns-tls:
    get:
      operationId: getDnsTlsStatus
      tags: [settings]
      responses:
        '200':
          description: DNS serving certificate loaded by this instance and per-engine acceptance.
          content:
            application/json:
              schema: {$ref: '#/components/schemas/DnsTlsStatus'}
```

```yaml
    DnsTlsStatus:
      type: object
      required: [configured, certificate, engines]
      properties:
        configured: {type: boolean}
        certificate:
          type: ['object', 'null']
          required: [subject, dns_names, ip_addresses, not_before, not_after, fingerprint_sha256]
          properties:
            subject: {type: string}
            dns_names: {type: array, items: {type: string}}
            ip_addresses: {type: array, items: {type: string}}
            not_before: {type: string, format: date-time}
            not_after: {type: string, format: date-time}
            fingerprint_sha256: {type: string}
        engines:
          type: array
          items:
            type: object
            required: [engine_id, node_name, fingerprint_sha256, applied, error, updated_at]
            properties:
              engine_id: {type: string, format: uuid}
              node_name: {type: string}
              fingerprint_sha256: {type: string}
              applied: {type: boolean}
              error: {type: string}
              updated_at: {type: string, format: date-time}
```

Add `"getDnsTlsStatus": RoleViewer` to `permissions.go`; implement `mgmt/internal/api/dnstls.go` returning `configured=false, certificate=null` when `fanout.Current()` is nil, and `engines` from `ListEngineTLSState` joined with engine node names; the response never contains PEM or key fields.
- [ ] Add a case to `mgmt/internal/api/policies_test.go`: `TestDnsTlsStatusWithoutCertificate` calling `srv.As("viewer").JSON("GET", "/api/v1/settings/dns-tls", nil, &out)` and asserting status 200, `out["configured"] == false`, `out["certificate"] == nil`.
- [ ] Run `scripts/dev-exec.sh make proto && scripts/dev-exec.sh go test ./mgmt/...` — expect PASS including `TestDNSTLSFanoutPushesOnlyWhenFingerprintDiffers` and `TestDnsTlsStatusWithoutCertificate`.
- [ ] Commit: `git add mgmt web/src/api/schema.d.ts && git commit -m "feat(mgmt): DNS TLS certificate loading, push to engines and status API"`.

## Task 10: GUI screens `/policies`, `/rewrites` and Settings TLS section

Files:
- `web/src/routes/policies.tsx` — global safe search card + policy group list and editor dialog.
- `web/src/routes/rewrites.tsx` — rewrite table with scope filter and editor dialog.
- `web/src/routes/settings.tsx` (M1) — "DNS encryption certificate" section.
- `web/src/api/policies.ts` — TanStack Query hooks for the new operations.
- `web/src/router.tsx` and the M1 navigation component — routes and nav entries "Policies" and "Rewrites".
- `web/e2e/policies.spec.ts`, `web/e2e/rewrites.spec.ts`, `web/e2e/settings-dns-tls.spec.ts` — Playwright tests.

Interfaces:
- Consumed from M1: `components/ui` (`Button`, `Input`, `Textarea`, `Label`, `Dialog`, `Table`, `Switch`, `RadioGroup`, `Select`, `Badge`, `Alert`); `api` (openapi-fetch client from `web/src/api/client.ts`); the API error helper that exposes `{code, message}`; Playwright helper `loginAs(page: Page, role: 'admin' | 'operator' | 'viewer'): Promise<void>` from `web/e2e/helpers/auth.ts`; the `TestGUICoverage` convention that a Playwright test covers an operation when it pushes the annotation `{ type: 'operation', description: '<operationId>' }`; the Go wrapper `TestPlaywrightSpecs` in `e2e/` that starts a harness mgmt with seeded users and runs the specs named in `NEXORA_PLAYWRIGHT_SPECS` (all M1, later task). (Task 2 `Makefile`): `make web-test` (typecheck + unit), `make lint`.
- Consumed from Tasks 8 and 9: operations `listPolicyGroups`, `createPolicyGroup`, `getPolicyGroup`, `updatePolicyGroup`, `deletePolicyGroup`, `getGlobalSafeSearch`, `updateGlobalSafeSearch`, `listRewrites`, `createRewrite`, `updateRewrite`, `deleteRewrite`, `getDnsTlsStatus`; M1 `listFilterLists`.
- Produced: hooks `usePolicyGroups()`, `usePolicyGroup(id)`, `useCreatePolicyGroup()`, `useUpdatePolicyGroup()`, `useDeletePolicyGroup()`, `useGlobalSafeSearch()`, `useUpdateGlobalSafeSearch()`, `useRewrites(scope: string)`, `useCreateRewrite()`, `useUpdateRewrite()`, `useDeleteRewrite()`, `useDnsTlsStatus()`; each mutation invalidates the query keys `['policy-groups']`, `['safe-search']`, `['rewrites']`.

Screen behaviour (binding):
- `/policies`: card "Global safe search" (heading text exact) with switches labelled `Google`, `Bing`, `DuckDuckGo`, a radio group labelled `YouTube` with options `Off`, `Moderate`, `Strict`, and button `Save global safe search`; success toast `Global safe search saved`. Below, heading `Policy groups`, help text `Clients in a group get only that group's filter lists, allowlist, safe search and rewrites. The most specific CIDR wins.`, button `New group`, and a table with columns `Name`, `CIDRs`, `Filter lists`, `Safe search`, `Actions` (row buttons `Edit` and `Delete`, accessible names `Edit <name>` and `Delete <name>`). Dialog titles `New policy group` / `Edit policy group`; fields labelled `Name`, `Description`, `Client CIDRs` (textarea, one per line), `Filter lists` (checkbox per list, text `No filter lists yet` when empty), `Allowlist` (textarea, one domain per line), the same safe-search controls; buttons `Save` and `Cancel`. Server 409 `conflict` shows alert `This group was changed by someone else. Reload to see the latest version.` with button `Reload`; 409 `cidr_in_use` and 422 show the server `message` under the dialog form. Delete asks `Delete policy group <name>? Its rewrites are deleted too.` with button `Delete`. Viewers see no `New group`, `Edit`, `Delete` or `Save global safe search` controls.
- `/rewrites`: heading `Rewrites`, select labelled `Scope` with `All`, `Global` and one option per group name; button `New rewrite`; table columns `Name`, `Type`, `Value`, `TTL`, `Scope`, `Actions` (`Edit <name> <type> <value>`, `Delete <name> <type> <value>`); dialog `New rewrite` / `Edit rewrite` with `Name` (placeholder `host.example or *.example`), `Type` (A, AAAA, CNAME), `Value`, `TTL` (default 300), `Scope`; 422/409 messages shown under the form.
- Settings: section heading `DNS encryption certificate`; when not configured, text `Not configured. Set NEXORA_DNS_TLS_CERT_FILE and NEXORA_DNS_TLS_KEY_FILE on every management instance; DoT, DoH and DoQ refuse handshakes until then.`; when configured: `Subject`, `Names`, `Expires` (badge amber under 21 days, red under 7 days), `Fingerprint`, and a table `Engines` with columns `Node`, `Status` (`Serving current certificate` when fingerprint matches and applied, `Outdated` when applied with another fingerprint, the error text when not applied).

- [ ] Write `web/e2e/policies.spec.ts`:

```ts
import { expect, test } from '@playwright/test';
import { loginAs } from './helpers/auth';

const covers = (...ops: string[]) =>
  test.info().annotations.push(...ops.map((description) => ({ type: 'operation', description })));

test('operator edits global safe search and manages a policy group', async ({ page }) => {
  covers('getGlobalSafeSearch', 'updateGlobalSafeSearch', 'listPolicyGroups', 'createPolicyGroup',
    'getPolicyGroup', 'updatePolicyGroup', 'deletePolicyGroup');
  await loginAs(page, 'operator');
  await page.goto('/policies');

  const global = page.getByRole('region', { name: 'Global safe search' });
  await global.getByRole('switch', { name: 'Bing' }).click();
  await global.getByRole('radio', { name: 'Moderate' }).click();
  await global.getByRole('button', { name: 'Save global safe search' }).click();
  await expect(page.getByText('Global safe search saved')).toBeVisible();
  await page.reload();
  await expect(page.getByRole('region', { name: 'Global safe search' }).getByRole('switch', { name: 'Bing' })).toBeChecked();

  const name = `kids-${Date.now()}`;
  await page.getByRole('button', { name: 'New group' }).click();
  const dialog = page.getByRole('dialog', { name: 'New policy group' });
  await dialog.getByLabel('Name').fill(name);
  await dialog.getByLabel('Client CIDRs').fill('192.168.250.0/24\n192.168.251.1/24');
  await dialog.getByLabel('Allowlist').fill('school.example');
  await dialog.getByRole('switch', { name: 'Google' }).click();
  await dialog.getByRole('radio', { name: 'Strict' }).click();
  await expect(dialog.getByRole('group', { name: 'Filter lists' })).toBeVisible();
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog.getByText('cidr 192.168.251.1/24 has host bits set')).toBeVisible();
  await dialog.getByLabel('Client CIDRs').fill('192.168.250.0/24');
  await dialog.getByRole('button', { name: 'Save' }).click();
  const row = page.getByRole('row', { name: new RegExp(name) });
  await expect(row).toContainText('192.168.250.0/24');

  await row.getByRole('button', { name: `Edit ${name}` }).click();
  const edit = page.getByRole('dialog', { name: 'Edit policy group' });
  await expect(edit.getByLabel('Allowlist')).toHaveValue('school.example');
  // a concurrent edit through the API makes the dialog's revision stale
  const groups = await (await page.request.get('/api/v1/policy-groups')).json();
  const g = groups.find((x: { name: string }) => x.name === name);
  const res = await page.request.put(`/api/v1/policy-groups/${g.id}`, {
    data: { ...g, description: 'changed elsewhere' },
    headers: { 'Content-Type': 'application/json' },
  });
  expect(res.status()).toBe(200);
  await edit.getByLabel('Description').fill('my change');
  await edit.getByRole('button', { name: 'Save' }).click();
  await expect(edit.getByText('This group was changed by someone else. Reload to see the latest version.')).toBeVisible();
  await edit.getByRole('button', { name: 'Reload' }).click();
  await expect(edit.getByLabel('Description')).toHaveValue('changed elsewhere');
  await edit.getByLabel('Description').fill('my change');
  await edit.getByRole('button', { name: 'Save' }).click();
  await expect(edit).toBeHidden();

  await row.getByRole('button', { name: `Delete ${name}` }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0);
});

test('viewer sees policies read-only', async ({ page }) => {
  covers('listPolicyGroups', 'getGlobalSafeSearch');
  await loginAs(page, 'viewer');
  await page.goto('/policies');
  await expect(page.getByRole('heading', { name: 'Policy groups' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'New group' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Save global safe search' })).toHaveCount(0);
});
```

The "Reload" button refetches `getPolicyGroup` for the edited id and resets the form with the fresh revision.
- [ ] Write `web/e2e/rewrites.spec.ts`:

```ts
import { expect, test } from '@playwright/test';
import { loginAs } from './helpers/auth';

test('operator creates, filters, edits and deletes rewrites', async ({ page }) => {
  test.info().annotations.push(
    ...['listRewrites', 'createRewrite', 'updateRewrite', 'deleteRewrite'].map((description) => ({ type: 'operation', description })),
  );
  await loginAs(page, 'operator');
  await page.goto('/rewrites');
  const host = `nas-${Date.now()}.home.test`;

  await page.getByRole('button', { name: 'New rewrite' }).click();
  let dialog = page.getByRole('dialog', { name: 'New rewrite' });
  await dialog.getByLabel('Name').fill(host);
  await dialog.getByLabel('Type').selectOption('A');
  await dialog.getByLabel('Value').fill('fd00::1');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog.getByText('value fd00::1 is not an IPv4 address')).toBeVisible();
  await dialog.getByLabel('Value').fill('192.168.1.50');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('row', { name: new RegExp(`${host} A 192.168.1.50 300 Global`) })).toBeVisible();

  await page.getByRole('button', { name: 'New rewrite' }).click();
  dialog = page.getByRole('dialog', { name: 'New rewrite' });
  await dialog.getByLabel('Name').fill(host);
  await dialog.getByLabel('Type').selectOption('CNAME');
  await dialog.getByLabel('Value').fill('other.home.test');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog.getByText(/CNAME cannot coexist/)).toBeVisible();
  await dialog.getByRole('button', { name: 'Cancel' }).click();

  await page.getByLabel('Scope').selectOption('global');
  await page.getByRole('button', { name: `Edit ${host} A 192.168.1.50` }).click();
  const edit = page.getByRole('dialog', { name: 'Edit rewrite' });
  await edit.getByLabel('TTL').fill('120');
  await edit.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('row', { name: new RegExp(`${host} A 192.168.1.50 120`) })).toBeVisible();

  await page.getByRole('button', { name: `Delete ${host} A 192.168.1.50` }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByRole('row', { name: new RegExp(host) })).toHaveCount(0);
});
```

The 409 `rewrite_conflict` message from Task 7/8 is `nas-….home.test already has A records; CNAME cannot coexist with other records` — set it exactly as `<name> already has <types> records; CNAME cannot coexist with other records` (or `<name> already has a CNAME record; CNAME cannot coexist with other records`) in `mgmt/internal/api/rewrites.go`.
- [ ] Write `web/e2e/settings-dns-tls.spec.ts`:

```ts
import { expect, test } from '@playwright/test';
import { loginAs } from './helpers/auth';

test('settings shows the DNS encryption certificate state', async ({ page }) => {
  test.info().annotations.push({ type: 'operation', description: 'getDnsTlsStatus' });
  await loginAs(page, 'viewer');
  await page.goto('/settings');
  const section = page.getByRole('region', { name: 'DNS encryption certificate' });
  await expect(section).toBeVisible();
  const status = await (await page.request.get('/api/v1/settings/dns-tls')).json();
  if (status.configured) {
    await expect(section.getByText(status.certificate.fingerprint_sha256)).toBeVisible();
    await expect(section.getByRole('table', { name: 'Engines' })).toBeVisible();
  } else {
    await expect(section.getByText(/Not configured\. Set NEXORA_DNS_TLS_CERT_FILE and NEXORA_DNS_TLS_KEY_FILE/)).toBeVisible();
  }
});
```

- [ ] Run the three specs through M1's Playwright Go wrapper: `scripts/dev-exec.sh bash -c 'make e2e-build && NEXORA_E2E_BIN_DIR=bin NEXORA_PLAYWRIGHT_SPECS=policies.spec.ts,rewrites.spec.ts,settings-dns-tls.spec.ts go test -count=1 -v ./e2e/ -run TestPlaywrightSpecs'` — expect FAIL with timeouts locating `role=region[name="Global safe search"]` (routes not implemented).
- [ ] Implement `web/src/api/policies.ts`, `web/src/routes/policies.tsx`, `web/src/routes/rewrites.tsx`, the settings section and the router/navigation entries with the exact texts, roles and labels in "Screen behaviour". Cards and settings sections are `<section aria-labelledby=…>` so they expose the region role with the heading as name; tables that need a name use `aria-label`. The `Client CIDRs` and `Allowlist` textareas split on newlines, trim, and drop empty lines before sending. The edit dialog sends the `revision` it loaded. Role-gated controls use M1's current-user role hook.
- [ ] Rerun the same wrapper command — expect PASS for `policies.spec.ts` (2 tests), `rewrites.spec.ts` (1), `settings-dns-tls.spec.ts` (1); then `scripts/dev-exec.sh make web-test lint` — expect exit 0.
- [ ] Commit: `git add web mgmt/internal/api/rewrites.go && git commit -m "feat(web): policies, rewrites and DNS TLS settings screens"`.

## Task 11: E2E harness encrypted clients and `TestEncryptedTransports`

Files:
- `go.mod`, `go.sum` — add `github.com/quic-go/quic-go v0.62.0` (M1 already pins `github.com/miekg/dns v1.1.73`).
- `e2e/harness/encrypted.go` — DoT/DoH/DoQ clients, PROXY v2 writer, `EventuallyTrue`.
- `e2e/harness/dnstls.go` — issue/rotate the DNS serving certificate for a management plane under test.
- `e2e/harness/engine.go`, `e2e/harness/harness.go` (M1) — encrypted listener options and DNS TLS options.
- `e2e/fixtures/cmd/nexora-fixture/dns.go`, `e2e/harness/fixture.go` (M1) — static record overrides (`POST /records`).
- `e2e/encrypted_transports_test.go` — acceptance test.

Interfaces:
- Consumed from M1 (Task 10): `harness.New(t *testing.T) *harness.Env`; `(*Env).StartDNSFixture() *DNSFixture` (fields `UDP`, `TCP`, `Control`); `(*Env).StartHTTPFixture() *HTTPFixture` with `SetList(t, name, body string)` and `URL(name string) string`; `type Engine struct { DNS, Metrics, StateDir, ConfigPath string; Proc *Proc }`; `harness.Eventually(t *testing.T, timeout time.Duration, cond func() error)`; `(*Env).Bin(name string) string`. (M1, later task): `(*Env).StartMgmt(opts harness.MgmtOptions) *harness.Mgmt` with `CACertFile`, `CAKeyFile string` and `AdminAPI(t *testing.T) *harness.API` (`Do(t, method, path string, body, out any) int`, `MustDo(t, method, path string, body, out any)`); `(*Env).StartEngine(m *Mgmt, opts harness.EngineOptions) *Engine` (enrolled engine using the M1 `Engine` type); M1 API `POST /api/v1/upstreams` with `{"name","protocol","address","enabled"}`.
- Consumed from Tasks 2-5, 9: engine.toml keys `listen_dot`, `listen_doh`, `listen_doq`, `doh_path`, `proxy_protocol_dot`, `proxy_protocol_trusted_cidrs`; CLI `nexora-mgmt ca issue-dns`; env `NEXORA_DNS_TLS_CERT_FILE`, `NEXORA_DNS_TLS_KEY_FILE`, `NEXORA_DNS_TLS_RELOAD_INTERVAL`; `GET /api/v1/settings/dns-tls`; metric names from Task 4.
- Produced (package `harness`):
  - `EngineOptions` fields `DoT, DoH, DoQ, ProxyProtocolDoT bool; ProxyTrustedCIDRs []string`; `(*Engine).DoTAddr() string`, `(*Engine).DoHURL() string` (`https://127.0.0.1:<port>/dns-query`), `(*Engine).DoQAddr() string`, `(*Engine).MetricsText(t *testing.T) string`, `(*Engine).PID() int`.
  - `MgmtOptions.DNSTLS bool` — before mgmt starts, runs `nexora-mgmt ca issue-dns --ca-cert <CACertFile> --ca-key <CAKeyFile> --names dns.nexora.test,127.0.0.1 --days 30 --out <env dir>/dnstls` and sets `NEXORA_DNS_TLS_CERT_FILE`, `NEXORA_DNS_TLS_KEY_FILE`, `NEXORA_DNS_TLS_RELOAD_INTERVAL=1s`; `(*Mgmt).DNSTLSDir string`, `(*Mgmt).DNSTLSRoots() *x509.CertPool`, `(*Mgmt).DNSTLSFingerprint() string`, `(*Mgmt).RotateDNSTLS(t *testing.T) string`.
  - `(*DNSFixture).SetRecords(t *testing.T, rrs ...string)` — exact-name static records (presentation format) answered before the prefix rules; names present with other types answer NODATA, names absent keep M1 behaviour.
  - `func EventuallyTrue(t *testing.T, timeout time.Duration, cond func() bool, msg string)`.
  - `type EncryptedClient struct { RootCAs *x509.CertPool; ServerName string; LocalIP net.IP }` with `DialDoT(ctx, addr string) (*dns.Conn, error)`, `DoT(ctx, addr string, q *dns.Msg) (*dns.Msg, tls.ConnectionState, error)`, `HTTPClient() *http.Client`, `DialDoQ(ctx, addr string) (*quic.Conn, error)`, `DoQ(ctx, addr string, q *dns.Msg) (*dns.Msg, tls.ConnectionState, error)`.
  - `func DoH(ctx context.Context, hc *http.Client, url, method string, q *dns.Msg) (*dns.Msg, *http.Response, error)`; `func DoQQuery(ctx context.Context, conn *quic.Conn, q *dns.Msg) (*dns.Msg, error)`; `func DoQRaw(ctx context.Context, conn *quic.Conn, framed []byte) ([]byte, error)`; `func WriteProxyV2(w io.Writer, src, dst *net.TCPAddr) error`; `func Fingerprint(cs tls.ConnectionState) string`; `func AnswerSet(m *dns.Msg) []string`.

- [ ] Write the failing acceptance test `e2e/encrypted_transports_test.go`:

```go
package e2e

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/quic-go/quic-go"
)

func TestEncryptedTransports(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	fx.SetRecords(t, "example.test. 300 IN A 192.0.2.10", "example.test. 300 IN AAAA 2001:db8::10")
	mg := env.StartMgmt(harness.MgmtOptions{DNSTLS: true})
	api := mg.AdminAPI(t)
	api.MustDo(t, http.MethodPost, "/api/v1/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "enabled": true}, nil)
	eng := env.StartEngine(mg, harness.EngineOptions{Name: "enc-1", DoT: true, DoH: true, DoQ: true})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := harness.EncryptedClient{RootCAs: mg.DNSTLSRoots(), ServerName: "dns.nexora.test"}
	firstFP := mg.DNSTLSFingerprint()
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		m, _, err := new(dns.Client).Exchange(question("example.test.", dns.TypeA), eng.DNS)
		return err == nil && len(m.Answer) == 1 && m.Answer[0].(*dns.A).A.String() == "192.0.2.10"
	}, "engine forwards to the fixture")

	// engine received the certificate over the control stream
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		_, cs, err := client.DoT(ctx, eng.DoTAddr(), question("example.test.", dns.TypeA))
		return err == nil && harness.Fingerprint(cs) == firstFP
	}, "DoT handshake with the pushed certificate")

	for _, qt := range []uint16{dns.TypeA, dns.TypeAAAA} {
		udp, _, err := new(dns.Client).Exchange(question("example.test.", qt), eng.DNS)
		if err != nil || udp.Rcode != dns.RcodeSuccess || len(udp.Answer) == 0 {
			t.Fatalf("UDP baseline: %v %v", udp, err)
		}
		want := strings.Join(harness.AnswerSet(udp), "|")

		dot, _, err := client.DoT(ctx, eng.DoTAddr(), question("example.test.", qt))
		mustSame(t, "DoT", want, dot, err)

		hc := client.HTTPClient()
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			m, resp, err := harness.DoH(ctx, hc, eng.DoHURL(), method, question("example.test.", qt))
			mustSame(t, "DoH "+method, want, m, err)
			if resp.ProtoMajor != 2 {
				t.Errorf("DoH %s used HTTP/%d, want HTTP/2", method, resp.ProtoMajor)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/dns-message" {
				t.Errorf("DoH %s content-type %q", method, ct)
			}
			if cc := resp.Header.Get("Cache-Control"); !strings.HasPrefix(cc, "max-age=") || cc == "max-age=0" {
				t.Errorf("DoH %s cache-control %q", method, cc)
			}
		}

		doq, _, err := client.DoQ(ctx, eng.DoQAddr(), question("example.test.", qt))
		mustSame(t, "DoQ", want, doq, err)
	}

	t.Run("doh-http2-multiplexing", func(t *testing.T) {
		hc := client.HTTPClient()
		var dials atomic.Int32
		trace := &httptrace.ClientTrace{ConnectStart: func(string, string) { dials.Add(1) }}
		tctx := httptrace.WithClientTrace(ctx, trace)
		if _, _, err := harness.DoH(tctx, hc, eng.DoHURL(), http.MethodPost, question("example.test.", dns.TypeA)); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 50)
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m, _, err := harness.DoH(tctx, hc, eng.DoHURL(), http.MethodPost, question("example.test.", dns.TypeA))
				if err == nil && len(m.Answer) == 0 {
					err = errors.New("empty answer")
				}
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if n := dials.Load(); n != 1 {
			t.Fatalf("51 DoH requests used %d TCP connections, want 1 (multiplexed)", n)
		}
	})

	t.Run("dot-pipelining", func(t *testing.T) {
		conn, err := client.DialDoT(ctx, eng.DoTAddr())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		ids := map[uint16]bool{}
		for i := 0; i < 10; i++ {
			q := question("example.test.", dns.TypeA)
			ids[q.Id] = true
			if err := conn.WriteMsg(q); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 10; i++ {
			r, err := conn.ReadMsg()
			if err != nil || !ids[r.Id] || len(r.Answer) == 0 {
				t.Fatalf("pipelined reply %d: %v %v", i, r, err)
			}
			delete(ids, r.Id)
		}
	})

	t.Run("doq-stream-per-query-and-protocol-error", func(t *testing.T) {
		conn, err := client.DialDoQ(ctx, eng.DoQAddr())
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if m, err := harness.DoQQuery(ctx, conn, question("example.test.", dns.TypeA)); err != nil || m.Id != 0 || len(m.Answer) == 0 {
					t.Errorf("DoQ concurrent query: %v %v", m, err)
				}
			}()
		}
		wg.Wait()
		q := question("example.test.", dns.TypeA)
		q.Id = 0x1234
		wire, _ := q.Pack()
		framed := append([]byte{byte(len(wire) >> 8), byte(len(wire))}, wire...)
		_, _ = harness.DoQRaw(ctx, conn, framed)
		<-conn.Context().Done()
		var appErr *quic.ApplicationError
		if !errors.As(context.Cause(conn.Context()), &appErr) || appErr.ErrorCode != 0x2 {
			t.Fatalf("non-zero DoQ message id: close cause %v, want application error 0x2", context.Cause(conn.Context()))
		}
	})

	t.Run("certificate-rotation-without-restart", func(t *testing.T) {
		pid := eng.PID()
		old, err := client.DialDoT(ctx, eng.DoTAddr())
		if err != nil {
			t.Fatal(err)
		}
		defer old.Close()
		newFP := mg.RotateDNSTLS(t)
		if newFP == firstFP {
			t.Fatal("rotation produced the same fingerprint")
		}
		harness.EventuallyTrue(t, 45*time.Second, func() bool {
			_, dotCS, err1 := client.DoT(ctx, eng.DoTAddr(), question("example.test.", dns.TypeA))
			_, dohResp, err2 := harness.DoH(ctx, client.HTTPClient(), eng.DoHURL(), http.MethodGet, question("example.test.", dns.TypeA))
			_, doqCS, err3 := client.DoQ(ctx, eng.DoQAddr(), question("example.test.", dns.TypeA))
			return err1 == nil && err2 == nil && err3 == nil &&
				harness.Fingerprint(dotCS) == newFP && harness.Fingerprint(*dohResp.TLS) == newFP && harness.Fingerprint(doqCS) == newFP
		}, "all encrypted transports serve the rotated certificate")
		_ = old.SetDeadline(time.Now().Add(5 * time.Second))
		if err := old.WriteMsg(question("example.test.", dns.TypeA)); err != nil {
			t.Fatalf("connection opened before rotation: %v", err)
		}
		if r, err := old.ReadMsg(); err != nil || len(r.Answer) == 0 {
			t.Fatalf("connection opened before rotation stopped answering: %v %v", r, err)
		}
		if eng.PID() != pid {
			t.Fatal("engine restarted during rotation")
		}
		var status struct {
			Engines []struct {
				Fingerprint string `json:"fingerprint_sha256"`
				Applied     bool   `json:"applied"`
			} `json:"engines"`
		}
		harness.EventuallyTrue(t, 10*time.Second, func() bool {
			api.Do(t, http.MethodGet, "/api/v1/settings/dns-tls", nil, &status)
			return len(status.Engines) == 1 && status.Engines[0].Applied && status.Engines[0].Fingerprint == newFP
		}, "status API shows the engine serving the rotated certificate")
	})

	t.Run("no-key-material-on-engine-disk", func(t *testing.T) {
		keyPEM, err := os.ReadFile(filepath.Join(mg.DNSTLSDir, "tls.key"))
		if err != nil {
			t.Fatal(err)
		}
		body := strings.TrimSpace(strings.Split(strings.SplitN(string(keyPEM), "\n", 2)[1], "-----END")[0])
		scanned := 0
		err = filepath.WalkDir(eng.StateDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			scanned++
			if strings.Contains(string(b), "PRIVATE KEY") && !strings.HasPrefix(p, filepath.Join(eng.StateDir, "identity")) {
				t.Errorf("%s contains a PEM private key", p)
			}
			if strings.Contains(string(b), body[:40]) {
				t.Errorf("%s contains DNS TLS key material", p)
			}
			return nil
		})
		if err != nil || scanned == 0 {
			t.Fatalf("walk state dir: scanned=%d err=%v (the snapshot file must exist)", scanned, err)
		}
	})

	t.Run("per-transport-metrics", func(t *testing.T) {
		metrics := eng.MetricsText(t)
		for _, want := range []string{
			`nexora_queries_total{transport="dot",rcode="NOERROR"}`,
			`nexora_queries_total{transport="doh",rcode="NOERROR"}`,
			`nexora_queries_total{transport="doq",rcode="NOERROR"}`,
			`nexora_tls_handshakes_total{transport="dot",result="ok"}`,
			`nexora_doh_requests_total{method="GET",status="200"}`,
			`nexora_doq_protocol_errors_total`,
			`nexora_tls_certificate_not_after_seconds`,
		} {
			if !strings.Contains(metrics, want) {
				t.Errorf("metrics missing %s", want)
			}
		}
	})
}

func question(name string, qtype uint16) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	return m
}

func mustSame(t *testing.T, transport, want string, m *dns.Msg, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", transport, err)
	}
	if got := strings.Join(harness.AnswerSet(m), "|"); got != want {
		t.Fatalf("%s answer %q differs from UDP %q", transport, got, want)
	}
}
```

The key-body check skips the engine's own `identity/` directory (its mTLS client key is expected there) and asserts the DNS serving key's base64 body appears in no file.
- [ ] Run `scripts/dev-exec.sh bash -c 'NEXORA_E2E_BIN_DIR=bin go test -count=1 ./e2e/ -run TestEncryptedTransports'` — expect FAIL with "unknown field DNSTLS in struct literal of type harness.MgmtOptions".
- [ ] Implement `e2e/harness/encrypted.go`:

```go
package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

type EncryptedClient struct {
	RootCAs    *x509.CertPool
	ServerName string
	LocalIP    net.IP
}

func (c EncryptedClient) tlsConfig(alpn ...string) *tls.Config {
	return &tls.Config{RootCAs: c.RootCAs, ServerName: c.ServerName, NextProtos: alpn, MinVersion: tls.VersionTLS12}
}

func (c EncryptedClient) dialer() *net.Dialer {
	d := &net.Dialer{Timeout: 5 * time.Second}
	if c.LocalIP != nil {
		d.LocalAddr = &net.TCPAddr{IP: c.LocalIP}
	}
	return d
}

func (c EncryptedClient) DialDoT(ctx context.Context, addr string) (*dns.Conn, error) {
	td := &tls.Dialer{NetDialer: c.dialer(), Config: c.tlsConfig("dot")}
	conn, err := td.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return &dns.Conn{Conn: conn}, nil
}

func (c EncryptedClient) DoT(ctx context.Context, addr string, q *dns.Msg) (*dns.Msg, tls.ConnectionState, error) {
	conn, err := c.DialDoT(ctx, addr)
	if err != nil {
		return nil, tls.ConnectionState{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMsg(q); err != nil {
		return nil, tls.ConnectionState{}, err
	}
	r, err := conn.ReadMsg()
	return r, conn.Conn.(*tls.Conn).ConnectionState(), err
}

// HTTPClient returns a client limited to one connection per host, so concurrent
// requests must share one HTTP/2 connection.
func (c EncryptedClient) HTTPClient() *http.Client {
	tr := &http.Transport{
		DialContext:       c.dialer().DialContext,
		TLSClientConfig:   &tls.Config{RootCAs: c.RootCAs, ServerName: c.ServerName, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
		MaxConnsPerHost:   1,
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func DoH(ctx context.Context, hc *http.Client, url, method string, q *dns.Msg) (*dns.Msg, *http.Response, error) {
	q = q.Copy()
	q.Id = 0
	wire, err := q.Pack()
	if err != nil {
		return nil, nil, err
	}
	var req *http.Request
	switch method {
	case http.MethodGet:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, url+"?dns="+base64.RawURLEncoding.EncodeToString(wire), nil)
	case http.MethodPost:
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(wire))
		if req != nil {
			req.Header.Set("Content-Type", "application/dns-message")
		}
	default:
		return nil, nil, fmt.Errorf("doh: unsupported method %s", method)
	}
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/dns-message")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp, fmt.Errorf("doh: status %d", resp.StatusCode)
	}
	m := new(dns.Msg)
	return m, resp, m.Unpack(body)
}

func (c EncryptedClient) DialDoQ(ctx context.Context, addr string) (*quic.Conn, error) {
	conf := &quic.Config{MaxIdleTimeout: 30 * time.Second}
	if c.LocalIP == nil {
		return quic.DialAddr(ctx, addr, c.tlsConfig("doq"), conf)
	}
	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: c.LocalIP})
	if err != nil {
		return nil, err
	}
	return quic.Dial(ctx, pc, remote, c.tlsConfig("doq"), conf)
}

func (c EncryptedClient) DoQ(ctx context.Context, addr string, q *dns.Msg) (*dns.Msg, tls.ConnectionState, error) {
	conn, err := c.DialDoQ(ctx, addr)
	if err != nil {
		return nil, tls.ConnectionState{}, err
	}
	defer conn.CloseWithError(0, "")
	m, err := DoQQuery(ctx, conn, q)
	return m, conn.ConnectionState().TLS, err
}

func DoQQuery(ctx context.Context, conn *quic.Conn, q *dns.Msg) (*dns.Msg, error) {
	q = q.Copy()
	q.Id = 0
	wire, err := q.Pack()
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 2+len(wire))
	binary.BigEndian.PutUint16(frame, uint16(len(wire)))
	copy(frame[2:], wire)
	resp, err := DoQRaw(ctx, conn, frame)
	if err != nil {
		return nil, err
	}
	if len(resp) < 2 || int(binary.BigEndian.Uint16(resp)) != len(resp)-2 {
		return nil, fmt.Errorf("doq: bad framing (%d bytes)", len(resp))
	}
	m := new(dns.Msg)
	return m, m.Unpack(resp[2:])
}

// DoQRaw sends one framed message on a new stream, closes the send side, and reads to FIN.
func DoQRaw(ctx context.Context, conn *quic.Conn, framed []byte) ([]byte, error) {
	st, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := st.Write(framed); err != nil {
		return nil, err
	}
	if err := st.Close(); err != nil {
		return nil, err
	}
	_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
	return io.ReadAll(st)
}

func WriteProxyV2(w io.Writer, src, dst *net.TCPAddr) error {
	sig := []byte("\r\n\r\n\x00\r\nQUIT\n")
	var b bytes.Buffer
	b.Write(sig)
	if s4, d4 := src.IP.To4(), dst.IP.To4(); s4 != nil && d4 != nil {
		b.Write([]byte{0x21, 0x11, 0x00, 0x0c})
		b.Write(s4)
		b.Write(d4)
	} else {
		b.Write([]byte{0x21, 0x21, 0x00, 0x24})
		b.Write(src.IP.To16())
		b.Write(dst.IP.To16())
	}
	_ = binary.Write(&b, binary.BigEndian, uint16(src.Port))
	_ = binary.Write(&b, binary.BigEndian, uint16(dst.Port))
	_, err := w.Write(b.Bytes())
	return err
}

func EventuallyTrue(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	Eventually(t, timeout, func() error {
		if cond() {
			return nil
		}
		return fmt.Errorf("not yet: %s", msg)
	})
}

func Fingerprint(cs tls.ConnectionState) string {
	if len(cs.PeerCertificates) == 0 {
		return ""
	}
	sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:])
}

// AnswerSet renders answer RRs with TTL zeroed, sorted, so transports can be compared.
func AnswerSet(m *dns.Msg) []string {
	out := make([]string, 0, len(m.Answer)+1)
	out = append(out, dns.RcodeToString[m.Rcode])
	for _, rr := range m.Answer {
		c := dns.Copy(rr)
		c.Header().Ttl = 0
		out = append(out, c.String())
	}
	sort.Strings(out[1:])
	return out
}
```

- [ ] Implement `e2e/harness/dnstls.go`, the option fields and the fixture records: with `MgmtOptions.DNSTLS`, run `env.Bin("nexora-mgmt") ca issue-dns --ca-cert <CACertFile> --ca-key <CAKeyFile> --names dns.nexora.test,127.0.0.1 --days 30 --out <env.Dir>/dnstls` before starting mgmt, store the directory in `DNSTLSDir`, and pass the three env vars (reload interval `1s`); `DNSTLSRoots` loads `CACertFile` into a pool; `DNSTLSFingerprint` hashes the first certificate in `tls.crt`; `RotateDNSTLS` issues into `<env.Dir>/dnstls-next`, then renames `tls.key` into place followed by `tls.crt` (the watcher keeps the old material while the pair is inconsistent) and returns the new fingerprint. `EngineOptions` add `listen_dot = ["127.0.0.1:<free>"]`, `listen_doh = ["127.0.0.1:<free>"]`, `listen_doq = ["127.0.0.1:<free>"]` (a separate free UDP port), `proxy_protocol_dot` and `proxy_protocol_trusted_cidrs` to the generated `engine.toml`; `MetricsText` GETs `http://<Metrics>/metrics`; `PID` returns `Proc.Cmd.Process.Pid`. In the fixture, `POST /records` with `{"rrs": ["<presentation RR>", ...]}` parses each with `dns.NewRR`, stores them keyed by lowercase owner name (replacing earlier records for that name), and the DNS handler answers a stored name with its records of the asked type (AA=0, RA=1), NODATA when it has none of that type; `DNSFixture.SetRecords` posts to `Control`.
- [ ] Run `scripts/dev-exec.sh sh -c 'go get github.com/quic-go/quic-go@v0.62.0 && go mod tidy'` — expect `go.mod` to list `github.com/quic-go/quic-go v0.62.0` and still `github.com/miekg/dns v1.1.73`.
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -v ./e2e/ -run TestEncryptedTransports'` — expect PASS with subtests `doh-http2-multiplexing`, `dot-pipelining`, `doq-stream-per-query-and-protocol-error`, `certificate-rotation-without-restart`, `no-key-material-on-engine-disk`, `per-transport-metrics`.
- [ ] Commit: `git add go.mod go.sum e2e && git commit -m "test(e2e): TestEncryptedTransports over DoT, DoH and DoQ"`.

## Task 12: `TestPerClientPolicy` and `TestSafeSearchRewrites`

Files:
- `e2e/per_client_policy_test.go` — S-10 acceptance.
- `e2e/safe_search_rewrites_test.go` — S-11 acceptance.

Interfaces:
- Consumed from M1: everything listed under Task 11 "Consumed from M1"; (M1, later task) filter list API `POST /api/v1/filter-lists` with `{"name","url","format":"domains","kind":"blocklist","enabled"}` returning `{"id"}` and `GET /api/v1/filter-lists/{id}` returning `current_blob_sha256` (empty until the first successful fetch).
- Consumed from Task 11: `harness.EncryptedClient`, `harness.DoH`, `harness.WriteProxyV2`, `harness.EventuallyTrue`, `EngineOptions.{DoT, DoH, ProxyProtocolDoT, ProxyTrustedCIDRs}`, `MgmtOptions.DNSTLS`, `(*Mgmt).DNSTLSRoots()`, `(*DNSFixture).SetRecords`, `(*Engine).DoHURL()`, `(*Engine).DoTAddr()`.
- Consumed from Task 8: `/api/v1/policy-groups`, `/api/v1/safe-search`, `/api/v1/rewrites`.
- Produced: tests `TestPerClientPolicy`, `TestSafeSearchRewrites`; helper `udpFrom(t *testing.T, localIP, server, name string, qtype uint16) *dns.Msg` in `e2e/per_client_policy_test.go`.

- [ ] Write the failing test `e2e/per_client_policy_test.go`:

```go
package e2e

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

func udpFrom(t *testing.T, localIP, server, name string, qtype uint16) *dns.Msg {
	t.Helper()
	c := &dns.Client{Net: "udp", Timeout: 3 * time.Second, Dialer: &net.Dialer{LocalAddr: &net.UDPAddr{IP: net.ParseIP(localIP)}}}
	m, _, err := c.Exchange(question(name, qtype), server)
	if err != nil {
		t.Fatalf("query %s from %s: %v", name, localIP, err)
	}
	return m
}

func firstA(m *dns.Msg) string {
	for _, rr := range m.Answer {
		if a, ok := rr.(*dns.A); ok {
			return a.A.String()
		}
	}
	return ""
}

func TestPerClientPolicy(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	fx.SetRecords(t, "ads.example.test. 300 IN A 203.0.113.10", "ok.ads.example.test. 300 IN A 203.0.113.11")
	hf := env.StartHTTPFixture()
	hf.SetList(t, "ads.txt", "ads.example.test\n")
	mg := env.StartMgmt(harness.MgmtOptions{DNSTLS: true})
	op := mg.AdminAPI(t)
	op.MustDo(t, http.MethodPost, "/api/v1/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "enabled": true}, nil)
	eng := env.StartEngine(mg, harness.EngineOptions{Name: "policy-1", DoH: true})
	proxied := env.StartEngine(mg, harness.EngineOptions{Name: "policy-2", DoT: true, ProxyProtocolDoT: true, ProxyTrustedCIDRs: []string{"127.0.0.1/32"}})

	// positive path first: before any list exists every client resolves the name
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		m, _, err := new(dns.Client).Exchange(question("ads.example.test.", dns.TypeA), eng.DNS)
		return err == nil && firstA(m) == "203.0.113.10"
	}, "engine forwards to the fixture")
	for _, ip := range []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"} {
		if got := firstA(udpFrom(t, ip, eng.DNS, "ads.example.test.", dns.TypeA)); got != "203.0.113.10" {
			t.Fatalf("baseline from %s = %q", ip, got)
		}
	}

	var list struct {
		ID      string `json:"id"`
		BlobSHA string `json:"current_blob_sha256"`
	}
	if code := op.Do(t, http.MethodPost, "/api/v1/filter-lists", map[string]any{"name": "ads", "url": hf.URL("ads.txt"), "format": "domains", "kind": "blocklist", "enabled": false}, &list); code != http.StatusCreated {
		t.Fatalf("create list = %d", code)
	}
	if code := op.Do(t, http.MethodPost, "/api/v1/policy-groups", map[string]any{
		"name": "wide", "cidrs": []string{"127.0.0.0/29"}, "filter_list_ids": []string{list.ID}, "allowlist": []string{"ok.ads.example.test"},
	}, nil); code != http.StatusCreated {
		t.Fatalf("create wide = %d", code)
	}
	if code := op.Do(t, http.MethodPost, "/api/v1/policy-groups", map[string]any{
		"name": "open", "cidrs": []string{"127.0.0.3/32"},
	}, nil); code != http.StatusCreated {
		t.Fatalf("create open = %d", code)
	}
	harness.EventuallyTrue(t, 30*time.Second, func() bool {
		op.Do(t, http.MethodGet, "/api/v1/filter-lists/"+list.ID, nil, &list)
		return list.BlobSHA != ""
	}, "a disabled list referenced by a policy group is fetched")

	// 127.0.0.2 is only in "wide" (list selected): blocked with null_ip
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		return firstA(udpFrom(t, "127.0.0.2", eng.DNS, "ads.example.test.", dns.TypeA)) == "0.0.0.0"
	}, "wide group blocks ads.example.test")
	// 127.0.0.3 is in both; the /32 is more specific, and "open" selects no list
	if got := firstA(udpFrom(t, "127.0.0.3", eng.DNS, "ads.example.test.", dns.TypeA)); got != "203.0.113.10" {
		t.Fatalf("open group (most specific CIDR) = %q, want 203.0.113.10", got)
	}
	// the list is not enabled globally: 127.0.0.9 is in no group and resolves
	if got := firstA(udpFrom(t, "127.0.0.9", eng.DNS, "ads.example.test.", dns.TypeA)); got != "203.0.113.10" {
		t.Fatalf("global client = %q, want 203.0.113.10", got)
	}
	// group allowlist beats the group's blocklist
	if got := firstA(udpFrom(t, "127.0.0.2", eng.DNS, "ok.ads.example.test.", dns.TypeA)); got != "203.0.113.11" {
		t.Fatalf("allowlisted name for wide = %q", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	t.Run("doh-policy-uses-tcp-peer", func(t *testing.T) {
		for ip, want := range map[string]string{"127.0.0.2": "0.0.0.0", "127.0.0.3": "203.0.113.10"} {
			c := harness.EncryptedClient{RootCAs: mg.DNSTLSRoots(), ServerName: "dns.nexora.test", LocalIP: net.ParseIP(ip)}
			var m *dns.Msg
			harness.EventuallyTrue(t, 30*time.Second, func() bool {
				var err error
				m, _, err = harness.DoH(ctx, c.HTTPClient(), eng.DoHURL(), http.MethodPost, question("ads.example.test.", dns.TypeA))
				return err == nil
			}, "DoH reachable")
			if got := firstA(m); got != want {
				t.Fatalf("DoH from %s = %q, want %q", ip, got, want)
			}
		}
	})

	t.Run("dot-proxy-protocol-source", func(t *testing.T) {
		query := func(claimed string) string {
			raw, err := net.DialTimeout("tcp", proxied.DoTAddr(), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			dst := raw.RemoteAddr().(*net.TCPAddr)
			if err := harness.WriteProxyV2(raw, &net.TCPAddr{IP: net.ParseIP(claimed), Port: 40000}, dst); err != nil {
				t.Fatal(err)
			}
			tc := tls.Client(raw, &tls.Config{RootCAs: mg.DNSTLSRoots(), ServerName: "dns.nexora.test"})
			conn := &dns.Conn{Conn: tc}
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMsg(question("ads.example.test.", dns.TypeA)); err != nil {
				t.Fatal(err)
			}
			r, err := conn.ReadMsg()
			if err != nil {
				t.Fatal(err)
			}
			return firstA(r)
		}
		harness.EventuallyTrue(t, 30*time.Second, func() bool { return query("127.0.0.3") == "203.0.113.10" }, "PROXY source in open group resolves")
		if got := query("127.0.0.2"); got != "0.0.0.0" {
			t.Fatalf("PROXY source 127.0.0.2 = %q, want 0.0.0.0 (policy from PROXY header, not TCP peer 127.0.0.1)", got)
		}
	})
}
```

Both engines are enrolled with the same management plane, so both receive the groups.
- [ ] Write the failing test `e2e/safe_search_rewrites_test.go`:

```go
package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

func TestSafeSearchRewrites(t *testing.T) {
	env := harness.New(t)
	fx := env.StartDNSFixture()
	fx.SetRecords(t,
		"www.google.com. 300 IN A 142.250.1.1",
		"forcesafesearch.google.com. 300 IN A 216.239.38.120",
		"www.bing.com. 300 IN A 13.107.21.200",
		"strict.bing.com. 300 IN A 204.79.197.220",
		"duckduckgo.com. 300 IN A 52.142.124.215",
		"safe.duckduckgo.com. 300 IN A 52.142.126.100",
		"www.youtube.com. 300 IN A 142.250.1.2",
		"restrict.youtube.com. 300 IN A 216.239.38.120",
		"restrictmoderate.youtube.com. 300 IN A 216.239.38.119",
	)
	mg := env.StartMgmt(harness.MgmtOptions{})
	op := mg.AdminAPI(t)
	op.MustDo(t, http.MethodPost, "/api/v1/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "enabled": true}, nil)
	eng := env.StartEngine(mg, harness.EngineOptions{Name: "ss-1"})
	a := func(ip, name string) string { return firstA(udpFrom(t, ip, eng.DNS, name, dns.TypeA)) }

	// positive path: safe search off returns the provider's normal address
	harness.EventuallyTrue(t, 30*time.Second, func() bool { return a("127.0.0.1", "www.google.com.") == "142.250.1.1" }, "baseline www.google.com")

	var ss struct {
		Revision int64 `json:"revision"`
	}
	op.Do(t, http.MethodGet, "/api/v1/safe-search", nil, &ss)
	if code := op.Do(t, http.MethodPut, "/api/v1/safe-search", map[string]any{
		"google": true, "bing": true, "duckduckgo": true, "youtube": "strict", "revision": ss.Revision,
	}, &ss); code != http.StatusOK {
		t.Fatalf("enable safe search = %d", code)
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool { return a("127.0.0.1", "www.google.com.") == "216.239.38.120" }, "google safe search applied")
	m := udpFrom(t, "127.0.0.1", eng.DNS, "WWW.GOOGLE.COM.", dns.TypeA)
	cname, ok := m.Answer[0].(*dns.CNAME)
	if !ok || cname.Target != "forcesafesearch.google.com." || m.Answer[0].Header().Name != "WWW.GOOGLE.COM." {
		t.Fatalf("google answer = %v", m.Answer)
	}
	for name, want := range map[string]string{
		"www.google.co.uk.": "216.239.38.120", "www.bing.com.": "204.79.197.220",
		"duckduckgo.com.": "52.142.126.100", "www.youtube.com.": "216.239.38.120",
	} {
		if got := a("127.0.0.1", name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if code := op.Do(t, http.MethodPut, "/api/v1/safe-search", map[string]any{
		"google": true, "bing": true, "duckduckgo": true, "youtube": "moderate", "revision": ss.Revision,
	}, &ss); code != http.StatusOK {
		t.Fatalf("youtube moderate = %d", code)
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool { return a("127.0.0.1", "www.youtube.com.") == "216.239.38.119" }, "youtube moderate applied")

	// custom rewrites: A, AAAA, wildcard CNAME chased to a rewrite, exact beats wildcard
	for _, r := range []map[string]any{
		{"name": "nas.home.test", "type": "A", "value": "192.168.1.50", "ttl": 120},
		{"name": "nas.home.test", "type": "AAAA", "value": "fd00::50", "ttl": 120},
		{"name": "*.lab.home.test", "type": "CNAME", "value": "nas.home.test", "ttl": 60},
		{"name": "special.lab.home.test", "type": "A", "value": "192.168.1.60"},
	} {
		if code := op.Do(t, http.MethodPost, "/api/v1/rewrites", r, nil); code != http.StatusCreated {
			t.Fatalf("create rewrite %v = %d", r, code)
		}
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool { return a("127.0.0.1", "nas.home.test.") == "192.168.1.50" }, "custom rewrite applied")
	nas := udpFrom(t, "127.0.0.1", eng.DNS, "nas.home.test.", dns.TypeA)
	if nas.Answer[0].Header().Ttl != 120 {
		t.Errorf("rewrite ttl = %d, want 120", nas.Answer[0].Header().Ttl)
	}
	aaaa := udpFrom(t, "127.0.0.1", eng.DNS, "nas.home.test.", dns.TypeAAAA)
	if len(aaaa.Answer) != 1 || aaaa.Answer[0].(*dns.AAAA).AAAA.String() != "fd00::50" {
		t.Errorf("AAAA rewrite = %v", aaaa.Answer)
	}
	mx := udpFrom(t, "127.0.0.1", eng.DNS, "nas.home.test.", dns.TypeMX)
	if mx.Rcode != dns.RcodeSuccess || len(mx.Answer) != 0 {
		t.Errorf("MX on rewritten name = rcode %d answers %v, want NODATA", mx.Rcode, mx.Answer)
	}
	if got := a("127.0.0.1", "x.lab.home.test."); got != "192.168.1.50" {
		t.Errorf("wildcard CNAME chase = %q", got)
	}
	if got := a("127.0.0.1", "special.lab.home.test."); got != "192.168.1.60" {
		t.Errorf("exact beats wildcard = %q", got)
	}

	// a group gets only its own safe search and rewrites
	if code := op.Do(t, http.MethodPost, "/api/v1/policy-groups", map[string]any{
		"name": "adults", "cidrs": []string{"127.0.0.5/32"},
		"safe_search": map[string]any{"google": false, "bing": false, "duckduckgo": false, "youtube": "off"},
	}, nil); code != http.StatusCreated {
		t.Fatalf("create group = %d", code)
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool { return a("127.0.0.5", "www.google.com.") == "142.250.1.1" }, "group without safe search")
	if got := a("127.0.0.5", "nas.home.test."); got == "192.168.1.50" {
		t.Errorf("group client got global rewrite %q; a group replaces global rewrites", got)
	}
	if got := a("127.0.0.1", "www.google.com."); got != "216.239.38.120" {
		t.Errorf("global client lost safe search: %q", got)
	}
}
```

The `nas.home.test` query from the "adults" client goes upstream, where the fixture answers its default `192.0.2.1`; only the rewrite address is a failure.
- [ ] Prove the tests catch the break: temporarily change the first line of `PolicyTable::select` in `engine/src/filter.rs` to `return (&self.global, None);`, run `scripts/dev-exec.sh bash -c 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test ./e2e/ -run TestPerClientPolicy -count=1'` — expect FAIL with "wide group blocks ads.example.test" (condition not met); then temporarily change `EffectivePolicy::check` to skip the rewrite lookup, run `scripts/dev-exec.sh bash -c 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test ./e2e/ -run TestSafeSearchRewrites -count=1'` — expect FAIL with "google safe search applied" (condition not met). Revert both edits with `git checkout engine/src/filter.rs`.
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -v ./e2e/ -run "TestPerClientPolicy|TestSafeSearchRewrites|TestEncryptedTransports|TestBlocklistSubscription|TestForwardCacheTTL"'` — expect PASS for all five.
- [ ] Commit: `git add e2e && git commit -m "test(e2e): TestPerClientPolicy and TestSafeSearchRewrites"`.

## Task 13: kw deployment, smoke subtests, GUI coverage and perf gate

Files:
- `deploy/kw/engine.yaml` (M1) — engine.toml listener keys, container ports 853/tcp, 853/udp, 443/tcp.
- `deploy/kw/engine-encrypted-service.yaml` — LoadBalancer Service for DoT/DoQ/DoH.
- `deploy/kw/mgmt.yaml` (M1) — DNS TLS secret mount and env vars.
- `e2e/kwsmoke/smoke_test.go` (M1 `TestKWSmoke`) — subtests `dot`, `doh-get`, `doh-post`, `doq`.

Interfaces:
- Consumed from M1 (later tasks; kw deployment, smoke test and perf gate were not yet published in the M1 plan): namespace `nexora`; engine Deployment `nexora-engine` (pods labelled `app.kubernetes.io/name: nexora-engine`) with ConfigMap `nexora-engine-config` key `engine.toml`; mgmt Deployment `nexora-mgmt` with CA secret `nexora-ca` (keys `ca.crt`, `ca.key`) mounted at `/etc/nexora/ca`; `TestKWSmoke` reading `NEXORA_KW_DNS_ADDR`; `bin/perfgate relative` built by `make e2e-build` from `bench/cmd/perfgate`; `TestGUICoverage` in `e2e/`.
- Consumed from Task 11: `harness.EncryptedClient`, `harness.DoH`.
- Produced: Service `nexora-engine-encrypted` (ports `dot` 853/TCP, `doq` 853/UDP, `doh` 443/TCP, `externalTrafficPolicy: Local` so the engine sees client IPs); Secret `nexora-dns-tls` (type `kubernetes.io/tls`); smoke env `NEXORA_KW_ENCRYPTED_ADDR` (LB IP), `NEXORA_KW_CA_FILE`, `NEXORA_KW_DNS_TLS_NAME` (`dns.nexora.kw.local`).

- [ ] Write the failing smoke subtests in `e2e/kwsmoke/smoke_test.go`, inside `TestKWSmoke` after M1's UDP check:

```go
	encAddr := os.Getenv("NEXORA_KW_ENCRYPTED_ADDR")
	if encAddr == "" {
		t.Fatal("NEXORA_KW_ENCRYPTED_ADDR is required (LoadBalancer IP of nexora-engine-encrypted)")
	}
	caPEM, err := os.ReadFile(os.Getenv("NEXORA_KW_CA_FILE"))
	if err != nil {
		t.Fatalf("NEXORA_KW_CA_FILE: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	enc := harness.EncryptedClient{RootCAs: roots, ServerName: os.Getenv("NEXORA_KW_DNS_TLS_NAME")}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	q := func() *dns.Msg { m := new(dns.Msg); m.SetQuestion("example.com.", dns.TypeA); return m }
	ok := func(t *testing.T, m *dns.Msg, err error) {
		t.Helper()
		if err != nil || m.Rcode != dns.RcodeSuccess || len(m.Answer) == 0 {
			t.Fatalf("answer %v err %v", m, err)
		}
	}
	t.Run("dot", func(t *testing.T) {
		m, _, err := enc.DoT(ctx, net.JoinHostPort(encAddr, "853"), q())
		ok(t, m, err)
	})
	t.Run("doh-get", func(t *testing.T) {
		m, _, err := harness.DoH(ctx, enc.HTTPClient(), "https://"+net.JoinHostPort(encAddr, "443")+"/dns-query", http.MethodGet, q())
		ok(t, m, err)
	})
	t.Run("doh-post", func(t *testing.T) {
		m, _, err := harness.DoH(ctx, enc.HTTPClient(), "https://"+net.JoinHostPort(encAddr, "443")+"/dns-query", http.MethodPost, q())
		ok(t, m, err)
	})
	t.Run("doq", func(t *testing.T) {
		m, _, err := enc.DoQ(ctx, net.JoinHostPort(encAddr, "853"), q())
		ok(t, m, err)
	})
```

(imports: `context`, `crypto/x509`, `net`, `net/http`, `os`, `time`, `github.com/miekg/dns`, `github.com/piwi3910/nexora/e2e/harness`.)
- [ ] Run `scripts/dev-exec.sh env NEXORA_KW_DNS_ADDR=<M1 DNS LB IP> go test -count=1 ./e2e/kwsmoke -run TestKWSmoke` — expect FAIL with "NEXORA_KW_ENCRYPTED_ADDR is required".
- [ ] Write `deploy/kw/engine-encrypted-service.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: nexora-engine-encrypted
  namespace: nexora
  labels:
    app.kubernetes.io/name: nexora-engine
    app.kubernetes.io/component: dns-encrypted
spec:
  type: LoadBalancer
  externalTrafficPolicy: Local
  selector:
    app.kubernetes.io/name: nexora-engine
  ports:
    - name: dot
      protocol: TCP
      port: 853
      targetPort: dot
    - name: doq
      protocol: UDP
      port: 853
      targetPort: doq
    - name: doh
      protocol: TCP
      port: 443
      targetPort: doh
```

- [ ] Edit `deploy/kw/engine.yaml`: in the engine container add ports `- {name: dot, containerPort: 853, protocol: TCP}`, `- {name: doq, containerPort: 853, protocol: UDP}`, `- {name: doh, containerPort: 443, protocol: TCP}`; in `nexora-engine-config` `engine.toml` add:

```toml
listen_dot = ["0.0.0.0:853"]
listen_doh = ["0.0.0.0:443"]
listen_doq = ["0.0.0.0:853"]
doh_path = "/dns-query"
proxy_protocol_dot = false
proxy_protocol_doh = false
```

- [ ] Edit `deploy/kw/mgmt.yaml`: add volume `dns-tls` from secret `nexora-dns-tls` (`defaultMode: 0400`) mounted read-only at `/etc/nexora/dns-tls`, and env `NEXORA_DNS_TLS_CERT_FILE=/etc/nexora/dns-tls/tls.crt`, `NEXORA_DNS_TLS_KEY_FILE=/etc/nexora/dns-tls/tls.key`, `NEXORA_DNS_TLS_RELOAD_INTERVAL=30s`. (A Secret volume is updated in place by the kubelet, so rotating the Secret rotates the certificate without restarting mgmt or engines.)
- [ ] Build and push images: `scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine -t m2-$(git rev-parse --short HEAD)` and `scripts/build-image.sh -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t m2-$(git rev-parse --short HEAD)` — expect `pull as 192.168.10.131/azrtydxb/...` lines; set both Deployment image tags to that tag.
- [ ] Apply the Service and read its IP: `kubectl --context kw apply -f deploy/kw/engine-encrypted-service.yaml && kubectl --context kw -n nexora get svc nexora-engine-encrypted -o jsonpath='{.status.loadBalancer.ingress[0].ip}'` — expect an IP (call it `$ENC_IP`).
- [ ] Issue the DNS certificate inside a running mgmt pod and create the Secret without leaving key files behind: `kubectl --context kw -n nexora exec deploy/nexora-mgmt -- nexora-mgmt ca issue-dns --ca-cert /etc/nexora/ca/ca.crt --ca-key /etc/nexora/ca/ca.key --names dns.nexora.kw.local,$ENC_IP --days 90 --out /tmp/dnstls`, then `d=$(mktemp -d) && kubectl --context kw -n nexora exec deploy/nexora-mgmt -- cat /tmp/dnstls/tls.crt > $d/tls.crt && kubectl --context kw -n nexora exec deploy/nexora-mgmt -- cat /tmp/dnstls/tls.key > $d/tls.key && kubectl --context kw -n nexora create secret tls nexora-dns-tls --cert=$d/tls.crt --key=$d/tls.key --dry-run=client -o yaml | kubectl --context kw apply -f - ; rm -rf $d; kubectl --context kw -n nexora exec deploy/nexora-mgmt -- rm -rf /tmp/dnstls` — expect `secret/nexora-dns-tls created`.
- [ ] Apply `kubectl --context kw apply -f deploy/kw/engine.yaml -f deploy/kw/mgmt.yaml && kubectl --context kw -n nexora rollout status deploy/nexora-mgmt deploy/nexora-engine --timeout=5m` — expect both `successfully rolled out`.
- [ ] Copy the CA for the smoke test into the dev pod: `kubectl --context kw -n nexora get secret nexora-ca -o jsonpath='{.data.ca\.crt}' | base64 -d | scripts/dev-exec.sh sh -c 'cat > /work/kw-ca.crt'`.
- [ ] Run `scripts/dev-exec.sh env NEXORA_KW_DNS_ADDR=<M1 DNS LB IP> NEXORA_KW_ENCRYPTED_ADDR=$ENC_IP NEXORA_KW_CA_FILE=/work/kw-ca.crt NEXORA_KW_DNS_TLS_NAME=dns.nexora.kw.local go test ./e2e/kwsmoke -run 'TestKWSmoke' -count=1 -v` — expect PASS including `TestKWSmoke/dot`, `TestKWSmoke/doh-get`, `TestKWSmoke/doh-post`, `TestKWSmoke/doq`.
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -v ./e2e/ -run TestGUICoverage'` — expect PASS; the output lists `listPolicyGroups`, `createPolicyGroup`, `getPolicyGroup`, `updatePolicyGroup`, `deletePolicyGroup`, `getGlobalSafeSearch`, `updateGlobalSafeSearch`, `listRewrites`, `createRewrite`, `updateRewrite`, `deleteRewrite`, `getDnsTlsStatus` as covered. A missing operation fails with its operationId named; fix the owning Playwright spec from Task 10.
- [ ] Run the relative perf gate exactly as `.github/workflows/perf-gate.yml` runs it on PRs: `scripts/dev-exec.sh bash -c 'make e2e-build && bin/perfgate relative'` — expect `PASS` with QPS within 5% of main's recorded baseline (the UDP cache-hit path gained one `PolicyTable::select` per packet; if the drop exceeds 5%, profile `select` with `perf record` on the dnsperf run and reduce the per-length probes, since a policy table without groups must add no more than one branch).
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test hot_path_alloc cache_hit_path_does_not_allocate` — expect PASS.
- [ ] Commit: `git add deploy/kw e2e/kwsmoke && git commit -m "deploy(kw): encrypted DNS service, DNS TLS secret, smoke subtests"`.
