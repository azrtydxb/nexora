#!/usr/bin/env bash
# Deploy Nexora to kw (namespace nexora): build and push the images, apply the supporting manifests in
# deploy/kw (OpenSearch, CNPG, collector, block list, BIND primary), create the CA, key-encryption key,
# demo TSIG key and DNS TLS secrets, install the Helm release nexora (deploy/helm/nexora with
# deploy/kw/values-kw.yaml) in two phases around deploy/kw/bootstrap.sh: the management plane first,
# then the engines of the engine groups default and edge-b. Ends by printing the environment of the kw
# tests (see deploy/kw/README.md; scripts/kw-acceptance.sh runs them).
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
if ! k get secret nexora-dns-tls >/dev/null 2>&1; then
	(umask 077 && mkdir -p "$tmp/ca-in" &&
		k get secret nexora-ca -o jsonpath='{.data.ca\.crt}' | base64 -d >"$tmp/ca-in/ca.crt" &&
		k get secret nexora-ca -o jsonpath='{.data.ca\.key}' | base64 -d >"$tmp/ca-in/ca.key")
	(cd "$root" && go run ./mgmt/cmd/nexora-mgmt ca issue-dns --ca-cert "$tmp/ca-in/ca.crt" --ca-key "$tmp/ca-in/ca.key" \
		--names dns.nexora.kw.local,192.168.10.136,192.168.10.137 --days 90 --out "$tmp/dnstls")
	k create secret tls nexora-dns-tls --cert="$tmp/dnstls/tls.crt" --key="$tmp/dnstls/tls.key"
	rm -rf "$tmp/ca-in" "$tmp/dnstls"
fi

kubectl --context "$ctx" label node worker-24 worker-25 nexora.io/engine-group=edge-b --overwrite

# One-time switch from the kubectl-applied M1-M4 manifests to the chart: remove objects Helm does not own.
if k get deployment nexora-mgmt >/dev/null 2>&1 &&
	[ "$(k get deployment nexora-mgmt -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}')" != Helm ]; then
	k delete deployment/nexora-mgmt daemonset/nexora-engine service/nexora-mgmt service/nexora-mgmt-grpc \
		service/nexora-mgmt-lb service/nexora-dns service/nexora-engine-metrics ingress/nexora \
		configmap/nexora-engine-config --ignore-not-found
fi

release() {
	helm --kube-context "$ctx" -n "$ns" upgrade --install nexora "$root/deploy/helm/nexora" \
		-f "$kw/values-kw.yaml" --set image.tag="$tag" --wait --timeout 15m "$@"
}

# Phase 1: the management plane; the engine join tokens come from its API.
release --set engine.enabled=false
k rollout status deployment/nexora-mgmt --timeout=10m
k rollout status deployment/nexora-otelcol --timeout=5m
k rollout status deployment/nexora-blocklist --timeout=5m
"$kw/bootstrap.sh"

# Phase 2: engines of both engine groups.
release
k rollout status daemonset/nexora-engine --timeout=15m
k rollout status daemonset/nexora-engine-edge-b --timeout=15m
"$kw/bootstrap.sh"

dns_ip=$(k get service nexora-dns -o jsonpath='{.spec.loadBalancerIP}')
edge_ip=$(k get service nexora-dns-edge-b -o jsonpath='{.spec.loadBalancerIP}')
mgmt_ip=$(k get service nexora-mgmt-lb -o jsonpath='{.spec.loadBalancerIP}')
engines=$(($(k get daemonset nexora-engine -o jsonpath='{.status.desiredNumberScheduled}') + \
$(k get daemonset nexora-engine-edge-b -o jsonpath='{.status.desiredNumberScheduled}')))
edge_ips=$(k get pods -l nexora.io/engine-group=edge-b -o jsonpath='{range .items[*]}{.status.podIP}{","}{end}')
echo "NEXORA_KW_DNS_ADDR=${dns_ip}:53"
echo "NEXORA_KW_EDGE_B_DNS_ADDR=${edge_ip}:53"
echo "NEXORA_KW_EDGE_B_ENGINE_IPS=${edge_ips%,}"
echo "NEXORA_KW_API_URL=${NEXORA_KW_API_URL:-https://nexora.kw.local}"
echo "NEXORA_KW_ENCRYPTED_ADDR=${dns_ip}"
echo "NEXORA_KW_DNS_TLS_NAME=dns.nexora.kw.local"
echo "NEXORA_KW_ENGINES=${engines}"
echo "NEXORA_KW_MGMT_LB_IP=${mgmt_ip}"
