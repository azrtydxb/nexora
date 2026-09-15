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

2026-09-15 verification sweep, head 15978fd:
- ci: success https://github.com/azrtydxb/nexora/actions/runs/34957578087
- fuzz (workflow_dispatch on the M7 head): success https://github.com/azrtydxb/nexora/actions/runs/34958189142 . Last nightly: success https://github.com/azrtydxb/nexora/actions/runs/34943154465 (07:43 UTC, before the M7 head was pushed; the nightly criterion still needs the next 02:17 UTC run).
- images: NOT PROVEN. https://github.com/azrtydxb/nexora/actions/runs/34946571726 has arm64 builds green and both amd64 builds queued since 08:22 UTC, holding the concurrency group, so https://github.com/azrtydxb/nexora/actions/runs/34957578223 is pending.
- perf-gate dispatch https://github.com/azrtydxb/nexora/actions/runs/34958192321 : `relative` job queued (runner arc-azrtydxb-amd64), absolute skipped. Last schedule run: skipped https://github.com/azrtydxb/nexora/actions/runs/34947292049 .
- Cause (infrastructure, not repo code): on the novanas cluster both amd64 ARC listeners (`arc-systems/arc-azrtydxb-amd64-*-listener`, `arc-azrtydxb-amd64-publish-*-listener`) are in Error and `arc-gha-rs-controller` logs `dial tcp 10.43.0.1:443: connect: no route to host`. Recorded in plan-review.md; this task stays open until that cluster's ARC is repaired and the amd64 runs go green.

