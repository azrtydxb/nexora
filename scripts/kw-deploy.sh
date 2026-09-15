#!/usr/bin/env bash
# Deploy Nexora to kw (namespace nexora): build and push the images, apply the supporting manifests in
# deploy/kw (OpenSearch, CNPG, collector, block list, BIND primary), create the CA, key-encryption key,
# demo TSIG key and DNS TLS secrets, install the Helm release nexora (deploy/helm/nexora with
# deploy/kw/values-kw.yaml) and run deploy/kw/bootstrap.sh. A redeploy is a single rolling upgrade that
# loses no DNS query; a first install runs in two phases (management plane, bootstrap, engines). Ends by
# printing the environment of the kw tests (see deploy/kw/README.md; scripts/kw-acceptance.sh runs them).
#   scripts/kw-deploy.sh [--tag TAG] [--skip-build]
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
ctx="${NEXORA_KW_CONTEXT:-kw}"
ns=nexora
tag="sha-$(git -C "$root" rev-parse --short=7 HEAD)"
build=1
while [ $# -gt 0 ]; do
	case "$1" in
	--tag)
		tag="$2"
		shift 2
		;;
	--skip-build)
		build=0
		shift
		;;
	*)
		echo "usage: $0 [--tag TAG] [--skip-build]" >&2
		exit 2
		;;
	esac
done
k() { kubectl --context "$ctx" -n "$ns" "$@"; }
kw="$root/deploy/kw"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if [ "$build" = 1 ]; then
	# Build from a clean worktree of HEAD, so the image tag sha-<7> names exactly what is in the image.
	git -C "$root" worktree add --detach "$tmp/src" HEAD
	"$root/scripts/build-image.sh" -f deploy/docker/engine.Dockerfile -n nexora-engine -t "$tag" "$tmp/src"
	"$root/scripts/build-image.sh" -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t "$tag" "$tmp/src"
	git -C "$root" worktree remove --force "$tmp/src"
fi

kubectl --context "$ctx" apply -f "$kw/namespace.yaml"
k apply -f "$kw/opensearch.yaml" -f "$kw/cnpg-cluster.yaml" -f "$kw/otelcol.yaml" -f "$kw/blocklist.yaml"
# A changed collector ConfigMap does not restart the pod: stamp its hash into the pod template, so the
# collector (e.g. the nexora-querylog-v2 rename) is live before any engine of the new release sends records.
otel_sha=$(shasum -a 256 "$kw/otelcol.yaml" | cut -c1-16)
k patch deployment nexora-otelcol -p "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"nexora.io/config-sha\":\"$otel_sha\"}}}}}"
k rollout status deployment/nexora-otelcol --timeout=5m
k wait --for=condition=Ready cluster/nexora-db --timeout=15m

# The CA key only ever exists in a temporary directory and in the Secret.
if ! k get secret nexora-ca >/dev/null 2>&1; then
	(cd "$root" && go run ./mgmt/cmd/nexora-mgmt ca init --out "$tmp/ca")
	k create secret generic nexora-ca --from-file=ca.crt="$tmp/ca/ca.crt" --from-file=ca.key="$tmp/ca/ca.key"
	rm -rf "$tmp/ca"
fi

# The key-encryption key sealing RPZ TSIG secrets (M4: TSIG keys and DNSSEC keys too); never printed.
if ! k get secret nexora-kek >/dev/null 2>&1; then
	openssl rand -base64 32 | k create secret generic nexora-kek --from-file=kek=/dev/stdin
fi

# The demo zones' TSIG key (transfers, dynamic updates, and the BIND primary of bind-demo.kw.); bootstrap.sh
# registers the same secret in Nexora. Never printed; read it as described in deploy/kw/README.md.
if ! k get secret nexora-demo-tsig >/dev/null 2>&1; then
	(
		umask 077
		openssl rand -base64 32 | tr -d '\n' >"$tmp/tsig-secret"
		printf 'key "nexora-demo-xfr." {\n\talgorithm hmac-sha256;\n\tsecret "%s";\n};\n' "$(cat "$tmp/tsig-secret")" >"$tmp/named.key"
		k create secret generic nexora-demo-tsig --from-literal=name=nexora-demo-xfr. --from-literal=algorithm=hmac-sha256 \
			--from-file=secret="$tmp/tsig-secret" --from-file=named.key="$tmp/named.key"
	)
	rm -f "$tmp/tsig-secret" "$tmp/named.key"
fi
k apply -f "$kw/bind-primary.yaml"
k rollout status deployment/nexora-bind --timeout=5m

# The DNS serving certificate for DoT/DoH/DoQ; its key only exists in the temporary directory and the Secret.
# Reissued when an address is missing from its SANs (e.g. after adding a DNS address).
# The addresses come from the chart: every engine group Service (main and extraServices) with a
# loadBalancerIP, so adding a DNS address (up to four client addresses are planned) only touches values-kw.yaml.
dns_ips=$(helm --kube-context "$ctx" template nexora "$root/deploy/helm/nexora" -n "$ns" -f "$kw/values-kw.yaml" \
	--api-versions monitoring.coreos.com/v1 --api-versions postgresql.cnpg.io/v1 |
	awk '/^kind: Service$/ {svc = 1} /^---/ {svc = 0; eng = 0} svc && /nexora.io\/engine-group:/ {eng = 1} svc && eng && /loadBalancerIP:/ {print $2; eng = 0}' |
	sort -u | paste -sd, -)
[ -n "$dns_ips" ] || { echo "no engine group loadBalancerIP found in the chart" >&2; exit 1; }
dns_names="dns.nexora.kw.watteel.lab,$dns_ips"
if k get secret nexora-dns-tls >/dev/null 2>&1; then
	sans=$(k get secret nexora-dns-tls -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -ext subjectAltName 2>/dev/null)
	for n in ${dns_names//,/ }; do
		grep -q -- "$n" <<<"$sans" || { k delete secret nexora-dns-tls >/dev/null && break; }
	done
fi
if ! k get secret nexora-dns-tls >/dev/null 2>&1; then
	(umask 077 && mkdir -p "$tmp/ca-in" &&
		k get secret nexora-ca -o jsonpath='{.data.ca\.crt}' | base64 -d >"$tmp/ca-in/ca.crt" &&
		k get secret nexora-ca -o jsonpath='{.data.ca\.key}' | base64 -d >"$tmp/ca-in/ca.key")
	(cd "$root" && go run ./mgmt/cmd/nexora-mgmt ca issue-dns --ca-cert "$tmp/ca-in/ca.crt" --ca-key "$tmp/ca-in/ca.key" \
		--names "$dns_names" --days 90 --out "$tmp/dnstls")
	k create secret tls nexora-dns-tls --cert="$tmp/dnstls/tls.crt" --key="$tmp/dnstls/tls.key"
	rm -rf "$tmp/ca-in" "$tmp/dnstls"
fi

# The former engine group edge-b ran on nodes labelled nexora.io/engine-group=edge-b; the label is no
# longer used (removing an absent label is a no-op).
kubectl --context "$ctx" label node worker-24 worker-25 nexora.io/engine-group-

# One-time switch from the kubectl-applied M1-M4 manifests to the chart: remove objects Helm does not own.
if k get deployment nexora-mgmt >/dev/null 2>&1 &&
	[ "$(k get deployment nexora-mgmt -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}')" != Helm ]; then
	k delete deployment/nexora-mgmt daemonset/nexora-engine service/nexora-mgmt service/nexora-mgmt-grpc \
		service/nexora-mgmt-lb service/nexora-dns service/nexora-engine-metrics ingress/nexora \
		configmap/nexora-engine-config --ignore-not-found
fi

# --force-conflicts: the chart is the source of truth, so fields last changed by hand with kubectl
# (kubectl set image, kubectl apply) do not block Helm 4's server-side apply.
release() {
	helm --kube-context "$ctx" -n "$ns" upgrade --install nexora "$root/deploy/helm/nexora" \
		-f "$kw/values-kw.yaml" --set image.tag="$tag" --force-conflicts --wait --timeout 15m "$@"
}

# A redeploy is one helm upgrade: mgmt rolls (two replicas, PodDisruptionBudget) while the engines keep
# serving their snapshot, and each node's new engine becomes ready beside the old one before the old
# one drains (maxSurge 1, /ready, shutdownDrainSeconds), so no DNS query is lost (issue #53).
# Only a first install, before the join token secrets exist, needs two phases: the management plane
# first (the join tokens come from its API), then the engines.
# The engines are two instances of the default group (nexora-engine-a, -b); nexora-engine is the
# group workload they replaced.
engines_installed() {
	k get secret nexora-join-token >/dev/null 2>&1 &&
		{ k get daemonset nexora-engine >/dev/null 2>&1 || k get daemonset nexora-engine-a >/dev/null 2>&1; }
}
if ! engines_installed; then
	release --set engine.enabled=false
	k rollout status deployment/nexora-mgmt --timeout=10m
	k rollout status deployment/nexora-otelcol --timeout=5m
	k rollout status deployment/nexora-blocklist --timeout=5m
	"$kw/bootstrap.sh"
fi
release
k rollout status deployment/nexora-mgmt --timeout=10m
k rollout status daemonset/nexora-engine-a --timeout=15m
k rollout status daemonset/nexora-engine-b --timeout=15m
"$kw/bootstrap.sh"

dns_ip=$(k get service nexora-dns -o jsonpath='{.spec.loadBalancerIP}')
dns_ip_2=$(k get service nexora-dns-2 -o jsonpath='{.spec.loadBalancerIP}')
mgmt_ip=$(k get service nexora-mgmt-lb -o jsonpath='{.spec.loadBalancerIP}')
engines=$(k get daemonsets -l app.kubernetes.io/name=nexora-engine -o jsonpath='{range .items[*]}{.status.desiredNumberScheduled}{"\n"}{end}' |
	awk '{n += $1} END {print n + 0}')
engine_ip=$(k get pods -l app.kubernetes.io/name=nexora-engine,nexora.io/engine-instance=a --field-selector=status.phase=Running \
	-o jsonpath='{.items[0].status.podIP}')
echo "NEXORA_KW_DNS_ADDR=${dns_ip}:53"
echo "NEXORA_KW_DNS_ADDR_2=${dns_ip_2}:53"
echo "NEXORA_KW_ENGINE_ADDR=${engine_ip}:53"
echo "NEXORA_KW_API_URL=${NEXORA_KW_API_URL:-https://nexora.kw.watteel.lab}"
echo "NEXORA_KW_ENCRYPTED_ADDR=${dns_ip}"
echo "NEXORA_KW_DNS_TLS_NAME=dns.nexora.kw.watteel.lab"
echo "NEXORA_KW_ENGINES=${engines}"
echo "NEXORA_KW_MGMT_LB_IP=${mgmt_ip}"
