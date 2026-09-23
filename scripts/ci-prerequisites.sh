#!/usr/bin/env bash
# Isolated prerequisite checks for ordinary CI; no live/privileged opt-ins.
set -euo pipefail
[[ $(uname -s) == Linux ]] || {
	echo 'Supported verification requires Linux (Darwin is not a pass).' >&2
	exit 1
}
mode=${1:?usage: ci-prerequisites.sh linux|race|e2e|contracts}
require() { command -v "$1" >/dev/null || {
	echo "Missing prerequisite: $1" >&2
	exit 1
}; }
# Never inherit live acceptance configuration into ordinary verification.
while IFS= read -r name; do
	case "$name" in
	NEXORA_KW_* | NEXORA_NETLAB)
		echo "Unexpected live/lab environment: $name" >&2
		exit 1
		;;
	esac
done < <(compgen -e)
case "$mode" in
linux) ;;
race | e2e)
	for tool in go cc initdb pg_ctl createdb helm otelcol-contrib; do require "$tool"; done
	if [[ $(id -u) == 0 ]]; then id dev >/dev/null; fi
	[[ $(go env GOOS) == linux && $(go env CGO_ENABLED) == 1 ]]
	;;
contracts)
	for tool in python3 cc make; do require "$tool"; done
	;;
*)
	echo "Unknown prerequisite mode: $mode" >&2
	exit 2
	;;
esac
if [[ $mode == e2e ]]; then
	for tool in cargo pnpm node protoc named named-checkconf named-checkzone dig delv nsupdate \
		softhsm2-util pkcs11-tool dnsperf clickhouse loki unshare nsenter ip curl; do require "$tool"; done
	[[ -r /usr/lib/softhsm/libsofthsm2.so ]]
	: "${NEXORA_E2E_OPENSEARCH_URL:?Set the isolated/test OpenSearch URL}"
	: "${NEXORA_E2E_JAEGER_QUERY_URL:?Set the test Jaeger query URL}"
	curl --fail --silent --show-error --max-time 20 "$NEXORA_E2E_OPENSEARCH_URL/_cluster/health" >/dev/null
	curl --fail --silent --show-error --max-time 20 "$NEXORA_E2E_JAEGER_QUERY_URL/api/services" >/dev/null
	# Enabled mDNS tests need private namespaces; failure is a prerequisite failure,
	# never permission to drop tests or add privileged container settings.
	unshare -Urn --kill-child -- sh -ec 'ip link set lo up; ip link add ci-probe type veth peer name ci-peer'
	(cd web && pnpm exec node --input-type=module -e \
		'import { chromium } from "@playwright/test"; const browser = await chromium.launch(); await browser.close();')
fi
