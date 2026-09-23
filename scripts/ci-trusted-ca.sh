#!/usr/bin/env bash
# Canonical pre-checkout bootstrap, embedded verbatim in workflows; tested for drift.
# Input: one existing, authenticated, self-signed PEM root CA (no private key).
set +x
set -euo pipefail
fail_ca() {
	echo 'Trusted CI CA missing, invalid, or unavailable; parent provisioning required.' >&2
	exit 1
}
[[ -n ${NEXORA_CI_CA_PEM:-} ]] || fail_ca
: "${RUNNER_TEMP:?}" "${GITHUB_ENV:?}"
umask 077
ca_dir=$(mktemp -d "$RUNNER_TEMP/nexora-ca.XXXXXX")
trap 'rm -rf "$ca_dir"' EXIT
printf '%s\n' "$NEXORA_CI_CA_PEM" >"$ca_dir/input.pem"
unset NEXORA_CI_CA_PEM
# Reject additional objects, keys, trailing garbage and multiple certificates.
awk '
  /^-----BEGIN CERTIFICATE-----$/ { if (state != 0) exit 1; state=1; next }
  /^-----END CERTIFICATE-----$/ { if (state != 1) exit 1; state=2; next }
  state == 1 { if ($0 !~ /^[A-Za-z0-9+\/=]+$/) exit 1; next }
  /[^[:space:]]/ { exit 1 }
  END { if (state != 2) exit 1 }
' "$ca_dir/input.pem" || fail_ca
openssl x509 -in "$ca_dir/input.pem" -out "$ca_dir/cluster-ca.crt" 2>/dev/null || fail_ca
openssl x509 -in "$ca_dir/cluster-ca.crt" -noout -ext basicConstraints 2>/dev/null | grep -E '^[[:space:]]*CA:TRUE(, pathlen:[0-9]+)?[[:space:]]*$' >/dev/null || fail_ca
openssl verify -check_ss_sig -CAfile "$ca_dir/cluster-ca.crt" "$ca_dir/cluster-ca.crt" >/dev/null 2>&1 || fail_ca
# Keep public roots; install only a job-private bundle, never change host/cluster trust.
cat /etc/ssl/certs/ca-certificates.crt "$ca_dir/cluster-ca.crt" >"$ca_dir/ca-bundle.crt"
rm "$ca_dir/input.pem"
{
	echo "NEXORA_CI_CA_FILE=$ca_dir/cluster-ca.crt"
	echo "NODE_EXTRA_CA_CERTS=$ca_dir/cluster-ca.crt"
	echo "CARGO_HTTP_CAINFO=$ca_dir/ca-bundle.crt"
	echo "SSL_CERT_FILE=$ca_dir/ca-bundle.crt"
	echo "GIT_SSL_CAINFO=$ca_dir/ca-bundle.crt"
	echo "CURL_CA_BUNDLE=$ca_dir/ca-bundle.crt"
} >>"$GITHUB_ENV"
trap - EXIT
