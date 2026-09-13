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

- RESOLVED: e2e harness free-port picking can race (DNS fixture once exited at startup during Task 11). Harden before M1 closes: bind listeners on :0 inside the child and report the bound port back (e.g. via a ready line on stdout) instead of pre-picking ports. -> nexora-fixture, nexora-engine and nexora-mgmt listen on port 0 and print `READY key=addr ...`; the harness parses it (mgmt's NEXORA_PUBLIC_URL is a harness-held port forwarded to the instance); PostgreSQL and otelcol-contrib retry with a fresh port on bind failure. `go test -count=5` of the Task 11 tests and harness tests passed.
- PARTLY RESOLVED: PR perf gate flaked (7.53% "regression" base==head) on the loaded dev pod. Before M1 closes: raise rounds and use median-of-rounds with interleaving, and run PR tier on a dedicated runner (`arc-azrtydxb-amd64`, 14 CPU, one warm) rather than shared arm64; re-measure base==head noise and set threshold above measured noise floor with evidence. -> Done: 9 interleaved rounds of 15 s, median per-round head/base ratio, PR tier on `arc-azrtydxb-amd64`, `workflow_dispatch` runs base==head. OPEN: noise not shown below 5%. Base==head on the dev pod (7-CPU quota, load 5-9 from concurrent agents): per-run spread up to 25%; independent 9-round gates read +4.21, +0.41, +8.82, -2.46% (median ratio) and +3.33, +1.85, +7.66, +5.76% (median of each side); the old 3-round method read -15.19% to +8.95%. Threshold stays at the spec's 5%; measure with workflow_dispatch A/A runs on the amd64 runner before relying on it.
- RESOLVED: fuzz.yml uses `ubuntu-24.04`; must use `arc-azrtydxb` + `container: nexora-dev` (repo will live in azrtydxb org).
- RESOLVED: `nexora-engine --version` missing (clap `#[command(version)]`).
- RESOLVED: perfgate pre-picks free ports (same race as harness). -> perfgate reads the fixture's and engine's READY lines.
- First bench datapoint (arm64 dev pod, loaded, 2 workers, not reference box): 144k-195k QPS cache-hit, p99 ~1.6 ms.

## After M1 kw deployment (2026-09-13)

- DNS LoadBalancer uses `externalTrafficPolicy: Cluster`, so engines see node/pod IPs, not client IPs. M2 per-client policy on kw needs real client IPs: switch engines to a DaemonSet (engine on every node) with `externalTrafficPolicy: Local`, or PROXY v2 where supported. Must be fixed in M2 Task 13 (kw) and verified by a smoke subtest that checks the query log shows the dev pod's IP.
- `/health` version is `dev`: build-image.sh must pass `--opt build-arg:VERSION=sha-<7>` and Dockerfiles must stamp it.
- Session cookies not `Secure` on kw (HTTP ingress). Serve GUI over HTTPS on kw (cert-manager cluster-ca already issues `nexora.kw.local`) and set `NEXORA_SECURE_COOKIES=true`; plain-HTTP LoadBalancer access then becomes a redirect.
- Engine restart re-enrolls as a new engine (emptyDir state) — M5 moves to hostPath (accepted until then).

## M3 reconciliation review (2026-09-13)

- REJECTED design change #2 ("global forwarding not DNSSEC-validated"): spec S-7 requires validation of recursive AND forwarded answers. Binding correction for M3 build: add global setting `dnssec_validate_forwarded` (settings table + snapshot field in the M3 100-199 range + API/GUI Settings toggle), default `true` for new installs and the kw deployment. Forward-mode validation fetches DS/DNSKEY through the forwarder up to the root trust anchor. e2e harness snapshots/settings for tests whose fixtures cannot serve a chain of trust set it `false` explicitly; `TestDNSSECValidation` must include a forward-mode subtest (validating forwarder over the private signed hierarchy) proving AD=1 / bogus->SERVFAIL in forward mode too.
- Accepted: secrets helper pulled forward from M4, TSIG secrets only over control stream; resolution/DNSSEC/RPZ snapshot-wide (not per group); RPZ file zones as blobs; kw state emptyDir until M5; NetworkPolicy dropped.

## After M3 kw deployment (2026-09-13)

- kw network redirects all outbound UDP/TCP 53 to another resolver (non-recursive queries to root IPs get recursive answers), so real-root recursion cannot work on kw; kw runs forward mode + forwarded DNSSEC validation. Report to user; recursion is verified in the private hierarchy e2e tests only.
- BUG: forwarded answers on the CD (pass-through) path keep the upstream's AA bit. A recursive/forwarding server must clear AA on answers it did not serve authoritatively. Fix in engine forward path (all modes), with a unit test and an e2e assertion; do it in the M4 engine track (authoritative stage owns AA semantics).
- toolbox pod lacks env vars declared in deploy/dev/dev-pod.yaml (NEXORA_E2E_OPENSEARCH_URL/JAEGER): re-apply dev-pod.yaml when no agent is running tests.
- M4 Task 16 must build nexora-mgmt with CGO_ENABLED=1 on a glibc base (PKCS#11 needs cgo; current Dockerfile is CGO_ENABLED=0) and run TestKeyStorageBackends-equivalent smoke with SoftHSM in the image or document HSM module mounting.
- RESOLVED (M4 Task 13, KeyRing::wait_for): Engine NOTIFY after restart: targets needing TSIG count as 'nokey' if the persisted snapshot loads before KeyMaterial arrives. Fix: queue NOTIFYs that lack a key and send when KeyMaterial containing it is applied (bounded wait, then count nokey). Assign to M4 engine Task 13 agent.
