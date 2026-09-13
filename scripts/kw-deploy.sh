#!/usr/bin/env bash
# Deploy Nexora M1 to kw (namespace nexora): build and push the images, apply deploy/kw, create
# the CA secret, bootstrap through the API (deploy/kw/bootstrap.sh) and roll out the engines.
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
	"$root/scripts/build-image.sh" -f deploy/docker/engine.Dockerfile -n nexora-engine -t "$tag" "$root"
	"$root/scripts/build-image.sh" -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t "$tag" "$root"
fi

kubectl --context "$ctx" apply -f "$kw/namespace.yaml"
k apply -f "$kw/opensearch.yaml" -f "$kw/cnpg-cluster.yaml" -f "$kw/otelcol.yaml" -f "$kw/blocklist.yaml"
k wait --for=condition=Ready cluster/nexora-db --timeout=15m

# The CA key only ever exists in a temporary directory and in the Secret.
if ! k get secret nexora-ca >/dev/null 2>&1; then
	(cd "$root" && go run ./mgmt/cmd/nexora-mgmt ca init --out "$tmp/ca")
	k create secret generic nexora-ca --from-file=ca.crt="$tmp/ca/ca.crt" --from-file=ca.key="$tmp/ca/ca.key"
	rm -rf "$tmp/ca"
fi

sed "s/NEXORA_TAG/$tag/" "$kw/mgmt.yaml" | k apply -f -
k rollout status deployment/nexora-mgmt --timeout=10m
k rollout status deployment/nexora-otelcol --timeout=5m
k rollout status deployment/nexora-blocklist --timeout=5m

"$kw/bootstrap.sh"

sed "s/NEXORA_TAG/$tag/" "$kw/engine.yaml" | k apply -f -
k rollout status deployment/nexora-engine --timeout=10m
dns_ip=$(k get service nexora-dns -o jsonpath='{.spec.loadBalancerIP}')
echo "NEXORA_KW_DNS_ADDR=${dns_ip}:53"
echo "NEXORA_KW_API_URL=${NEXORA_KW_API_URL:-http://nexora.kw.local}"
