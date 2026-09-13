#!/usr/bin/env bash
# Deploy Nexora to kw (namespace nexora): build and push the images, apply deploy/kw, create the CA,
# key-encryption key, demo TSIG key and DNS TLS secrets, run the BIND primary of the demo secondary
# zone, bootstrap through the API (deploy/kw/bootstrap.sh) and roll out the engines.
# Ends by printing the environment TestKwSmoke needs (see deploy/kw/README.md).
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
		--names dns.nexora.kw.local,192.168.10.136 --days 90 --out "$tmp/dnstls")
	k create secret tls nexora-dns-tls --cert="$tmp/dnstls/tls.crt" --key="$tmp/dnstls/tls.key"
	rm -rf "$tmp/ca-in" "$tmp/dnstls"
fi

sed "s/NEXORA_TAG/$tag/" "$kw/mgmt.yaml" | k apply -f -
k rollout status deployment/nexora-mgmt --timeout=10m
k rollout status deployment/nexora-otelcol --timeout=5m
k rollout status deployment/nexora-blocklist --timeout=5m

"$kw/bootstrap.sh"

# Server-side apply: client-side apply merges Service ports by port number alone, so 853/UDP (DoQ)
# would be dropped next to 853/TCP (DoT).
sed "s/NEXORA_TAG/$tag/" "$kw/engine.yaml" | k apply --server-side --force-conflicts -f -
k rollout status daemonset/nexora-engine --timeout=15m
# M1 ran the engines as a Deployment; the DaemonSet replaces it once it serves on every node.
k delete deployment nexora-engine --ignore-not-found
dns_ip=$(k get service nexora-dns -o jsonpath='{.spec.loadBalancerIP}')
mgmt_ip=$(k get service nexora-mgmt-lb -o jsonpath='{.spec.loadBalancerIP}')
engines=$(k get daemonset nexora-engine -o jsonpath='{.status.desiredNumberScheduled}')
echo "NEXORA_KW_DNS_ADDR=${dns_ip}:53"
echo "NEXORA_KW_API_URL=${NEXORA_KW_API_URL:-https://nexora.kw.local}"
echo "NEXORA_KW_ENCRYPTED_ADDR=${dns_ip}"
echo "NEXORA_KW_DNS_TLS_NAME=dns.nexora.kw.local"
echo "NEXORA_KW_ENGINES=${engines}"
echo "NEXORA_KW_MGMT_LB_IP=${mgmt_ip}"
