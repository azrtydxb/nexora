# Nexora on kw

Namespace `nexora` on the kw cluster (context `kw`). Deploy or redeploy everything with:

```sh
scripts/kw-deploy.sh [--tag sha-<7>] [--skip-build]
```

It builds and pushes `nexora-engine` and `nexora-mgmt` (`scripts/build-image.sh`, which stamps the tag
into both binaries), applies the manifests here, creates the CA, key-encryption key and DNS TLS secrets, runs
`bootstrap.sh` against the API and rolls out the engines. It ends by printing the smoke-test
environment (`NEXORA_KW_DNS_ADDR`, `NEXORA_KW_API_URL`, `NEXORA_KW_ENCRYPTED_ADDR`,
`NEXORA_KW_DNS_TLS_NAME`, `NEXORA_KW_ENGINES`, `NEXORA_KW_MGMT_LB_IP`).

`TestKwSmoke` runs from the dev pod. It also needs the Nexora CA (verifies the DNS TLS certificate),
the cluster CA (verifies the ingress; the pod does not trust it) and the admin password, copied
into the pod once:

```sh
k() { kubectl --context kw -n nexora "$@"; }
pod() { kubectl --context kw -n nexora-dev exec -i deploy/toolbox -c toolbox -- sh -c "$1"; }
k get secret nexora-ca -o jsonpath='{.data.ca\.crt}' | base64 -d | pod 'cat > /work/kw-ca.crt'
k get secret nexora-ingress-tls -o jsonpath='{.data.ca\.crt}' | base64 -d | pod 'cat > /work/kw-cluster-ca.crt'
k get secret nexora-admin -o jsonpath='{.data.password}' | base64 -d | pod 'umask 077; cat > /work/kw-admin-password'

scripts/dev-exec.sh env NEXORA_KW_DNS_ADDR=192.168.10.136:53 NEXORA_KW_API_URL=https://nexora.kw.local \
  NEXORA_KW_API_CA_FILE=/work/kw-cluster-ca.crt NEXORA_KW_ENCRYPTED_ADDR=192.168.10.136 \
  NEXORA_KW_CA_FILE=/work/kw-ca.crt NEXORA_KW_DNS_TLS_NAME=dns.nexora.kw.local NEXORA_KW_ENGINES=8 \
  NEXORA_KW_MGMT_LB_IP=192.168.10.135 NEXORA_KW_ADMIN_PASSWORD_FILE=/work/kw-admin-password \
  go test -count=1 -v -run TestKwSmoke ./e2e/
```

The smoke test creates and removes a policy group `kw-smoke-client`, rewrites under
`nexora-smoke.test` and the RPZ zone `rpz.kwsmoke.nexora.`. Its M3 subtests check DNSSEC validation
of forwarded answers (`dnssec-forwarded`: AD for `www.iana.org`/`cloudflare.com`, SERVFAIL for
`dnssec-failed.org`), `recursion` (skipped on kw, see "Known limits"), the root trust anchor on every
engine, RPZ (`example.net` NXDOMAIN from `rpz.kw.nexora.`) and the engine M3 metrics
(`NEXORA_KW_ENGINE_METRICS_URL`, default `http://nexora-engine-metrics.nexora.svc.cluster.local:9153/metrics`).
Manual checks: `delv @192.168.10.136 dnssec-failed.org` fails (bogus, SERVFAIL, EDE 9) and
`dig @192.168.10.136 +dnssec cloudflare.com` has the `ad` flag.

| File                | Objects                                                                                |
| ------------------- | -------------------------------------------------------------------------------------- |
| `namespace.yaml`    | Namespace `nexora`                                                                     |
| `opensearch.yaml`   | StatefulSet/Service `opensearch` (query-log backend)                                   |
| `cnpg-cluster.yaml` | CNPG Cluster `nexora-db` (app secret `nexora-db-app`, key `uri`)                       |
| `otelcol.yaml`      | `nexora-otelcol`: logs to OpenSearch, traces to `jaeger.observability.svc:4317`        |
| `blocklist.yaml`    | `nexora-blocklist`: static block list for the smoke test                               |
| `mgmt.yaml`         | `nexora-mgmt` (2 replicas), Services, gRPC LoadBalancer, TLS Ingress `nexora.kw.local` |
| `engine.yaml`       | DaemonSet `nexora-engine` (one per node), DNS LoadBalancer (`Local`), metrics Service  |
| `bootstrap.sh`      | API bootstrap over HTTPS: admin, upstreams, block list, resolution, RPZ, join token    |

## Addresses

- GUI/API: `https://nexora.kw.local` only (ingress, certificate from the `cluster-ca` ClusterIssuer;
  plain `http://nexora.kw.local` answers 308 to HTTPS). Session cookies are `Secure`. The
  LoadBalancer IP `192.168.10.135` has no HTTP port, so there is no cleartext login path.
- Engine gRPC: `nexora-mgmt-grpc.nexora.svc.cluster.local:9443` in the cluster,
  `192.168.10.135:9443` outside (both in the server certificate).
- DNS on `192.168.10.136`: 53 UDP/TCP, DoT 853/TCP, DoQ 853/UDP, DoH `https://192.168.10.136/dns-query`
  (443/TCP). The serving certificate names `dns.nexora.kw.local` and `192.168.10.136` and is issued
  by the Nexora CA (`nexora-ca`), e.g.
  `kdig @192.168.10.136 +tls-ca=/work/kw-ca.crt +tls-hostname=dns.nexora.kw.local example.com`
  (`+https`, `+quic` likewise).
- The DNS Service uses `externalTrafficPolicy: Local` and the engines run on every node, so engines
  (per-client policy, query log) see the real client address.

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
  seals RPZ TSIG secrets (M4: TSIG keys and DNSSEC keys too); losing it makes them unreadable, so
  back it up outside git.

- `nexora-admin` (`username`, `password`), by `bootstrap.sh` with a random password, then used for
  first-run setup. Read it with:

  ```sh
  kubectl --context kw -n nexora get secret nexora-admin -o jsonpath='{.data.password}' | base64 -d
  ```

- `nexora-dns-tls` (type `kubernetes.io/tls`), the DoT/DoH/DoQ serving certificate, by
  `scripts/kw-deploy.sh` with `nexora-mgmt ca issue-dns` from `nexora-ca` (90 days). nexora-mgmt
  reloads it every 30 s and pushes it to the engines; to rotate, replace the Secret.

- `nexora-join-token` (`join-token`), by `bootstrap.sh` from `POST /api/v1/join-tokens`
  (valid one year, reusable by every engine replica).
- `nexora-db-app` is generated by CNPG.

## Resolution settings

`bootstrap.sh` keeps kw in forward mode (upstreams 1.1.1.1 and 9.9.9.9) with DNSSEC validation and
`validate_forwarded` on, and uploads the RPZ file zone `rpz.kw.nexora.` (`example.net CNAME .`) once.

## Known limits

- No recursion from the root servers: kw's network transparently redirects every outbound UDP/TCP
  53 query to a resolver (`dig +norec @198.41.0.4 example.com` and even `@192.0.2.1` get recursive
  answers), so kw runs forward mode and `TestKwSmoke/recursion` skips itself when it detects the
  redirect. DNSSEC-validating forward mode works through it (the redirect passes DNSSEC records).
- Engine state is an `emptyDir`: a restarted engine pod enrolls as a new engine and rebuilds its
  RFC 5011 trust-anchor state and RPZ last-good zone copies (M5 moves it to `hostPath`).
- kube-vip (ARP) holds `192.168.10.136` on one control-plane node; with `externalTrafficPolicy: Local`
  external queries to the VIP are dropped while the engine on that node restarts.
