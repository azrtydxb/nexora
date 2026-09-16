#!/usr/bin/env bash
# Run the kw acceptance tests (TestKwSmoke, TestKwSmokeM4, TestKwFullProduct, TestKwFilterCategories, TestKwSmokeAI)
# in the dev pod against the live deployment.
# TestKwFilterCategories writes per-engine filter index memory and decision time to
# /work/kw-filter-categories.json in the toolbox pod.
#   scripts/kw-acceptance.sh [go test -run pattern]   # default 'TestKwSmoke|TestKwFullProduct|TestKwFilterCategories'
set -euo pipefail
ctx="${NEXORA_KW_CONTEXT:-kw}"
run="${1:-TestKwSmoke|TestKwFullProduct|TestKwFilterCategories|TestKwSmokeAI}"
k() { kubectl --context "$ctx" -n nexora "$@"; }
pod() { kubectl --context "$ctx" -n nexora-dev exec -i deploy/toolbox -c toolbox -- sh -c "$1"; }
k get secret nexora-ca -o jsonpath='{.data.ca\.crt}' | base64 -d | pod 'cat > /work/kw-ca.crt'
k get secret nexora-ingress-tls -o jsonpath='{.data.ca\.crt}' | base64 -d | pod 'cat > /work/kw-cluster-ca.crt'
k get secret nexora-admin -o jsonpath='{.data.password}' | base64 -d | pod 'umask 077; cat > /work/kw-admin-password'

engines=$(k get daemonsets -l app.kubernetes.io/name=nexora-engine -o jsonpath='{range .items[*]}{.status.desiredNumberScheduled}{"\n"}{end}' |
	awk '{n += $1} END {print n + 0}')
engine_ip=$(k get pods -l app.kubernetes.io/name=nexora-engine,nexora.io/engine-instance=a --field-selector=status.phase=Running \
	-o jsonpath='{.items[0].status.podIP}')

exec "$(dirname "$0")/dev-exec.sh" env \
	NEXORA_KW_DNS_ADDR=192.168.10.136:53 NEXORA_KW_DNS_ADDR_2=192.168.10.139:53 NEXORA_KW_ENGINE_ADDR="${engine_ip}:53" \
	NEXORA_KW_API_URL=https://nexora.kw.watteel.lab \
	NEXORA_KW_API_CA_FILE=/work/kw-cluster-ca.crt NEXORA_KW_ENCRYPTED_ADDR=192.168.10.136 \
	NEXORA_KW_CA_FILE=/work/kw-ca.crt NEXORA_KW_DNS_TLS_NAME=dns.nexora.kw.watteel.lab NEXORA_KW_ENGINES="$engines" \
	NEXORA_KW_MGMT_LB_IP=192.168.10.135 NEXORA_KW_ADMIN_PASSWORD_FILE=/work/kw-admin-password \
	NEXORA_KW_PROMETHEUS_URL=http://kps-prometheus.monitoring.svc:9090 NEXORA_KW_FILTER_REPORT=/work/kw-filter-categories.json \
	go test -count=1 -v -timeout 75m -run "$run" ./e2e/
