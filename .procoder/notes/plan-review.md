# Plan review log

## M9 operator e2e re-run (2026-09-15, `dev-m9-8483b44`)

Images built from HEAD `8483b44` by `scripts/kw-operator-e2e.sh --tag dev-m9-8483b44 --keep` (run A,
test unchanged, exit 1), then `--skip-build --keep` with the changed `rolling-update` (run B, exit 0);
each run was cleaned up with the script's cleanup sequence after the join tokens were counted.

| Subtest              | A    | B    | B duration | Notes (B)                                                                          |
| -------------------- | ---- | ---- | ---------- | ---------------------------------------------------------------------------------- |
| guards               | PASS | PASS | 2.0 s      |                                                                                    |
| install              | PASS | PASS | 120 s      | Ready after 1m54s                                                                  |
| engine-groups        | PASS | PASS | 1.5 s      |                                                                                    |
| rolling-update       | FAIL | PASS | 241 s      | roll 29 s; probe 0 lost of 1174 (a) and 1173 (b), gap <= 1 s; dnsperf ECONNABORTED |
| join-token-rotation  | PASS | PASS | 95 s       | 31 s and 1m2s                                                                      |
| prune                | PASS | PASS | 2.8 s      |                                                                                    |
| cnpg-failover        | PASS | PASS | 59 s       | new primary 50 s after the deletion (A: 43 s), API 200 5 s later, engines at 58 s  |
| cnpg-backup-restore  | PASS | PASS | 123 s      | backup 32 s, restore verified 1m30s later                                          |
| delete-retains-state | PASS | PASS | 18 s       |                                                                                    |

- RESOLVED: failover is within the 120 s target (43 s and 50 s, previously 3m6s).
- RESOLVED: no join token storm. `join_tokens` per group at the end: A `default` 1, `edge` 6; B
  `default` 1, `edge` 8 (install token, missing-Secret rotation, then one renewal a minute under the
  test's `ttl: 3m`/`renewBefore: 2m`); one or two active, zero `error` lines in the operator log.
- DECIDED: `rolling-update` proves zero loss with the fresh-socket probe (`digLoop`), not `dnsperf`.
  Evidence: kw's Cilium has socket LB enabled (`cilium-dbg status`: `Socket LB: Enabled`, coverage
  Full), which destroys connected UDP sockets whose ClusterIP backend is removed; both `dnsperf`s died
  with `ECONNABORTED` 11 s after the change (first old pod leaving), in all four kw runs so far,
  while the probe sending 5 queries/s through that same instant lost 0 every time. `dnsperf` measures
  its own socket's fate, not the engines'. The probe is not weaker: each query has one try and 2 s, a
  query in flight on a destroyed socket counts as lost, and the subtest now also requires >= 960 sent
  in 240 s, no gap over 1 s, and a send window from before the change to after every pod is ready.
  `dnsperf` still runs and is asserted (0 lost, NOERROR only) whenever it completes. The spec's
  "dnsperf reports `Queries lost: 0`" (S line 683) should read "the probe"; left to the lead.

Production untouched before and after both runs (`nexora` has no `NexoraInstallation`; `nexora-dns` and
`nexora-dns-2` still hold 192.168.10.136 and 192.168.10.139); `nexora-optest` is deleted (NotFound), the
S3 prefixes and node state directories were removed, and the two CRDs stay installed.

## M9 operator e2e (2026-09-15)

`scripts/kw-operator-e2e.sh --skip-build --tag dev-m9-749166b-dirty` in `nexora-optest` on kw (images
`dev-m9-749166b` plus the `internal/mgmtapi` Content-Type fix; run 1 used `dev-m9-749166b` unfixed).
Run 2 (756 s total, exit 1):

| Subtest              | Result | Duration | Notes                                                                                         |
| -------------------- | ------ | -------- | --------------------------------------------------------------------------------------------- |
| guards               | PASS   | 1.8 s    | no LoadBalancer/NodePort Service, no object outside the namespace                             |
| install              | PASS   | 117 s    | Ready after 1m53s; zone answered by both instance Services                                    |
| engine-groups        | PASS   | 1.3 s    | an engine of `edge` enrolled on worker-23                                                     |
| rolling-update       | FAIL   | 241 s    | every pod replaced in 27 s; dig loop 0 lost of 1174 per instance; dnsperf aborted (see below) |
| join-token-rotation  | PASS   | 94 s     | rotation on the missing Secret 33 s, renewal rotation 59 s, predecessors revoked              |
| prune                | PASS   | 2.7 s    | `edge` DaemonSet and Service removed                                                          |
| cnpg-failover        | FAIL   | 192 s    | new primary after 3m6s (> 120 s); API 200 1 s later; group change on every engine 5 s later   |
| cnpg-backup-restore  | PASS   | 86 s     | backup completed in 20 s, restore Ready and `edge` row found 1m6s later                       |
| delete-retains-state | PASS   | 19 s     | workloads gone in 18 s; `Cluster` and the three Secrets kept                                  |

Findings (details in the plan, Task 10 "Findings from the kw runs"):

- The operator's bodiless `DELETE`s reached the API without `Content-Type: application/json` and got
  `415`; every join token revoke and group delete failed. Fixed in `operator/internal/mgmtapi/client.go`
  with the rule enforced by `internal/mgmtapi/fake`.
- RESOLVED (Task 7 files, 2026-09-15): with that failure the controller created ~2500 join tokens in
  nine minutes (rotate writes the Secret, then fails revoking the predecessor, and rotates again on the
  Secret event). `rotate` now records the new token in status right after the Secret write, before
  anything else can fail; `syncJoinToken` revokes every superseded and previous token _before_ it
  creates one and stops with `JoinTokenReady=False`, reason `JoinTokenRevokeFailed`, when a revoke
  fails (requeue 30 s, Secret untouched, `Synced` still `True`); `maxOwnedTokens` (3) is a hard ceiling
  on the active tokens carrying the CR's marker, counted from the management plane, with reason
  `JoinTokenLimit`. `TestEngineGroupNeverMultipliesTokensWhenRevokeFails` holds the count at 2 over 140
  reconciles with every revoke answered 415.
- `dnsperf` dies with `ECONNABORTED` when an engine leaves a ClusterIP's backends (Cilium 1.19.4 socket
  LB destroys connected UDP sockets). A fresh-socket probe at the same rate lost nothing.
- RESOLVED (Task 4 files, 2026-09-15): a graceful primary deletion fails over in about 3 minutes
  because CNPG's `smartShutdownTimeout` (180 s) waits for the management plane's sessions. The chart
  value `database.cnpg.smartShutdownTimeout` (default 30) now renders `Cluster.spec`, and the
  management plane closes idle pooled sessions within 45 s (`MaxConnIdleTime` 30 s, `HealthCheckPeriod`
  15 s, `MaxConnLifetime` 30 min). The kw production render is byte-identical (kw uses
  `database.mode: external`); kw's own `deploy/kw/cnpg-cluster.yaml` is out of M9 scope and still
  carries CNPG's 180 s default. RESOLVED: the re-run above measured 43 s and 50 s.

Production was untouched: `kubectl --context kw -n nexora get nexorainstallations` printed
`No resources found` before and after, and `nexora-dns`/`nexora-dns-2` still hold 192.168.10.136 and
192.168.10.139. `nexora-optest` was deleted; the two cluster-scoped CRDs stay installed, as the spec says.

## M3 (2026-09-13)

- RESOLVED (M3: `rpz_zones.tsig_secret_envelope` holds an NXE1 envelope from `mgmt/internal/secrets`, CHECK-constrained to the `NXE1` magic; no plaintext column exists): FIX BEFORE M3 BUILD: plan stores RPZ TSIG secret plaintext in `rpz_zones.tsig_secret`. Violates architecture rule "secrets never plaintext in DB" and S-23. Resolution: pull the M4 KEK envelope-encryption helper (`mgmt/internal/secrets`, AES-256-GCM, refuses without KEK) forward into M3 and store the RPZ TSIG secret through it.
- RESOLVED (M3 built against the committed M1 names; full suite green in the 2026-09-14 sweep): Reconcile M1 names M3 assumed: `Upstreams::query`, `BlobStore::get`, harness `New`/`StartMgmt`/`StartEngine`/`Metric`/`WaitApplied`, Playwright `fixtures.ts`.

## M2 (2026-09-13)

- RESOLVED (M5 uses `EngineMessage.cert_request = 500`, `ServerMessage.cert_issued = 500`, `renew_certificate = 501`; M5 added no `Stats` or `ConfigSnapshot` fields, so no collision with M3's 100-102): Proto field ranges: M2 300-399, M3 100-199, M4 200-299. M3 and M5 both claimed `Stats` field 100 -> reassign M5 Stats fields to 500+ (and ConfigSnapshot fields to 500+) before M5 build.
- RESOLVED (M2 built against committed M1 code): M2 marks M1 names "(M1, later task)"; reconcile against final M1 code before M2 build.

## M1 (2026-09-13)

- RESOLVED (`.github/workflows/images.yml` pushes `192.168.10.131:5000/azrtydxb` with NEXUS credentials from `arc-azrtydxb-publish`; `deploy/deploytest/workflow_test.go`): Task 22: images.yml must push to Nexus (192.168.10.131:5000 via NEXUS_USER/NEXUS_PASSWORD, runners arc-azrtydxb-publish), not ghcr.io. Repo has no GitHub remote yet; kw deploys use scripts/build-image.sh images.
- Accepted: join tokens reusable until expiry (needed for autoscaled engines; M5 adds group binding).
- RESOLVED in code (Helm chart `engine.stateDir.type: hostPath` default, `deploy/kw/values-kw.yaml` `hostPathPrefix: /var/lib/nexora`); takes effect on kw with the M5 Task 14 Helm deployment: Accepted for M1 only: engine state emptyDir on kw (re-enrolls on restart); M5 moves to hostPath.

## Open follow-ups found during M1 build

- RESOLVED: e2e harness free-port picking can race (DNS fixture once exited at startup during Task 11). Harden before M1 closes: bind listeners on :0 inside the child and report the bound port back (e.g. via a ready line on stdout) instead of pre-picking ports. -> nexora-fixture, nexora-engine and nexora-mgmt listen on port 0 and print `READY key=addr ...`; the harness parses it (mgmt's NEXORA_PUBLIC_URL is a harness-held port forwarded to the instance); PostgreSQL and otelcol-contrib retry with a fresh port on bind failure. `go test -count=5` of the Task 11 tests and harness tests passed.
- ACCEPTED LIMITATION: base==head noise below 5% can only be shown by `workflow_dispatch` A/A runs on the dedicated `arc-azrtydxb-amd64` runner, which this checkout cannot reach (no GitHub remote yet) and there is no amd64 reference box; the shared dev pod cannot measure it (spread up to 25% under load). Stated in `docs/operations.md` Known limitations ("Perf gate noise"). Revisit: first A/A runs on the amd64 runner. History: PARTLY RESOLVED: PR perf gate flaked (7.53% "regression" base==head) on the loaded dev pod. Before M1 closes: raise rounds and use median-of-rounds with interleaving, and run PR tier on a dedicated runner (`arc-azrtydxb-amd64`, 14 CPU, one warm) rather than shared arm64; re-measure base==head noise and set threshold above measured noise floor with evidence. -> Done: 9 interleaved rounds of 15 s, median per-round head/base ratio, PR tier on `arc-azrtydxb-amd64`, `workflow_dispatch` runs base==head. OPEN: noise not shown below 5%. Base==head on the dev pod (7-CPU quota, load 5-9 from concurrent agents): per-run spread up to 25%; independent 9-round gates read +4.21, +0.41, +8.82, -2.46% (median ratio) and +3.33, +1.85, +7.66, +5.76% (median of each side); the old 3-round method read -15.19% to +8.95%. Threshold stays at the spec's 5%; measure with workflow_dispatch A/A runs on the amd64 runner before relying on it.
- RESOLVED: fuzz.yml uses `ubuntu-24.04`; must use `arc-azrtydxb` + `container: nexora-dev` (repo will live in azrtydxb org).
- RESOLVED: `nexora-engine --version` missing (clap `#[command(version)]`).
- RESOLVED: perfgate pre-picks free ports (same race as harness). -> perfgate reads the fixture's and engine's READY lines.
- First bench datapoint (arm64 dev pod, loaded, 2 workers, not reference box): 144k-195k QPS cache-hit, p99 ~1.6 ms.

## After M1 kw deployment (2026-09-13)

- RESOLVED (M2 Task 13: engines run as a DaemonSet with `externalTrafficPolicy: Local` on 192.168.10.136; `TestKwSmoke` checks real client addresses in the query log; the chart keeps `Local` for the kw `default` group): DNS LoadBalancer uses `externalTrafficPolicy: Cluster`, so engines see node/pod IPs, not client IPs. M2 per-client policy on kw needs real client IPs: switch engines to a DaemonSet (engine on every node) with `externalTrafficPolicy: Local`, or PROXY v2 where supported. Must be fixed in M2 Task 13 (kw) and verified by a smoke subtest that checks the query log shows the dev pod's IP.
- RESOLVED (`scripts/build-image.sh` passes `build-arg:VERSION`, both Dockerfiles stamp it; `TestKwSmoke` fails on an empty or `dev` version): `/health` version is `dev`: build-image.sh must pass `--opt build-arg:VERSION=sha-<7>` and Dockerfiles must stamp it.
- RESOLVED (kw ingress serves HTTPS from `cluster-ca`, `NEXORA_SECURE_COOKIES=true`/`mgmt.secureCookies: true`, plain HTTP answers 308; `TestKwSmoke/http-redirects-to-https`): Session cookies not `Secure` on kw (HTTP ingress). Serve GUI over HTTPS on kw (cert-manager cluster-ca already issues `nexora.kw.local`) and set `NEXORA_SECURE_COOKIES=true`; plain-HTTP LoadBalancer access then becomes a redirect.
- RESOLVED in code (chart `stateDir` hostPath, see the M1 item above; effective on kw with M5 Task 14): Engine restart re-enrolls as a new engine (emptyDir state) — M5 moves to hostPath (accepted until then).

## M3 reconciliation review (2026-09-13)

- RESOLVED (`ConfigSnapshot.dnssec_validate_forwarded = 105`, `/dnssec/settings` `validate_forwarded` default true, `TestDNSSECValidation/forward_mode_validates_answers_from_the_global_upstreams` proves AD=1 and bogus->SERVFAIL in forward mode): REJECTED design change #2 ("global forwarding not DNSSEC-validated"): spec S-7 requires validation of recursive AND forwarded answers. Binding correction for M3 build: add global setting `dnssec_validate_forwarded` (settings table + snapshot field in the M3 100-199 range + API/GUI Settings toggle), default `true` for new installs and the kw deployment. Forward-mode validation fetches DS/DNSKEY through the forwarder up to the root trust anchor. e2e harness snapshots/settings for tests whose fixtures cannot serve a chain of trust set it `false` explicitly; `TestDNSSECValidation` must include a forward-mode subtest (validating forwarder over the private signed hierarchy) proving AD=1 / bogus->SERVFAIL in forward mode too.
- Accepted: secrets helper pulled forward from M4, TSIG secrets only over control stream; resolution/DNSSEC/RPZ snapshot-wide (not per group); RPZ file zones as blobs; kw state emptyDir until M5; NetworkPolicy dropped.

## After M3 kw deployment (2026-09-13)

- RESOLVED 2026-09-14 (issue #1): the interception was the UniFi gateway's DNS content filter; the user disabled it, kw node netplan nameservers now 192.168.10.1 (local names), kw runs recursive mode and `TestKwSmoke/recursion` passes against the real root servers. Previously: ACCEPTED LIMITATION: cannot be fixed in code — the kw network itself intercepts outbound port 53; stated in `docs/operations.md` Known limitations and `deploy/kw/README.md` Known limits; recursion is verified against the private hierarchy (`TestRecursionRootHints`). kw network redirects all outbound UDP/TCP 53 to another resolver (non-recursive queries to root IPs get recursive answers), so real-root recursion cannot work on kw; kw runs forward mode + forwarded DNSSEC validation. Report to user; recursion is verified in the private hierarchy e2e tests only.
- RESOLVED (M4 Task 3: `cache::write_cached` clears AA on every relayed reply — forwarded, recursive, CD pass-through, stale, RPZ; unit test `cache::tests::upstream_aa_is_cleared_on_every_served_reply`; e2e assertion added in the 2026-09-14 sweep to `TestDNSSECValidation/CD_bit_returns_unvalidated_data_without_AD`, where the hierarchy's authoritative servers answer AA=1): BUG: forwarded answers on the CD (pass-through) path keep the upstream's AA bit. A recursive/forwarding server must clear AA on answers it did not serve authoritatively. Fix in engine forward path (all modes), with a unit test and an e2e assertion; do it in the M4 engine track (authoritative stage owns AA semantics).
- RESOLVED (verified 2026-09-14: the toolbox pod has `NEXORA_E2E_OPENSEARCH_URL` and `NEXORA_E2E_JAEGER_QUERY_URL`): toolbox pod lacks env vars declared in deploy/dev/dev-pod.yaml (NEXORA_E2E_OPENSEARCH_URL/JAEGER): re-apply dev-pod.yaml when no agent is running tests.
- RESOLVED (`deploy/docker/mgmt.Dockerfile` builds with `CGO_ENABLED=1` on Debian trixie glibc; `TestKeyStorageBackends` runs mgmt against SoftHSM2; module mounting documented in `docs/operations.md` Key storage and Known limitations, and `deploy/kw/README.md`): M4 Task 16 must build nexora-mgmt with CGO_ENABLED=1 on a glibc base (PKCS#11 needs cgo; current Dockerfile is CGO_ENABLED=0) and run TestKeyStorageBackends-equivalent smoke with SoftHSM in the image or document HSM module mounting.
- RESOLVED (M4 Task 13, KeyRing::wait_for): Engine NOTIFY after restart: targets needing TSIG count as 'nokey' if the persisted snapshot loads before KeyMaterial arrives. Fix: queue NOTIFYs that lack a key and send when KeyMaterial containing it is applied (bounded wait, then count nokey). Assign to M4 engine Task 13 agent.
- RESOLVED (M4 Task 14, AuthZone.primary_tsig_keys = 200): engine cannot bind a signed incoming NOTIFY to a specific primary's TSIG key because the zone config sent to engines lacks per-primary key names; NOTIFY is accepted by source IP (+ any valid key). Fix before M4 closes: add per-primary tsig key name to the zone config in the snapshot (M4 field range) and require the matching key when configured; engine test + e2e assertion that a NOTIFY signed with a different valid key is refused.

## M5 reconciliation review (2026-09-14)

- ACCEPTED LIMITATION (documented in `docs/operations.md` Known limitations and, as of the 2026-09-14 sweep, `deploy/kw/README.md` Known limits): kw `edge-b` engine group LB 192.168.10.137 uses externalTrafficPolicy Cluster (kube-vip may place the VIP on a node without an edge-b engine), so edge-b sees node IPs; per-client policy is verified on the `default` group (.136, Local). README and operations docs must state this.
- No action (false positive): The gate's "credential-looking string" in `mgmt/internal/api/gen.go` is oapi-codegen's embedded base64 swagger spec, not a secret.
- RESOLVED for the mgmt side (`NEXORA_ENGINE_CERT_TTL` and `NEXORA_ROLLOUT_TICK` in `mgmt/internal/config/config.go`, `ca init --if-missing` in `mgmt/cmd/nexora-mgmt/main.go`); deleting the kubectl-created objects before `helm install` stays a M5 Task 14 deployment step. M5 Task 14: delete kubectl-created nexora-mgmt/nexora-engine Deployment/DaemonSet/Services before helm install (selectors immutable, Helm won't adopt). Compose uses 'ca init --if-missing'; chart sets NEXORA_ENGINE_CERT_TTL and NEXORA_ROLLOUT_TICK — mgmt tasks must implement these.
- RESOLVED (migration `00501_engine_cert_renewed_at.sql`; `Server.renew` checks and sets `engines.cert_renewed_at` in the issuing transaction under `select ... for update` of the engine row, signing only when allowed; `TestCertificateIssuanceRateLimitIsPerEngine` proves a reconnected stream gets nothing within 10 s and two parallel streams get one certificate — red against the per-stream code): Certificate issuance rate limit ('once per engine per 10 s') is per stream; reconnects bypass it. Make it per engine (e.g. last_issued_at on engine_certificates checked under row lock) with a test before release.

## Pre-release hardening sweep (2026-09-14)

- RESOLVED (debt marker in `mgmt/internal/dnssec/lifecycle.go`, data loss): `SweepTokenOrphans` destroyed every token object labelled `nexora-dnssec`, so two installations sharing a PKCS#11 token would destroy each other's live DNSSEC keys after the 1 h grace. Migration `00502_installation.sql` adds a one-row `installation` id (`store.InstallationID`), `secrets.Config.Installation` is required with PKCS#11, and key objects are labelled `nexora-dnssec:<installation id>`; generation and sweeps use only that label. `TestSweepLeavesOtherInstallationsTokenKeys` (red before the change: the sweep destroyed both keys). A database restored into a second installation shares the id; `docs/operations.md` Key storage says to give it its own token.

## Filter categories plan review (2026-09-14)

- Accepted: URLhaus also behind a license acknowledgement (its terms may require a paid subscription for commercial use); OpenSearch query log moves to `nexora-querylog-v2` with a collector rename rule; packed 128-byte block index (~21 B/name) instead of the spec's 16-byte slot layout; ARM prefetch via inline asm on stable Rust; catalog mirror env var for e2e fixtures.
- Filter rebuild memory (2026-09-14): the index cap alone did not prevent OOM (rebuild peak 4.4x the index). Now enforced by `engine/src/filter/memory.rs` (`BuildMemory` checks cgroup memory.current - page cache + next step against memory.max minus margin before each step; snapshot rejected, previous index serves; test `filter_rebuild_over_the_memory_limit_is_rejected_before_building`). Peak reduced to 2.5x (target of 2x not met: 24-byte records per line coexist with list text during dedup; 16-byte records would be needed). The plan's `default_max_bytes(&Path)`/`CGROUP_MEMORY_MAX` text is superseded by `filter/memory.rs`.
- Paused #53 rolling-deploy engine changes (uncommitted) break TestGUICoverage spec 23 (blocked category query never appears in the query log) — fix before resuming.
- 2026-09-14, user request: the kw engine group `edge-b` (DaemonSet `nexora-engine-edge-b`, 192.168.10.137, nodes worker-24/25) is removed from `deploy/kw/values-kw.yaml`, the kw scripts, tests and docs; `deploy/kw/bootstrap.sh` deletes the group (its engines, join tokens, scoped configuration) and Secret `nexora-join-token-edge-b`, and `scripts/kw-deploy.sh` removes the node label. kw now runs two engines of the `default` group via the new chart `engine.groups[].instances` (`nexora-engine-a` on master-12 behind `nexora-dns` 192.168.10.136, `nexora-engine-b` on master-13 behind `nexora-dns-2` 192.168.10.139, each Service selecting only its engine; cpu request 500m). kw no longer proves engine-group scoping or canary rollout/rollback live; `TestEngineGroupScopedConfig`, `TestFleetRolloutAndPartition`, `TestCanaryRolloutHaltsOnFailure` and `TestFleetAPI` (local e2e) do. `TestKwFullProduct` keeps certificate rotation, now proving the rotated engine keeps answering DNS. The migration of the live kw release (group DaemonSet to instances) is done by hand by the lead.

- RESOLVED (M9 T7, 2026-09-15; token names carry `op/<cr uid>/` and each reconcile revokes older unrecorded marked tokens, `TestEngineGroupRevokesUnrecordedToken`): a join token created by the engine group controller whose status write then fails is not recorded and stays valid until its TTL. Fix before M9 closes if cheap: name tokens with the CR uid and revoke unrecorded ones on the next reconcile.
