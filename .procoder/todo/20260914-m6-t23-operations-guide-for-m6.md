# M6 T23: Operations guide for M6

Status: done (not committed; the lead commits)
Created: 2026-09-14
Completed: 2026-09-15

## Description

Implements Task 23 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Run the baseline grep and expect FAIL: `0`.
- [x] Edit `docs/operations.md`: the `NEXORA_REPOSITORY_URL` (and
      `NEXORA_TRUSTED_PROXY_CIDRS`) environment rows, the Forwarding & recursion
      wording in `## First-run setup and access`, the new `## Access control`,
      the `parallel`/`parallel_max` upstream bullet under `## Performance
    tuning`, the new `## Engine logs and metrics`, the dashboard ranges,
      rollup retention and health alert table under `## Monitoring and alerts`,
      the query log search, reason fields, OTLP attributes and wildcard `debt:`
      note under `### Query logs, traces and OTLP`, the new `## Account and
    version`, and the two new `## Known limitations` entries.
- [x] Run the grep again and expect a count of 5 or more (got 8). Run
      `scripts/pc-format.sh docs/operations.md` and the two doc tests.
- [ ] Report the paths. Commit message: `docs: operations guide for M6` (the lead commits).

## Evidence

Baseline (red):

```
$ grep -c "authoritative_allow_cidrs\|parallel_max\|/api/v1/engines/{id}/logs\|NEXORA_REPOSITORY_URL\|Forwarding & recursion" docs/operations.md
0
```

After the edits:

```
$ grep -c "authoritative_allow_cidrs\|parallel_max\|/api/v1/engines/{id}/logs\|NEXORA_REPOSITORY_URL\|Forwarding & recursion" docs/operations.md
8
$ scripts/pc-format.sh docs/operations.md          # clean, no output
$ scripts/dev-exec.sh 'go test ./deploy/deploytest -run "TestOperationsDoc" -count=1 -v'
=== RUN   TestOperationsDoc
--- PASS: TestOperationsDoc (0.00s)
ok  	github.com/piwi3910/nexora/deploy/deploytest	0.009s
$ scripts/dev-exec.sh 'go test ./deploy/deploytest -run "TestOperationsDoc|TestHelpTopicsReferenceOperationsDoc" -count=1'
--- FAIL: TestHelpTopicsReferenceOperationsDoc (0.00s)
    help_test.go:40: ai.md names missing docs/operations.md heading "AI"
```

`TestHelpTopicsReferenceOperationsDoc` fails only on M11's
`web/src/help/topics/ai.md`, which names a `## AI` heading that M11 Task 31 adds
to `docs/operations.md`. No M6 help topic names a missing heading, so that
section was deliberately left to M11 and not written here.

Every statement in the new sections was checked against the code:
`mgmt/internal/config/config.go` (`NEXORA_REPOSITORY_URL`,
`NEXORA_TRUSTED_PROXY_CIDRS`), `docs/architecture.md` `### ACL` and
`mgmt/internal/api/handlers_dns.go` (the two ACLs, revisions, seeds),
`mgmt/internal/zone/service.go` (`allow_query_cidrs`, `update_allow_cidrs`),
`engine/src/upstream/mod.rs` (`PARALLEL_LIMIT = 8`, the race),
`engine/src/telemetry/logbuf.rs` (2,000 lines, 512 octets, 100/s with burst 200,
redaction), `mgmt/internal/api/engine_logs.go` and `engine_metrics.go` (409/504/501,
windows), `mgmt/internal/auth/permissions.go` (operator for logs, viewer for
metrics and version), `mgmt/internal/stats/*` (ranges, 24 h and 8 d retention,
alert kinds and severities), `mgmt/api/openapi.yaml` (query log parameters and
record fields), `engine/src/telemetry/otlp.rs` (the `nexora.*` attributes),
`mgmt/internal/querylog/opensearch.go` (the wildcard `debt:` note),
`mgmt/internal/auth/account.go` (10 failures per username and client address in
15 minutes) and `deploy/docker/mgmt.Dockerfile` with `scripts/build-image.sh`
(`VERSION`, `COMMIT`, `BUILD_DATE`).
