# Wave 3 integration and acceptance evidence

Baseline: `f9c1653`. Work in progress, not milestone or sprint closure.
The user requested continued gap-driven implementation and verification, not a
checkpoint presented as completion. Isolated implementation worktrees and raw
logs are under `/tmp/nexora-wave3/`; parent alone owns shared Linux and live work.
The dirty M8 source tree and historical unfinished merge remain preserved.

## Parallel execution

- Full roadmap audit including M1–M5, categories, M6–M11 and FG/CP acceptance.
- M8 committed/dirty engine integration, migration compatibility and contracts.
- M8 GUI, API-backed browser coverage and feature help.
- Trusted Kubernetes eligibility observation reader.
- Disposable actual-engine attachment and source-policy verification fixture.
- Actual recursion-cache bytes/limit telemetry and M11 capacity sampling.
- Acceptance defects found during audit: strict management reconnect deadline and
  non-vacuous UUID-bound filter-budget fleet verification.

These assignments are not evidence of delivered or accepted implementation.
Supported baseline Linux verification is running in `/work/nexora-wave3` while
agents use isolated laptop worktrees. Do not sync over that active test tree.

## Live migration history (read-only)

`kubectl --context kw -n nexora exec nexora-db-1 -- psql -U postgres -d nexora -At
-c 'select version_id,is_applied from goose_db_version order by id'` recorded
applied versions through 1207, with no M8 1300–1303 history. Raw output:
`live-migrations.log`. This is evidence for this database only, not permission to
rename migrations in other already-applied M8 databases. Combined code must reject
ambiguous/inconsistent old M8 history before changing it. No migration ran live.

## Cross-host controls and preserved attempts

Hosts: existing kw workers 21/22 (`192.168.10.104` / `.105`). Tools staged only in
`/tmp/nexora-wave3-lab`; no host network, CNI, serving VIP, trust, engine state or
retained deployment lock changes. Namespace-only firewall rules implement the
SNAT control. The temporary TLS key was removed from both hosts and the laptop
once experiments ended. No `fx-` namespace remained on either host.

1. `416aee61` SNAT setup **RED**: worker22's staged tcpdump lacked libibverbs.
   Both supervisors stopped; namespaces were removed. Capture finalization errors
   remain in `cleanup.json`, not relabeled successful cleanup. Restaged the missing
   library from the prior private lab bundle; no system package installation.
2. `c5307f51` SNAT control **PASS**: both supervisors exited zero. All five
   transports from both clients in both isolated groups rejected the observed
   translated `.12`/`.13` source explicitly: 20 expected source-mismatch results.
   Timeouts/TLS failures were not counted as successful negative controls.
3. `cd03f78e` duplicate-advertiser control **RED**: three actual responder MACs
   were observed, including the frontend, but iputils arping with `-c 3` exits
   nonzero when replies outnumber requests. The harness incorrectly treated this
   expected duplicate-response behavior as a missing frontend. Source inspection
   of iputils 20240117 confirmed the sent/received exit-code condition. No repeat
   probe was issued within the failed run.
4. Fix: use the existing bounded four-second ARP observation window without a
   reply count; still require exit zero and frontend MAC, and require an additional
   responder only in duplicate mode. Normal modes continue rejecting extra MACs.
   Added positive/deadline and missing-frontend/missing-duplicate unit regressions;
   ten crosshost tests passed locally (`arp-unit.log`).
5. `5ec88036` corrected duplicate control **PASS**: both supervisors exited zero,
   frontend plus extra advertisers detected on both clients in both groups.
6. `eafe5413` post-fix DR **PASS**: both supervisors exited zero; optimized-Python
   `verify_pair.py` accepted 20 exact bidirectional tuples per group, 40 total,
   two clients and both backends across UDP/TCP DNS, DoT, HTTP/2 DoH and DoQ.

Raw evidence for every attempt is preserved in `evidence/`, plus separate
`crosshost-*-{run,left,right}.log` files and DR verifier output. SNAT and duplicate
results do not use the positive tuple verifier. These are still echo-probe
experiments, not real-engine policy evidence, deployable attachment or HA proof.
Paired preflights passed before and after controls, including all four identities,
current configuration, endpoints and direct/frontend UDP/TCP DNS. No member was
removed and no serving release was deployed.

## Actual-engine attachment and strengthened controls

**Review correction:** `57f032b7` used a verifier that checked capture tuples and
OTLP independently. Swapping backend records could pass; its attribution claim
below is historical, not accepted evidence. The corrected socket/backend/OTLP
join passed fresh run `fb374f92`; see `wave3-reviewed-evidence.md`.

Run `57f032b7` used the existing supported Linux baseline Nexora engine binary
(the manifest records its byte hash, not a combined-M8 release claim). Both
supervisors and the optimized-Python pair verifier exited zero. The verifier
accepted **80 exact bidirectional flows**, 40 per group, both clients and both
backends across five transports. Actual `/32`-selected rewrite policies and
persisted OTLP records matched original client IP, group and backend. Ports were
verified from paired captures, not invented query-log fields. Large TXT/RRSIG
stream replies verified cryptographically against disposable lab trust; UDP
truncated at the advertised 1232 bytes without fallback. Independent namespace
management attachments passed readiness and telemetry checks. Evidence:
`crosshost-engine-*.log`, `evidence/nexora-crosshost-57f032b7-*`, `verified-engine/`.

This proves standalone engine attachment, not enrolled control-stream continuity,
DNSSEC chain validation, general PMTU/fragmentation, reused sessions or frontend HA.
Managed attachment, production provisioning, fencing and release acceptance stay
open. The temporary engine TLS key was removed from both hosts and laptop;
all owned namespaces were removed and `preflight-after-engine.log` passed.

Agent integration strengthened controls to exact structured SNAT mismatches and
exact frontend plus both assigned backend MACs. New runs on that implementation:
`fb04bedb` SNAT and `1119d895` duplicate **PASS**, both supervisors zero; all raw
logs/evidence retained. Parent's deadline-only arping fix survived integration;
19 crosshost tests passed after adapting its positive fixture to the stricter
three-MAC contract. Control TLS keys were removed and no owned namespace remained.

## Integrated implementation and verification so far

M8 committed implementation plus reviewed dirty engine T21 was imported without
altering the original tree (both status and binary diff compared unchanged).
Main 1300/1301 versions are preserved; M8 uses 1302–1305 and rejects ambiguous
legacy histories. Added M8 GUI/browser seeds, ODoH/catalog product tests, actual
capacity byte telemetry, trusted read-only eligibility adapter and stricter HA/
fleet acceptance. No milestone/task status was closed. The reader still requires
an authoritative publisher and production wiring; it cannot grant ownership.

- Supported combined database/race tests **PASS**: store 80.720s, zone 70.581s,
  stats 43.009s, control 110.570s, catzone 21.326s, ODoH 20.346s, snapshot 38.845s,
  capacity 20.759s and failover 16.015s (`m8-integrated-linux.log`).
- Full combined Rust all-targets **PASS** with isolated `/work/target-wave3`
  (`m8-engine-isolated-linux.log`, exit 0), including mDNS and capacity telemetry.
  First attempt failed because the shared target retained pre-M8 generated prost
  output despite current M8 proto input; diagnosis saved separately. Do not use
  shared cross-worktree target output as fresh generation evidence.
- Combined TypeScript **PASS** after removing duplicate, identical M8 permission
  entries from the two independently integrated patches. Original diagnostics
  remain in `combined-web-types.log`; reviewed result in its companion log.
- OpenAPI regeneration now succeeds using pinned oapi-codegen v2.8.0. `Makefile`
  pins that version rather than an incompatible installed v2.6.0; nullable AI
  schemas were preserved, not rewritten to avoid generation.
- Baseline f9c1653 Rust and full Linux management/deploy race passed. Its full
  product attempt was **RED** (1165.844s): isolated working copy lacked installed
  Playwright dependencies. The final combined product directory explicitly uses
  installed dependencies and verifies Playwright availability; baseline failure
  is retained, not presented as a pass.
- First combined product pipeline stopped because a harness test ran before the
  new engine binary existed. Corrected build ordering; preserved original log.
  Real combined protocol/GUI/full product acceptance is still running or pending,
  not certified by unit tests or the standalone attachment experiment.

Subsequent review found and corrected fresh-secret enqueue fencing, tuple/OTLP
joining, GUI acceptance and platform provisioning defects. The integrated kernel
expiry and platform staging candidates now have isolated Linux execution evidence;
multicast gateway/reflector product tests passed. Full combined management/race and
product verification is running. See `wave3-reviewed-evidence.md` for exact evidence
and remaining boundaries. These are not frontend HA or release acceptance claims.
