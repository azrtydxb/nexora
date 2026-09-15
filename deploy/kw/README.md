# Nexora on kw

Namespace `nexora` on the kw cluster (context `kw`). Nexora itself is the Helm release `nexora` from
`deploy/helm/nexora` with `values-kw.yaml`; the supporting services (CNPG, OpenSearch, collector,
block list, BIND primary) are plain manifests here. Deploy or redeploy everything, then run the kw
acceptance tests, with:

```sh
scripts/kw-deploy.sh [--tag sha-<7>] [--skip-build]
scripts/kw-acceptance.sh
```

`kw-deploy.sh` builds and pushes `nexora-engine` and `nexora-mgmt` from a clean worktree of HEAD
(`scripts/build-image.sh`, which stamps the tag into both binaries), applies the manifests here,
creates the CA, key-encryption key, demo TSIG key and DNS TLS secrets, removes the label
`nexora.io/engine-group` of the former engine group `edge-b` from `worker-24` and `worker-25`, and
installs the release. A redeploy is one rolling upgrade; a first install (no join token secret or
engine DaemonSet yet) first installs the management plane (`engine.enabled=false`) and runs
`bootstrap.sh` against the API (which creates the join token secret). The engines roll, then
`bootstrap.sh` runs once more. Migrations run in the mgmt pods' `migrate` init container. It ends by
printing the test environment (`NEXORA_KW_DNS_ADDR`, `NEXORA_KW_DNS_ADDR_2`, `NEXORA_KW_ENGINE_ADDR`,
`NEXORA_KW_API_URL`, `NEXORA_KW_ENCRYPTED_ADDR`, `NEXORA_KW_DNS_TLS_NAME`, `NEXORA_KW_ENGINES`,
`NEXORA_KW_MGMT_LB_IP`).

Before a release that adds migrations, take a dump of the database into the dev pod and check it
restores (`pg_restore -l`, or a scratch PostgreSQL in the pod):

```sh
kubectl --context kw -n nexora get secret nexora-db-app -o jsonpath='{.data.uri}' | base64 -d |
  kubectl --context kw -n nexora-dev exec -i deploy/toolbox -c toolbox -- sh -c \
  'umask 077; mkdir -p /work/kw-backup; u=$(cat); pg_dump -Fc -f /work/kw-backup/nexora-$(date +%Y%m%d%H%M).dump "$u"'
```

`scripts/kw-acceptance.sh [run pattern]` copies the Nexora CA (verifies the DNS TLS certificate), the
cluster CA (verifies the ingress; the pod does not trust it) and the admin password into the dev pod
(`/work/kw-ca.crt`, `/work/kw-cluster-ca.crt`, `/work/kw-admin-password`), and runs `TestKwSmoke`,
`TestKwSmokeM4`, `TestKwFullProduct` and `TestKwFilterCategories` (default pattern
`TestKwSmoke|TestKwFullProduct|TestKwFilterCategories`) in the dev pod with the whole environment set.

`TestKwSmokeM4` creates a primary zone `smoke-<unix time>.nexora-smoke.test.`, checks the
authoritative answer (AA), an AXFR over the TCP LoadBalancer and online signing (RRSIG after
`PUT /zones/{id}/dnssec`), and deletes the zone.

`TestKwFullProduct` checks the M5 fleet: two engines, both in group `default`, each named after its
node with one record per node and all connected (`NEXORA_KW_ENGINES`, the scheduled engine pods);
`192.168.10.136` and `192.168.10.139` are each answered by exactly one engine, a different one (the
query log's `engine_id`); certificate rotation of the engine behind `NEXORA_KW_ENGINE_ADDR` while it
keeps answering `www.nexora-demo.kw.` every 50 ms without a failure; and the fleet metrics and alert
rules in Prometheus (`NEXORA_KW_PROMETHEUS_URL`, default `http://kps-prometheus.monitoring.svc:9090`).
It changes no configuration clients see and never pauses rollouts. kw no longer proves
engine-group-scoped configuration or canary rollouts live (they ran on the engine group `edge-b`,
removed 2026-09-14); the local e2e tests `TestEngineGroupScopedConfig`,
`TestFleetRolloutAndPartition`, `TestCanaryRolloutHaltsOnFailure` and `TestFleetAPI` do.

The smoke test creates and removes a policy group `kw-smoke-client`, rewrites under
`nexora-smoke.test` and the RPZ zone `rpz.kwsmoke.nexora.`. Its M3 subtests check DNSSEC validation
of forwarded answers (`dnssec-forwarded`: AD for `www.iana.org`/`cloudflare.com`, SERVFAIL for
`dnssec-failed.org`), `recursion` (skipped on kw, see "Known limits"), the root trust anchor on every
engine, RPZ (`example.net` NXDOMAIN from `rpz.kw.nexora.`) and the engine M3 metrics
(`NEXORA_KW_ENGINE_METRICS_URL`, default `http://nexora-engine-metrics.nexora.svc.cluster.local:9153/metrics`).
Manual checks: `delv @192.168.10.136 dnssec-failed.org` fails (bogus, SERVFAIL, EDE 9) and
`dig @192.168.10.136 +dnssec cloudflare.com` has the `ad` flag.

| File                | Objects                                                                                                                                                                                                            |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `namespace.yaml`    | Namespace `nexora`                                                                                                                                                                                                 |
| `opensearch.yaml`   | StatefulSet/Service `opensearch` (query-log backend)                                                                                                                                                               |
| `cnpg-cluster.yaml` | CNPG Cluster `nexora-db` (app secret `nexora-db-app`, key `uri`)                                                                                                                                                   |
| `otelcol.yaml`      | `nexora-otelcol`: logs to OpenSearch, traces to `jaeger.observability.svc:4317`                                                                                                                                    |
| `blocklist.yaml`    | `nexora-blocklist`: static block list for the smoke test                                                                                                                                                           |
| `values-kw.yaml`    | Helm release `nexora` (`deploy/helm/nexora`): `nexora-mgmt` (2 replicas), DaemonSets `nexora-engine-a` and `nexora-engine-b`, DNS LoadBalancers, gRPC LoadBalancer, TLS Ingress, ServiceMonitor and PrometheusRule |
| `bind-primary.yaml` | `nexora-bind`: BIND primary of the secondary zone `bind-demo.kw.` (ClusterIP 10.43.200.53:5353)                                                                                                                    |
| `bootstrap.sh`      | API bootstrap over HTTPS: admin, upstreams, block list, resolution, RPZ, demo zones, filter categories, join token, removal of engine group `edge-b` and of engines that no longer run                             |

## Operator e2e (namespace nexora-optest)

`scripts/kw-operator-e2e.sh [--tag TAG] [--skip-build] [--keep]` proves the Kubernetes operator
(`docs/operations.md`, "Install with the Kubernetes operator") on kw without touching production. It
runs from the laptop with kube context `kw` (`NEXORA_KW_CONTEXT`):

1. Builds and pushes `nexora-engine`, `nexora-mgmt` and `nexora-operator` at `sha-<7>` from the
   committed tree (`git archive HEAD`, so uncommitted changes are not in the images); `--skip-build`
   reuses `--tag`.
2. Creates the namespace `nexora-optest` with the label `nexora.io/e2e=operator`, or reuses it only when
   it carries that label.
3. Server-side applies `deploy/operator/crds/` and installs `deploy/helm/nexora-operator` as release
   `nexora-operator` in `nexora-optest` with `rbac.scope=namespace` (it watches only that namespace).
4. Copies the MinIO root credentials into the Secret `optest-s3` (never printed), starts the probe pod
   `probe` (dig, dnsperf, curl, psql) and creates the bucket `nexora-optest` on
   `minio.minio.svc.cluster.local:9000`.
5. Runs `TestKwOperator` (`go test -tags kwe2e ./test/kw` in `operator/`) with the installation and
   engine groups in `operator/test/kw/testdata/`: CNPG with two instances and backups to
   `s3://nexora-optest/<run timestamp>`, two management replicas, the `default` group as instances `a`
   and `b` and an `edge` group, every Service `ClusterIP`. Subtests: `guards`, `install`,
   `engine-groups`, `rolling-update`, `join-token-rotation`, `prune`, `cnpg-failover`,
   `cnpg-backup-restore`, `delete-retains-state`.
6. Unless `--keep`: deletes the installation and waits for the engine pods, deletes the CNPG clusters,
   removes the run's S3 prefix and the bucket, runs a cleanup Job per node that removes
   `/var/lib/nexora-optest`, deletes the engine groups while the operator can still run their
   finalizers, uninstalls the operator and deletes the namespace. A failed cleanup step exits non-zero.
   The two CRDs stay installed (cluster-scoped, unused by production).

Guards: the script refuses the namespace `nexora` and the nodes `master-12` and `master-13`, which carry
the production addresses; the `guards` subtest renders the installation first and fails the run (before
anything is applied) on a namespace other than `nexora-optest`, a context other than `kw`, a node list
without exactly three nodes or with `master-12`/`master-13`, any rendered LoadBalancer or NodePort
Service, any `loadBalancerIP`, or an object outside `nexora-optest`. Engines run only on `worker-21`,
`worker-22` and `worker-23` (`NEXORA_OPTEST_NODES`), with hostPath prefix `/var/lib/nexora-optest`.
Check production before and after a run: `kubectl --context kw -n nexora get nexorainstallations`
prints `No resources found`, and `kubectl --context kw -n nexora get svc nexora-dns nexora-dns-2 -o wide`
still shows `192.168.10.136` and `192.168.10.139`.

`--keep` leaves the namespace, workloads, database, S3 prefix and node directories for debugging
(`kubectl --context kw -n nexora-optest describe nxi nexora-optest`, `get nxeg`, the operator's log);
debug only in `nexora-optest`, never in `nexora`. Clean up afterwards by running the script's cleanup
sequence by hand or a run without `--keep`.

What the run measures:

- `rolling-update` changes `engine.workers` and requires zero lost queries from a fresh-socket probe
  (one `dig` per query, 5 queries/s per instance Service, at least 960 sent, no gap over 1 s, from before
  the change until every pod is ready). `dnsperf` runs too, but kw's Cilium socket load balancer destroys
  its connected UDP socket when the first old pod leaves the Service (`ECONNABORTED`), which is a client
  socket event rather than a lost query; it is asserted only when it completes.
- `cnpg-failover` deletes the primary pod and measures the time to a new primary, a healthy API and a
  group change on every engine.
- `cnpg-backup-restore` takes a `Backup` and restores it into a new one-instance cluster
  `nexora-db-restore`.
- `delete-retains-state` checks that deleting the `NexoraInstallation` removes the workloads and keeps
  the `Cluster` and the Secrets `nexora-optest-ca`, `-kek` and `-operator-token`.

Last recorded result (2026-09-15, images `dev-m9-8483b44`, details in `.procoder/notes/plan-review.md`):
every subtest passed. Ready after 1m54s; the roll replaced every engine pod in 29 s with 0 of 1174 and 0
of 1173 probe queries lost; join token rotation 31 s (missing Secret) and 1m2s (renewal), with at most
one or two active tokens per group; new primary 50 s after the deletion (43 s in the previous run), API
healthy 5 s later, the group change on every engine at 58 s; backup 32 s and restore verified 1m30s
later; workloads gone 18 s after deleting the installation.

## Addresses

- GUI/API: `https://nexora.kw.watteel.lab` only (ingress, certificate from the `cluster-ca` ClusterIssuer;
  plain `http://nexora.kw.watteel.lab` answers 308 to HTTPS). Session cookies are `Secure`. The
  LoadBalancer IP `192.168.10.135` has no HTTP port, so there is no cleartext login path.
- Engine gRPC: `nexora-mgmt-grpc.nexora.svc.cluster.local:9443` in the cluster,
  `192.168.10.135:9443` outside (both in the server certificate).
- DNS, engine group `default`, two engines (chart `instances`, one workload per engine pinned to a
  node): DaemonSet `nexora-engine-a` on `master-12` behind Service `nexora-dns` `192.168.10.136`, and
  DaemonSet `nexora-engine-b` on `master-13` behind Service `nexora-dns-2` `192.168.10.139`. Each
  Service selects only its engine (pod label `nexora.io/engine-instance`), so each address is
  answered by exactly one engine. Both serve 53 UDP/TCP, DoT 853/TCP, DoQ 853/UDP and DoH
  `https://192.168.10.136/dns-query` (443/TCP; `.139` likewise) — hand out both `192.168.10.136` and
  `192.168.10.139` as DNS servers to LAN clients. The engines share the group's ConfigMap and state
  directory name (`/var/lib/nexora/nexora-engine`), so an engine keeps the identity the former group
  DaemonSet had on that node. The serving certificate names `dns.nexora.kw.watteel.lab`, `192.168.10.136`
  and `192.168.10.139` (a certificate issued before the removal of `edge-b` also still names
  `192.168.10.137`, which is unused) and is issued by the Nexora CA (`nexora-ca`), e.g.
  `kdig @192.168.10.136 +tls-ca=/work/kw-ca.crt +tls-hostname=dns.nexora.kw.watteel.lab example.com`
  (`+https`, `+quic` likewise).
- The DNS Services use `externalTrafficPolicy: Local`, so engines (per-client policy, query log) see
  the real client address. With `Local`, an address only answers when kube-vip announces it from the
  node of its engine (`master-12` for `.136`, `master-13` for `.139`); check the leases below after
  kube-vip changes or node maintenance.
- kube-vip was upgraded on 2026-09-14 from v0.8.7 to v1.2.3 (DaemonSet `kube-system/kube-vip-ds`,
  image `ghcr.io/kube-vip/kube-vip:v1.2.3`; env `vip_subnet=32` added because v1 no longer reads
  `vip_cidr`; ClusterRole `system:kube-vip-role` gained `nodes: patch` and `pods: list`), because
  v0.8.7 moved a `Local`-policy VIP whenever its tracked endpoint went away, which broke
  zero-downtime engine updates.
- kube-vip (DaemonSet `kube-system/kube-vip-ds`, applied by hand, not by this repository) runs with
  per-service election (`svc_election=true`) and `vip_nodename` from `spec.nodeName`, so each
  LoadBalancer IP is announced by one control-plane node and `Local` services match the Kubernetes node
  names of their endpoints (without `vip_nodename` kube-vip uses the OS hostname, e.g. `km02`, never
  matches `master-12`, and leaves `Local` services pending). The kube-vip pool (`kube-system/kubevip`,
  `range-global`) is `192.168.10.120-137,139-154` (`.138` is another device; UniFi DHCP excludes
  139–154). Election is first come, first served: check that `.136` and `.139` sit on different nodes
  (`kubectl -n nexora get lease kubevip-nexora-dns kubevip-nexora-dns-2`); if not, delete the lease
  `kubevip-nexora-dns-2` until it moves.
- More client DNS addresses (up to four are planned): add an instance (`name`, `node`, `service` with
  a free pool address, `192.168.10.140`–`154`) to the `default` group in `values-kw.yaml`, or
  `extraServices` for an address over every engine, and run `scripts/kw-deploy.sh`; the DNS serving
  certificate's SANs are derived from the chart and reissued when an address is missing. kube-vip
  only runs on the three control-plane nodes, so with four addresses at least two share a node —
  spread them with the lease check above.
- Moving a home network over: point DHCP clients at `.136` and `.139`, but keep the gateway's own
  upstream DNS and the kw nodes' resolver (`192.168.10.1`) independent of Nexora, or the cluster ends up
  depending on its own DNS. Every `scripts/kw-deploy.sh` run currently interrupts DNS (issue #53).
- Engine metrics: `nexora-engine-metrics.nexora.svc.cluster.local:9153`; Prometheus (`kps`) scrapes
  mgmt and engines through the ServiceMonitor `monitoring/nexora`, and loads the PrometheusRule
  `monitoring/nexora`.

## Secrets

No key material or password is in git. The secrets are created imperatively, once:

- `nexora-ca` (`ca.crt`, `ca.key`), by `scripts/kw-deploy.sh`:

  ```sh
  go run ./mgmt/cmd/nexora-mgmt ca init --out "$tmp/ca"
  kubectl --context kw -n nexora create secret generic nexora-ca \
    --from-file=ca.crt="$tmp/ca/ca.crt" --from-file=ca.key="$tmp/ca/ca.key"
  ```

- `nexora-kek` (`kek`: 32 random bytes, base64), by `scripts/kw-deploy.sh` with
  `openssl rand -base64 32`, mounted into nexora-mgmt as `NEXORA_KEK_FILE=/etc/nexora/kek/kek`. It
  seals RPZ TSIG secrets, TSIG keys and KEK-backed DNSSEC private keys; losing it makes them
  unreadable, so back it up outside git.

- `nexora-demo-tsig` (`name` = `nexora-demo-xfr.`, `algorithm` = `hmac-sha256`, `secret`, and
  `named.key` for BIND), by `scripts/kw-deploy.sh` with `openssl rand -base64 32`; `bootstrap.sh`
  registers the same secret in Nexora. Read it with
  `kubectl --context kw -n nexora get secret nexora-demo-tsig -o jsonpath='{.data.secret}' | base64 -d`.

- `nexora-admin` (`username`, `password`), by `bootstrap.sh` with a random password, then used for
  first-run setup. Read it with:

  ```sh
  kubectl --context kw -n nexora get secret nexora-admin -o jsonpath='{.data.password}' | base64 -d
  ```

- `nexora-dns-tls` (type `kubernetes.io/tls`), the DoT/DoH/DoQ serving certificate, by
  `scripts/kw-deploy.sh` with `nexora-mgmt ca issue-dns` from `nexora-ca` (90 days). nexora-mgmt
  reloads it every 30 s and pushes it to the engines; to rotate, replace the Secret.

- `nexora-join-token` (`join-token`), by `bootstrap.sh` from `POST /api/v1/join-tokens`
  (engine group `default`, valid one year, reusable by every engine pod).
- `nexora-db-app` is generated by CNPG.

## Key storage and HSMs

kw stores key material under the key-encryption key (`nexora-kek`). The nexora-mgmt image is built
with cgo on `debian:trixie-slim` (glibc), so it can load a PKCS#11 module, but it ships none. To use
an HSM, mount the vendor's module (and whatever configuration or client files it needs) into the
pod, put the user PIN in a Secret, and set all three of `NEXORA_PKCS11_MODULE` (module path),
`NEXORA_PKCS11_TOKEN_LABEL` and `NEXORA_PKCS11_PIN_FILE`; new DNSSEC keys then default to the
`pkcs11` backend; keep `NEXORA_KEK_FILE` set so existing KEK envelopes stay readable. The module must be built for
glibc on the image's architecture. PKCS#11 with SoftHSM2 is exercised by `TestKeyStorageBackends`
in the dev pod, not on kw.

## Authoritative demo zones

`bootstrap.sh` keeps these (idempotent; records it finds are left alone):

- TSIG key `nexora-demo-xfr.` (hmac-sha256, the secret of `nexora-demo-tsig`).
- Primary zone `nexora-demo.kw.`: `ns1` A 192.168.10.136, `www` A/AAAA, `mail` A, apex MX and TXT;
  online DNSSEC signing (ECDSA P-256, NSEC3) with KEK-backed keys; AXFR/IXFR only with the key and
  from pod addresses (`10.42.0.0/16`); RFC 2136 updates only with the key.
- Secondary zone `bind-demo.kw.`, transferred with the key from `nexora-bind` (10.43.200.53:5353),
  served by the engines; transfers out as for `nexora-demo.kw.`.

Manual checks from the dev pod (`kubectl --context kw -n nexora-dev exec -it deploy/toolbox -c toolbox -- bash`,
with the TSIG secret copied to `/work/kw-demo-tsig` like the admin password):

```sh
dig @192.168.10.136 nexora-demo.kw. SOA +dnssec        # flags aa, RRSIG SOA present
# trust anchor from the API: GET /api/v1/zones/{id}/dnssec, the DNSKEY with flags 257, as
#   trust-anchors { nexora-demo.kw. static-key 257 3 13 "<key>"; };
delv @192.168.10.136 -a anchor.conf +root=nexora-demo.kw. nexora-demo.kw. SOA   # ; fully validated
dig @192.168.10.136 nexora-demo.kw. AXFR -y "hmac-sha256:nexora-demo-xfr.:$(cat /work/kw-demo-tsig)"
dig @192.168.10.136 nexora-demo.kw. AXFR                # Transfer failed (REFUSED/NOTAUTH)
printf 'server 192.168.10.136\nzone nexora-demo.kw.\nupdate add test.nexora-demo.kw. 60 A 192.0.2.99\nsend\n' |
  nsupdate -y "hmac-sha256:nexora-demo-xfr.:$(cat /work/kw-demo-tsig)"
dig @192.168.10.136 www.bind-demo.kw. A                 # aa, 192.0.2.53
```

## Filter categories

Categories are off after install. `bootstrap.sh` enables the categories in `NEXORA_KW_CATEGORIES`
(default `malware phishing ads-tracking crypto-mining`; empty for none; an unknown key fails the
bootstrap) with `acknowledge_license: true`, since kw is a non-commercial lab and OISD and URLhaus
are not free for commercial use. It refreshes the enabled sources of a category it newly enabled and
reports a failing source without failing. The engines have an explicit 1 GiB memory limit
(`values-kw.yaml`), so their filter index cap is 512 MiB; with every category (5.1M names, 112 MiB
index) a rebuild peaks at about 830 MiB (see `docs/operations.md`, Filter categories).

`TestKwFilterCategories` enables every catalog category with the real sources (acknowledging the
license notices), refreshes them, waits until all engines applied the version, checks that each
category blocks names taken from one of its sources on `192.168.10.136`, reads `filter_index` from
`GET /engines/{id}/stats` for every engine (at least 1M names, memory, build time and decision time
budgets) and writes that report to `/work/kw-filter-categories.json` in the dev pod. It restores the
previous enabled state of every category afterwards.

The collector (`otelcol.yaml`) renames `nexora.filter` to `nexora.filter.result` and writes
`nexora-querylog-v2-YYYY.MM.DD`; mgmt reads `nexora-querylog-*`, so older `nexora-querylog-YYYY.MM.DD`
indices stay readable (without categories) until they are deleted. `kw-deploy.sh` stamps the
collector config hash into its pod template, so a changed collector config is live before the engines
roll.

Manual checks (laptop):

```sh
dig @192.168.10.136 <name from an enabled source> A    # 0.0.0.0 (e.g. doubleclick.net, ads-tracking)
kdig @192.168.10.139 +https <name> A                   # 0.0.0.0 over DoH (any RFC 8484 client)
kubectl --context kw -n nexora top pod -l app.kubernetes.io/name=nexora-engine
kubectl --context kw -n nexora exec <engine pod> -c engine -- cat /sys/fs/cgroup/memory.peak
```

The GUI query log (Query log, filter `blocked`) shows the category of each blocked name.

## Resolution settings

`bootstrap.sh` keeps kw in recursive mode (iterative resolution from the root servers) with DNSSEC
validation and `validate_forwarded` on (upstreams 1.1.1.1 and 9.9.9.9 stay configured for forward
mode, which `TestKwSmoke/dnssec-forwarded` switches to temporarily), and uploads the RPZ file zone
`rpz.kw.nexora.` (`example.net CNAME .`) once.

Recursion needs direct outbound UDP/TCP 53 to the internet. The UniFi gateway (192.168.10.1) had a
DNS content filter that redirected all outbound port-53 traffic to its own resolver; it is disabled
(issue #1). `TestKwSmoke/recursion` fails with a clear message if that redirect comes back.

Node resolvers: the kw nodes' netplan (`/etc/netplan/netcfg.yaml`, backup `netcfg.yaml.bak-nexora`)
uses `192.168.10.1` as nameserver; they previously listed 8.8.8.8/1.1.1.1, which only worked while
the gateway intercepted DNS. The `watteel.lab.` primary zone (with `kw` a subdomain inside
it, `*.kw.watteel.lab` -> 192.168.10.120) is served by Nexora itself on 192.168.10.136 and .139,
and the UniFi gateway forwards `watteel.lab` back to those two addresses. CoreDNS forwards `.` to
the node resolver, so the chain pod -> CoreDNS -> 192.168.10.1 -> Nexora resolves every
`watteel.lab` name from inside the cluster; no CoreDNS conditional forward is needed. Note the
circularity this creates: Nexora serves the hostname of its own management GUI, so while both
engines are down, reach it by address (the ingress is 192.168.10.120).

## Known limits

- Each DNS address has one engine: while its node is down the address does not answer and clients
  use the other one (a rolling update starts the new engine beside the old one first, so an update
  does not take the address down).
- Engine state is hostPath `/var/lib/nexora/nexora-engine` (the group workload name, shared by its
  instances); a restarted pod keeps its engine id; removing
  that directory and the pod re-enrolls it as a new engine (delete the old engine record).
- kube-vip (ARP) holds `192.168.10.136` and `192.168.10.139` each on one control-plane node; with
  `externalTrafficPolicy: Local` external queries to a VIP are dropped when it is announced from a
  node other than its engine's.
- The engine group `edge-b` (`192.168.10.137`, nodes `worker-24` and `worker-25`) was removed on
  2026-09-14 at the user's request, and the default group went from one engine per node to two
  pinned engines; kw no longer runs engine-group scoping or canary rollouts live.
