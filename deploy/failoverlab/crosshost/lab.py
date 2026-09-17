#!/usr/bin/env python3
"""Explicit local-only cross-host fixture. No SSH, host routes or host links."""

import argparse
import ipaddress
import json
import os
import re
import select
import shutil
import signal
import socket
import subprocess
import sys
import time
from pathlib import Path

VIP = "198.18.0.100"
SERVICES = [("u", 53), ("t", 53), ("t", 853), ("t", 443), ("u", 853)]


def require(ok, message):
    if not ok:
        raise ValueError(message)


def validate(p):
    require(set(p) == {"run", "hosts", "groups", "mtu"}, "unknown/missing plan fields")
    require(
        isinstance(p["run"], str) and re.fullmatch("[a-z0-9]{8}", p["run"]),
        "run: eight lowercase letters/digits",
    )
    require(set(p["hosts"]) == {"left", "right"}, "exactly left/right hosts required")
    for value in p["hosts"].values():
        a = ipaddress.IPv4Address(value)
        require(
            str(a) == value
            and not (
                a.is_unspecified
                or a.is_multicast
                or a.is_loopback
                or a.is_link_local
                or a.is_reserved
            ),
            "literal unicast host IPv4 required",
        )
    require(p["hosts"]["left"] != p["hosts"]["right"], "hosts must differ")
    require(
        type(p["mtu"]) is int and 1280 <= p["mtu"] <= 1400,
        "inner MTU must be 1280..1400",
    )
    require(
        isinstance(p["groups"], list) and len(p["groups"]) == 2,
        "exactly two isolated groups required",
    )
    for g in p["groups"]:
        require(set(g) == {"name", "vni", "port"}, "unknown/missing group fields")
        require(g["name"] in ("g1", "g2"), "group must be g1/g2")
        require(type(g["vni"]) is int and 1 <= g["vni"] <= 16777215, "invalid VNI")
        require(
            type(g["port"]) is int and 49152 <= g["port"] <= 65535,
            "dedicated high UDP port required",
        )
    for key in ("name", "vni", "port"):
        require(len({g[key] for g in p["groups"]}) == 2, f"duplicate {key}")
    return p


def valid_frame(data, vni, mtu):
    # Strict VXLAN flags/reserved bits; only bounded inner Ethernet IPv4/ARP.
    return (
        22 <= len(data) <= mtu + 22
        and data[:4] == b"\x08\0\0\0"
        and data[4:7] == vni.to_bytes(3, "big")
        and data[7] == 0
        and data[20:22] in (b"\x08\0", b"\x08\x06")
    )


def stop_probe(pending):
    # _probe owns a session: its currently running `ip netns exec` child must
    # stop too, otherwise deleting the namespace name leaves a live namespace.
    if pending and pending.poll() is None:
        os.killpg(pending.pid, signal.SIGTERM)
        try:
            pending.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(pending.pid, signal.SIGKILL)
            pending.wait(timeout=10)


def relay(fd, port, vni, mtu):
    """Namespace half: UDP sockets are born here, never moved from host netns."""
    channel = socket.socket(fileno=fd)
    udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    udp.bind(("198.19.0.2", port))
    while True:
        for src in select.select([channel, udp], [], [])[0]:
            data = src.recv(65536)
            require(valid_frame(data, vni, mtu), "invalid namespace VXLAN frame")
            if src is channel:
                udp.sendto(data, ("198.19.0.1", port))
            else:
                channel.send(data)


class Lab:
    def __init__(self, args, plan):
        self.args, self.plan = args, plan
        self.base = Path(args.evidence).resolve()
        self.owned, self.children, self.captures, self.channels = [], [], [], []
        self.probed = False
        self.created_base = False
        self.command_log = None

    def cmd(self, *args):
        if self.command_log:
            self.command_log.write(json.dumps(args) + "\n")
            self.command_log.flush()
        return subprocess.check_output(args, text=True)

    def ns(self, ns, *args):
        return self.cmd("ip", "netns", "exec", ns, *map(str, args))

    def ip(self, ns, *args):
        return self.cmd("ip", "-n", ns, *map(str, args))

    def spawn(self, ns, path, *args, pass_fds=()):
        with open(path, "w") as log:
            p = subprocess.Popen(
                ["ip", "netns", "exec", ns, *map(str, args)],
                stdout=log,
                stderr=subprocess.STDOUT,
                pass_fds=pass_fds,
            )
        self.children.append(p)
        return p

    def setup(self):
        require(
            sys.platform == "linux"
            and os.geteuid() == 0
            and os.getenv("FAILOVER_CROSSHOST_ENABLE") == "yes",
            "Linux root and FAILOVER_CROSSHOST_ENABLE=yes required",
        )
        tools = ["ip", "sysctl", "tcpdump", "arping", "ethtool"]
        if self.args.mode == "snat":
            tools.append("iptables")
        for tool in tools:
            require(shutil.which(tool) is not None, f"missing required tool: {tool}")
        self.base.mkdir(mode=0o700)  # Existing evidence is never reused.
        self.created_base = True
        self.command_log = open(self.base / "commands.jsonl", "w")
        (self.base / "plan.json").write_text(json.dumps(self.plan, indent=2))
        (self.base / "role").write_text(self.args.role)
        (self.base / "mode").write_text(self.args.mode)
        os.environ["FAILOVER_LAB_DIR"] = str(Path(self.args.tls).resolve())
        for name in ("cert.pem", "key.pem"):
            require((Path(self.args.tls) / name).is_file(), f"missing fixture {name}")
        for binary in (self.args.probe, self.args.ipvs):
            require(
                Path(binary).is_absolute() and os.access(binary, os.X_OK),
                "absolute executable required",
            )
        left = self.args.role == "left"
        local, remote = (
            self.plan["hosts"][self.args.role],
            self.plan["hosts"]["right" if left else "left"],
        )
        # Bind all exclusive host sockets BEFORE creating namespaces. No SO_REUSE*.
        for g in self.plan["groups"]:
            udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            self.channels.append((udp, None, g))
            udp.bind((local, g["port"]))
            udp.connect((remote, g["port"]))  # Kernel filters foreign source IP+port.
            # PMTU errors fail the run; don't silently fragment the outer IPv4.
            udp.setsockopt(
                socket.IPPROTO_IP, 10, 2
            )  # Linux IP_MTU_DISCOVER/IP_PMTUDISC_DO
        for idx, (udp, _, g) in enumerate(self.channels):
            out = self.base / g["name"]
            out.mkdir()
            prefix = f"fx-{self.plan['run']}-{g['name']}"
            bridge = prefix + "-sw"
            relay_ns = prefix + "-relay"
            roles = ("a", "c") if left else ("b", "d")
            for ns in [bridge, relay_ns, *(prefix + "-" + r for r in roles)]:
                self.cmd("ip", "netns", "add", ns)
                self.owned.append(ns)  # Only successful creates are cleanup-owned.
                (self.base / "owned.json").write_text(json.dumps(self.owned))
                self.ns(
                    ns,
                    "sysctl",
                    "-qw",
                    "net.ipv6.conf.all.disable_ipv6=1",
                    "net.ipv6.conf.default.disable_ipv6=1",
                )
                self.ip(ns, "link", "set", "lo", "up")
            self.ip(bridge, "link", "add", "br0", "type", "bridge")
            self.ip(
                bridge,
                "link",
                "set",
                "br0",
                "address",
                ("02:00:00:71:00:0" if left else "02:00:00:71:01:0") + g["name"][-1],
                "up",
            )
            # Linux VXLAN binds a wildcard UDP socket; its userspace peer must
            # live in a different namespace, not another loopback address.
            self.ip(
                bridge,
                "link",
                "add",
                "ul0",
                "type",
                "veth",
                "peer",
                "name",
                "ul0",
                "netns",
                relay_ns,
            )
            for ns, address in ((bridge, "198.19.0.1/30"), (relay_ns, "198.19.0.2/30")):
                self.ip(ns, "addr", "add", address, "dev", "ul0")
                self.ip(ns, "link", "set", "ul0", "up")
            self.ip(
                bridge,
                "link",
                "add",
                "vx0",
                "type",
                "vxlan",
                "id",
                g["vni"],
                "local",
                "198.19.0.1",
                "remote",
                "198.19.0.2",
                "dstport",
                g["port"],
                "dev",
                "ul0",
                "nolearning",
            )
            self.ip(
                bridge,
                "link",
                "set",
                "vx0",
                "mtu",
                self.plan["mtu"],
                "master",
                "br0",
                "up",
            )
            host_end, ns_end = socket.socketpair(socket.AF_UNIX, socket.SOCK_DGRAM)
            self.channels[idx] = (udp, host_end, g)
            try:
                self.spawn(
                    relay_ns,
                    out / "relay.log",
                    sys.executable,
                    str(Path(__file__).resolve()),
                    "_relay",
                    ns_end.fileno(),
                    g["port"],
                    g["vni"],
                    self.plan["mtu"],
                    pass_fds=(ns_end.fileno(),),
                )
            finally:
                ns_end.close()
            for r in roles:
                ns = prefix + "-" + r
                self.ip(
                    bridge,
                    "link",
                    "add",
                    "v-" + r,
                    "type",
                    "veth",
                    "peer",
                    "name",
                    "eth0",
                    "netns",
                    ns,
                )
                self.ip(
                    bridge,
                    "link",
                    "set",
                    "v-" + r,
                    "mtu",
                    self.plan["mtu"],
                    "master",
                    "br0",
                    "up",
                )
                self.ip(ns, "link", "set", "eth0", "mtu", self.plan["mtu"], "up")
                # The userspace carrier cannot transport CHECKSUM_PARTIAL/GSO
                # metadata. Complete checksums before frames leave owned veths.
                self.ns(
                    ns,
                    "ethtool",
                    "-K",
                    "eth0",
                    "tx",
                    "off",
                    "tso",
                    "off",
                    "gso",
                    "off",
                    "gro",
                    "off",
                )
                host = {"a": 3, "b": 4, "c": 10, "d": 11}[r]
                self.ip(ns, "addr", "add", f"198.18.0.{host}/24", "dev", "eth0")
                self.ns(
                    ns,
                    "sysctl",
                    "-qw",
                    "net.ipv4.conf.all.rp_filter=0",
                    "net.ipv4.conf.eth0.rp_filter=0",
                    "net.ipv4.conf.all.arp_ignore=1",
                    "net.ipv4.conf.all.arp_announce=2",
                )
                if r in "ab":
                    self.ip(ns, "addr", "add", VIP + "/32", "dev", "lo")
                    if self.args.mode == "duplicate":
                        self.ns(ns, "sysctl", "-qw", "net.ipv4.conf.all.arp_ignore=0")
                    self.spawn(ns, out / f"backend-{r}.log", self.args.probe, "serve")
                elif self.args.mode == "snat":
                    lost = "198.18.0.12" if left else "198.18.0.13"
                    self.ip(ns, "addr", "add", lost + "/32", "dev", "eth0")
                    self.ns(
                        ns,
                        "iptables",
                        "-t",
                        "nat",
                        "-A",
                        "POSTROUTING",
                        "-s",
                        f"198.18.0.{host}/32",
                        "-d",
                        VIP + "/32",
                        "-j",
                        "SNAT",
                        "--to-source",
                        lost,
                    )
                cap = self.spawn(
                    ns,
                    out / f"capture-{r}.log",
                    "tcpdump",
                    "-Z",
                    "root",  # Evidence stays private; never relax directory permissions.
                    "--immediate-mode",
                    "-U",
                    "-nn",
                    "-i",
                    "eth0",
                    "-w",
                    out / f"{r}.pcap",
                    "net 198.18.0.0/24 and (port 53 or port 853 or port 443)",
                )
                self.captures.append((cap, out, r))
            if left:
                self.ip(bridge, "addr", "add", "198.18.0.2/24", "dev", "br0")
                self.ip(bridge, "addr", "add", VIP + "/32", "dev", "br0")
                self.ns(
                    bridge,
                    "sysctl",
                    "-qw",
                    "net.ipv4.ip_forward=1",
                    "net.ipv4.conf.all.rp_filter=0",
                    "net.ipv4.conf.br0.rp_filter=0",
                    "net.ipv4.conf.all.send_redirects=0",
                    "net.ipv4.conf.br0.send_redirects=0",
                )
                for proto, port in SERVICES:
                    self.ns(
                        bridge,
                        self.args.ipvs,
                        "-A",
                        "-" + proto,
                        f"{VIP}:{port}",
                        "-s",
                        "rr",
                    )
                    for real in (3, 4):
                        self.ns(
                            bridge,
                            self.args.ipvs,
                            "-a",
                            "-" + proto,
                            f"{VIP}:{port}",
                            "-r",
                            f"198.18.0.{real}:{port}",
                            "-g",
                        )
            for ns in [bridge, *(prefix + "-" + r for r in roles)]:
                for kind in ("addr", "route", "neigh"):
                    (out / f"{ns}-{kind}.txt").write_text(self.ip(ns, kind, "show"))
        time.sleep(1)  # Fixed startup grace; never repeats a failed probe.
        self.health()

    def health(self):
        require(
            all(p.poll() is None for p in self.children),
            "fixture process exited; inspect logs",
        )

    def probe(self):
        require(
            not self.probed,
            "probe attempt already consumed; use a new run after any failure",
        )
        self.probed = True
        # Probe subprocesses must run while this process services relay sockets.
        return subprocess.Popen(
            [
                sys.executable,
                str(Path(__file__).resolve()),
                "_probe",
                str(self.base),
                self.args.probe,
                self.args.tls,
            ],
            start_new_session=True,
        )

    def run(self):
        control = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        control.bind(str(self.base / "control.sock"))
        control.listen(1)
        control.settimeout(2)
        pending = None
        print(f"READY role={self.args.role} evidence={self.base}", flush=True)
        try:
            while True:
                self.health()
                if pending and pending.poll() is not None:
                    code = pending.returncode
                    (self.base / "probe-exit").write_text(str(code))
                    require(code == 0, "probe failed; run stopped without retry")
                    pending = None
                sources = [control] + [s for u, c, _ in self.channels for s in (u, c)]
                for src in select.select(sources, [], [], 0.2)[0]:
                    if src is control:
                        with control.accept()[0] as conn:
                            conn.settimeout(2)
                            action = conn.recv(64).decode().strip()
                            if action == "probe":
                                pending = self.probe()
                                conn.sendall(b"probe started; inspect probe-exit\n")
                            elif action == "finish":
                                require(
                                    pending is None and self.probed,
                                    "finish requires completed probe",
                                )
                                if self.args.role == "left":
                                    for g in self.plan["groups"]:
                                        ns = f"fx-{self.plan['run']}-{g['name']}-sw"
                                        (
                                            self.base / g["name"] / "ipvs-stats.txt"
                                        ).write_text(
                                            self.ns(
                                                ns, self.args.ipvs, "-Ln", "--stats"
                                            )
                                        )
                                conn.sendall(b"finishing\n")
                                return
                            else:
                                raise ValueError("unknown control action")
                    else:
                        udp, channel, g = next(
                            item for item in self.channels if src in item[:2]
                        )
                        data = src.recv(65536)
                        require(
                            valid_frame(data, g["vni"], self.plan["mtu"]),
                            "foreign VNI/invalid frame: isolation failure",
                        )
                        (channel if src is udp else udp).send(data)
        finally:
            stop_probe(pending)
            control.close()

    def cleanup(self):
        errors = []
        for cap, out, r in self.captures:
            if cap.poll() is None:
                cap.send_signal(signal.SIGINT)
            try:
                require(cap.wait(timeout=10) == 0, "capture failed")
                (out / f"packets-{r}.txt").write_text(
                    self.cmd(
                        "tcpdump",
                        "-Z",
                        "root",
                        "-nn",
                        "-tt",
                        "-r",
                        str(out / f"{r}.pcap"),
                    )
                )
            except Exception as e:
                errors.append(str(e))
        for p in self.children:
            if p.poll() is None:
                p.terminate()
                try:
                    p.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    p.kill()
                    p.wait()
        for ns in reversed(self.owned):
            try:
                self.cmd("ip", "netns", "del", ns)
            except Exception as e:
                errors.append(str(e))
        for udp, channel, _ in self.channels:
            udp.close()
            if channel:
                channel.close()
        if self.created_base:
            (self.base / "cleanup.json").write_text(json.dumps({"errors": errors}))
        if self.command_log:
            self.command_log.close()
        require(not errors, "cleanup incomplete: " + "; ".join(errors))


def probes(base, probe, tls):
    p = validate(json.loads((base / "plan.json").read_text()))
    left = (base / "role").read_text() == "left"
    mode = (base / "mode").read_text()
    os.environ["FAILOVER_LAB_DIR"] = str(Path(tls).resolve())
    for g in p["groups"]:
        ns = f"fx-{p['run']}-{g['name']}-{'c' if left else 'd'}"
        expected = "198.18.0.10" if left else "198.18.0.11"
        with open(base / g["name"] / "arp.log", "x") as log:
            result = subprocess.run(
                [
                    "ip",
                    "netns",
                    "exec",
                    ns,
                    "arping",
                    "-b",
                    "-I",
                    "eth0",
                    "-c",
                    "3",
                    "-w",
                    "4",
                    VIP,
                ],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                timeout=6,
            )
            log.write(result.stdout)
            macs = set(re.findall(r"\[([0-9a-fA-F:]{17})\]", result.stdout.lower()))
            expected_mac = "02:00:00:71:00:0" + g["name"][-1]
            require(
                result.returncode == 0 and expected_mac in macs,
                "ARP control missing frontend reply",
            )
            if mode == "duplicate":
                require(len(macs) > 1, "duplicate advertisement was not detected")
                continue
            require(macs == {expected_mac}, "duplicate VIP advertisement detected")
        with open(base / g["name"] / "probe.log", "x") as log:
            for _ in range(2 if mode == "dr" else 1):
                for transport in ("udp", "tcp", "dot", "doh", "doq"):
                    result = subprocess.run(
                        [
                            "ip",
                            "netns",
                            "exec",
                            ns,
                            probe,
                            "probe",
                            VIP,
                            expected,
                            transport,
                        ],
                        text=True,
                        stdout=subprocess.PIPE,
                        stderr=subprocess.STDOUT,
                        timeout=15,
                    )
                    log.write(result.stdout)
                    log.flush()
                    if mode == "dr":
                        require(
                            result.returncode == 0, "failed probe: " + result.stdout
                        )
                    else:
                        require(
                            result.returncode != 0
                            and re.search(
                                transport
                                + r" source mismatch:.*"
                                + re.escape("198.18.0.12" if left else "198.18.0.13")
                                + ".*expected "
                                + re.escape(expected),
                                result.stdout,
                            ),
                            "negative control failed for wrong reason: "
                            + result.stdout,
                        )


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "_relay":
        relay(*map(int, sys.argv[2:]))
        return
    if len(sys.argv) > 1 and sys.argv[1] == "_probe":
        probes(Path(sys.argv[2]), *sys.argv[3:])
        return
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    v = sub.add_parser("validate")
    v.add_argument("plan")
    r = sub.add_parser("run")
    r.add_argument("plan")
    r.add_argument("--role", choices=["left", "right"], required=True)
    r.add_argument("--evidence", required=True)
    r.add_argument("--probe", required=True)
    r.add_argument("--ipvs", required=True)
    r.add_argument("--tls", required=True)
    r.add_argument("--mode", choices=["dr", "snat", "duplicate"], default="dr")
    c = sub.add_parser("control")
    c.add_argument("evidence")
    c.add_argument("command", choices=["probe", "finish"])
    args = parser.parse_args()
    if args.action == "control":
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as s:
            s.connect(str(Path(args.evidence) / "control.sock"))
            s.sendall(args.command.encode())
            print(s.recv(1024).decode())
        return
    p = validate(json.loads(Path(args.plan).read_text()))
    if args.action == "validate":
        print(json.dumps(p, indent=2))
        return
    os.umask(0o077)
    lab = Lab(args, p)

    def interrupted(signum, frame):
        raise RuntimeError(f"interrupted by signal {signum}")

    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        lab.setup()
        lab.run()
    finally:
        lab.cleanup()
    (lab.base / "run-complete").write_text("0")


if __name__ == "__main__":
    main()
