# M8 T6: Engine mDNS interface lookup and gateway core

Status: open
Created: 2026-09-15

## Description

Implements Task 6 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to `engine/src/mdns/gateway.rs` (below the doc comment) this test module:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib mdns::'` and expect FAIL:
- [x] Implement `iface.rs` with `nix::ifaddrs::getifaddrs()`:
- [x] Implement `gateway.rs`:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib mdns:: && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
- [ ] Report the paths. The lead commits `M8 T6: engine mDNS gateway core`.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'CARGO_TARGET_DIR=/work/target-m8-t6 cargo test --locked -p nexora-engine --lib mdns::'`
  failed to compile: `error[E0425]: cannot find type \`Gateway\` in this scope`(gateway.rs),`cannot find function \`lookup\`` (iface.rs).
- Green, in a private copy `/work/m8-t6` of the synced tree with the in-progress Task 5 (`engine/src/zonemd.rs`) and Task 7
  (`engine/src/server/odoh.rs`, `engine/Cargo.toml`, `Cargo.lock`) files restored to HEAD, since those did not compile yet:
  `CARGO_TARGET_DIR=/work/target-m8-t6 cargo test --locked -p nexora-engine --lib mdns::` ->
  `test result: ok. 4 passed; 0 failed; 0 ignored; 0 measured; 258 filtered out; finished in 1.01s`;
  `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> `Finished` (no warnings);
  `rustfmt --edition 2024 --check engine/src/mdns/{gateway,iface}.rs` -> clean.
- Mutation check: removing the reply ID check and the TTL cap fails `reply_checks_and_ttl_cap` (`TTL capped at 10`).
- Deviation (plan text updated): hickory-proto 0.26.3 `Record` has fields, not `ttl()/dns_class()/data()` accessors;
  tests serialise on a `SERIAL` tokio mutex because the in-flight cap is a process static; `WireAnswer` type alias for clippy.
- Not committed (the lead commits `M8 T6: engine mDNS gateway core`).
