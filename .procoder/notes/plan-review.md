# Plan review log

## M3 (2026-09-13)
- FIX BEFORE M3 BUILD: plan stores RPZ TSIG secret plaintext in `rpz_zones.tsig_secret`. Violates architecture rule "secrets never plaintext in DB" and S-23. Resolution: pull the M4 KEK envelope-encryption helper (`mgmt/internal/secrets`, AES-256-GCM, refuses without KEK) forward into M3 and store the RPZ TSIG secret through it.
- Reconcile M1 names M3 assumed: `Upstreams::query`, `BlobStore::get`, harness `New`/`StartMgmt`/`StartEngine`/`Metric`/`WaitApplied`, Playwright `fixtures.ts`.

## M2 (2026-09-13)
- Proto field ranges: M2 300-399, M3 100-199, M4 200-299. M3 and M5 both claimed `Stats` field 100 -> reassign M5 Stats fields to 500+ (and ConfigSnapshot fields to 500+) before M5 build.
- M2 marks M1 names "(M1, later task)"; reconcile against final M1 code before M2 build.

## M1 (2026-09-13)
- Task 22: images.yml must push to Nexus (192.168.10.131:5000 via NEXUS_USER/NEXUS_PASSWORD, runners arc-azrtydxb-publish), not ghcr.io. Repo has no GitHub remote yet; kw deploys use scripts/build-image.sh images.
- Accepted: join tokens reusable until expiry (needed for autoscaled engines; M5 adds group binding).
- Accepted for M1 only: engine state emptyDir on kw (re-enrolls on restart); M5 moves to hostPath.

## Open follow-ups found during M1 build
- e2e harness free-port picking can race (DNS fixture once exited at startup during Task 11). Harden before M1 closes: bind listeners on :0 inside the child and report the bound port back (e.g. via a ready line on stdout) instead of pre-picking ports.
- PR perf gate flaked (7.53% "regression" base==head) on the loaded dev pod. Before M1 closes: raise rounds and use median-of-rounds with interleaving, and run PR tier on a dedicated runner (`arc-azrtydxb-amd64`, 14 CPU, one warm) rather than shared arm64; re-measure base==head noise and set threshold above measured noise floor with evidence.
- fuzz.yml uses `ubuntu-24.04`; must use `arc-azrtydxb` + `container: nexora-dev` (repo will live in azrtydxb org).
- `nexora-engine --version` missing (clap `#[command(version)]`).
- perfgate pre-picks free ports (same race as harness).
- First bench datapoint (arm64 dev pod, loaded, 2 workers, not reference box): 144k-195k QPS cache-hit, p99 ~1.6 ms.
