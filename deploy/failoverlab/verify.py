"""Verify tuple preservation; not a packet-loss or payload-equivalence claim."""

import pathlib
import re
import sys

VIP = "198.18.0.100"
CLIENTS = {"198.18.0.10", "198.18.0.11"}
SERVICES = {
    ("udp", "53"),
    ("tcp", "53"),
    ("tcp", "853"),
    ("tcp", "443"),
    ("udp", "853"),
}
PATTERN = re.compile(r" IP (198\.18\.0\.\d+)\.(\d+) > (198\.18\.0\.\d+)\.(\d+):")
PEER = re.compile(
    r"peer=(198\.18\.0\.\d+):(\d+) question=(udp|tcp|dot|doh|doq)\.dsr-lab\.test\."
)
TRANSPORT = {
    "udp": ("udp", "53"),
    "tcp": ("tcp", "53"),
    "dot": ("tcp", "853"),
    "doh": ("tcp", "443"),
    "doq": ("udp", "853"),
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def flows(text):
    result = set()
    for line in text.splitlines():
        match = PATTERN.search(line)
        if not match:
            continue
        if "Flags [" in line:
            proto = "tcp"
        elif (
            "UDP," in line or " UDP " in line or ": " in line and match.group(4) == "53"
        ):
            # tcpdump decodes DNS/53 rather than printing UDP explicitly.
            proto = "udp"
        else:
            # DNS responses have an ephemeral destination port.
            require(match.group(2) == "53", f"unrecognized transport: {line}")
            proto = "udp"
        result.add((proto, *match.groups()))
    return result


def verify(path):
    captures = {n: flows((path / f"packets-{n}.txt").read_text()) for n in "abcd"}
    for n in "abcd":
        log = (path / f"capture-{n}.log").read_text()
        require(
            re.search(r"\b0 packets dropped by kernel\b", log),
            f"{n}: missing zero-drop capture report",
        )
    clients = captures["c"] | captures["d"]
    backends = captures["a"] | captures["b"]
    requests = {f for f in clients if f[3] == VIP}
    received = {f for f in backends if f[3] == VIP}
    require(len(requests) == 20, f"expected 20 distinct flows, got {len(requests)}")
    require(
        requests == received,
        f"source/destination tuple mismatch: {requests ^ received}",
    )
    for client in CLIENTS:
        for proto, port in SERVICES:
            require(
                sum(f[0] == proto and f[1] == client and f[4] == port for f in requests)
                == 2,
                f"missing two {proto}/{port} flows from {client}",
            )
    reverse = {(proto, dst, dp, src, sp) for proto, src, sp, dst, dp in requests}
    require(
        reverse == {f for f in clients if f[1] == VIP}, "client reply tuple mismatch"
    )
    require(
        reverse == {f for f in backends if f[1] == VIP}, "backend reply tuple mismatch"
    )
    require(
        not (
            {f for f in captures["a"] if f[3] == VIP}
            & {f for f in captures["b"] if f[3] == VIP}
        ),
        "a flow reached both backends",
    )
    for n in "ab":
        peers = PEER.findall((path / f"backend-{n}.log").read_text())
        require(len(peers) == 10, f"{n}: expected ten backend queries")
        require({p[2] for p in peers} == set(TRANSPORT), f"{n}: missing transport")
        for ip, port, transport in peers:
            proto, service = TRANSPORT[transport]
            require(
                ip in CLIENTS and (proto, ip, port, VIP, service) in captures[n],
                f"{n}: backend socket identity missing from capture",
            )
    return "PASS: 20 exact bidirectional tuples; two clients; both backends served all five transports."


if __name__ == "__main__":
    try:
        report = verify(pathlib.Path(sys.argv[1]))
    except (ValueError, OSError, IndexError) as error:
        sys.exit(f"FAIL: {error}")
    print(report)
    (pathlib.Path(sys.argv[1]) / "packet-verification.txt").write_text(report + "\n")
