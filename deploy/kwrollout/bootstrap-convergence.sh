#!/usr/bin/env bash
# shellcheck disable=SC2154 # api, tmp, jar and root belong to bootstrap.sh.
# Sourced by bootstrap.sh after authentication. Only this bootstrap boundary waits;
# the parent's fleet binding, management and DNS gates remain strict observations.
bootstrap_pin_engines() {
	call "$api/api/v1/engines" >"$tmp/bootstrap-engines.json"
	jq -e 'type == "array" and
		([.[] | select(.connected == true)] |
		 length > 0 and all(.[]; all(.id, .node_name, .engine_group_id; type == "string" and length > 0)) and
		 (map(.id) | length == (unique | length)) and
		 (map(.node_name) | length == (unique | length)))' "$tmp/bootstrap-engines.json" >/dev/null
}

bootstrap_wait_convergence() {
	local budget="${NEXORA_KW_BOOTSTRAP_WAIT_SECONDS:-120}" deadline version after state remaining http_status
	case "$budget" in '' | *[!0-9]*)
		echo 'invalid bootstrap wait budget' >&2
		return 1
		;;
	esac
	if [ "${#budget}" -gt 3 ] || [ "$budget" -lt 1 ] || [ "$budget" -gt 120 ]; then
		echo 'bootstrap wait budget must be 1..120 seconds' >&2
		return 1
	fi
	deadline=$((SECONDS + 10#$budget))
	# Pin the published version once after all bootstrap writes. Never chase a
	# newer publication, republish a snapshot, or retry a failed HTTP/DNS request.
	bootstrap_remaining() {
		remaining=$((deadline - SECONDS))
		if [ "$remaining" -le 0 ]; then
			echo "bootstrap configuration convergence timed out (${budget}s)" >&2
			return 1
		fi
		[ "$remaining" -le 5 ] || remaining=5
	}
	bootstrap_read() {
		bootstrap_remaining || return 1
		guard || return 1
		bootstrap_remaining || return 1
		http_status=$(command curl --cacert "$tmp/ingress-ca.crt" --max-time "$remaining" \
			-fsS -b "$jar" -H 'Accept: application/json' --max-filesize 2097152 \
			-o "$tmp/bootstrap-response.json" -w '%{http_code}' "$api/api/v1/$1") || return 1
		[ "$http_status" = 200 ] || {
			echo 'bootstrap management request did not return HTTP 200' >&2
			return 1
		}
		cat "$tmp/bootstrap-response.json"
	}
	bootstrap_version() {
		bootstrap_read 'config-versions?limit=1' | jq -er '
			if type == "array" and length == 1 and
			(.[0].version | type == "number" and . > 0 and . == floor)
			then .[0].version else error("invalid current configuration version") end'
	}
	version=$(bootstrap_version) || return 1
	while :; do
		bootstrap_read engines >"$tmp/bootstrap-observed.json" || return 1
		state=$(jq -er --argjson version "$version" \
			--slurpfile baseline "$tmp/bootstrap-engines.json" \
			-f "$root/deploy/kwrollout/bootstrap-convergence.jq" "$tmp/bootstrap-observed.json") || {
			echo 'bootstrap configuration convergence refused unhealthy or changed fleet' >&2
			return 1
		}
		after=$(bootstrap_version) || return 1
		if [ "$after" != "$version" ]; then
			echo "bootstrap configuration target moved from $version to $after; refusing to chase it" >&2
			return 1
		fi
		if [ "$state" = ready ]; then
			guard || return 1
			[ "$SECONDS" -lt "$deadline" ] || {
				echo "bootstrap configuration convergence timed out (${budget}s)" >&2
				return 1
			}
			# A slow version/ownership read must not turn a once-fresh sample
			# into a successful stale acknowledgement.
			jq -er --argjson version "$version" \
				--slurpfile baseline "$tmp/bootstrap-engines.json" \
				-f "$root/deploy/kwrollout/bootstrap-convergence.jq" "$tmp/bootstrap-observed.json" >/dev/null || return 1
			echo "bootstrap configuration $version acknowledged by pinned engines"
			return 0
		fi
		echo "waiting for bootstrap configuration $version acknowledgement" >&2
		sleep 1
	done
}
