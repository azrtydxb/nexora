#!/usr/bin/env bash
# Bootstrap a running nexora-mgmt on kw through its API: the first admin, upstream forwarders,
# the smoke-test block list, forward mode with DNSSEC validation, the smoke-test RPZ zone and the
# engines' join token. Idempotent; run by scripts/kw-deploy.sh.
#
# The admin credentials live only in the Secret nexora-admin (keys username, password), created
# here with a random password on the first run. Read the password with:
#   kubectl --context kw -n nexora get secret nexora-admin -o jsonpath='{.data.password}' | base64 -d
set -euo pipefail
ctx="${NEXORA_KW_CONTEXT:-kw}"
ns=nexora
api="${NEXORA_KW_API_URL:-https://nexora.kw.local}"
k() { kubectl --context "$ctx" -n "$ns" "$@"; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
jar="$tmp/cookies"

# The ingress certificate comes from cert-manager's cluster-ca, which also stores its CA in the
# Secret; curl trusts exactly that CA. The session cookie is Secure, so it only travels over HTTPS.
for _ in $(seq 60); do
	{ k get secret nexora-ingress-tls -o jsonpath='{.data.ca\.crt}' | base64 -d; } >"$tmp/ingress-ca.crt" 2>/dev/null || true
	[ -s "$tmp/ingress-ca.crt" ] && break
	sleep 2
done
curl() { command curl --cacert "$tmp/ingress-ca.crt" "$@"; }

if ! k get secret nexora-admin >/dev/null 2>&1; then
	k create secret generic nexora-admin --from-literal=username=admin \
		--from-literal=password="$(openssl rand -base64 24 | tr -d '\n')"
fi
user=$(k get secret nexora-admin -o jsonpath='{.data.username}' | base64 -d)
k get secret nexora-admin -o jsonpath='{.data.password}' | base64 -d >"$tmp/password"

# The ingress can answer 503 for a few seconds after a rollout while its endpoints catch up.
for _ in $(seq 60); do
	curl -fsS "$api/api/v1/health" >/dev/null 2>&1 && break
	sleep 2
done
required=$(curl -fsS "$api/api/v1/setup" | jq -r .required)
if [ "$required" = true ]; then
	token=$(k logs -l app.kubernetes.io/name=nexora-mgmt --tail=-1 --prefix=false |
		sed -n 's/.*setup token: \([^ ]*\).*/\1/p' | head -1)
	[ -n "$token" ] || {
		echo "setup required but no setup token in the nexora-mgmt logs" >&2
		exit 1
	}
	jq -n --arg t "$token" --arg u "$user" --rawfile p "$tmp/password" \
		'{token:$t, username:$u, email:($u+"@kw.local"), password:$p}' |
		curl -fsS -c "$jar" -H 'Content-Type: application/json' -d @- "$api/api/v1/setup" >/dev/null
	echo "setup completed: admin user $user"
else
	jq -n --arg u "$user" --rawfile p "$tmp/password" '{username:$u, password:$p}' |
		curl -fsS -c "$jar" -H 'Content-Type: application/json' -d @- "$api/api/v1/auth/login" >/dev/null
fi
call() { curl -fsS -b "$jar" -H 'Content-Type: application/json' "$@"; }

if [ "$(call "$api/api/v1/upstreams" | jq length)" = "0" ]; then
	call -d '{"name":"cloudflare","protocol":"udp","address":"1.1.1.1:53","timeout_ms":500,"enabled":true,"position":0}' "$api/api/v1/upstreams" >/dev/null
	call -d '{"name":"quad9","protocol":"udp","address":"9.9.9.9:53","timeout_ms":500,"enabled":true,"position":1}' "$api/api/v1/upstreams" >/dev/null
	echo "upstreams created: 1.1.1.1, 9.9.9.9"
fi

list_id=$(call "$api/api/v1/filter-lists" | jq -r '.[] | select(.name=="kw-smoke") | .id')
if [ -z "$list_id" ]; then
	list_id=$(call -d '{"name":"kw-smoke","kind":"block","url":"http://nexora-blocklist.nexora.svc.cluster.local/smoke.txt","refresh_interval_seconds":3600,"enabled":true}' \
		"$api/api/v1/filter-lists" | jq -r .id)
	echo "filter list kw-smoke created"
fi
call -X POST "$api/api/v1/filter-lists/$list_id/refresh" | jq -e '.entry_count >= 1 and .last_error == ""' >/dev/null ||
	{
		echo "filter list kw-smoke did not refresh" >&2
		exit 1
	}

# Resolution: forward mode with DNSSEC validation of the forwarded answers. kw's network transparently
# redirects every outbound UDP/TCP 53 query to a local resolver (even 192.0.2.1 and the root servers
# answer recursively), so recursion from the real root servers cannot work there.
res=$(call "$api/api/v1/resolution")
if [ "$(jq -r .mode <<<"$res")" != forward ]; then
	jq '.mode = "forward"' <<<"$res" | call -X PUT -d @- "$api/api/v1/resolution" >/dev/null
	echo "resolution mode set to forward"
fi
ds=$(call "$api/api/v1/dnssec/settings")
if [ "$(jq -r '.validation and .validate_forwarded' <<<"$ds")" != true ]; then
	jq '.validation = true | .validate_forwarded = true' <<<"$ds" | call -X PUT -d @- "$api/api/v1/dnssec/settings" >/dev/null
	echo "DNSSEC validation (including forwarded answers) enabled"
fi

# The smoke test's RPZ file zone: example.net answers NXDOMAIN.
rpz=$(call "$api/api/v1/rpz-zones" | jq -c '.[] | select(.name=="rpz.kw.nexora.")')
if [ -z "$rpz" ]; then
	rpz=$(call -d '{"name":"rpz.kw.nexora.","source_type":"file","policy_override":"given","min_refresh_seconds":300}' "$api/api/v1/rpz-zones")
	echo "RPZ zone rpz.kw.nexora. created"
fi
if [ "$(jq -r .file_records <<<"$rpz")" = null ]; then
	zone=$'$TTL 60\n@ SOA ns.rpz.kw.nexora. hostmaster.rpz.kw.nexora. 1 300 60 86400 60\n@ NS ns.rpz.kw.nexora.\nexample.net CNAME .\n'
	jq -n --arg c "$zone" --argjson r "$(jq .revision <<<"$rpz")" '{content:$c, revision:$r}' |
		call -X PUT -d @- "$api/api/v1/rpz-zones/$(jq -r .id <<<"$rpz")/file" >/dev/null
	echo "RPZ zone rpz.kw.nexora. uploaded"
fi

if ! k get secret nexora-join-token >/dev/null 2>&1; then
	call -d '{"name":"kw-engines","ttl_seconds":31536000}' "$api/api/v1/join-tokens" | jq -r .token | tr -d '\n' >"$tmp/join-token"
	k create secret generic nexora-join-token --from-file=join-token="$tmp/join-token"
fi
