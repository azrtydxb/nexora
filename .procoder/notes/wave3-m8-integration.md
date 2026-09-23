# Wave 3 M8 integration handoff — 2026-09-17

Baseline `f9c1653`, incoming committed M8 `3fb09b9`. This is an uncommitted
integration handoff, not milestone, task, sprint, release or live acceptance.
Only `/tmp/nexora-wave3/m8-integration` was edited as a worktree. Source M8 and
other worktrees remained read-only. No SSH, cluster, shared toolbox, dev-sync,
dev-exec, deployment, trust regeneration, commit or push was used.

## Integration and review

Resolved all four conflict files semantically: `.procoder/notes/plan-review.md`,
`mgmt/internal/api/gen.go`, `mgmt/internal/control/hub.go`, and
`mgmt/internal/stats/resolution.go`. Kept main TLS/session registration fields,
literal LISTEN subscriptions and transactional stats querier; added the ODoH
channel/key delivery and ZONEMD status columns. Main M10/AI/failover code and
rollout guards remain. No new GUI components were implemented. Existing queued
outbound messages are not retroactively fenced; this does not establish stronger
ownership guarantees than the baseline.

Generated contracts from the combined source, rather than choosing a conflicted
blob. The installed oapi-codegen v2.6.0 fails on AI `type: [string, "null"]`.
Built the already-cached v2.8.0 source offline and generated successfully without
rewriting nullable schemas. A regression checks embedded nullable AI schemas,
explicit-null JSON behavior, and M8 fields. Existing permission and proto
round-trip/no-seeds-in-snapshot tests pass. TypeScript was regenerated using the
existing read-only source tree's openapi-typescript 7.13.0 executable, then
formatted locally. Generated Go records `(devel)` because the cached source was
built directly. The first generator invocation resolved the configured relative
output outside the worktree in scratch; that scratch output was removed and the
command rerun from `mgmt/api`. No external worktree file was changed.

Adopted the engine-only subset of the preserved dirty patch separately from the
committed merge, plus the source's `engine/tests/mdns_pipeline.rs`. Reviewed
routing order, ACL/RPZ path, no-answer non-caching, hot-path fixtures, reflector
sync, and metrics. Fixed gateway cache identity to include configured interfaces,
resolved targets and timeout, not merely enabled state; added an actual cache
invalidation regression to `engine/tests/snapshot_apply.rs`. Added reflector
shutdown on `MdnsState` drop. Rust behavior remains unverified on Linux.

Added `e2e/mdns_test.go::TestMdnsOffForwardsLocalNames`: real standalone engine,
absent and explicitly disabled config, UDP/TCP answers and fixture query counts.
It is not a multicast gateway/reflector proof. Added operational sections for all
four protocols in `docs/operations.md`. Restored the required M7 signed-zone debt
marker wording while retaining the added ZONEMD limitation; its existing test
failed before the fix and passed afterward. No test assertion was weakened.

## Migration compatibility

Preserved `01300_failover_groups.sql` and `01301_engine_connection_session.sql`.
Integrated M8 uses `01302_zonemd.sql`, `01303_catalog_zones.sql`, `01304_odoh.sql`,
`01305_engine_group_mdns.sql`; plan/spec references were updated. Source history
shows `f08c686` introduced the conflicting original M8 01300–01303 range. No live
history was inspected and no claim is made that renaming already-applied M8
migrations is safe.

`mgmt/internal/store/m8_history.go` checks the actual schema markers against the
latest goose record for each overlapping version under the existing migration
lock, before goose Up. Inconsistent or legacy state is refused with an explicit
operator-history-inspection error, without rewriting history. This is a refusal
guard, not a legacy migration transition or general schema-integrity checker.
The parent must inspect live history and design any needed transition.

`m8_migration_test.go` now covers fresh installation, upgrade from 1301 preserving
engine/session, installation, config, failover/reservation and resolver rows, and
all four actual legacy M8 prefixes reconstructed with original filenames. Legacy
refusal must leave rows/history unchanged. Existing pre-M8 behavioral-default
coverage remains. `stats/resolution_test.go` adds a caller-transaction rollback
regression for DNSSEC and ZONEMD writes. All database execution remains blocked
locally by missing initdb; these tests have not passed here.

## Commands and evidence

Logs below are in `/tmp/nexora-wave3/`. Go commands used
`GOPROXY=off GOCACHE=/tmp/nexora-wave3/m8-go-cache`.

- Contract generation:
  `go build -mod=readonly -o /tmp/nexora-wave3/oapi-codegen-v2.8 ./cmd/oapi-codegen`
  from `/Users/pascal/go/pkg/mod/github.com/oapi-codegen/oapi-codegen/v2@v2.8.0`;
  then from `mgmt/api`, `/tmp/nexora-wave3/oapi-codegen-v2.8 -config oapi-codegen.yaml openapi.yaml`.
  `go run ...@v2.8.0` first failed offline module deprecation lookup; direct cached
  source build succeeded. Installed v2.6 generation failed on AiAgentState null.
- `protoc -I proto --go_out=gen/go --go_opt=paths=source_relative --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative proto/nexora/control/v1/control.proto` passed.
  Local tools: protoc 6.33.4, protoc-gen-go-grpc 1.6.1 (version header differs
  from baseline). Rust prost generation is part of the Cargo build.
- `/Users/pascal/Development/nexora/web/node_modules/.bin/openapi-typescript mgmt/api/openapi.yaml -o web/src/api/schema.d.ts` passed. `procoder format`
  supplied the reviewed TypeScript output. `make proto` still needs a compatible
  oapi-codegen on PATH and its pnpm install step; it was not falsely reported run.
- `go test ./mgmt/... ./e2e/... -run '^$'`: PASS, compile only,
  `m8-final-compile.log`. Earlier attempts failed on absent embedded web assets,
  then the unresolved generated blob; fixed with `make webui-placeholder` and
  correct generation. The placeholder is not a GUI build.
- `go test ./mgmt/internal/zonemd ./mgmt/internal/catzone ./mgmt/internal/api ./e2e/harness -run '^(TestDigestMatchesRFC8976AppendixA|TestVerifyRules|TestCatalogBuildIsStable|TestCatalogParseBrokenRules|TestODoHConfigParsingAndKeyID|TestPermissionsCoverEveryOperation)$' -count=1 -v`:
  PASS, `m8-unit.log` (all six expected tests appear).
- `go test ./mgmt/internal/api ./mgmt/internal/control -run '^(TestMergedContractPreservesNullableAIAndM8|TestPermissionsCoverEveryOperation|TestContractM8FieldsRoundTrip|TestSnapshotCannotReachOdohKeys)$' -count=1 -v`:
  PASS, `m8-contract.log` (all four expected tests appear).
- `go test ./mgmt/internal/store -run '^TestM8' -count=1 -v`: FAIL before DB
  startup, initdb absent; `m8-migrations.log`.
- `go test ./mgmt/internal/stats -run '^TestRecordM3ZonemdUsesCallerTransaction$' -count=1 -v`:
  FAIL before DB startup, initdb absent; `m8-stats-transaction.log`.
- `go test ./e2e -run '^TestMdnsOffForwardsLocalNames$' -count=1 -v`: FAIL before
  engine startup, nexora-fixture absent; `m8-mdns-product.log`.
- `cargo test --locked --offline -p nexora-engine --test mdns_pipeline --test snapshot_apply --test hot_path_alloc`:
  FAIL compiling Linux-only libc mmsghdr/recvmmsg/sendmmsg on macOS; `m8-rust.log`.
  No engine test pass is claimed. `cargo fmt --all` passed.
- `procoder test`: FAIL, reports 211 Go failures and Rust compilation failure;
  `m8-procoder-test.log`. This predates the final debt-marker wording fix and
  cannot be relabeled green by focused passes. No broad rerun masked failures.
- `go test ./deploy/deploytest -run '^TestM7DebtMarkersResolved$' -count=1 -v`:
  initially FAIL, then PASS after restoring required wording; `m8-debt-fixed.log`.
- `procoder review`: unavailable in installed procoder 1.0.2 (usage/exit 2),
  `m8-review.log`. Manual diff review/adversarial/polish performed as above.
- Initial `procoder check` flagged unformatted TS; corrected. Final gate:
  `GIT_DIR=/tmp/nexora-wave3/private-m8-git GIT_WORK_TREE=/tmp/nexora-wave3/m8-integration GOCACHE=/tmp/nexora-wave3/m8-go-cache GOPROXY=off procoder check`:
  **FAIL**, `m8-check-handoff.log`: 132 clean, 0 unformatted, 0 unchecked,
  16 out of scope, 1387 hygiene findings, 3 blocking. All three blockers are
  existing Mermaid diagrams in `docs/operations.md` (lines 42, 840, 1352): the
  checker cannot launch its browser. Diagrams were preserved, not removed to
  bypass the check. ESLint also reports unreadable output as an advisory.
  Intermediate gate output is not final integrated acceptance evidence.
- Final `go test ./mgmt/... ./e2e/... -run '^$'`: PASS, `m8-handoff-compile.log`.
  Final private-index `git diff --cached --check`: PASS; `git ls-files -u` empty.
  Original main index remains unchanged and unresolved, by design.

## Parent next steps and boundaries

The original worktree index is intentionally untouched and will still show its
original unmerged entries. Resolutions are staged only in the private copied
metadata at `/tmp/nexora-wave3/private-m8-git`, with
`GIT_WORK_TREE=/tmp/nexora-wave3/m8-integration`. Do not copy this private index or
refs into main metadata. Parent can inspect the files and stage the resolved
merge in its authorized integration workflow. No merge abort/reset occurred.
The exact integrated path manifest is `wave3-m8-files.txt` beside this note.

Run supported Linux full management/control/stats/failover suites, particularly
new migrations and transaction rollback tests, then full Cargo all-targets,
mDNS pipeline/snapshot/hot-path and actual multicast namespace proofs. Build
real product binaries and run the existing ZONEMD/RPZ product tests and new
mDNS-off test. T24 ODoH target/proxy/key-rotation product acceptance, T25 BIND
catalog producer/consumer acceptance, and T26 multicast gateway/reflector
acceptance remain absent. T32 help-topic integration/document checks and T34
smoke/script wiring remain. GUI work belongs to the other agent. Full combined
GUI/product/release and any live history/deployment evidence belong to parent.
Preserve retained CAS lock, exact client tuples, serial paired rollout,
OnDelete and wait=legacy. No split-DNS implementation or CNI migration.
