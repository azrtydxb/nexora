# M4 Task 12: Management-plane online DNSSEC signer

Status: open
Created: 2026-09-13

## Description

Implement Task 12 ("Management-plane online DNSSEC signer") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 12 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [ ] `TestEnableSigns` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestEnableSignsAndEditsAreResigned` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSignNSEC3IncludesEmptyNonTerminalsWithZeroIterations` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSignNSECChainAndSignatures` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSignatureReuseAndRefresh` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

