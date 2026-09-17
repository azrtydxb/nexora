#!/usr/bin/env bash
# Opt-in, same-host namespace lab. Never attaches to a host/LAN interface.
set -euo pipefail
if [[ $# != 3 || ${FAILOVER_LAB_ENABLE:-} != yes || $(id -u) != 0 ]]; then
	echo 'usage: FAILOVER_LAB_ENABLE=yes sudo -E bash run.sh /absolute/probe /absolute/ipvsadm dr|snat' >&2
	exit 2
fi
probe=$(realpath "$1")
ipvs=$(realpath "$2")
mode=$3
[[ -x $probe && -x $ipvs && ($mode == dr || $mode == snat) ]] || exit 2
for cmd in ip sysctl openssl tcpdump python3; do command -v "$cmd" >/dev/null; done
if [[ $mode == snat ]]; then command -v iptables >/dev/null; fi
script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
umask 077
base=$(mktemp -d /tmp/nexora-failoverlab.XXXXXXXX)
export FAILOVER_LAB_DIR=$base
prefix="fg-${base##*.}"
echo "EVIDENCE=$base MODE=$mode"
created=()
capture_pids=()
cleanup() {
	local n p failed=0
	for n in "${created[@]}"; do
		for p in $(ip netns pids "$n"); do kill -TERM "$p" 2>/dev/null || true; done
	done
	sleep 1
	for n in "${created[@]}"; do ip netns del "$n" || failed=1; done
	# Attempt every cleanup even if one namespace deletion fails.
	rm -f "$base/key.pem" || failed=1
	if [[ $failed != 0 ]]; then
		echo "cleanup incomplete; inspect $base and $prefix namespaces" >&2
		exit 1
	fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
for suffix in lb a b c d; do
	n="$prefix-$suffix"
	# Creation failure must not add an existing namespace to the cleanup set.
	ip netns add "$n"
	created+=("$n")
	ip -n "$n" link set lo up
done
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=dsr-lab.test \
	-addext subjectAltName=DNS:dsr-lab.test -keyout "$base/key.pem" -out "$base/cert.pem" >"$base/cert.log" 2>&1
lb="$prefix-lb"
ip -n "$lb" link add br0 type bridge
ip -n "$lb" link set br0 up
ip -n "$lb" addr add 198.18.0.2/24 dev br0
ip -n "$lb" addr add 198.18.0.100/32 dev br0
for spec in a:3 b:4 c:10 d:11; do
	n=${spec%:*}
	host=${spec#*:}
	ip -n "$lb" link add "br-$n" type veth peer name eth0 netns "$prefix-$n"
	ip -n "$lb" link set "br-$n" master br0
	ip -n "$lb" link set "br-$n" up
	ip -n "$prefix-$n" link set eth0 up
	ip -n "$prefix-$n" addr add "198.18.0.$host/24" dev eth0
	ip netns exec "$prefix-$n" sysctl -qw net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.eth0.rp_filter=0
done
for n in a b; do
	ip netns exec "$prefix-$n" sysctl -qw net.ipv4.conf.all.arp_ignore=1 net.ipv4.conf.all.arp_announce=2
	ip -n "$prefix-$n" addr add 198.18.0.100/32 dev lo
	ip netns exec "$prefix-$n" "$probe" serve >"$base/backend-$n.log" 2>&1 &
done
ip netns exec "$lb" sysctl -qw net.ipv4.ip_forward=1 net.ipv4.conf.all.send_redirects=0 net.ipv4.conf.br0.send_redirects=0
if [[ $mode == snat ]]; then
	# Deliberately lose the client's original identity before forwarding. IPVS/DR
	# bypasses the LB's ordinary POSTROUTING path, so inject at the client edge.
	ip -n "$prefix-c" addr add 198.18.0.12/32 dev eth0
	ip netns exec "$prefix-c" iptables -t nat -A POSTROUTING -s 198.18.0.10/32 -d 198.18.0.100/32 -j SNAT --to-source 198.18.0.12
fi
for spec in u:53 t:53 t:853 t:443 u:853; do
	proto=${spec%:*}
	port=${spec#*:}
	ip netns exec "$lb" "$ipvs" -A "-$proto" "198.18.0.100:$port" -s rr
	for real in 198.18.0.3 198.18.0.4; do
		ip netns exec "$lb" "$ipvs" -a "-$proto" "198.18.0.100:$port" -r "$real:$port" -g -w 1
	done
done
for n in a b c d; do
	ip netns exec "$prefix-$n" tcpdump --immediate-mode -U -nn -i eth0 -w "$base/$n.pcap" \
		'net 198.18.0.0/24 and (port 53 or port 853 or port 443)' >"$base/capture-$n.log" 2>&1 &
	capture_pids+=("$!")
done
sleep 1
if [[ $mode == dr ]]; then
	for round in 1 2; do
		for spec in c:10 d:11; do
			echo "ROUND=$round CLIENT=${spec#*:}"
			ip netns exec "$prefix-${spec%:*}" "$probe" probe 198.18.0.100 "198.18.0.${spec#*:}"
		done
	done
else
	# A network/TLS/startup failure is NOT a successful negative control.
	for transport in udp tcp dot doh doq; do
		if ip netns exec "$prefix-c" "$probe" probe 198.18.0.100 198.18.0.10 "$transport" >"$base/negative-$transport.log" 2>&1; then
			echo "negative control incorrectly accepted $transport" >&2
			exit 1
		fi
		grep -q "$transport source mismatch:.*198.18.0.12.*expected 198.18.0.10" "$base/negative-$transport.log"
		echo "PASS negative source-loss detection $transport"
	done
fi
ip netns exec "$lb" "$ipvs" -Ln --stats >"$base/ipvs-stats.txt"
sleep 1
for pid in "${capture_pids[@]}"; do kill -INT "$pid"; done
for pid in "${capture_pids[@]}"; do wait "$pid"; done
for n in a b c d; do tcpdump -nn -tt -r "$base/$n.pcap" >"$base/packets-$n.txt" 2>/dev/null; done
if [[ $mode == dr ]]; then python3 "$script_dir/verify.py" "$base"; fi
