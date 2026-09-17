#!/usr/bin/env bash
# Upgrade the existing kw release through the guarded four-engine workflow.
# Builds committed images, then holds one non-expiring lock and DNS monitor over
# supporting resources, serial Helm stages and bootstrap. Failures retain the
# lock for diagnosis; sampled DNS success is not a zero-loss guarantee.
# Fresh installation and legacy kubectl-to-Helm adoption are intentionally refused:
# they require a separate bootstrap workflow without an existing healthy fleet.
#   scripts/kw-deploy.sh [--tag TAG] [--skip-build]
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
ctx="${NEXORA_KW_CONTEXT:-kw}"
ns=nexora
tag=""
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
if [ -z "$tag" ]; then
	tag="sha-$(git -C "$root" rev-parse --short=7 HEAD)"
fi
phase="${NEXORA_KW_DEPLOY_PHASE:-outer}"
case "$phase" in outer | support | bootstrap) ;; *)
	echo "invalid deployment phase" >&2
	exit 2
	;;
esac
guard() { "$root/scripts/kw-guard.sh"; }
k() { guard && kubectl --context "$ctx" -n "$ns" "$@"; }
kw="$root/deploy/kw"
tmp=$(mktemp -d)
probe="/tmp/nexora-kw-probe-${tmp##*/}"
cleanup() {
	if [ "$phase" = outer ]; then
		kubectl --context "$ctx" -n nexora-dev exec deploy/toolbox -c toolbox -- rm -f "$probe" >/dev/null 2>&1 || true
	fi
	rm -rf "$tmp"
}
trap cleanup EXIT
if [ "$phase" != outer ]; then
	guard
	build=0
fi
if [ "$phase" = bootstrap ]; then
	"$kw/bootstrap.sh"
	exit 0
fi
if [ "$phase" = outer ]; then
	[ -z "$(git -C "$root" status --porcelain --untracked-files=normal)" ] || {
		echo "commit the source tree before deploying" >&2
		exit 1
	}
	[ "$tag" = "sha-$(git -C "$root" rev-parse --short=7 HEAD)" ] || {
		echo "tag must name source HEAD" >&2
		exit 1
	}
fi

if [ "$build" = 1 ]; then
	# Build from a clean worktree of HEAD, so the image tag sha-<7> names exactly what is in the image.
	git -C "$root" worktree add --detach "$tmp/src" HEAD
	"$root/scripts/build-image.sh" -f deploy/docker/engine.Dockerfile -n nexora-engine -t "$tag" "$tmp/src"
	"$root/scripts/build-image.sh" -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t "$tag" "$tmp/src"
	git -C "$root" worktree remove --force "$tmp/src"
fi

if [ "$phase" = outer ]; then
	NEXORA_DEV_CONTEXT="$ctx" NEXORA_DEV_NAMESPACE=nexora-dev NEXORA_DEV_DEPLOY=toolbox \
		"$root/scripts/dev-exec.sh" "go build -o '$probe' ./deploy/kwrollout/cmd/kw-rollout"
	(cd "$root" && go run ./deploy/kwrollout/cmd/kw-rollout deploy --context "$ctx" --root "$root" --tag "$tag" \
		--api "${NEXORA_KW_API_URL:-https://nexora.kw.watteel.lab}" --probe-helper "$probe")
	exit 0
fi

# Internal support phase: every production command checks the parent's owner.
# An upgrade must never replace lost trust or encryption keys with random ones.
for secret in nexora-ca nexora-kek nexora-demo-tsig nexora-dns-tls nexora-join-token; do
	if ! k get secret "$secret" >/dev/null 2>&1; then
		echo "required existing Secret $secret unavailable; restore it before upgrading" >&2
		exit 1
	fi
done
guard
kubectl --context "$ctx" apply -f "$kw/namespace.yaml"
# ClickHouse query-log backend (M10): passwords generated once (only in the temporary directory and the
# Secret, never on a command line), schema applied idempotently.
# Only an authoritative NotFound permits first-time credential creation. Transport/RBAC
# errors fail closed; never rotate credentials for an existing ClickHouse workload.
ch_secret=$(k get secret nexora-clickhouse --ignore-not-found -o name)
if [ -z "$ch_secret" ]; then
	ch_existing=$(k get statefulset clickhouse --ignore-not-found -o name)
	ch_data=$(k get pvc data-clickhouse-0 --ignore-not-found -o name)
	if [ -n "$ch_existing" ] || [ -n "$ch_data" ]; then
		echo "required existing Secret nexora-clickhouse unavailable; restore it before upgrading" >&2
		exit 1
	fi
	(
		umask 077
		openssl rand -base64 32 | tr -d '\n' >"$tmp/ch-writer"
		openssl rand -base64 32 | tr -d '\n' >"$tmp/ch-reader"
		k create secret generic nexora-clickhouse --from-file=writer-password="$tmp/ch-writer" \
			--from-file=reader-password="$tmp/ch-reader"
	)
	rm -f "$tmp/ch-writer" "$tmp/ch-reader"
fi
k apply -f "$kw/clickhouse.yaml"
ch_sha=$(shasum -a 256 "$kw/clickhouse.yaml" | cut -c1-16)
k patch statefulset clickhouse -p "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"nexora.io/config-sha\":\"$ch_sha\"}}}}}"
k rollout status statefulset/clickhouse --timeout=5m
k exec -i clickhouse-0 -c clickhouse -- clickhouse client --multiquery <"$root/deploy/clickhouse/querylog.sql"
k apply -f "$kw/opensearch.yaml" -f "$kw/cnpg-cluster.yaml" -f "$kw/otelcol.yaml" -f "$kw/blocklist.yaml"
# A changed collector ConfigMap does not restart the pod: stamp its hash into the pod template, so the
# collector (e.g. the nexora-querylog-v2 rename) is live before any engine of the new release sends records.
otel_sha=$(shasum -a 256 "$kw/otelcol.yaml" | cut -c1-16)
k patch deployment nexora-otelcol -p "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"nexora.io/config-sha\":\"$otel_sha\"}}}}}"
k rollout status deployment/nexora-otelcol --timeout=5m
k wait --for=condition=Ready cluster/nexora-db --timeout=15m

k apply -f "$kw/bind-primary.yaml"
k rollout status deployment/nexora-bind --timeout=5m

# Verify the existing DoT/DoH/DoQ certificate. Certificate/key rotation is a
# separate operation, not an implicit side effect of this availability migration.
dns_ips=$(helm --kube-context "$ctx" template nexora "$root/deploy/helm/nexora" -n "$ns" -f "$kw/values-kw.yaml" -f "$kw/values-pairs.yaml" \
	--api-versions monitoring.coreos.com/v1 --api-versions postgresql.cnpg.io/v1 |
	awk '/^kind: Service$/ {svc = 1} /^---/ {svc = 0; eng = 0} svc && /nexora.io\/engine-group:/ {eng = 1} svc && eng && /loadBalancerIP:/ {print $2; eng = 0}' |
	sort -u | paste -sd, -)
[ -n "$dns_ips" ] || {
	echo "no engine group loadBalancerIP found in the chart" >&2
	exit 1
}
dns_names="dns.nexora.kw.watteel.lab,$dns_ips"
sans=$(k get secret nexora-dns-tls -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -ext subjectAltName 2>/dev/null)
for n in ${dns_names//,/ }; do
	kind="IP Address"
	[ "$n" != dns.nexora.kw.watteel.lab ] || kind="DNS"
	if ! tr ',' '\n' <<<"$sans" | awk '{$1=$1; print}' | grep -Fxq -- "$kind:$n"; then
		echo "existing DNS certificate lacks required SAN $n; rotate separately before upgrading" >&2
		exit 1
	fi
done

# The former engine group edge-b ran on nodes labelled nexora.io/engine-group=edge-b; the label is no
# longer used (removing an absent label is a no-op).
guard
kubectl --context "$ctx" label node worker-24 worker-25 nexora.io/engine-group-

# No Helm mutation here: the parent performs each verified stage serially.
# No automatic resource adoption/deletion is safe during an availability migration.
