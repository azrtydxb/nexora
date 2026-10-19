#!/usr/bin/env bash
# Build an image on the kw BuildKit (arm64) or novanas BuildKit (amd64) and push
# it to Kuvryn Ant (ant.azrty.com/oci-hosted). No Docker on the laptop — see azrtydxb/internal-lab docs/dev-builds.md.
#
#   scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine [-t tag] [-p linux/arm64] [context]
#
# The tag is passed as the build argument VERSION, the full commit hash as COMMIT and the UTC build
# time as BUILD_DATE; the Nexora Dockerfiles stamp them into the binaries and the GUI
# (nexora-mgmt /api/v1/health and /api/v1/version, nexora-engine --version).
#
# Push credentials: ANT_TOKEN must hold a Kuvryn Ant token with publish on the
# oci-hosted feed (Settings, Tokens on https://ant.azrty.com). It is written into a
# temporary DOCKER_CONFIG that is deleted on exit, never under $HOME. CI publishes
# the dev toolbox through .github/workflows/toolbox.yml with GitHub OIDC instead.
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
	*)
		echo "usage: $0 -f dockerfile -n name [-t tag] [-p platform] [context]" >&2
		exit 2
		;;
	esac
done
shift $((OPTIND - 1))
[ $# -gt 0 ] && context="$1"
[ -n "$name" ] || {
	echo "-n name is required" >&2
	exit 2
}

case "$platform" in
linux/arm64)
	addr="tcp://192.168.10.130:1234"
	kctx="kw"
	;;
linux/amd64)
	addr="tcp://192.168.10.211:1234"
	kctx="novanas"
	;;
*)
	echo "no builder for $platform" >&2
	exit 2
	;;
esac

if [ -z "$tag" ]; then
	sha=$(git -C "$context" rev-parse --short HEAD 2>/dev/null || echo nogit)
	git -C "$context" diff --quiet 2>/dev/null || sha="${sha}-dirty"
	tag="dev-${sha}"
fi

commit=$(git -C "$context" rev-parse HEAD 2>/dev/null || echo "")
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for f in ca.crt tls.crt tls.key; do
	kubectl --context "$kctx" get secret buildkit-client-tls -n buildkit \
		-o "jsonpath={.data.${f/./\\.}}" | base64 -d >"$work/$f"
done
[ -n "${ANT_TOKEN:-}" ] || {
	echo "ANT_TOKEN (Kuvryn Ant token with publish on oci-hosted) is required" >&2
	exit 1
}
mkdir -p "$work/docker"
(umask 077 && jq -n --arg a "$(printf "ci:%s" "$ANT_TOKEN" | base64 | tr -d "\n")" '{auths: {"ant.azrty.com": {auth: $a}}}' >"$work/docker/config.json")

image="ant.azrty.com/oci-hosted/azrtydxb/${name}:${tag}"
echo "building ${image} (${platform})"
DOCKER_CONFIG="$work/docker" buildctl --addr "$addr" \
	--tlscacert "$work/ca.crt" --tlscert "$work/tls.crt" --tlskey "$work/tls.key" \
	build --frontend dockerfile.v0 \
	--local context="$context" \
	--local dockerfile="$(dirname "$context/$dockerfile")" \
	--opt filename="$(basename "$dockerfile")" \
	--opt platform="$platform" \
	--opt build-arg:VERSION="$tag" \
	--opt build-arg:COMMIT="$commit" \
	--opt build-arg:BUILD_DATE="$build_date" \
	--output "type=image,name=${image},push=true"
echo "pull on kw as ant.kw.watteel.lab/oci-hosted/azrtydxb/${name}:${tag} (or the legacy 192.168.10.131/azrtydxb/${name}:${tag} via the node rewrite) once replicated"
