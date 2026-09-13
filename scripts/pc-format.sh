#!/usr/bin/env bash
# Write procoder's formatted result back over each given file. The formatter
# prints a status header on stderr and the formatted bytes (if any) on stdout.
set -euo pipefail
P=/Users/pascal/.claude/plugins/cache/procoder/procoder/3.6.0/hooks/launcher.sh
for f in "$@"; do
	abs=$(cd "$(dirname "$f")" && pwd)/$(basename "$f")
	"$P" format "$abs" >"$abs.fmt" 2>/dev/null || true
	if [ -s "$abs.fmt" ]; then mv "$abs.fmt" "$abs"; else rm -f "$abs.fmt"; fi
done
