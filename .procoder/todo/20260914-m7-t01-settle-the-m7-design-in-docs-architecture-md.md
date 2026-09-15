# M7 T1: Settle the M7 design in docs/architecture.md

Status: open
Created: 2026-09-14

## Description

Implements Task 1 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Run `grep -c "BADVERS\|cache_max_bytes = 800\|server::buffers" docs/architecture.md` and expect `0`.
- [x] In `### Wire handling`, after the FORMERR bullet, add: `- An OPT record with an EDNS version other than 0 -> extended RCODE BADVERS (header RCODE 0, OPT extended RCODE 1, version 0, no answer), before the authoritative, ACL, filter, cache and resolution stages.`
- [x] In `### ACL`, append: `The allow list is kept as sorted, merged address ranges per family and looked up by binary search.`
- [x] In `### Process and threads`, append the bullet: ``- TCP, DoT and DoQ queries take their query, 64 KiB answer and frame buffers from a per-worker pool (`server::buffers`, at most 256 buffers of 65,537 octets). Zone transfers are built on tokio's blocking pool.``
- [x] In `### Telemetry`, replace `The telemetry thread batches records (max 1000 or 1 s)` with ``The telemetry thread batches records (max 1000 or 1 s) and runs up to 4 exports at once (`MAX_INFLIGHT`)``, and append the bullet: ``- The upstream and policy-group labels of a record come from `Runtime.label
- [x] After the `### Snapshot application` section add a `### Recursor memory` section: ``One budget, `RecursionConfig.cache_max_bytes` (0 = 64 MiB, else 4 MiB..16 GiB), split 12/16 RRset cache, 3/16 aggressive NSEC cache, 1/16 infrastructure caches, each weighted by estimated bytes (`recursor::memory`).
- [x] In `## Contract`, append: ``- M7: fields added to existing messages use 800-899: `RecursionConfig.cache_max_bytes` (800).``
- [x] In `## Management plane`, append these bullets:
- [x] In `### Distribution`, after the compose bullet, add: ``- `scripts/compose-verify.sh <user@host>` runs the documented Compose install, backup and restore on a Docker host.``
- [x] Run `grep -c "BADVERS" docs/architecture.md; grep -c "cache_max_bytes. (800)" docs/architecture.md; grep -c "server::buffers" docs/architecture.md` and expect three non-zero counts.

## Evidence

- Before: `grep -c "BADVERS\|cache_max_bytes = 800\|server::buffers" docs/architecture.md` -> `0`.
- All eight edits made in `docs/architecture.md` (Wire handling, ACL, Process and threads, Telemetry
  sentence + bullet, new `### Recursor memory` after Snapshot application, Contract M7 bullet,
  Management plane M7 bullets, Distribution compose-verify bullet).
- After: `grep -c "BADVERS"` -> 1; `grep -c "cache_max_bytes. (800)"` -> 1; `grep -c "server::buffers"` -> 1.
- Numbers checked free on this branch: no proto field 800-899 in use, highest migration 00503.
- `procoder check docs/architecture.md` -> 1 clean, 0 unformatted, 0 blocking.
