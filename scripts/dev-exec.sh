#!/usr/bin/env bash
# Sync, then run a command in the kw dev pod from /work/nexora.
#   scripts/dev-exec.sh make engine-test
set -euo pipefail
"$(dirname "$0")/dev-sync.sh"
pod=$(kubectl --context kw -n nexora-dev get pod -l app=toolbox -o jsonpath='{.items[0].metadata.name}')
exec kubectl --context kw -n nexora-dev exec -i "$pod" -- bash -c "cd /work/nexora && $*"
