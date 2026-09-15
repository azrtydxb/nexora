# M6 T16: Access control split in the engine, hot-path proof and e2e

Status: open (blocked on the query-log rule mapping outside Task 16's files)
Created: 2026-09-14

## Description

Implements Task 16 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `engine/tests/authoritative_acl.rs`. Copy `start_zone` and `ask` from
- [x] Extend `cache_hit_path_does_not_allocate` in `engine/tests/hot_path_alloc.rs`:
- [x] Run
- [x] Implement:
- [x] Create `e2e/access_split_test.go`:
- [x] Create `e2e/gui_seed_access_test.go`, which registers a seed that creates the primary zone
- [ ] Run (e2e: `TestAuthoritativeAccessSplit` fails at the query-log `rule` step; see Evidence)
- [x] Run `scripts/dev-exec.sh 'cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`.

## Evidence

Verified 2026-09-15 in pod `toolbox` with `CARGO_TARGET_DIR=/work/target-t16` and e2e binaries in
`/tmp/t16-bin` (both deleted afterwards):

- Red: `cargo test --locked -p nexora-engine --test authoritative_acl` -> FAILED, `left: NoError right: Refused`
  at the zone-override assertion (and "the set flag applies the list").
- `cargo test --locked -p nexora-engine --all-targets` -> every suite `ok` (lib 244 passed incl.
  `update_source_cidrs_refuse_outside_senders`; authoritative_acl 2 passed; hot_path_alloc 2 passed).
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> Finished, no warnings.
- `go vet ./e2e` -> clean; `gofmt` applied.
- `go test ./e2e -run "TestAuthoritativeZonePropagation|TestAXFRIXFROut|TestSecondaryAndDynamicUpdate"` -> `ok` (all PASS).
- `go test ./e2e -run TestAuthoritativeAccessSplit` -> FAIL only at
  `condition not met within 30s: query log records both refusal kinds`: all udp/tcp/dot/doh subtests
  and the override refusals pass; the query log returns `{Source:acl Rule: Client:127.0.0.3}`. The
  API never maps `Record.ACLRefused` to `rule` (`mgmt/internal/api/querylog_resolve.go`, Task 5).
  Needed fix (not Task 16's file): `Rule: r.ACLRefused` when `r.Source == "acl"`.
- GUI seed `NEXORA_E2E_ACL_ZONE` compiles; spec 30 not run here (needs the web build).

