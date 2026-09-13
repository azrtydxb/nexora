#!/usr/bin/env bash
# Write procoder's formatted result back over each given file (strips the header line).
set -euo pipefail
P=/Users/pascal/.claude/plugins/cache/procoder/procoder/3.6.0/hooks/launcher.sh
for f in "$@"; do
	abs=$(cd "$(dirname "$f")" && pwd)/$(basename "$f")
	out=$("$P" format "$abs") || continue
	case "$out" in "== "*"formatted result"*) printf '%s\n' "$out" | tail -n +2 >"$abs.fmt" && mv "$abs.fmt" "$abs" ;; esac
done
