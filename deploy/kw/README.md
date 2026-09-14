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
creates the CA, key-encryption key, demo TSIG key and DNS TLS secrets, labels `worker-24` and
`worker-25` with `nexora.io/engine-group=edge-b`, and installs the release in two phases: the
management plane (`engine.enabled=false`), `bootstrap.sh` against the API (which creates the engine
group `edge-b` and both join token secrets), then the engines of both groups and `bootstrap.sh` once
more. Migrations run in the mgmt pods' `migrate` init container. It ends by printing the test
environment (`NEXORA_KW_DNS_ADDR`, `NEXORA_KW_EDGE_B_DNS_ADDR`, `NEXORA_KW_EDGE_B_ENGINE_IPS`,
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
(`/work/kw-ca.crt`, `/work/kw-cluster-ca.crt`, `/work/kw-admin-password`), restarts the `edge-b`
engines (so `TestKwFullProduct` proves identities survive restarts), and runs `TestKwSmoke`,
`TestKwSmokeM4` and `TestKwFullProduct` (default pattern `TestKwSmoke|TestKwFullProduct`) in the dev
pod with the whole environment set.

`TestKwSmokeM4` creates a primary zone `smoke-<unix time>.nexora-smoke.test.`, checks the
authoritative answer (AA), an AXFR over the TCP LoadBalancer and online signing (RRSIG after
`PUT /zones/{id}/dnssec`), and deletes the zone.

`TestKwFullProduct` checks the M5 fleet: one engine per node named after it (`edge-b-` prefix in
group `edge-b`), 8 connected engines, 2 in `edge-b`; a rewrite and a zone scoped to `edge-b` are
served by the `edge-b` engines and `192.168.10.137` but not by `192.168.10.136`; a canary rollout on
`edge-b` (canary `edge-b-worker-24`), rollback to the previous version and resume; certificate
rotation of `edge-b-worker-25`; and the fleet metrics and alert rules in Prometheus
(`NEXORA_KW_PROMETHEUS_URL`, default `http://kps-prometheus.monitoring.svc:9090`). It leaves `edge-b`
on the `all_at_once` strategy.

The smoke test creates and removes a policy group `kw-smoke-client`, rewrites under
`nexora-smoke.test` and the RPZ zone `rpz.kwsmoke.nexora.`. Its M3 subtests check DNSSEC validation
of forwarded answers (`dnssec-forwarded`: AD for `www.iana.org`/`cloudflare.com`, SERVFAIL for
`dnssec-failed.org`), `recursion` (skipped on kw, see "Known limits"), the root trust anchor on every
engine, RPZ (`example.net` NXDOMAIN from `rpz.kw.nexora.`) and the engine M3 metrics
(`NEXORA_KW_ENGINE_METRICS_URL`, default `http://nexora-engine-metrics.nexora.svc.cluster.local:9153/metrics`).
Manual checks: `delv @192.168.10.136 dnssec-failed.org` fails (bogus, SERVFAIL, EDE 9) and
`dig @192.168.10.136 +dnssec cloudflare.com` has the `ad` flag.

| File                | Objects                                                                                                                                                                                                               |
| ------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `namespace.yaml`    | Namespace `nexora`                                                                                                                                                                                                    |
| `opensearch.yaml`   | StatefulSet/Service `opensearch` (query-log backend)                                                                                                                                                                  |
| `cnpg-cluster.yaml` | CNPG Cluster `nexora-db` (app secret `nexora-db-app`, key `uri`)                                                                                                                                                      |
| `otelcol.yaml`      | `nexora-otelcol`: logs to OpenSearch, traces to `jaeger.observability.svc:4317`                                                                                                                                       |
| `blocklist.yaml`    | `nexora-blocklist`: static block list for the smoke test                                                                                                                                                              |
| `values-kw.yaml`    | Helm release `nexora` (`deploy/helm/nexora`): `nexora-mgmt` (2 replicas), DaemonSets `nexora-engine` and `nexora-engine-edge-b`, DNS LoadBalancers, gRPC LoadBalancer, TLS Ingress, ServiceMonitor and PrometheusRule |
| `bind-primary.yaml` | `nexora-bind`: BIND primary of the secondary zone `bind-demo.kw.` (ClusterIP 10.43.200.53:5353)                                                                                                                       |
| `bootstrap.sh`      | API bootstrap over HTTPS: admin, upstreams, block list, resolution, RPZ, demo zones, engine group `edge-b`, join tokens, pruning of pre-M5 engines                                                                    |

## Addresses

- GUI/API: `https://nexora.kw.local` only (ingress, certificate from the `cluster-ca` ClusterIssuer;
  plain `http://nexora.kw.local` answers 308 to HTTPS). Session cookies are `Secure`. The
  LoadBalancer IP `192.168.10.135` has no HTTP port, so there is no cleartext login path.
- Engine gRPC: `nexora-mgmt-grpc.nexora.svc.cluster.local:9443` in the cluster,
  `192.168.10.135:9443` outside (both in the server certificate).
- DNS, engine group `default` (DaemonSet `nexora-engine`, every node without the label below), on
  `192.168.10.136`: 53 UDP/TCP, DoT 853/TCP, DoQ 853/UDP, DoH `https://192.168.10.136/dns-query`
  (443/TCP). The serving certificate names `dns.nexora.kw.local`, `192.168.10.136` and `192.168.10.137` and is issued
  by the Nexora CA (`nexora-ca`), e.g.
  `kdig @192.168.10.136 +tls-ca=/work/kw-ca.crt +tls-hostname=dns.nexora.kw.local example.com`
  (`+https`, `+quic` likewise).
- The `default` DNS Service uses `externalTrafficPolicy: Local` and its engines run on every node
  kube-vip may announce from, so engines (per-client policy, query log) see the real client address.
- DNS, engine group `edge-b` (DaemonSet `nexora-engine-edge-b`, nodes `worker-24` and `worker-25`
  labelled `nexora.io/engine-group=edge-b`, engine names `edge-b-<node>`), on `192.168.10.137` with the
  same ports and `externalTrafficPolicy: Cluster`: engines there see node addresses.
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
- `nexora-join-token-edge-b` (`join-token`), likewise for engine group `edge-b`.
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

## Resolution settings

`bootstrap.sh` keeps kw in recursive mode (iterative resolution from the root servers) with DNSSEC
validation and `validate_forwarded` on (upstreams 1.1.1.1 and 9.9.9.9 stay configured for forward
mode, which `TestKwSmoke/dnssec-forwarded` switches to temporarily), and uploads the RPZ file zone
`rpz.kw.nexora.` (`example.net CNAME .`) once.

Recursion needs direct outbound UDP/TCP 53 to the internet. The UniFi gateway (192.168.10.1) had a
DNS content filter that redirected all outbound port-53 traffic to its own resolver; it is disabled
(issue #1). `TestKwSmoke/recursion` fails with a clear message if that redirect comes back.

Node resolvers: the kw nodes' netplan (`/etc/netplan/netcfg.yaml`, backup `netcfg.yaml.bak-nexora`)
uses `192.168.10.1` as nameserver so CoreDNS resolves UniFi-local names such as `nexora.kw.local`;
they previously listed 8.8.8.8/1.1.1.1, which only worked while the gateway intercepted DNS.

## Known limits

- Inside the cluster, the DNS LoadBalancer IPs spread queries over all engines of the group (each with
  its own cache); `externalTrafficPolicy` only applies to clients outside the cluster.
- Engine state is hostPath `/var/lib/nexora/<workload>`; a restarted pod keeps its engine id; removing
  that directory and the pod re-enrolls it as a new engine (delete the old engine record).
- kube-vip (ARP) holds `192.168.10.136` on one control-plane node; with `externalTrafficPolicy: Local`
  external queries to the VIP are dropped while the engine on that node restarts.
- Engine group `edge-b` (Helm release, `values-kw.yaml`) is served on `192.168.10.137` with
  `externalTrafficPolicy: Cluster`: kube-vip may announce that VIP from a node without an `edge-b`
  engine, where `Local` would drop the traffic, so `edge-b` engines see node addresses and per-client
  policy does not apply there. Per-client policy on kw is verified on the `default` group
  (`192.168.10.136`, `Local`).
