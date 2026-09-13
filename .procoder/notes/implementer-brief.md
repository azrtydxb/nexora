# Implementer brief (read fully before touching code)

You implement plan tasks for Nexora in `/Users/pascal/Development/nexora`.

## Sources of truth, in order
1. The plan task(s) you were assigned in `.procoder/plans/nexora-v1-mN.md` —
   read your task(s) completely, and read earlier tasks' `Interfaces:` lines for
   names you consume.
2. `docs/architecture.md` — binding design.
3. `.procoder/specs/nexora-v1.md` — requirements.
4. `.procoder/notes/plan-review.md` — corrections that override the plans.

If the code already in the repo contradicts the plan (a name, a signature, a
file), the code wins for anything already committed: adapt your task to it and
edit the plan text of YOUR task to match what you built (keep the plan truthful).
If reality makes a plan step impossible or wrong, change the plan first, then
build, and say so in your final report.

## Environment
- Edit files on the laptop (this checkout). Never edit inside the pod.
- Build/test in the kw dev pod: `scripts/dev-exec.sh '<command>'` (syncs, then
  runs in /work/nexora on Linux arm64 with Rust 1.97, Go 1.27, Node 26, pnpm,
  Postgres 17 binaries, dnsperf, bind9, otelcol-contrib, Playwright chromium).
  Concurrent agents share the pod; do not delete others' files or kill
  processes you did not start. Use unique temp dirs and random ports.
- Generated sources that are committed are produced on the laptop
  (`buf`, `protoc`, `go`, `pnpm` are installed locally).
- Laptop has NO Docker. Images build with `scripts/build-image.sh`.
- Package proxies (optional, faster): Go
  `GOPROXY=https://192.168.10.131:8443/repository/golang/,direct`.

## Discipline
- TDD: write the plan's failing test, run it, see it fail for the stated
  reason, implement, see it pass. Never weaken a test to make it pass; never
  delete a test.
- Follow the procoder engineering principles: minimum code, no speculative
  abstractions, no placeholders, validation at trust boundaries, security never
  simplified away. Mark deliberate ceilings with `debt:` comments naming the
  ceiling and the revisit condition.
- No logging/allocation/DB on the engine query hot path.
- Run the relevant formatters/linters (`cargo fmt`, `cargo clippy -D warnings`,
  `gofmt`, `go vet`, `pnpm lint`) on what you changed.
- Commit only files you own for this task: `git add <explicit paths>` then
  `git commit -m "<M# Task N: summary>"`. Never `git add -A`, never amend or
  rewrite others' commits, never push. If `index.lock` exists, wait and retry.
- Update the matching todo file in `.procoder/todo/` (tick criteria you verified,
  fill Evidence with the exact commands and results).

## Final report (your last message, under 300 words)
- Tasks done, commit hashes.
- Test commands run and their pass/fail result (paste the summary line).
- Any deviation from the plan and why (plan text updated: yes/no).
- Anything left undone or failing — say it plainly; do not claim done for
  anything you did not verify.
