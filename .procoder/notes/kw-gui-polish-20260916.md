# GUI clarity fixes and kw verification (2026-09-16)

## Changes

- Query log: constrain the page's flex sizing and table columns; keep every column reachable in a labelled, keyboard-focusable horizontal scroll region. Long values truncate in rows and remain available through titles and expanded details.
- Query-log AI answers: safe Markdown in labelled summary/interpretation sections; no raw HTML or remote images; suggested follow-ups wrap and still submit their original prompt.
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

Pending commit/build/rolling deployment and post-deploy strict acceptance. Do not infer that the live release contains these fixes from the pre-deploy results.

Split-horizon DNS remains planning only. No Pi-hole parity feature or backlog work was added, and no milestone is closed by this verification.
