# M3 Task 10: RPZ sources — blob files, AXFR/IXFR with TSIG, SOA refresh, last-good persistence

Status: open
Created: 2026-09-13

## Description

Implement Task 10 ("RPZ sources — blob files, AXFR/IXFR with TSIG, SOA refresh, last-good persistence") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] (library steps done; integration steps pending Task 5) Every step of Task 10 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Built: `engine/src/recursor/rpz/{tsig,transfer,manager,transfer_tests}.rs`, `RpzState` in `rpz/mod.rs`.
- `cargo test --locked -p nexora-engine --lib recursor::rpz`: `test result: ok. 16 passed; 0 failed` (8 transfer_tests incl. TSIG, IXFR, last-good persistence, in-memory keys, file zones, metrics).
- Mutation: ignoring the HMAC verify result makes `tsig_sign_and_verify_detects_tampering_wrong_key_and_time` fail (restored).
- BIND 9 interop (temporary ignored test, removed): named primary with hmac-sha256 key, 3001-record zone: `AXFR serial 10 records 3004`; wrong key `TSIG verification failed: BadSig`; no key `AXFR: Query Refused`; after zone update + SIGHUP `soa: Ok(11)`, `IXFR serial 11 records 3004 has new true lacks deleted true` (named log: `IXFR started: TSIG rpz-key (serial 10 -> 11)`).
- `make engine-test`: exit 0. clippy: clean under `recursor/rpz/`.
- NOT DONE: control-stream `RpzTsigKeys` arm, `Stats.rpz_zones`, metrics render hook, `ResolutionRuntime.rpz`, `RecursorState` embedding and background `run` — blocked on Task 5; plan marks them "Integration (after Task 5)". Red-before-green not observed for transfer_tests (implementation written first; mutation check instead). Not committed.

- Integration steps completed together with M3 Task 5 (see Task 5 As-built notes in the plan): `ResolutionRuntime.rpz`, `RecursorState.rpz` + `sync`, `manager.run` on `nexora-recursor`, `RpzTsigKeys` control-stream arm, `Stats.rpz_zones`, RPZ metrics in `render`. Verified by `recursor::dispatch_tests` (6 passed), `tests/rpz_pipeline.rs` (1 passed), `cache_hit_path_does_not_allocate` (forward, recursive+validation, RPZ triggers) and `make engine-test` exit 0. Not committed (lead commits).
