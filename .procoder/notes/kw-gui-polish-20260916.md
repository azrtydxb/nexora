# GUI clarity fixes and kw verification (2026-09-16)

## Changes

- Query log: constrain the page's flex sizing and table columns; keep every column reachable in a labelled, keyboard-focusable horizontal scroll region. Long values truncate in rows and remain available through titles and expanded details.
- Query-log AI answers: safe Markdown in labelled summary/interpretation sections; no raw HTML or remote images; suggested follow-ups wrap and populate the original prompt for submission.
- AI health: `max(0, 10 - 3 * open critical - open warning)`; 10 is healthy, 0 is poor. Loading/errors are not numeric scores. API, generated clients, tests and design documentation agree. Rollout risk is unchanged.
- Upstreams: distinguish unmeasured engines from measured/up engines; exclude zero samples from RTT averages. Explain that RTT is passive and recursive mode may leave forwarders unused. No DNS resolution configuration changes.
- Align profile fields and keep AI Run now actions compact with stable icon/text sizing.

## Pre-deploy evidence

Linux toolbox, using the synced working tree:

- `go test ./... -count=1` in `mgmt`: PASS across the complete management module, including API/store tests.
- `go test -race ./internal/stats ./internal/ai/insight -count=1`: PASS.
- Web typecheck and production build: PASS. Existing >500 kB chunk warning remains.
- Targeted Playwright: 14/14 PASS, covering action buttons in light/dark, query-log containment and full details at 400/900/1600 px, safe Markdown/follow-ups, profile alignment, unmeasured/mixed/down upstreams, and score loading/error states.
- `TestGUICoverage`: PASS in 261.482s against rebuilt management/fixture binaries and the current release engine binary, with embedded current web assets and real fixture services. Initial `make e2e-build` could not overwrite an in-use dev engine executable (`Text file busy`); copying to a temporary filename and atomically renaming it allowed the fresh build without stopping unrelated dev processes.
- `procoder check`: 27 clean, 0 unformatted, 0 unchecked, 0 out of scope, 0 blocking. Informational findings remain, including the laptop test report (39 failing Go tests). This is **not** a green laptop/full-repository test claim; the Linux suites above are separate evidence.

Local logs: `/tmp/nexora-ui-{playwright,full-go,race,gui-suite}.log`. Screenshot reviewed: `/tmp/nexora-querylog-fixed.png`; Playwright artifacts are in the dev pod's `web/test-results/` (later runs may replace them).

## Deployment

Deployed commit `55dcf46` as Helm revision 25. The build/rollout probe recorded 1,972 successful UDP/TCP DNS samples across `.136` and `.139`, zero failures. Evidence: `/tmp/nexora-deploy-55dcf46/{deploy,dns}.log`.

**Post-deploy acceptance is NOT green.** Initial acceptance failed in 553.046s: master-13 had a null `connected_instance` despite fresh stats and applied snapshots. This recurs despite the earlier lifecycle mutex fix; do not claim that fix resolved the incident's root cause. Logs: `/tmp/nexora-acceptance-55dcf46.log`, `/tmp/nexora-ui-{mgmt,engine}-incident.log`.

A management-only rolling restart restored both connection records without restarting engines. During the recorded recovery window, 2,332 sampled UDP/TCP DNS queries passed with zero failures. The attached recovery run was interrupted before its final summary was captured. Its log already records a filtering-budget failure (302 ns blocked against a 300 ns limit), so it is not a pass. Evidence: `/tmp/nexora-recovery-55dcf46/{dns,recovery-acceptance}.log`.

One subsequent complete acceptance run failed in 456.936s: `TestKwSmokeM4/dnssec` timed out waiting for every engine to apply version 1221. AI, filtering, topology/certificate rotation and the other smoke test passed; filtering measured 267/222 ns blocked on master-12/master-13, without changing the 300 ns limit. Both engines remained connected, but master-13 sometimes lagged configuration updates. Logs: `/tmp/nexora-acceptance-55dcf46-confirm.{log,exit}`. No thresholds or assertions were relaxed and no further retry is being treated as a substitute for diagnosis.

Live ego-browser verification confirmed the `sha-55dcf46` footer, contained query-log scrolling with Duration reachable, equal Theme/Time zone label positions, and all eight Run now buttons at 32 px height with 14 px icons. The health card displayed 7/10 for one open critical insight with the new higher-is-healthier explanation. Browser task space 13 was closed after verification.

The explicit laptop `procoder test` report remains red: 230 Go failures and the macOS Rust `libc::mmsghdr` build error. Adversarial/edge-case review was invoked for the scoring, upstream aggregation and query-log changes; broader control-plane connection ownership and delayed snapshot application remain unresolved. This deployment is live, not fully signed off.

Split-horizon DNS remains planning only. No Pi-hole parity feature or backlog work was added, and no milestone is closed by this verification.
