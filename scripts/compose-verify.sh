#!/usr/bin/env bash
# Runs the "Install with Docker Compose" and "Plain PostgreSQL and Compose" steps of docs/operations.md
# on a Docker host over ssh, checks enrollment, DNS, the collector, backup and restore, and removes
# everything it created. Usage: scripts/compose-verify.sh user@host
# shellcheck disable=SC2016 # single-quoted commands expand on the host, not here
set -euo pipefail
HOST=${1:?usage: scripts/compose-verify.sh user@host}
ADDR=${HOST#*@}
TAG=${NEXORA_TAG:-sha-$(git rev-parse --short=7 HEAD)}
SRC=${NEXORA_SOURCE_REGISTRY:-192.168.10.131:5000/azrtydxb}
DIR=nexora-compose-verify
API=http://$ADDR:18080/api/v1
WORK=$(mktemp -d)
JAR=$WORK/cookies
ok() { echo "ok $1"; }
remote() { ssh -o BatchMode=yes "$HOST" "cd ~/$DIR && export COMPOSE_PROJECT_NAME=nexora-verify && $*"; }
api() { curl -fsS -b "$JAR" -c "$JAR" -H 'Content-Type: application/json' "$@"; }

cleanup() {
	remote 'docker compose --profile engine --profile otel down -v --remove-orphans' >/dev/null 2>&1 || true
	ssh -o BatchMode=yes "$HOST" "comm -13 ~/$DIR.images-before <(docker image ls -q | sort -u) | xargs -r docker image rm -f; rm -rf ~/$DIR ~/$DIR.images-before" >/dev/null 2>&1 || true
	rm -rf "$WORK"
}
trap cleanup EXIT
trap 'echo "failed at line $LINENO: $BASH_COMMAND" >&2' ERR

arch=$(ssh -o BatchMode=yes "$HOST" 'uname -m')
case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
ssh -o BatchMode=yes "$HOST" "test ! -e ~/$DIR && docker image ls -q | sort -u > ~/$DIR.images-before"
for image in nexora-mgmt nexora-engine; do
	# --insecure: the kw Nexus certificate is not in the laptop's trust store either.
	crane pull --insecure --platform "linux/$arch" "$SRC/$image:$TAG" "$WORK/$image.tar"
	ssh -o BatchMode=yes "$HOST" 'docker load' <"$WORK/$image.tar" >/dev/null
done
ok pull

tar --no-xattrs -C deploy/compose -cf - . | ssh -o BatchMode=yes "$HOST" "mkdir -p ~/$DIR && tar -C ~/$DIR -xf -"
# The documented steps, with this run's registry, tag, address and ports.
remote "cp .env.example .env && sed -i \
  -e 's|^NEXORA_TAG=.*|NEXORA_TAG=$TAG|' -e 's|^NEXORA_REGISTRY=.*|NEXORA_REGISTRY=$SRC|' \
  -e 's|^NEXORA_PUBLIC_URL=.*|NEXORA_PUBLIC_URL=http://$ADDR:18080|' -e 's|^NEXORA_PUBLIC_HOST=.*|NEXORA_PUBLIC_HOST=$ADDR|' \
  -e 's|^NEXORA_DNS_PORT=.*|NEXORA_DNS_PORT=15353|' -e 's|^NEXORA_HTTP_PORT=.*|NEXORA_HTTP_PORT=18080|' \
  -e 's|^NEXORA_GRPC_PORT=.*|NEXORA_GRPC_PORT=19443|' -e 's|^NEXORA_METRICS_PORT=.*|NEXORA_METRICS_PORT=19153|' \
  -e 's|^NEXORA_OTLP_ENDPOINT=.*|NEXORA_OTLP_ENDPOINT=http://otel-collector:4317|' .env"
remote 'openssl rand -hex 24 > secrets/postgres-password'
remote 'printf "postgres:5432:nexora:nexora:%s\n" "$(cat secrets/postgres-password)" > secrets/pgpass'
remote 'chmod 0644 secrets/postgres-password secrets/pgpass'
remote 'docker compose up -d'
ok up

token=""
for _ in $(seq 60); do
	token=$(remote 'docker compose logs mgmt' 2>/dev/null | sed -n 's/.*setup token: //p' | tail -1)
	[ -n "$token" ] && break
	sleep 2
done
[ -n "$token" ] || {
	echo "no setup token in the mgmt log"
	exit 1
}
ok setup-token

password=$(openssl rand -hex 16)
api -X POST "$API/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"email\":\"admin@example.net\",\"password\":\"$password\"}" >/dev/null
api -X POST "$API/auth/login" -d "{\"username\":\"admin\",\"password\":\"$password\"}" >/dev/null
zone=$(api -X POST "$API/zones" -d '{"name":"compose.test.","kind":"primary","default_ttl":300,"soa":{"mname":"ns1.compose.test.","rname":"hostmaster.compose.test."},"nameservers":["ns1.compose.test."]}' | jq -r .id)
api -X POST "$API/zones/$zone/records" -d '{"name":"www.compose.test.","type":"A","ttl":300,"data":"192.0.2.10"}' >/dev/null
ok setup

remote 'docker compose run --rm -T mgmt join-token create --engine-group default --ttl 1h > secrets/join-token'
remote 'docker compose --profile engine up -d'
remote 'docker compose --profile otel up -d'
ok join-token

for _ in $(seq 60); do
	api "$API/engines" | jq -e '.[] | select(.connected and .status == "current")' >/dev/null && break
	sleep 2
done
api "$API/engines" | jq -e '.[] | select(.connected and .status == "current")' >/dev/null
ok engine-enrolled

dns() { dig +short +time=2 +tries=3 @"$ADDR" -p 15353 www.compose.test A; }
for _ in $(seq 30); do
	[ "$(dns)" = 192.0.2.10 ] && break
	sleep 2
done
[ "$(dns)" = 192.0.2.10 ]
ok dns-answers

# With the built-in query log the engine sends query logs to mgmt, not to the collector; the collector's
# debug exporter logs the engine's OTLP metrics (pushed every 15 s) as "data points".
for _ in $(seq 30); do
	remote 'docker compose logs otel-collector' | grep -q 'data points' && break
	sleep 2
done
remote 'docker compose logs otel-collector' | grep -q 'data points'
ok otel-logs

remote 'docker compose exec -T postgres pg_dump -U nexora -Fc nexora > nexora-backup.dump'
record=$(api "$API/zones/$zone/records?name=www.compose.test." | jq -r '.items[] | select(.name=="www.compose.test.") | "\(.id) \(.revision)"')
api -X DELETE "$API/zones/$zone/records/${record% *}?revision=${record#* }" >/dev/null
for _ in $(seq 30); do
	[ -z "$(dns)" ] && break
	sleep 2
done
[ -z "$(dns)" ]
ok backup

# The version the engine runs now; the restored database's newest version is below it.
engine_version=$(api "$API/engines" | jq '[.[].applied_version] | max')
remote 'docker compose stop mgmt'
remote 'docker compose exec -T postgres pg_restore -U nexora --clean --if-exists -d nexora < nexora-backup.dump'
remote 'docker compose start mgmt'
for _ in $(seq 60); do
	api -X POST "$API/auth/login" -d "{\"username\":\"admin\",\"password\":\"$password\"}" >/dev/null 2>&1 && break
	sleep 2
done
api "$API/zones/$zone/records?name=www.compose.test." | jq -e '.items[] | select(.name=="www.compose.test.")' >/dev/null
ok restore

# docs/operations.md "Engines ahead of a restored database": publish until the newest version is above
# the engine's. The restored engine row reads "current" until the engine reconnects, so it is not the test.
for _ in $(seq 5); do
	[ "$(api "$API/config-versions?limit=1" | jq '.[0].version')" -gt "$engine_version" ] && break
	remote 'docker compose exec -T postgres psql -U nexora -d nexora -c "update engine_groups set rollouts_paused = true where name = '"'"'default'"'"';"' >/dev/null
	api -X POST "$API/engine-groups/00000000-0000-0000-0000-000000000001/resume-rollouts" >/dev/null
done
[ "$(api "$API/config-versions?limit=1" | jq '.[0].version')" -gt "$engine_version" ]
for _ in $(seq 60); do
	api "$API/engines" | jq -e ".[] | select(.connected and .status == \"current\" and .applied_version > $engine_version)" >/dev/null && break
	sleep 2
done
for _ in $(seq 30); do
	[ "$(dns)" = 192.0.2.10 ] && break
	sleep 2
done
[ "$(dns)" = 192.0.2.10 ]
ok restored-dns

cleanup
trap - EXIT
ok cleanup
