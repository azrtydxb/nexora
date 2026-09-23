"""Parent-only isolated Linux kernel test. No live VIPs, no host tc changes."""

import argparse
import errno
import json
import os
import select
import signal
import socket
import struct
import subprocess
import sys
import time
import uuid
from pathlib import Path


def run(*args):
    """Run an isolated lab command with a bounded wait and retained failure."""
    return subprocess.run(
        args, check=True, text=True, capture_output=True, timeout=15
    ).stdout


def packet_probe(allowed):
    """Require exact peer frames or their absence, plus management delivery."""
    # Peer receives real Ethernet frames beyond the egress hook. Full byte match
    # ignores unrelated IPv6/ARP chatter. Management has no gate.
    marker = uuid.uuid4().bytes
    eth = bytes.fromhex("020000000002020000000001")
    arp = (
        eth
        + b"\x08\x06"
        + struct.pack(
            "!HHBBH6s4s6s4s",
            1,
            0x800,
            6,
            4,
            2,
            eth[6:12],
            socket.inet_aton("198.18.0.1"),
            eth[:6],
            socket.inet_aton("198.18.0.2"),
        )
        + marker
    )
    garp = (
        bytes.fromhex("ffffffffffff")
        + eth[6:12]
        + b"\x08\x06"
        + struct.pack(
            "!HHBBH6s4s6s4s",
            1,
            0x800,
            6,
            4,
            2,
            eth[6:12],
            socket.inet_aton("198.18.0.1"),
            bytes.fromhex("ffffffffffff"),
            socket.inet_aton("198.18.0.1"),
        )
        + marker
    )
    payload = b"FG05-UDP-" + marker
    udp = struct.pack("!HHHH", 53001, 53, 8 + len(payload), 0) + payload
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
        socket.inet_aton("198.18.0.1"),
        socket.inet_aton("198.18.0.2"),
    )
    total = sum(struct.unpack("!10H", ip))
    total = (total & 65535) + (total >> 16)
    total = (total & 65535) + (total >> 16)
    ip = ip[:10] + struct.pack("!H", ~total & 65535) + ip[12:]
    data = eth + b"\x08\x00" + ip + udp
    for interface, peer, expected in [("fg0", "fp0", allowed), ("mg0", "mp0", True)]:
        for kind, frame in [("arp", arp), ("garp", garp), ("data", data)]:
            with (
                socket.socket(socket.AF_PACKET, socket.SOCK_RAW, socket.htons(3)) as rx,
                socket.socket(socket.AF_PACKET, socket.SOCK_RAW, socket.htons(3)) as tx,
            ):
                rx.bind((peer, 0))
                tx.bind((interface, 0))
                rx.settimeout(0.15)
                try:
                    tx.send(frame)
                except OSError as exc:
                    # TC_ACT_SHOT may be surfaced by AF_PACKET as ENOBUFS.
                    # Peer absence below is still required; error alone is no proof.
                    if expected or exc.errno != errno.ENOBUFS:
                        raise
                found = False
                until = time.monotonic() + 0.15
                while time.monotonic() < until:
                    try:
                        if rx.recv(65535) == frame:
                            found = True
                            break
                    except TimeoutError:
                        break
                if found != expected:
                    raise RuntimeError(
                        f"{interface} {kind}: observed={found}, expected={expected}"
                    )
                print(
                    json.dumps(
                        {"interface": interface, "kind": kind, "delivered": found}
                    ),
                    flush=True,
                )


def main():
    """Create only fresh owned lab resources after explicit parent invocation."""
    if not __debug__:
        raise RuntimeError("optimized Python disables assertions; refused")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute-isolated-kernel-test", action="store_true")
    parser.add_argument("--packet-probe", choices=["allow", "deny"])
    args = parser.parse_args()
    if args.packet_probe:
        packet_probe(args.packet_probe == "allow")
        return
    if not args.execute_isolated_kernel_test:
        parser.error("parent must explicitly pass --execute-isolated-kernel-test")
    if sys.platform != "linux" or os.geteuid() != 0:
        raise RuntimeError("requires root on disposable Linux host; no fallback")
    root = Path(__file__).resolve().parent
    namespace = "nxfg05-" + uuid.uuid4().hex[:12]
    token = "nexora-fence:" + uuid.uuid4().hex
    driver = None
    created = False
    try:
        run("ip", "netns", "add", namespace)
        created = True
        prefix = ["ip", "netns", "exec", namespace]
        for front, peer in [("fg0", "fp0"), ("mg0", "mp0")]:
            run(
                "ip",
                "-n",
                namespace,
                "link",
                "add",
                front,
                "type",
                "veth",
                "peer",
                "name",
                peer,
            )
        run("ip", "-n", namespace, "link", "set", "fg0", "alias", token)
        command = [
            *prefix,
            str(root / "fence-driver"),
            "fg0",
            token,
            str(root / "gate.bpf.o"),
        ]
        for object_name, reason in [
            ("missing-map.bpf.o", "missing/extra program/map"),
            ("unsupported.bpf.o", "kernel gate unsupported/load failed"),
        ]:
            negative = command[:-1] + [str(root / object_name)]
            result = subprocess.run(
                negative,
                input="",
                text=True,
                capture_output=True,
                check=False,
                timeout=15,
            )
            assert result.returncode != 0 and reason in result.stderr, result.stderr
            print(f"REFUSED {object_name}: {reason}", flush=True)
        driver = subprocess.Popen(
            command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, bufsize=1
        )

        def response():
            if not select.select([driver.stdout], [], [], 5)[0]:
                raise RuntimeError("driver response timeout")
            line = driver.stdout.readline().strip()
            print(line, flush=True)
            if not line:
                raise RuntimeError(
                    "driver exited; unsupported kernel/load is a failure"
                )
            return line

        def send(line):
            driver.stdin.write(line + "\n")
            driver.stdin.flush()
            return response()

        assert response().startswith("READY ")
        # Existing hook is foreign to every new driver: no reuse of its map/filter.
        refused = subprocess.run(
            command, input="", text=True, capture_output=True, check=False, timeout=15
        )
        assert refused.returncode != 0, "existing hook was taken over"
        foreign = command.copy()
        foreign[-2] = "nexora-fence:" + "f" * 32
        assert (
            subprocess.run(
                foreign,
                input="",
                text=True,
                capture_output=True,
                check=False,
                timeout=15,
            ).returncode
            != 0
        )
        for name in ["fg0", "fp0", "mg0", "mp0"]:
            run("ip", "-n", namespace, "link", "set", name, "up")

        def probe(allowed):
            print(
                run(
                    *prefix,
                    sys.executable,
                    str(root / "smoke.py"),
                    "--packet-probe",
                    "allow" if allowed else "deny",
                ),
                end="",
                flush=True,
            )

        probe(False)  # absent key must deny ARP and IPv4 data
        ticket = send("CAPTURE").split()
        assert len(ticket) == 3 and ticket[0] == "TICKET"
        assert send("ARM " + ticket[1]) == "ARMED"
        probe(True)
        os.kill(driver.pid, signal.SIGSTOP)
        # Verify stop via proc, not only successful delivery of the signal.
        status = Path(f"/proc/{driver.pid}/status").read_text()
        for _ in range(100):
            if "\nState:\tT" in status:
                break
            time.sleep(0.01)
            status = Path(f"/proc/{driver.pid}/status").read_text()
        assert "\nState:\tT" in status, "owner did not stop"
        probe(True)  # stopping userspace alone does not invalidate a live window
        deadline = int(ticket[2])
        while time.clock_gettime_ns(time.CLOCK_BOOTTIME) <= deadline + 100_000_000:
            time.sleep(0.02)
        probe(False)  # real per-packet kernel denial while owner is SIGSTOP
        os.kill(driver.pid, signal.SIGCONT)
        assert send("ARM " + ticket[1]) == "REJECTED"
        probe(False)
        # Capture before a delayed allocator response; never arm a refreshed TTL.
        delayed = send("CAPTURE").split()
        os.kill(driver.pid, signal.SIGSTOP)
        time.sleep(5.2)
        os.kill(driver.pid, signal.SIGCONT)
        assert send("ARM " + delayed[1]) == "REJECTED"
        probe(False)
        fresh = send("CAPTURE").split()
        assert send("ARM " + fresh[1]) == "ARMED"
        probe(True)
        assert send("DENY") == "DENIED"
        probe(False)
        # Death with a LIVE window must retain the gate and expire independently.
        dying = send("CAPTURE").split()
        assert send("ARM " + dying[1]) == "ARMED"
        driver.stdin.close()
        assert driver.wait(timeout=5) == 0
        probe(True)
        while time.clock_gettime_ns(time.CLOCK_BOOTTIME) <= int(dying[2]) + 100_000_000:
            time.sleep(0.02)
        probe(False)
    finally:
        try:
            if driver and driver.poll() is None:
                os.kill(driver.pid, signal.SIGCONT)
                driver.terminate()
                try:
                    driver.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    driver.kill()
                    driver.wait(timeout=5)
                    raise RuntimeError(
                        "driver required SIGKILL; cleanup is not green"
                    ) from None
        finally:
            if created:
                run("ip", "netns", "delete", namespace)
    print(
        "PASS: kernel expiry blocks ARP+GARP+data with paused owner; management and cleanup pass"
    )


if __name__ == "__main__":
    main()
