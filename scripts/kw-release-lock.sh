#!/usr/bin/env bash
# Guarded recovery for the retained kw deployment lock after a run died. Follow
# the procedure in docs/operations.md first: inspect, confirm nothing is running,
# confirm the failed stage. Without --confirm it only shows what it would release.
#   scripts/kw-release-lock.sh --owner TOKEN [--confirm]
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
ctx="${NEXORA_KW_CONTEXT:-kw}"
cd "$root"
exec go run ./deploy/kwrollout/cmd/kw-rollout release-lock --context "$ctx" "$@"
