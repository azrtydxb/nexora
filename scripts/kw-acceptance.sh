#!/usr/bin/env bash
# Run the kw acceptance tests (TestKwSmoke, TestKwSmokeM4, TestKwFullProduct) in the dev pod against
# the live deployment. The edge-b engines are restarted first, so TestKwFullProduct proves that
# engine identities survive pod restarts (hostPath state).
#   scripts/kw-acceptance.sh [go test -run pattern]   # default 'TestKwSmoke|TestKwFullProduct'
set -euo pipefail
ctx="${NEXORA_KW_CONTEXT:-kw}"
run="${1:-TestKwSmoke|TestKwFullProduct}"
k() { kubectl --context "$ctx" -n nexora "$@"; }
pod() { kubectl --context "$ctx" -n nexora-dev exec -i deploy/toolbox -c toolbox -- sh -c "$1"; }
k get secret nexora-ca -o jsonpath='{.data.ca\.crt}' | base64 -d | pod 'cat > /work/kw-ca.crt'
k get secret nexora-ingress-tls -o jsonpath='{.data.ca\.crt}' | base64 -d | pod 'cat > /work/kw-cluster-ca.crt'
k get secret nexora-admin -o jsonpath='{.data.password}' | base64 -d | pod 'umask 077; cat > /work/kw-admin-password'

k rollout restart daemonset/nexora-engine-edge-b
k rollout status daemonset/nexora-engine-edge-b --timeout=10m
engines=$(($(k get daemonset nexora-engine -o jsonpath='{.status.desiredNumberScheduled}') + \
$(k get daemonset nexora-engine-edge-b -o jsonpath='{.status.desiredNumberScheduled}')))
engine_ip=$(k get pods -l app.kubernetes.io/name=nexora-engine,nexora.io/engine-group=default --field-selector=status.phase=Running \
	-o jsonpath='{.items[0].status.podIP}')
edge_ips=$(k get pods -l nexora.io/engine-group=edge-b --field-selector=status.phase=Running \
	-o jsonpath='{range .items[*]}{.status.podIP}{","}{end}')

exec "$(dirname "$0")/dev-exec.sh" env \
	NEXORA_KW_DNS_ADDR=192.168.10.136:53 NEXORA_KW_ENGINE_ADDR="${engine_ip}:53" NEXORA_KW_EDGE_B_DNS_ADDR=192.168.10.137:53 \
	NEXORA_KW_EDGE_B_ENGINE_IPS="${edge_ips%,}" NEXORA_KW_API_URL=https://nexora.kw.local \
	NEXORA_KW_API_CA_FILE=/work/kw-cluster-ca.crt NEXORA_KW_ENCRYPTED_ADDR=192.168.10.136 \
	NEXORA_KW_CA_FILE=/work/kw-ca.crt NEXORA_KW_DNS_TLS_NAME=dns.nexora.kw.local NEXORA_KW_ENGINES="$engines" \
	NEXORA_KW_MGMT_LB_IP=192.168.10.135 NEXORA_KW_ADMIN_PASSWORD_FILE=/work/kw-admin-password \
	NEXORA_KW_PROMETHEUS_URL=http://kps-prometheus.monitoring.svc:9090 \
	go test -count=1 -v -timeout 45m -run "$run" ./e2e/
