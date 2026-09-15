# M7 T22: Workflows proven on the ARC runners (#3)

Status: open
Created: 2026-09-14

## Description

Implements Task 22 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Run `gh run list -R azrtydxb/nexora --workflow ci.yml --commit "$(git rev-parse HEAD)"` and `gh run list -R azrtydxb/nexora --workflow images.yml --commit "$(git rev-parse HEAD)"`; wait for completion with `gh run watch <id> -R azrtydxb/nexora --exit-status`. Expect `success` for both. A red job cau
- [ ] Run `gh run list -R azrtydxb/nexora --workflow fuzz.yml --event schedule --limit 1 --json conclusion,url,createdAt`. The 02:17 UTC nightly run must be a completed `success` after the M7 head was pushed. If none exists yet, wait for the next schedule rather than dispatching, because the criterion is
- [ ] Run `gh workflow run perf-gate.yml -R azrtydxb/nexora --ref main`, then `gh run watch <id> -R azrtydxb/nexora`.
- [ ] Run `gh run list -R azrtydxb/nexora --workflow perf-gate.yml --event schedule --limit 1 --json conclusion,url`. Expect `success` (the relative job is skipped on schedule, and the absolute job is skipped without `NEXORA_REFERENCE_HOST`).
- [ ] Record every run URL in `.procoder/todo/` for the closing comment of #3.

## Evidence
