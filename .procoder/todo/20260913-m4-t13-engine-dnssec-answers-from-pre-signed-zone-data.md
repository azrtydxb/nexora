# M4 Task 13: Engine DNSSEC answers from pre-signed zone data

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 13 ("Engine DNSSEC answers from pre-signed zone data") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 13 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib authoritative::dnssec_tests'` before the implementation — `error[E0583]: file not found for module dnssec` / `nsec3`.
- Green: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --all-targets'` — lib `188 passed; 0 failed; 1 ignored`, every integration target ok (authoritative_pipeline 2 passed incl. `signed_answers_validate_with_delv`; hot_path_alloc 2 passed incl. DO=1 signed NSEC and NSEC3 answers).
- delv: `signed_answers_validate_with_delv` checks 12 query kinds per image (positive, CNAME, wildcard, DS, CDS, CDNSKEY, NXDOMAIN, NSEC3 closest-encloser NXDOMAIN, NODATA, ENT NODATA, wildcard NODATA, DS NODATA at an unsigned cut) for "; fully validated" / "; negative response, fully validated"; mutation (dropping the NSEC3 closest-encloser record) fails it with "broken trust chain".
- NOTIFY follow-up (plan-review): `notify_needing_a_key_is_sent_once_key_material_arrives` passes; with the old immediate lookup (`keyring.get`) it FAILS.
- `cargo fmt --all -- --check` and `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean; `gofmt -l` / `go vet ./testdata/nzf/gen-signed` clean; `go run ./testdata/nzf/gen-signed` twice gives identical SHA-1s.
- Not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 91cf9a4
