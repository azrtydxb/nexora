# M8 T33: Full verification

Status: open
Created: 2026-09-15

## Description

Implements Task 33 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Run `scripts/dev-exec.sh 'make engine-test && make mgmt-test && make web-test && make lint'` and
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/... -count=1 -timeout 120m'` and expect
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test hot_path_alloc -- --nocapture'`
- [ ] Run `rg -n "debt: ODoH keys are ignored" engine/src` and expect no match (Task 19 removed it).
- [ ] Record each command's summary line in `.procoder/todo/` evidence for the M8 tasks.

## Evidence
