"""Exact isolated all-egress bridge. Never a production Adapter override."""

import hashlib
import json
import os
import subprocess
from pathlib import Path

NAMES = ("fg0", "fp0", "bg0", "bp0", "mg0", "mp0")
PAIRS = (("fg0", "fp0"), ("bg0", "bp0"), ("mg0", "mp0"))
GATED = ("fg0", "bg0")


def require(ok, reason):
    if not ok:
        raise ValueError(reason)


def run(*args):
    result = subprocess.run(
        args,
        check=True,
        capture_output=True,
        text=True,
        timeout=10,
        env={"LC_ALL": "C", "PATH": "/usr/sbin:/usr/bin"},
    )
    require(len(result.stdout) <= 65536, "oversized lab inventory")
    return result.stdout


def forbidden_offloads(features):
    """Reject segmentation/coalescing and hardware TC; veth checksum metadata
    is software-only and does not bypass the egress hook in this lab contract.
    Unknown reported feature states are never interpreted as off.
    """
    result = []
    for row in features.splitlines():
        fields = row.strip().split()
        if len(fields) < 2:
            continue
        key = fields[0].removesuffix(":")
        if (
            any(
                token in key
                for token in ("segmentation", "-gro", "-gso", "receive-offload")
            )
            or key == "hw-tc-offload"
        ):
            require(fields[1] in ("on", "off"), "unknown offload state")
            if fields[1] == "on":
                result.append(key)
    return result


def digest(m):
    return hashlib.sha256(
        json.dumps(m, sort_keys=True, separators=(",", ":")).encode()
    ).hexdigest()


def alias(m, name):
    return "nexora-fence:" + digest(m) + "." + name


def trusted(path, executable=False):
    p = Path(path)
    require(
        p.is_absolute() and str(p.resolve()) == str(p), "canonical code path required"
    )
    for item in (p, *p.parents):
        st = item.lstat()
        require(
            st.st_uid == 0 and not st.st_mode & 0o022 and not item.is_symlink(),
            "immutable administrator-owned code/ancestor required",
        )
    require(
        p.is_file() and (not executable or os.access(p, os.X_OK)), "invalid code file"
    )


def validate(m, links, up):
    require(
        set(m) == {"version", "run", "netns", "links"}
        and m["version"] == "isolated-dual-v1",
        "explicit isolated manifest required",
    )
    require(
        len(m["run"]) == 32 and all(c in "0123456789abcdef" for c in m["run"]),
        "invalid run",
    )
    require(set(m["links"]) == set(NAMES), "exact manifest attachment set required")
    by = {x["ifname"]: x for x in links}
    require(
        len(by) == len(links) == 7 and set(by) == {*NAMES, "lo"},
        "extra/replaced interface",
    )
    require(
        by["lo"].get("ifalias") == "nexora-lab:" + digest(m),
        "namespace ownership mismatch",
    )
    require(
        by["lo"].get("link_type") == "loopback"
        and not by["lo"].get("xdp")
        and "master" not in by["lo"]
        and "link_netnsid" not in by["lo"],
        "foreign loopback topology",
    )
    indices = set()
    for name in NAMES:
        expected, link = m["links"][name], by[name]
        require(set(expected) == {"index", "peer", "mac"}, "manifest schema mismatch")
        require(
            link["ifindex"] == expected["index"]
            and link["link_index"] == expected["peer"]
            and link["address"] == expected["mac"]
            and link.get("mtu") == 1500
            and link.get("ifalias") == alias(m, name),
            "exact attachment mismatch",
        )
        require(
            link["ifindex"] not in indices and link["ifindex"] > 0, "duplicate index"
        )
        indices.add(link["ifindex"])
        require(
            link.get("linkinfo", {}).get("info_kind") == "veth"
            and "master" not in link
            and "link_netnsid" not in link
            and not link.get("addr_info")
            and not link.get("xdp"),
            "foreign topology/address/XDP",
        )
        require(
            ("UP" in link.get("flags", [])) == (name in up),
            "unexpected activation state",
        )
    for left, right in PAIRS:
        require(
            m["links"][left]["peer"] == m["links"][right]["index"]
            and m["links"][right]["peer"] == m["links"][left]["index"],
            "external/nonreciprocal peer",
        )
    for address in by["lo"].get("addr_info", []):
        require(
            (address["local"], address["prefixlen"])
            in (("127.0.0.1", 8), ("::1", 128)),
            "foreign loopback address",
        )


class ActiveLabBridge:
    """Single-use DOWN -> attached DENY -> UP transition on owned lab veths.

    Driver READY2 is required by the Go caller before this class is invoked.
    Both TC filters must read back the same program ID. There is no second gate,
    alias rewrite, hook adoption, frontend IP assignment or production manifest.
    """

    def __init__(self, manifest):
        self.m = manifest

    def inspect(self, up, attached):
        require(
            os.readlink("/proc/self/ns/net")
            == self.m["netns"]
            != os.readlink("/proc/1/ns/net"),
            "namespace replaced or not isolated",
        )
        validate(
            self.m, json.loads(run("/usr/sbin/ip", "-j", "-d", "address", "show")), up
        )
        for family in ("-4", "-6"):
            routes = json.loads(
                run("/usr/sbin/ip", "-j", family, "route", "show", "table", "all")
            )
            for route in routes:
                dev, dst = route.get("dev"), route.get("dst")
                allowed = (
                    dev == "lo"
                    and dst in ("127.0.0.0/8", "127.0.0.1", "127.255.255.255", "::1")
                    or family == "-4"
                    and dev == "lo"
                    and dst in ("198.18.100.53", "198.18.100.53/32")
                    and route.get("type") == "local"
                    or family == "-4"
                    and dev == "bg0"
                    and dst in ("198.18.101.2", "198.18.101.2/32")
                    and route.get("scope") == "link"
                )
                require(
                    allowed
                    and not any(
                        k in route for k in ("gateway", "multipath", "encap", "nhid")
                    ),
                    "foreign lab route",
                )
            rules = json.loads(run("/usr/sbin/ip", "-j", family, "rule", "show"))
            for rule in rules:
                require(
                    (rule.get("priority"), str(rule.get("table")))
                    in ((0, "local"), (32766, "main"), (32767, "default"))
                    and rule.get("src", "all") == "all"
                    and set(rule) <= {"priority", "src", "table", "protocol"},
                    "foreign policy rule",
                )
        require(
            set(run("/usr/sbin/ipvsadm", "-Sn").splitlines())
            == {
                "-A -u 198.18.100.53:53 -s rr",
                "-a -u 198.18.100.53:53 -r 198.18.101.2:53 -g -w 1",
            },
            "foreign IPVS configuration",
        )
        for name in ("all", "default", *NAMES):
            require(
                Path(f"/proc/sys/net/ipv6/conf/{name}/disable_ipv6").read_text().strip()
                == "1",
                "IPv6 bypass path",
            )
        # Inherited cgroup hooks are outside netns link inventory. Require a
        # visible canonical cgroup-v2 hierarchy and reject effective ancestors.
        cgroup = Path("/proc/self/cgroup").read_text().splitlines()
        require(
            len(cgroup) == 1 and cgroup[0].startswith("0::/"),
            "visible cgroup v2 required",
        )
        relative = cgroup[0][3:]
        require(".." not in Path(relative).parts, "ambiguous cgroup path")
        group = Path("/sys/fs/cgroup") / relative.lstrip("/")
        require(
            str(group.resolve()) == str(group) and (group / "cgroup.procs").is_file(),
            "canonical cgroup hierarchy required",
        )
        require(
            json.loads(
                run(
                    "/usr/sbin/bpftool", "-j", "cgroup", "show", str(group), "effective"
                )
            )
            == [],
            "foreign inherited cgroup BPF hooks",
        )
        nft = json.loads(run("/usr/sbin/nft", "-j", "list", "ruleset"))
        require(
            all(set(item) == {"metainfo"} for item in nft.get("nftables", [])),
            "foreign netfilter hooks",
        )
        for table in ("ip_tables_names", "ip6_tables_names", "arp_tables_names"):
            path = Path("/proc/net") / table
            require(
                not path.exists() or not path.read_text().strip(),
                "foreign legacy netfilter hooks",
            )
        ids = []
        for name in (*NAMES, "lo"):
            # Hardware/segmentation offload is outside the candidate contract.
            features = run("/usr/sbin/ethtool", "-k", name) if name != "lo" else ""
            for feature in (
                "generic-receive-offload",
                "generic-segmentation-offload",
                "tcp-segmentation-offload",
                "large-receive-offload",
                "hw-tc-offload",
            ):
                rows = [
                    s.strip()
                    for s in features.splitlines()
                    if s.strip().startswith(feature + ":")
                ]
                require(
                    name == "lo" or (len(rows) == 1 and rows[0].split()[1] == "off"),
                    "offload enabled/unknown: " + feature,
                )
            require(
                not forbidden_offloads(features),
                "segmentation/coalescing offload remains enabled",
            )
            qdiscs = json.loads(run("/usr/sbin/tc", "-j", "qdisc", "show", "dev", name))
            expected = (
                {"noqueue", "clsact"} if attached and name in GATED else {"noqueue"}
            )
            require(
                {q["kind"] for q in qdiscs} == expected
                and len(qdiscs) == len(expected),
                "foreign qdisc",
            )
            for direction in ("ingress", "egress"):
                filters = json.loads(
                    run("/usr/sbin/tc", "-j", "filter", "show", "dev", name, direction)
                )
                if attached and name in GATED and direction == "egress":
                    require(len(filters) == 1, "exact owned egress hook required")
                    f = filters[0]
                    options = f.get("options", {})
                    require(
                        f.get("kind") == "bpf"
                        and f.get("pref") == 1
                        and options.get("handle") in (1, "0x1")
                        and options.get("direct-action") is True
                        and not options.get("in_hw")
                        and options.get("id", 0) > 0,
                        "foreign/offloaded egress filter",
                    )
                    ids.append(options["id"])
                else:
                    require(not filters, "foreign hook")
        require(
            not attached or len(ids) == 2 and ids[0] == ids[1],
            "not one shared kernel program/map",
        )

    def activate_closed(self, fail_after_first=False):
        initial = {"fp0", "bp0", "mg0", "mp0"}
        self.inspect(initial, True)
        # Caller just acknowledged DENY. If either operation/readback fails it
        # must never CAS/ARM. Any already-UP link retains the empty shared map.
        run("/usr/sbin/ip", "link", "set", "fg0", "up")
        if fail_after_first:
            raise ValueError("injected partial activation after first UP")
        run("/usr/sbin/ip", "link", "set", "bg0", "up")
        self.inspect(set(NAMES), True)
