#!/usr/bin/env bash
# Sync, then run a command in the kw dev pod from /work/nexora (non-login bash).
#   scripts/dev-exec.sh 'make engine-test && make lint'   # one argument: a shell string
#   scripts/dev-exec.sh bash -c 'cd web && pnpm install'  # several arguments: quoted as words
set -euo pipefail
[ $# -gt 0 ] || {
	echo "usage: $0 <command...>" >&2
	exit 2
}
ctx="${NEXORA_DEV_CONTEXT:-kw}"
ns="${NEXORA_DEV_NAMESPACE:-nexora-dev}"
"$(dirname "$0")/dev-sync.sh"
if [ $# -eq 1 ]; then cmd="$1"; else cmd=$(printf '%q ' "$@"); fi
exec kubectl --context "$ctx" -n "$ns" exec -i deploy/toolbox -c toolbox -- bash -c "cd /work/nexora && $cmd"
