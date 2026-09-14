#!/usr/bin/env bash
# Bootstrap a running nexora-mgmt on kw through its API: the first admin, upstream forwarders,
# the smoke-test block list, forward mode with DNSSEC validation, the smoke-test RPZ zone, the M4
# authoritative demo (TSIG key, signed primary nexora-demo.kw., secondary bind-demo.kw.), the M5
# engine group edge-b and the join tokens of both engine groups (Secrets nexora-join-token and
# nexora-join-token-edge-b). Idempotent; run twice by scripts/kw-deploy.sh (before and after the engines).
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

# Resolution: full recursion from the root servers, with DNSSEC validation. The UniFi gateway's DNS
# content filter used to redirect every outbound UDP/TCP 53 query (issue #1); with it disabled the
# engines reach the root servers directly. Forwarded answers (forward zones, fallback) are validated too.
res=$(call "$api/api/v1/resolution")
if [ "$(jq -r .mode <<<"$res")" != recursive ]; then
	jq '.mode = "recursive"' <<<"$res" | call -X PUT -d @- "$api/api/v1/resolution" >/dev/null
	echo "resolution mode set to recursive"
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

# Engine group edge-b (M5): the engines on nodes labelled nexora.io/engine-group=edge-b. Created with
# canary parameters (one canary, 20 s health window) but the all_at_once strategy, so the smoke
# tests' config changes reach every engine quickly; TestKwFullProduct switches it to canary and back.
edge=$(call "$api/api/v1/engine-groups" | jq -r '.[] | select(.name=="edge-b") | .id')
if [ -z "$edge" ]; then
	edge=$(call -d '{"name":"edge-b","description":"engines on nodes labelled nexora.io/engine-group=edge-b","rollout_strategy":"all_at_once","canary_count":1,"health_window_seconds":20,"ack_timeout_seconds":60,"max_servfail_ratio":0.05,"min_health_queries":20}' \
		"$api/api/v1/engine-groups" | jq -r .id)
	echo "engine group edge-b created"
fi

# Authoritative demo (M4). The TSIG key nexora-demo-xfr. comes from the Secret nexora-demo-tsig
# (scripts/kw-deploy.sh). Transfers need the key and a pod address (10.42.0.0/16: the engines see the
# real client address); dynamic updates need the key.
pods=10.42.0.0/16
tsig_name=$(k get secret nexora-demo-tsig -o jsonpath='{.data.name}' | base64 -d)
key_id=$(call "$api/api/v1/tsig-keys" | jq -r --arg n "$tsig_name" '.[] | select(.name==$n) | .id')
if [ -z "$key_id" ]; then
	(umask 077 && k get secret nexora-demo-tsig -o jsonpath='{.data.secret}' | base64 -d >"$tmp/tsig-secret")
	key_id=$(jq -n --arg n "$tsig_name" --rawfile s "$tmp/tsig-secret" '{name:$n, algorithm:"hmac-sha256", secret:$s}' |
		call -d @- "$api/api/v1/tsig-keys" | jq -r .id)
	rm -f "$tmp/tsig-secret"
	echo "TSIG key $tsig_name created"
fi
zone_json() { call "$api/api/v1/zones" | jq -c --arg n "$1" '.[] | select(.name==$n)'; }

# Primary zone nexora-demo.kw., signed online with KEK-backed keys.
demo=nexora-demo.kw.
zone=$(zone_json "$demo")
if [ -z "$zone" ]; then
	zone=$(jq -n --arg n "$demo" --arg k "$key_id" --arg c "$pods" '{name:$n, kind:"primary", default_ttl:300,
		soa:{mname:("ns1."+$n), rname:("hostmaster."+$n)}, nameservers:["ns1."+$n],
		transfer:{allow_cidrs:[$c], tsig_key_id:$k}, update:{tsig_key_ids:[$k]}}' | call -d @- "$api/api/v1/zones")
	echo "zone $demo created"
fi
zid=$(jq -r .id <<<"$zone")
jq -c --arg n "$demo" '[
	{name:("ns1."+$n), type:"A", data:"192.168.10.136"},
	{name:("www."+$n), type:"A", data:"192.0.2.80"},
	{name:("www."+$n), type:"AAAA", data:"2001:db8::80"},
	{name:("mail."+$n), type:"A", data:"192.0.2.25"},
	{name:$n, type:"MX", data:("10 mail."+$n)},
	{name:$n, type:"TXT", data:"\"nexora authoritative demo\""}
][] | .ttl = 300' <<<'null' | while read -r rec; do
	have=$(call -G "$api/api/v1/zones/$zid/records" --data-urlencode "name=$(jq -r .name <<<"$rec")" \
		--data-urlencode "type=$(jq -r .type <<<"$rec")" | jq --arg d "$(jq -r .data <<<"$rec")" '[.items[] | select(.data==$d)] | length')
	if [ "$have" = 0 ]; then
		call -d "$rec" "$api/api/v1/zones/$zid/records" >/dev/null
		echo "record $(jq -r '.name+" "+.type+" "+.data' <<<"$rec") created"
	fi
done
if [ "$(call "$api/api/v1/zones/$zid/dnssec" | jq -r .enabled)" != true ]; then
	jq -n --argjson r "$(call "$api/api/v1/zones/$zid" | jq .revision)" '{revision:$r, enabled:true, key_backend:"kek"}' |
		call -X PUT -d @- "$api/api/v1/zones/$zid/dnssec" >/dev/null
	echo "DNSSEC signing of $demo enabled (KEK backend)"
fi

# Secondary zone bind-demo.kw., transferred from the in-cluster BIND primary (deploy/kw/bind-primary.yaml).
if [ -z "$(zone_json bind-demo.kw.)" ]; then
	jq -n --arg k "$key_id" --arg c "$pods" '{name:"bind-demo.kw.", kind:"secondary",
		primaries:[{address:"10.43.200.53:5353", tsig_key_id:$k}], transfer:{allow_cidrs:[$c], tsig_key_id:$k}}' |
		call -d @- "$api/api/v1/zones" >/dev/null
	echo "secondary zone bind-demo.kw. created"
fi

if ! k get secret nexora-join-token >/dev/null 2>&1; then
	call -d '{"name":"kw-engines","ttl_seconds":31536000}' "$api/api/v1/join-tokens" | jq -r .token | tr -d '\n' >"$tmp/join-token"
	k create secret generic nexora-join-token --from-file=join-token="$tmp/join-token"
fi
if ! k get secret nexora-join-token-edge-b >/dev/null 2>&1; then
	jq -n --arg g "$edge" '{name:"kw-engines-edge-b", ttl_seconds:31536000, engine_group_id:$g}' |
		call -d @- "$api/api/v1/join-tokens" | jq -r .token | tr -d '\n' >"$tmp/join-token-edge-b"
	k create secret generic nexora-join-token-edge-b --from-file=join-token="$tmp/join-token-edge-b"
fi

# Engines enrolled before M5 were named after their (emptyDir) pods and never reconnect.
call "$api/api/v1/engines" |
	jq -r '.[] | select(.connected | not) | select(.node_name | test("^(edge-b-)?(master|worker)-[0-9]+$") | not) | .id' |
	while read -r id; do
		call -X DELETE "$api/api/v1/engines/$id" >/dev/null && echo "removed pre-M5 engine $id"
	done
