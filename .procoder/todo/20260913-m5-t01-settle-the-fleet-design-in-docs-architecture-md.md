# M5 Task 1: Settle the fleet design in docs/architecture.md

Status: open
Created: 2026-09-13

## Description

Implement Task 1 ("Settle the fleet design in docs/architecture.md") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 1 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [ ] `TestKwFullProduct` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

Implemented 2026-09-14, not committed (lead commits).

- Identifier check (plan Task 1 step 1, run on the laptop checkout): newest migration `00403_dnssec_key_lifecycle.sql` (< 00500); counts `14`, `5`, `0`, `12`, `8`, `1`, `5` — all as expected, no "M1–M4 names" table needed.
- `grep -c "^## Fleet (M5)" docs/architecture.md` -> 1; `grep -c "192.168.10.13[5-7]" docs/architecture.md` -> 3; `grep -c "3 replicas, one per" docs/architecture.md` -> 0.
- `procoder check docs/architecture.md` -> `1 clean, 0 unformatted ... (0 blocking)`. Two sentences were reworded for the gate (a label-key regex read as a broken markdown link; two code spans split across lines); plan text updated to match.
- The "TestKwFullProduct passes" criterion belongs to Task 14, not this documentation task; not run here.
