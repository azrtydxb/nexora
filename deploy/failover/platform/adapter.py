"""Fail-closed Linux IPVS-DR reconciliation for explicitly provisioned attachments.

No executable or command fragments come from the manifest. The fence provider is
trusted code, supplied by the independent dataplane-fence integration.
"""

import copy
import hashlib
import ipaddress
import json
import os
import re
import subprocess
import sys
from typing import ClassVar, Protocol

SERVICES = (("udp", 53), ("tcp", 53), ("tcp", 853), ("tcp", 443), ("udp", 853))
TABLE = "186"
VIP_LINK = "nxvip"


def require(ok, message):
    """Fail closed with an actionable validation/operation error."""
    if not ok:
        raise ValueError(message)


def fields(obj, names):
    """Reject missing fields and unrecognized configuration extensions."""
    require(
        isinstance(obj, dict) and set(obj) == set(names.split()),
        "unknown/missing fields",
    )


def token(value):
    """Validate a bounded identity safe for markers and namespace names."""
    require(
        isinstance(value, str) and re.fullmatch(r"[a-z0-9][a-z0-9-]{0,39}", value),
        "invalid identity",
    )


def address(value):
    """Require a canonical literal unicast IPv4 endpoint."""
    a = ipaddress.IPv4Address(value)
    require(
        str(a) == value
        and not (
            a.is_multicast
            or a.is_unspecified
            or a.is_loopback
            or a.is_link_local
            or a.is_reserved
        ),
        "unicast IPv4 required",
    )
    return a


def validate(raw):
    """Return an isolated copy of an exact supported local resource manifest."""
    m = copy.deepcopy(raw)
    fields(m, "version uid run group vip services backends attachments")
    require(
        m["version"] == 1 and type(m["version"]) is int, "unsupported manifest version"
    )
    for key in ("uid", "run"):
        token(m[key])
    require(m["group"] in ("dns136", "dns139"), "unsupported group")
    require(
        m["vip"]
        == {"dns136": "192.168.10.136", "dns139": "192.168.10.139"}[m["group"]],
        "stable VIP mismatch",
    )
    require(
        m["services"] == [{"protocol": p, "port": n} for p, n in SERVICES],
        "exact five services required",
    )
    require(
        all(type(service["port"]) is int for service in m["services"]),
        "service ports must be integers",
    )
    expected = {"dns136": ["A", "C"], "dns139": ["B", "D"]}[m["group"]]
    require(
        isinstance(m["backends"], list) and len(m["backends"]) == 2,
        "two backend bindings required",
    )
    for b, slot in zip(m["backends"], expected):
        fields(b, "slot engine_uid address")
        require(b["slot"] == slot, "stable member mapping mismatch")
        token(b["engine_uid"])
        address(b["address"])
        require(
            b["address"] not in ("192.168.10.136", "192.168.10.139"),
            "backend cannot be a frontend",
        )
    for k in ("engine_uid", "address"):
        require(len({b[k] for b in m["backends"]}) == 2, "duplicate backend binding")
    require(
        isinstance(m["attachments"], list) and 1 <= len(m["attachments"]) <= 3,
        "one to three local attachments required",
    )
    roles, namespaces = set(), set()
    for a in m["attachments"]:
        fields(a, "role namespace inode link ifindex mac kind address mtu returns")
        require(
            a["role"] in ["frontend"] + expected and a["role"] not in roles,
            "invalid/duplicate local role",
        )
        roles.add(a["role"])
        token(a["namespace"])
        require(
            a["namespace"].startswith("nx-") and a["namespace"] not in namespaces,
            "dedicated namespace required",
        )
        namespaces.add(a["namespace"])
        require(
            type(a["inode"]) is int and a["inode"] > 0,
            "pinned namespace inode required",
        )
        require(
            isinstance(a["link"], str)
            and re.fullmatch(r"[a-z][a-z0-9]{0,14}", a["link"])
            and a["link"] not in ("lo", VIP_LINK),
            "dedicated link required",
        )
        require(a["kind"] in ("veth", "macvlan"), "unsupported attachment kind")
        require(
            type(a["ifindex"]) is int and a["ifindex"] > 1, "pinned ifindex required"
        )
        require(
            isinstance(a["mac"], str)
            and re.fullmatch(r"[0-9a-f]{2}(:[0-9a-f]{2}){5}", a["mac"])
            and int(a["mac"][:2], 16) & 1 == 0
            and a["mac"] != "00:00:00:00:00:00",
            "unicast MAC required",
        )
        require(type(a["mtu"]) is int and 1280 <= a["mtu"] <= 9000, "invalid MTU")
        iface = ipaddress.IPv4Interface(a["address"])
        require(str(iface) == a["address"], "canonical attachment CIDR required")
        require(
            iface.network.prefixlen <= 30
            and iface.ip
            not in (iface.network.network_address, iface.network.broadcast_address),
            "attachment needs usable shared-segment IPv4",
        )
        address(str(iface.ip))
        require(
            str(iface.ip) not in ("192.168.10.136", "192.168.10.139"),
            "attachment cannot advertise frontend",
        )
        if a["role"] != "frontend":
            binding = next(b for b in m["backends"] if b["slot"] == a["role"])
            require(
                str(iface.ip) == binding["address"],
                "backend attachment binding mismatch",
            )
        else:
            require(
                str(iface.ip) not in {b["address"] for b in m["backends"]},
                "frontend conflicts with backend",
            )
            require(
                all(
                    ipaddress.IPv4Address(b["address"]) in iface.network
                    for b in m["backends"]
                ),
                "DR backends require explicit on-link segment",
            )
            require(
                all(
                    ipaddress.IPv4Address(b["address"])
                    not in (
                        iface.network.network_address,
                        iface.network.broadcast_address,
                    )
                    for b in m["backends"]
                ),
                "backend must not be segment network/broadcast",
            )
        require(
            isinstance(a["returns"], list) and len(a["returns"]) <= 32,
            "invalid return routes",
        )
        require(
            (a["role"] == "frontend" and not a["returns"])
            or (a["role"] != "frontend" and a["returns"]),
            "backend needs explicit return routes; frontend needs none",
        )
        destinations = set()
        for r in a["returns"]:
            fields(r, "destination gateway")
            net = ipaddress.IPv4Network(r["destination"])
            address(str(net.network_address))
            address(str(net.broadcast_address))
            require(
                str(net) == r["destination"]
                and net.prefixlen > 0
                and r["destination"] not in destinations,
                "explicit unique return CIDR required; no default",
            )
            destinations.add(r["destination"])
            require(
                address(r["gateway"]) in iface.network
                and ipaddress.IPv4Address(r["gateway"])
                not in (
                    iface.ip,
                    iface.network.network_address,
                    iface.network.broadcast_address,
                ),
                "return gateway must be explicitly on-link",
            )
        require(
            not destinations.intersection({r["gateway"] + "/32" for r in a["returns"]}),
            "return destination conflicts with gateway host route",
        )
    return m


def identity(m):
    """Bind ownership to the complete validated manifest, UID and run."""
    digest = hashlib.sha256(
        json.dumps(m, sort_keys=True, separators=(",", ":")).encode()
    ).hexdigest()
    return f"nexora-fg04:{m['uid']}:{m['run']}:{digest}"


class Runner(Protocol):
    """Inject command execution and namespace identity lookup for tests."""

    def run(self, args: list[str]) -> str: ...

    def inode(self, namespace: str) -> int: ...


class LinuxRunner:
    """Fixed binaries, argv only, bounded calls; never shell or module loading."""

    binaries: ClassVar[dict[str, str]] = {
        "ip": "/usr/sbin/ip",
        "ipvsadm": "/usr/sbin/ipvsadm",
        "sysctl": "/usr/sbin/sysctl",
    }

    def run(self, args):
        require(sys.platform == "linux", "Linux required")
        argv = list(args)
        require(argv[0] in self.binaries, "unsupported executable")
        # Require visible preloaded capabilities rather than letting creation or
        # ipvsadm turn missing support into an implicit host module installation.
        modules = []
        if "ipvsadm" in argv:
            modules.append("ip_vs")
            if "-A" in argv:
                modules.append("ip_vs_rr")
        if "type" in argv:
            kind = argv[argv.index("type") + 1]
            if kind in ("dummy", "veth", "macvlan"):
                modules.append(kind)
        for module in modules:
            require(
                os.path.isdir("/sys/module/" + module),
                "preloaded kernel capability required: " + module,
            )
        if argv[:3] == ["ip", "netns", "exec"]:
            require(argv[4] in self.binaries, "unsupported namespace executable")
            argv[4] = self.binaries[argv[4]]
        argv[0] = self.binaries[argv[0]]
        result = subprocess.run(
            argv,
            capture_output=True,
            text=True,
            timeout=15,
            check=False,
            env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C"},
        )
        require(
            result.returncode == 0, f"command failed {args!r}: {result.stderr.strip()}"
        )
        return result.stdout

    def inode(self, namespace):
        path = "/run/netns/" + namespace
        require(not os.path.islink(path), "namespace path must not be a symlink")
        return os.stat(path).st_ino


class Fence(Protocol):
    """External provider MUST hold exclusive serialization and a kernel drop fence.

    run_closed verifies exact manifest identity and all attachment inodes, installs
    or verifies traffic AND advertisement denial before callback, retains that
    denial on errors, process death and lease loss, and never activates on return.
    A cooperative lock/check-before-call cannot implement this contract.
    """

    def run_closed(self, manifest: dict, callback): ...


class Adapter:
    """Inspect and reconcile exclusively owned, externally fenced staging state."""

    def __init__(self, manifest, runner):
        self.m = validate(manifest)
        self.owner = identity(self.m)
        self.runner = runner

    def ip(self, a, *args):
        return self.runner.run(["ip", "-n", a["namespace"], *map(str, args)])

    def ns(self, a, binary, *args):
        return self.runner.run(
            ["ip", "netns", "exec", a["namespace"], binary, *map(str, args)]
        )

    def service_lines(self):
        lines = set()
        for proto, port in SERVICES:
            flag = "-t" if proto == "tcp" else "-u"
            key = f"{flag} {self.m['vip']}:{port}"
            lines.add(f"-A {key} -s rr")
            for b in self.m["backends"]:
                lines.add(f"-a {key} -r {b['address']}:{port} -g -w 0")
        return lines

    def desired_routes(self, a):
        if a["role"] == "frontend":
            return {(b["address"] + "/32", None, None) for b in self.m["backends"]}
        gateways = {(r["gateway"] + "/32", None, None) for r in a["returns"]}
        return gateways | {
            (r["destination"], r["gateway"], self.m["vip"]) for r in a["returns"]
        }

    def inspect(self, a):
        """Validate complete exclusive resource pools before emitting any mutation."""
        require(
            self.runner.inode(a["namespace"]) == a["inode"],
            "namespace replaced/missing",
        )
        links = json.loads(self.ip(a, "-d", "-j", "link", "show"))
        by_name = {link["ifname"]: link for link in links}
        require(
            len(by_name) == len(links)
            and set(by_name) <= {"lo", a["link"], VIP_LINK}
            and {"lo", a["link"]} <= set(by_name),
            "unknown links in dedicated namespace",
        )
        require(
            by_name["lo"].get("ifalias") == self.owner + ":namespace:" + a["role"],
            "namespace ownership marker mismatch; no adoption",
        )
        link = by_name[a["link"]]
        require(
            link.get("ifalias") == self.owner + ":attachment:" + a["role"]
            and link["ifindex"] == a["ifindex"]
            and link.get("address") == a["mac"]
            and link.get("mtu") == a["mtu"]
            and link.get("linkinfo", {}).get("info_kind") == a["kind"]
            and "UP" in link.get("flags", []),
            "attachment mismatch",
        )
        vip_link = by_name.get(VIP_LINK)
        require(
            vip_link is not None
            and vip_link.get("ifalias") == self.owner + ":vip:" + a["role"]
            and vip_link.get("linkinfo", {}).get("info_kind") == "dummy"
            and "UP" not in vip_link.get("flags", []),
            "pre-provisioned DOWN owned VIP dummy required; "
            f"absent, mismatched or active VIP link: {vip_link!r}",
        )
        # Pre-provisioner must disable IPv6 on these dedicated attachments.
        addresses = json.loads(self.ip(a, "-j", "address", "show"))
        found, vip_found = [], False
        for dev in addresses:
            require(dev["ifname"] in by_name, "unknown address device")
            for info in dev.get("addr_info", []):
                cidr = f"{info['local']}/{info['prefixlen']}"
                if dev["ifname"] == "lo":
                    require(
                        cidr in ("127.0.0.1/8", "::1/128"),
                        "unexpected loopback address",
                    )
                elif dev["ifname"] == a["link"]:
                    found.append(cidr)
                else:
                    require(
                        a["role"] != "frontend"
                        and cidr == self.m["vip"] + "/32"
                        and info["family"] == "inet",
                        "unexpected VIP address; frontend staging cannot advertise",
                    )
                    vip_found = True
        require(found == [a["address"]], "attachment addresses mismatch")
        for key, value in (
            ("arp_ignore", "1"),
            ("arp_announce", "2"),
            ("rp_filter", "0"),
        ):
            for dev in ("all", a["link"]):
                require(
                    self.ns(a, "sysctl", "-n", f"net.ipv4.conf.{dev}.{key}").strip()
                    == value,
                    "pre-provisioned ARP/return-path settings required",
                )
        require(
            self.ns(a, "sysctl", "-n", "net.ipv4.ip_forward").strip() == "0",
            "namespace forwarding must be disabled during staging",
        )
        # ipvsadm can try modprobe when initialization fails. Check the already
        # present kernel API first, without invoking a loader or writing sysctls.
        require(
            self.ns(a, "sysctl", "-n", "net.ipv4.vs.expire_nodest_conn").strip()
            in ("0", "1"),
            "pre-loaded IPVS kernel support required",
        )
        # Reading IPVS also verifies namespace-local IPVS kernel/userspace support.
        ipvs = {
            line.strip()
            for line in self.ns(a, "ipvsadm", "-Sn").splitlines()
            if line.strip()
        }
        expected = self.service_lines() if a["role"] == "frontend" else set()
        require(
            ipvs <= expected,
            "unknown/mismatched IPVS services or destinations; no adoption",
        )
        # An unused numbered FIB may not exist yet. Dump all tables rather than
        # masking command errors (which could also mean lost capabilities).
        routes = json.loads(self.ip(a, "-j", "-4", "route", "show", "table", "all"))
        have = set()
        for route in routes:
            if str(route.get("protocol")) in ("kernel", "2"):
                require(
                    self.baseline_route(a, route, vip_found), "unexpected kernel route"
                )
                continue
            dst = route["dst"]
            if "/" not in dst:
                dst += "/32"
            key = (dst, route.get("gateway"), route.get("prefsrc"))
            require(
                route.get("dev") == a["link"]
                and str(route.get("table", "main"))
                == ("main" if a["role"] == "frontend" else TABLE)
                and str(route.get("protocol")) in (TABLE, "bgp")
                and route.get("type", "unicast") == "unicast"
                and route.get("scope", "global") == ("global" if key[1] else "link")
                and not (
                    set(route)
                    - {
                        "dst",
                        "gateway",
                        "prefsrc",
                        "dev",
                        "protocol",
                        "scope",
                        "flags",
                        "type",
                        "table",
                    }
                )
                and not (set(route.get("flags", [])) - {"linkdown"}),
                "foreign route in reserved table",
            )
            require(
                key in self.desired_routes(a) and key not in have,
                "unexpected/duplicate owned route",
            )
            have.add(key)
        rules = json.loads(self.ip(a, "-j", "-4", "rule", "show"))
        owned_rule = False
        for rule in rules:
            standard = (
                (rule.get("priority"), str(rule.get("table")))
                in ((0, "local"), (32766, "main"), (32767, "default"))
                and rule.get("src", "all") == "all"
                and set(rule) <= {"priority", "src", "table", "protocol"}
            )
            if standard:
                continue
            require(
                a["role"] != "frontend"
                and rule.get("priority") == 186
                and str(rule.get("table")) == TABLE
                and rule.get("src") in (self.m["vip"], self.m["vip"] + "/32")
                and set(rule) <= {"priority", "src", "table", "protocol"}
                and not owned_rule,
                "unexpected policy rule",
            )
            owned_rule = True
        return {
            "vip": vip_found,
            "ipvs": ipvs,
            "routes": have,
            "rule": owned_rule,
        }

    def baseline_route(self, a, route, vip_found):
        """Only automatic routes for pinned attachment, loopback and owned VIP."""
        if set(route) - {
            "type",
            "dst",
            "dev",
            "protocol",
            "scope",
            "prefsrc",
            "flags",
            "table",
        } or set(route.get("flags", [])) - {"linkdown"}:
            return False
        table = str(route.get("table", "main"))
        dst = route.get("dst", "")
        if "/" not in dst:
            dst += "/32"
        actual = (
            table,
            route.get("type", "unicast"),
            dst,
            route.get("dev"),
            route.get("scope", "global"),
            route.get("prefsrc"),
        )
        iface = ipaddress.IPv4Interface(a["address"])
        ip = str(iface.ip)
        allowed = {
            ("main", "unicast", str(iface.network), a["link"], "link", ip),
            ("local", "local", ip + "/32", a["link"], "host", ip),
        }
        for addr in (iface.network.network_address, iface.network.broadcast_address):
            allowed.add(
                ("local", "broadcast", str(addr) + "/32", a["link"], "link", ip)
            )
        allowed |= {
            ("local", "local", "127.0.0.0/8", "lo", "host", "127.0.0.1"),
            ("local", "local", "127.0.0.1/32", "lo", "host", "127.0.0.1"),
            ("local", "broadcast", "127.255.255.255/32", "lo", "link", "127.0.0.1"),
        }
        if vip_found:
            allowed.add(
                (
                    "local",
                    "local",
                    self.m["vip"] + "/32",
                    VIP_LINK,
                    "host",
                    self.m["vip"],
                )
            )
        return actual in allowed

    def plan(self, cleanup=False):
        states = [(a, self.inspect(a)) for a in self.m["attachments"]]
        commands = []
        for a, state in states:
            ip = ["ip", "-n", a["namespace"]]
            vs = ["ip", "netns", "exec", a["namespace"], "ipvsadm"]
            if cleanup:
                # Delete destinations before services; exact deletion only.
                for line in sorted(state["ipvs"], key=lambda s: s.startswith("-A")):
                    parts = line.split()
                    commands.append(
                        vs
                        + (
                            ["-d"] + parts[1:5]
                            if parts[0] == "-a"
                            else ["-D"] + parts[1:3]
                        )
                    )
                if state["rule"]:
                    commands.append(
                        ip
                        + [
                            "rule",
                            "del",
                            "priority",
                            TABLE,
                            "from",
                            self.m["vip"] + "/32",
                            "table",
                            TABLE,
                        ]
                    )
                for dst, gateway, src in sorted(
                    state["routes"], key=lambda r: (r[1] is None, r[0])
                ):
                    commands.append(ip + self.route_args("del", a, dst, gateway, src))
                if state["vip"]:
                    commands.append(
                        ip + ["address", "del", self.m["vip"] + "/32", "dev", VIP_LINK]
                    )
                continue
            if a["role"] != "frontend" and not state["vip"]:
                commands.append(
                    ip + ["address", "add", self.m["vip"] + "/32", "dev", VIP_LINK]
                )
            for dst, gateway, src in sorted(
                self.desired_routes(a) - state["routes"],
                key=lambda r: (r[1] is not None, r[0]),
            ):
                commands.append(ip + self.route_args("add", a, dst, gateway, src))
            if a["role"] != "frontend" and not state["rule"]:
                commands.append(
                    ip
                    + [
                        "rule",
                        "add",
                        "priority",
                        TABLE,
                        "from",
                        self.m["vip"] + "/32",
                        "table",
                        TABLE,
                    ]
                )
            if a["role"] == "frontend":
                for line in sorted(
                    self.service_lines() - state["ipvs"],
                    key=lambda s: s.startswith("-a"),
                ):
                    commands.append(vs + line.split())
        return commands

    @staticmethod
    def route_args(action, a, dst, gateway, src):
        args = [
            "route",
            action,
            dst,
            "table",
            "main" if a["role"] == "frontend" else TABLE,
            "proto",
            TABLE,
            "dev",
            a["link"],
        ]
        if gateway:
            args += ["via", gateway]
        else:
            args += ["scope", "link"]
        if src:
            args += ["src", src]
        return args

    def reconcile(self, fence: Fence, cleanup=False):
        require(fence is not None, "dataplane fence provider required")

        def apply():
            commands = self.plan(cleanup=cleanup)
            for command in commands:
                # Detect namespace replacement between commands; exclusive external
                # serialization is still required to prevent concurrent tampering.
                for a in self.m["attachments"]:
                    require(
                        self.runner.inode(a["namespace"]) == a["inode"],
                        "namespace replaced during apply",
                    )
                self.runner.run(command)
            require(not self.plan(cleanup=cleanup), "reconciliation did not converge")
            return {
                "identity": self.owner,
                "state": "cleaned" if cleanup else "staged-fenced",
                "commands": len(commands),
                "active": False,
            }

        return fence.run_closed(copy.deepcopy(self.m), apply)


def load_manifest(path):
    """Read strict JSON without accepting ambiguous duplicate fields."""

    def unique(pairs):
        obj = {}
        for key, value in pairs:
            require(key not in obj, "duplicate JSON field")
            obj[key] = value
        return obj

    with open(path, encoding="utf-8") as source:
        return validate(json.load(source, object_pairs_hook=unique))
