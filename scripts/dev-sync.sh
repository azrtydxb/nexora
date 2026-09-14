#!/usr/bin/env bash
# Mirror the working tree into the kw dev pod at /work/nexora (rsync over kubectl exec).
# The remote "host" is literally `rsync` and --rsync-path is empty, so the exec'd
# command becomes `rsync --server ...` inside the pod. Pod-side build outputs are
# excluded so --delete leaves them alone.
set -euo pipefail
ctx="${NEXORA_DEV_CONTEXT:-kw}"
ns="${NEXORA_DEV_NAMESPACE:-nexora-dev}"
pod="${NEXORA_DEV_DEPLOY:-toolbox}"
root=$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)
kubectl --context "$ctx" -n "$ns" exec deploy/$pod -c toolbox -- mkdir -p /work/nexora
rsync -a --delete --blocking-io \
	--exclude /.git/ --exclude target/ --exclude node_modules/ --exclude /web/dist/ \
	--exclude /bin/ --exclude /web/test-results/ --exclude /web/playwright-report/ \
	--exclude /mgmt/internal/webui/dist/ \
	--rsync-path= \
	-e "kubectl --context $ctx -n $ns exec -i deploy/$pod -c toolbox --" \
	"$root/" rsync:/work/nexora/
