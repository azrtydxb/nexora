#!/usr/bin/env python3
"""Opt-in, isolated kernel reconciliation test. Never attaches to host networking."""

import argparse
import json
import os
import subprocess
import sys
import uuid
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from adapter import Adapter, LinuxRunner, identity, require
from test_adapter import manifest


def command(args, pass_fds=()):
    """Run one fixed test-isolation operation without a shell."""
    subprocess.run(
        args,
        check=True,
        timeout=120,
        pass_fds=pass_fds,
        env={**os.environ, "LC_ALL": "C"},
    )


class IsolatedFence:
    """Only valid for this detached test topology, NOT a production provider."""

    def __init__(self, runner, original_netns, manifest):
        self.runner = runner
        self.original_netns = original_netns
        self.owner = identity(manifest)
        self.attachments = [
            (a["namespace"], a["inode"]) for a in manifest["attachments"]
        ]

    def run_closed(self, m, callback):
        require(identity(m) == self.owner, "detached fence manifest changed")
        for namespace, inode in self.attachments:
            require(
                self.runner.inode(namespace) == inode,
                "detached attachment namespace changed",
            )
        require(
            os.readlink("/proc/self/ns/net") != self.original_netns,
            "outer namespace isolation missing",
        )
        links = json.loads(self.runner.run(["ip", "-j", "link", "show"]))
        require(
            {link["ifname"] for link in links} == {"lo", "peer1", "peer2", "peer3"},
            "unexpected outer attachment",
        )
        require(
            all("UP" not in link.get("flags", []) for link in links),
            "outer test links must remain down",
        )
        return callback()


def isolated(namespace_fds, exercise=None):
    """Prove isolation before provisioning a disposable local fixture."""
    runner = LinuxRunner()
    require(os.getpid() == 1, "isolated PID namespace required")
    original_netns = None
    for kind, fd in zip(("net", "mnt"), namespace_fds):
        original = os.readlink(f"/proc/self/fd/{fd}")
        require(
            original.startswith(kind + ":[")
            and original != os.readlink("/proc/self/ns/" + kind),
            "inherited namespace FD and distinct isolated namespace required",
        )
        if kind == "net":
            original_netns = original
    require(
        not any(
            " shared:" in line
            for line in Path("/proc/self/mountinfo").read_text().splitlines()
        ),
        "private mount propagation required",
    )
    # No module loading: the parent must pre-provision the kernel capability.
    require(
        Path("/proc/net/ip_vs").exists(),
        "IPVS must already be available; this test does not load modules",
    )
    require(
        Path("/run/netns").is_dir() and not Path("/run/netns").is_symlink(),
        "pre-existing /run/netns directory required",
    )
    command(["/usr/bin/mount", "-t", "tmpfs", "-o", "mode=0700", "tmpfs", "/run/netns"])
    m = manifest()
    m["uid"] = "isolated-fg04"
    m["run"] = uuid.uuid4().hex
    owned = []
    try:
        for i, a in enumerate(m["attachments"], 1):
            ns = a["namespace"]
            runner.run(["ip", "netns", "add", ns])
            inode = runner.inode(ns)
            owned.append((ns, inode))
            a["inode"] = inode
            runner.run(
                [
                    "ip",
                    "link",
                    "add",
                    f"peer{i}",
                    "type",
                    "veth",
                    "peer",
                    "name",
                    a["link"],
                    "netns",
                    ns,
                ]
            )
            ip = ["ip", "-n", ns]
            runner.run(
                ip
                + [
                    "link",
                    "set",
                    "dev",
                    a["link"],
                    "address",
                    a["mac"],
                    "mtu",
                    str(a["mtu"]),
                ]
            )
            links = json.loads(
                runner.run(ip + ["-j", "link", "show", "dev", a["link"]])
            )
            a["ifindex"] = links[0]["ifindex"]
            for dev in ("all", "default", "lo", a["link"]):
                runner.run(
                    [
                        "ip",
                        "netns",
                        "exec",
                        ns,
                        "sysctl",
                        "-qw",
                        f"net.ipv6.conf.{dev}.disable_ipv6=1",
                    ]
                )
            for dev in ("all", a["link"]):
                for key, value in (
                    ("arp_ignore", 1),
                    ("arp_announce", 2),
                    ("rp_filter", 0),
                ):
                    runner.run(
                        [
                            "ip",
                            "netns",
                            "exec",
                            ns,
                            "sysctl",
                            "-qw",
                            f"net.ipv4.conf.{dev}.{key}={value}",
                        ]
                    )
            runner.run(
                ["ip", "netns", "exec", ns, "sysctl", "-qw", "net.ipv4.ip_forward=0"]
            )
            runner.run(ip + ["address", "add", a["address"], "dev", a["link"]])
            runner.run(ip + ["link", "set", "dev", a["link"], "up"])
            # This creator owns the fresh isolated namespace until handover.
            # Do not assume the kernel preserves an alias on link creation.
            runner.run(ip + ["link", "add", "name", "nxvip", "type", "dummy"])
        owner = identity(m)
        for a in m["attachments"]:
            for dev, suffix in (
                ("lo", ":namespace:"),
                (a["link"], ":attachment:"),
                ("nxvip", ":vip:"),
            ):
                runner.run(
                    [
                        "ip",
                        "-n",
                        a["namespace"],
                        "link",
                        "set",
                        "dev",
                        dev,
                        "alias",
                        owner + suffix + a["role"],
                    ]
                )
        adapter = Adapter(m, runner)
        fence = IsolatedFence(runner, original_netns, m)
        # Read back every marker/configuration before handing over to staging.
        # Any provisioning/readback failure reaches owned namespace cleanup.
        baseline = [adapter.inspect(a) for a in m["attachments"]]
        require(not adapter.plan(cleanup=True), "provisioned baseline is not empty")
        expected = len(adapter.plan())
        exercise = exercise or exercise_stage
        exercise(adapter, fence, baseline, expected)
    finally:
        errors = []
        for ns, inode in reversed(owned):
            try:
                require(
                    runner.inode(ns) == inode, "refusing cleanup of replaced namespace"
                )
                runner.run(["ip", "netns", "delete", ns])
            except (ValueError, OSError) as exc:
                errors.append(str(exc))
        require(not errors, "isolated namespace cleanup failed: " + "; ".join(errors))
    if exercise is not exercise_stage:
        return
    print(
        json.dumps(
            {
                "result": "PASS",
                "scope": "isolated Linux stage/repeat/restart/cleanup",
                "active": False,
                "mutations": expected,
            }
        )
    )


def exercise_stage(adapter, fence, baseline, expected):
    """Run the original stage/repeat/restart/cleanup regression."""
    staged = adapter.reconcile(fence)
    require(staged["commands"] == expected and not adapter.plan(), "stage failed")
    require(adapter.reconcile(fence)["commands"] == 0, "repeated stage not idempotent")
    Adapter(adapter.m, adapter.runner).reconcile(fence, cleanup=True)
    require(
        adapter.reconcile(fence, cleanup=True)["commands"] == 0,
        "cleanup not idempotent",
    )
    require(
        [adapter.inspect(a) for a in adapter.m["attachments"]] == baseline
        and len(adapter.plan()) == expected,
        "cleanup did not restore owned baseline",
    )


def main():
    """Require explicit opt-in and launch an isolated child with namespace FDs."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute", action="store_true")
    parser.add_argument("--isolated-fds", type=int, nargs=2, help=argparse.SUPPRESS)
    args = parser.parse_args()
    require(
        args.execute and os.getenv("NEXORA_FG04_ISOLATED_TEST") == "yes",
        "requires --execute and NEXORA_FG04_ISOLATED_TEST=yes",
    )
    require(sys.platform == "linux" and os.geteuid() == 0, "Linux root required")
    if args.isolated_fds:
        isolated(args.isolated_fds)
    else:
        fds = (
            os.open("/proc/self/ns/net", os.O_RDONLY),
            os.open("/proc/self/ns/mnt", os.O_RDONLY),
        )
        try:
            command(
                [
                    "/usr/bin/unshare",
                    "--mount",
                    "--net",
                    "--pid",
                    "--fork",
                    "--kill-child=SIGKILL",
                    "--mount-proc",
                    "--propagation",
                    "private",
                    sys.executable,
                    str(Path(__file__).resolve()),
                    "--execute",
                    "--isolated-fds",
                    *map(str, fds),
                ],
                pass_fds=fds,
            )
        finally:
            for fd in fds:
                os.close(fd)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as exc:
        sys.exit(str(exc))
