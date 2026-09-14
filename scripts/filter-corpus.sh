#!/usr/bin/env bash
# Download the default catalog selection (bench/filter/corpus-5m.tsv) as one normalised domain list
# per source for filter_bench: comments, hosts sink addresses, "*." prefixes and CRs removed,
# lower-cased, unique. The UT1 archive is downloaded once.
#   scripts/filter-corpus.sh /work/filter-corpus
set -euo pipefail
out="${1:?usage: $0 <dir>}"
tsv="$(cd "$(dirname "$0")/.." && pwd)/bench/filter/corpus-5m.tsv"
mkdir -p "$out"
ut1="$out/.ut1.tar.gz"
while IFS=$'\t' read -r key url member; do
	case "$key" in '' | '#'*) continue ;; esac
	if [ "$member" != "-" ]; then
		[ -s "$ut1" ] || curl -fsSL --retry 3 -o "$ut1" "$url"
		tar -xzOf "$ut1" "$member"
	else
		curl -fsSL --retry 3 "$url"
	fi | tr -d '\r' |
		sed -E -e 's/[[:space:]]*#.*$//' -e 's/^(0\.0\.0\.0|127\.0\.0\.1)[[:space:]]+//' -e 's/^\*\.//' |
		tr '[:upper:]' '[:lower:]' | awk 'NF == 1' | sort -u >"$out/$key.txt"
	echo "$key $(wc -l <"$out/$key.txt")"
done <"$tsv"
