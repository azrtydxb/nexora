#!/usr/bin/env bash
# Build an image on the kw BuildKit (arm64) or novanas BuildKit (amd64) and push
# it to Nexus. No Docker on the laptop — see azrtydxb/internal-lab docs/dev-builds.md.
#
#   scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine [-t tag] [-p linux/arm64] [context]
#
# Push credentials are read from the cluster (the `ci` account's dockerconfigjson)
# into a temporary DOCKER_CONFIG that is deleted on exit, so no password is ever
# written under $HOME.
set -euo pipefail

dockerfile="Dockerfile"
name=""
tag=""
platform="linux/arm64"
context="."
while getopts ":f:n:t:p:" opt; do
	case "$opt" in
	f) dockerfile="$OPTARG" ;;
	n) name="$OPTARG" ;;
	t) tag="$OPTARG" ;;
	p) platform="$OPTARG" ;;
	*) echo "usage: $0 -f dockerfile -n name [-t tag] [-p platform] [context]" >&2; exit 2 ;;
	esac
done
shift $((OPTIND - 1))
[ $# -gt 0 ] && context="$1"
[ -n "$name" ] || { echo "-n name is required" >&2; exit 2; }

case "$platform" in
linux/arm64) addr="tcp://192.168.10.130:1234"; kctx="kw" ;;
linux/amd64) addr="tcp://192.168.10.211:1234"; kctx="novanas" ;;
*) echo "no builder for $platform" >&2; exit 2 ;;
esac

if [ -z "$tag" ]; then
	sha=$(git -C "$context" rev-parse --short HEAD 2>/dev/null || echo nogit)
	git -C "$context" diff --quiet 2>/dev/null || sha="${sha}-dirty"
	tag="dev-${sha}"
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for f in ca.crt tls.crt tls.key; do
	kubectl --context "$kctx" get secret buildkit-client-tls -n buildkit \
		-o "jsonpath={.data.${f/./\\.}}" | base64 -d >"$work/$f"
done
mkdir -p "$work/docker"
kubectl --context kw -n novaforge-dev get secret nexus-pull \
	-o 'jsonpath={.data.\.dockerconfigjson}' | base64 -d >"$work/docker/config.json"
grep -q '192.168.10.131:5000' "$work/docker/config.json" || { echo "no push credentials found" >&2; exit 1; }

image="192.168.10.131:5000/azrtydxb/${name}:${tag}"
echo "building ${image} (${platform})"
DOCKER_CONFIG="$work/docker" buildctl --addr "$addr" \
	--tlscacert "$work/ca.crt" --tlscert "$work/tls.crt" --tlskey "$work/tls.key" \
	build --frontend dockerfile.v0 \
	--local context="$context" \
	--local dockerfile="$(dirname "$context/$dockerfile")" \
	--opt filename="$(basename "$dockerfile")" \
	--opt platform="$platform" \
	--output "type=image,name=${image},push=true"
echo "pull as 192.168.10.131/azrtydxb/${name}:${tag}"
