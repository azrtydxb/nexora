#!/usr/bin/env bash
# Mirror the working tree into the kw dev pod at /work/nexora (rsync over kubectl exec).
# The remote "host" is literally `rsync` and --rsync-path is empty, so the exec'd
# command becomes `rsync --server ...` inside the pod.
set -euo pipefail
root=$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)
pod=$(kubectl --context kw -n nexora-dev get pod -l app=toolbox -o jsonpath='{.items[0].metadata.name}')
rsync -a --delete --blocking-io \
	--exclude .git/ --exclude target/ --exclude node_modules/ --exclude web/dist/ \
	--rsync-path= \
	-e "kubectl --context kw -n nexora-dev exec -i $pod --" \
	"$root/" rsync:/work/nexora/
