#!/usr/bin/env bash
# Read-only production preflight: stable fleet identities, management/config,
# direct-pod and VIP UDP/TCP answers. Builds a unique disposable DNS helper in
# nexora-dev/toolbox because the laptop cannot route to the pod network.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
ctx="${NEXORA_KW_CONTEXT:-kw}"
tmp=$(mktemp -d)
probe="/tmp/nexora-kw-probe-${tmp##*/}"
cleanup() {
	kubectl --context "$ctx" -n nexora-dev exec deploy/toolbox -c toolbox -- rm -f "$probe" >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT
NEXORA_DEV_CONTEXT="$ctx" NEXORA_DEV_NAMESPACE=nexora-dev NEXORA_DEV_DEPLOY=toolbox \
	"$root/scripts/dev-exec.sh" "go build -o '$probe' ./deploy/kwrollout/cmd/kw-rollout"
cd "$root"
go run ./deploy/kwrollout/cmd/kw-rollout check --context "$ctx" \
	--api "${NEXORA_KW_API_URL:-https://nexora.kw.watteel.lab}" --probe-helper "$probe" "$@"
