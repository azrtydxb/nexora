# M8 T7: Engine ODoH codec and proxy core

Status: open
Created: 2026-09-15

## Description

Implements Task 7 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add `odoh-rs = "=1.0.5"` to `[dependencies]` in `engine/Cargo.toml` and run
- [x] Add to `engine/src/server/odoh.rs` this test module:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib server::odoh::tests'` and
- [x] Implement:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib server::odoh::tests && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
- [ ] Report the paths. The lead commits `M8 T7: engine ODoH codec and proxy core`.

## Evidence

All runs in pod toolbox-m7, in the private copy `/work/m8-t7` (laptop files synced by
`NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-sync.sh`, then copied in) with
`CARGO_TARGET_DIR=/work/target-m8-t7`.

- Lock: `cargo update -p odoh-rs --precise 1.0.5` fails (`package ID specification odoh-rs did not
match any packages`); `cargo tree -p nexora-engine -i odoh-rs` resolved it. The lock adds odoh-rs
  1.0.5, hpke 0.14.1, aes-gcm 0.11.1, x25519-dalek 3.0.0, curve25519-dalek 5.0.0, hkdf/hmac 0.13 and a
  second `thiserror` major (1.0.69). No second `sha2` (0.11.0 only) and no new `rand_core` major
  (hpke uses 0.10.1, already present). No existing entry changed version.
- Red: `cargo test --locked -p nexora-engine --lib server::odoh::tests` ->
  `error[E0425]: cannot find type Keyring in this scope` (and `open`, `Reject`, `ProxyConfig`,
  `parse_proxy_params`).
- Green: `cargo test --locked -p nexora-engine --lib server::odoh::tests` ->
  `test result: ok. 5 passed; 0 failed` (`target_round_trip`, `rejections_map_to_status`,
  `proxy_target_matching`, plus `forward_relays_target_response_and_maps_failures` and
  `counters_render_by_role_and_status`). The forward test failed first on `tls_protocol_error`
  (got `destination_unavailable`: the rustls error sits in nested `io::Error` payloads), fixed by
  unwrapping through `get_ref`.
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> exit 0, no warnings.
- `rustfmt --check --edition 2024 engine/src/server/odoh.rs engine/src/upstream/doh.rs` -> clean.
- `resolve_pins` refactor: `cargo test --locked -p nexora-engine --test upstream_encrypted` ->
  `test result: ok. 3 passed`.
