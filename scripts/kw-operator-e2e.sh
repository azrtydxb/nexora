#!/usr/bin/env bash
# Operator e2e on kw in the disposable namespace nexora-optest: build the three images, install the CRDs and
# the operator (namespace scope), run TestKwOperator from operator/test/kw, and clean up. Never touches the
# production namespace nexora or its addresses. The CRDs stay installed (cluster-scoped, unused by production).
#   scripts/kw-operator-e2e.sh [--tag TAG] [--skip-build] [--keep]
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
ctx="${NEXORA_KW_CONTEXT:-kw}"
ns=nexora-optest
nodes="${NEXORA_OPTEST_NODES:-worker-21,worker-22,worker-23}"
tag="sha-$(git -C "$root" rev-parse --short=7 HEAD)"
build=1 keep=0
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
	--keep)
		keep=1
		shift
		;;
	*)
		echo "usage: $0 [--tag TAG] [--skip-build] [--keep]" >&2
		exit 2
		;;
	esac
done
[ "$ns" != nexora ] || {
	echo "refusing the production namespace" >&2
	exit 2
}
for node in ${nodes//,/ }; do
	case "$node" in master-12 | master-13)
		echo "node $node carries the production DNS addresses; refusing it" >&2
		exit 2
		;;
	esac
done
k() { kubectl --context "$ctx" -n "$ns" "$@"; }
run="$(date +%Y%m%d%H%M%S)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if [ "$build" = 1 ]; then
	# The committed tree only (git archive), stamped with HEAD's commit through GIT_DIR.
	mkdir "$tmp/src"
	git -C "$root" archive HEAD | tar -x -C "$tmp/src"
	gitdir="$(git -C "$root" rev-parse --absolute-git-dir)"
	for img in engine mgmt operator; do
		GIT_DIR="$gitdir" "$root/scripts/build-image.sh" -f "deploy/docker/$img.Dockerfile" -n "nexora-$img" -t "$tag" "$tmp/src"
	done
fi

if kubectl --context "$ctx" get namespace "$ns" >/dev/null 2>&1; then
	[ "$(kubectl --context "$ctx" get namespace "$ns" -o jsonpath='{.metadata.labels.nexora\.io/e2e}')" = operator ] ||
		{
			echo "namespace $ns exists without label nexora.io/e2e=operator; refusing to reuse it" >&2
			exit 1
		}
else
	kubectl --context "$ctx" create namespace "$ns"
	kubectl --context "$ctx" label namespace "$ns" nexora.io/e2e=operator
fi
kubectl --context "$ctx" apply --server-side -f "$root/deploy/operator/crds/"
helm --kube-context "$ctx" upgrade --install nexora-operator "$root/deploy/helm/nexora-operator" -n "$ns" \
	--set image.tag="$tag" --set image.pullPolicy=Always --set rbac.scope=namespace --set-json "watchNamespaces=[\"$ns\"]" --wait

# MinIO credentials for the CNPG backup; never printed.
kubectl --context "$ctx" -n minio get secret minio-root -o json |
	jq '{apiVersion:"v1",kind:"Secret",metadata:{name:"optest-s3"},data:{ACCESS_KEY_ID:.data.MINIO_ROOT_USER,ACCESS_SECRET_KEY:.data.MINIO_ROOT_PASSWORD}}' |
	k apply -f -
k apply -f "$root/operator/test/kw/testdata/probe.yaml"
k wait --for=condition=Ready pod/probe --timeout=5m
# Expanded by the probe's shell, which reads the credentials from the mounted Secret.
# shellcheck disable=SC2016
s3='curl -fsS --aws-sigv4 aws:amz:us-east-1:s3 --user "$(cat /s3/ACCESS_KEY_ID):$(cat /s3/ACCESS_SECRET_KEY)"'
minio=http://minio.minio.svc.cluster.local:9000
k exec probe -- sh -c "curl -sS --aws-sigv4 aws:amz:us-east-1:s3 --user \"\$(cat /s3/ACCESS_KEY_ID):\$(cat /s3/ACCESS_SECRET_KEY)\" -X PUT $minio/nexora-optest -o /dev/null -w '%{http_code}\n' | grep -Eq '^(200|409)$'"

status=0
(cd "$root/operator" && NEXORA_KW_CONTEXT="$ctx" NEXORA_OPTEST_NAMESPACE="$ns" NEXORA_OPTEST_TAG="$tag" \
	NEXORA_OPTEST_NODES="$nodes" NEXORA_OPTEST_S3_PATH="s3://nexora-optest/$run" \
	go test -tags kwe2e -count=1 -timeout 90m -v ./test/kw -run TestKwOperator) || status=$?

if [ "$keep" = 0 ]; then
	# Order matters: the workloads and databases stop writing before their state is removed, and the
	# NexoraEngineGroup finalizers run while the operator is still installed.
	k delete nexorainstallations --all --wait=true --timeout=5m || status=1
	k wait --for=delete pod -l app.kubernetes.io/name=nexora-engine --timeout=5m || status=1
	k delete clusters.postgresql.cnpg.io --all --wait=true --timeout=5m || status=1
	k exec probe -- sh -c "set -e; for pass in 1 2 3; do keys=\$($s3 '$minio/nexora-optest?list-type=2&prefix=$run/' | grep -o '<Key>[^<]*' | cut -c6-) || exit 1; [ -n \"\$keys\" ] || break; for key in \$keys; do $s3 -X DELETE \"$minio/nexora-optest/\$key\"; done; done; [ -z \"\$($s3 '$minio/nexora-optest?list-type=2&prefix=$run/' | grep -o '<Key>')\" ]; $s3 -X DELETE $minio/nexora-optest || true" ||
		{
			echo "S3 prefix $run left behind" >&2
			status=1
		}
	for node in ${nodes//,/ }; do
		sed "s/NODE/$node/g" "$root/operator/test/kw/testdata/cleanup-job.yaml" | k apply -f -
	done
	k wait --for=condition=complete job -l nexora.io/e2e-cleanup=true --timeout=5m || {
		echo "node state cleanup incomplete" >&2
		status=1
	}
	k delete nexoraenginegroups --all --wait=true --timeout=5m || status=1
	helm --kube-context "$ctx" uninstall nexora-operator -n "$ns" --wait || true
	kubectl --context "$ctx" delete namespace "$ns" --wait=true --timeout=10m || status=1
fi
exit "$status"
