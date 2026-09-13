#!/usr/bin/env bash
# Runs inside the dev pod: fails unless every M1 toolchain is present at the pinned version.
set -euo pipefail
fail=0
check() { if ! out=$("$@" 2>&1); then
	echo "MISSING: $*"
	fail=1
else echo "ok: $* -> ${out%%$'\n'*}"; fi; }
check rustc --version
rustc --version | grep -q '^rustc 1\.97' || {
	echo "WRONG rustc"
	fail=1
}
check go version
go version | grep -q 'go1\.27' || {
	echo "WRONG go"
	fail=1
}
check node --version
node --version | grep -q '^v26\.' || {
	echo "WRONG node"
	fail=1
}
check pnpm --version
check protoc --version
check protoc-gen-go --version
check protoc-gen-go-grpc --version
check oapi-codegen --version
check initdb --version
check otelcol-contrib --version
command -v dnsperf >/dev/null || {
	echo "MISSING: dnsperf"
	fail=1
}
check cargo fuzz --help
check rsync --version
[ -d /work ] && [ -w /work ] || {
	echo "MISSING: writable /work"
	fail=1
}
df -BG /work | awk 'NR==2 { if ($2+0 < 90) { print "PVC too small: " $2; exit 1 } }' || fail=1
test "$(nproc)" -ge 7 || {
	echo "fewer than 7 CPUs: $(nproc)"
	fail=1
}
exit $fail
