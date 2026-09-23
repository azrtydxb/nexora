# M8 T5: ZONEMD digest and verification in the engine

Status: open
Created: 2026-09-15

## Description

Implements Task 5 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Replace the skeleton of `engine/src/zonemd.rs` with its doc comment plus this test module:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib zonemd::tests'` and expect
- [x] Implement:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib zonemd::tests && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
- [ ] Report the paths. The lead commits `M8 T5: ZONEMD digest and verification (engine)`.

## Evidence

Commit pending: the lead commits `M8 T5` (Status stays open until then).

- Environment: the shared synced tree `/work/nexora` in toolbox-m7 did not compile because of other
  wave-1 tasks' in-progress test modules (`odoh_rs`, `Gateway`, `Keyring`, `lookup` undefined), so the
  plan commands ran in a private copy `/work/m8-t5` (`git archive HEAD` plus this task's
  `engine/src/zonemd.rs`) with `CARGO_TARGET_DIR=/work/target-m8-t5`.
- Red: `cargo test --locked -p nexora-engine --lib zonemd::tests` failed to compile with
  `error[E0425]: cannot find function \`digest\` in this scope`(plus`verify`, `Verdict`,
`VerifyMode`, `TYPE_ZONEMD`), and `no method named \`name\`/\`data\`/\`ttl\``, fixed in the test by
  using 0.26.3's public fields (plan text updated).
- Green: `cargo test --locked -p nexora-engine --lib zonemd::tests` gave
  `test result: ok. 3 passed; 0 failed; 0 ignored; 0 measured; 258 filtered out`
  (`digest_matches_rfc8976_appendix_a` checked 4 digests, `verify_rules`,
  `opaque_rdata_names_are_lowercased`).
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings`: `Finished`, no warnings.
  `cargo fmt --check` on the file: clean.
- Mutation check (each reverted): no duplicate removal -> `digest_matches_rfc8976_appendix_a` fails;
  case-sensitive owner sort key -> `digest_matches_rfc8976_appendix_a` fails; no opaque RDATA name
  lowercasing -> `opaque_rdata_names_are_lowercased` fails.
