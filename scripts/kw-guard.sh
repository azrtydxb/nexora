#!/usr/bin/env bash
# A child phase cannot mutate unless the live parent confirms its lock ownership.
set -euo pipefail
socket="${NEXORA_KW_GUARD_SOCKET:-}"
[ -n "$socket" ] && [ -S "$socket" ] || {
	echo "deployment guard is absent; refusing production mutation" >&2
	exit 1
}
command curl --noproxy '*' --unix-socket "$socket" --max-time 20 --silent --show-error --fail \
	-X POST http://localhost/check >/dev/null
