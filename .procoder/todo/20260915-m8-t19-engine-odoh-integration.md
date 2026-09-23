# M8 T19: Engine ODoH integration

Status: open
Created: 2026-09-15

## Description

Implements Task 19 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `engine/tests/odoh_pipeline.rs`:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test odoh_pipeline'` and expect
- [x] Implement:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test odoh_pipeline && cargo test --locked -p nexora-engine --test upstream_encrypted && cargo test --locked -p nexora-engine --test snapshot_apply && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
- [ ] Report the paths. The lead commits `M8 T19: engine ODoH target and proxy on DoH listeners`.

## Evidence

All in pod toolbox-m7 with `CARGO_TARGET_DIR=/work/target-m8-t19`.

- Red: `cargo test --locked -p nexora-engine --test odoh_pipeline` failed to compile:
  `error[E0061]: this function takes 4 arguments but 6 arguments were supplied` (doh::handle) and
  `method recursion_allowed is not a member of trait Answerer`.
- Green: `odoh_pipeline` `test result: ok. 3 passed`; `upstream_encrypted` `ok. 3 passed`;
  `snapshot_apply` `ok. 12 passed`; `--lib server::` `ok. 24 passed`; `--lib telemetry::metrics`
  `ok. 3 passed`; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean;
  `rustfmt --edition 2024 --check` on the changed files: exit 0.
- Mutations (private copy /work/m8-t19): skipping the recursion ACL check fails
  `left: (502, destination_unavailable) right: (403, http_request_denied)`; dropping the target
  count and the metrics render fails both the counter and the metrics tests.
