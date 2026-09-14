# M7 T5: Zone transfers off the worker thread (#12)

Status: open
Created: 2026-09-14

## Description

Implements Task 5 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Add inside `mod authorization` in `xfr_tests.rs`:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib transfer_is_built_off_the_worker'`. Expect FAIL with `round 0: nothing else ran on the worker`: today `run_slow` never yields for transfers.
- [ ] In `dispatch.rs`, move the body of the `SlowKind::Transfer` arm (the `Question::parse` FORMERR reply, `authorize_and_plan`, `xfr::count`, `xfr::messages`) into `fn build_transfer(rt: &Runtime, shared: &Shared, job: &SlowJob) -> Vec<Vec<u8>>`. Use `shared.auth.keyring` and `shared.metrics.auth`, and 
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib authoritative:: && cargo test --locked -p nexora-engine --test authoritative_pipeline'` and expect every test to pass, including `signed_queries_and_transfers_through_the_pipeline`.

## Evidence

