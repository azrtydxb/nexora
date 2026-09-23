"""Parent-only real-frame/IPVS probe and root lab activation bridge."""

import argparse
import errno
import json
import socket
import struct
import sys
import time
import uuid
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "platform"))
from active_lab import ActiveLabBridge, require

VIP = "198.18.100.53"
BACKEND = "198.18.101.2"
CLIENT = "198.18.102.2"


def packet(src, dst, marker):
    udp = struct.pack("!HHHH", 53001, 53, 8 + len(marker), 0) + marker
    ip = struct.pack(
        "!BBHHHBBH4s4s",
        0x45,
        0,
        20 + len(udp),
        1,
        0,
        64,
        17,
        0,
        socket.inet_aton(src),
        socket.inet_aton(dst),
    )
    total = sum(struct.unpack("!10H", ip))
    total = (total & 65535) + (total >> 16)
    total = (total & 65535) + (total >> 16)
    return ip[:10] + struct.pack("!H", ~total & 65535) + ip[12:] + udp


def observe(txdev, rxdev, frame, expected, match=None):
    with (
        socket.socket(socket.AF_PACKET, socket.SOCK_RAW, socket.htons(3)) as rx,
        socket.socket(socket.AF_PACKET, socket.SOCK_RAW, socket.htons(3)) as tx,
    ):
        rx.bind((rxdev, 0))
        tx.bind((txdev, 0))
        rx.settimeout(0.12)
        try:
            tx.send(frame)
        except OSError as exc:
            if expected or exc.errno not in (errno.ENOBUFS, errno.ENETDOWN):
                raise
        seen = False
        end = time.monotonic() + 0.12
        while time.monotonic() < end:
            try:
                received = rx.recv(65535)
                if match(received) if match else received == frame:
                    seen = True
                    break
            except TimeoutError:
                break
        require(
            seen == expected, f"{txdev}->{rxdev}: observed {seen}, expected {expected}"
        )
        print(json.dumps({"tx": txdev, "rx": rxdev, "delivered": seen}), flush=True)


def probe(m, frontend, dr):
    def mac(name):
        return bytes.fromhex(m["links"][name]["mac"].replace(":", ""))

    # Advertisement + data egress, independent management for every probe phase.
    for txdev, rxdev, allowed in (("fg0", "fp0", frontend), ("mg0", "mp0", True)):
        for kind in ("arp", "garp", "data"):
            marker = uuid.uuid4().bytes
            dest = b"\xff" * 6 if kind == "garp" else mac(rxdev)
            eth = dest + mac(txdev)
            if kind == "data":
                frame = eth + b"\x08\x00" + packet(CLIENT, VIP, marker)
            else:
                frame = (
                    eth
                    + b"\x08\x06"
                    + struct.pack(
                        "!HHBBH6s4s6s4s",
                        1,
                        0x800,
                        6,
                        4,
                        2,
                        mac(txdev),
                        socket.inet_aton(VIP),
                        dest,
                        socket.inet_aton(VIP),
                    )
                    + marker
                )
            observe(txdev, rxdev, frame, allowed)
    # Client sends to its cached FRONTEND MAC, irrespective of advertisement.
    # Kernel IPVS must emit DR to BACKEND MAC without rewriting destination VIP.
    marker = b"STALE-MAC-DR-" + uuid.uuid4().bytes
    frame = mac("fg0") + mac("fp0") + b"\x08\x00" + packet(CLIENT, VIP, marker)

    def match(data):
        return (
            len(data) >= 42
            and data[:6] == mac("bp0")
            and data[12:14] == b"\x08\x00"
            and data[26:30] == socket.inet_aton(CLIENT)
            and data[30:34] == socket.inet_aton(VIP)
            and data.endswith(marker)
        )

    observe("fp0", "bp0", frame, dr, match)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", required=True)
    parser.add_argument(
        "--phase",
        choices=("down", "activate", "partial", "allow", "deny", "leak"),
        required=True,
    )
    args = parser.parse_args()
    m = json.loads(Path(args.manifest).read_text())
    bridge = ActiveLabBridge(m)
    if args.phase == "down":
        bridge.inspect({"fp0", "bp0", "mg0", "mp0"}, False)
    elif args.phase in ("activate", "partial"):
        try:
            bridge.activate_closed(args.phase == "partial")
        except ValueError as exc:
            if (
                args.phase == "partial"
                and str(exc) == "injected partial activation after first UP"
            ):
                sys.exit(42)
            raise
    else:
        # Parent probes can inspect a partially UP negative fixture too.
        require(
            __import__("os").readlink("/proc/self/ns/net") == m["netns"],
            "probe namespace mismatch",
        )
        probe(m, args.phase == "allow", args.phase in ("allow", "leak"))


if __name__ == "__main__":
    main()
