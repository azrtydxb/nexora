import copy
import importlib.util
import json
import unittest
from pathlib import Path
from unittest.mock import patch

from adapter import SERVICES, Adapter, LinuxRunner, identity, validate


def manifest():
    """Return a detached three-role fixture, never live resource inventory."""
    m = {
        "version": 1,
        "uid": "group-uid-136",
        "run": "run57f032b7",
        "group": "dns136",
        "vip": "192.168.10.136",
        "services": [{"protocol": p, "port": n} for p, n in SERVICES],
        "backends": [
            {"slot": "A", "engine_uid": "persistent-a", "address": "198.18.0.2"},
            {"slot": "C", "engine_uid": "persistent-c", "address": "198.18.0.3"},
        ],
        "attachments": [],
    }
    for i, role in enumerate(("frontend", "A", "C"), 1):
        m["attachments"].append(
            {
                "role": role,
                "namespace": f"nx-test-{i}",
                "inode": 100 + i,
                "link": "dr0",
                "ifindex": i + 1,
                "mac": f"02:00:00:00:00:{i:02x}",
                "kind": "veth",
                "address": f"198.18.0.{i}/24",
                "mtu": 1400,
                "returns": []
                if role == "frontend"
                else [{"destination": "198.19.0.0/24", "gateway": "198.18.0.254"}],
            }
        )
    return m


class ClosedFence:
    """Test double only; never export as a runtime fence provider."""

    def __init__(self):
        self.calls = 0

    def run_closed(self, m, callback):
        self.calls += 1
        return callback()


class Kernel:
    """Persistent fake kernel, deliberately separate from the adapter instance."""

    def __init__(self, m):
        self.m = m
        self.data = {}
        self.commands = []
        self.fail_at = None
        self.fail_after = None
        self.mutations = 0
        for a in m["attachments"]:
            owner = identity(m)
            self.data[a["namespace"]] = {
                "inode": a["inode"],
                "links": [
                    {"ifname": "lo", "ifalias": owner + ":namespace:" + a["role"]},
                    {
                        "ifname": a["link"],
                        "ifalias": owner + ":attachment:" + a["role"],
                        "ifindex": a["ifindex"],
                        "address": a["mac"],
                        "mtu": a["mtu"],
                        "linkinfo": {"info_kind": a["kind"]},
                        "flags": ["UP"],
                    },
                    {
                        "ifname": "nxvip",
                        "ifalias": owner + ":vip:" + a["role"],
                        "linkinfo": {"info_kind": "dummy"},
                        "flags": [],
                    },
                ],
                "addresses": [
                    {
                        "ifname": a["link"],
                        "addr_info": [
                            {
                                "family": "inet",
                                "local": a["address"].split("/")[0],
                                "prefixlen": 24,
                            }
                        ],
                    }
                ],
                "routes": [
                    {
                        "dst": "198.18.0.0/24",
                        "dev": a["link"],
                        "protocol": "kernel",
                        "scope": "link",
                        "prefsrc": a["address"].split("/")[0],
                        "flags": ["linkdown"],
                    },
                    {
                        "dst": a["address"].split("/")[0],
                        "dev": a["link"],
                        "protocol": "kernel",
                        "scope": "host",
                        "prefsrc": a["address"].split("/")[0],
                        "table": "local",
                        "type": "local",
                    },
                ],
                "rules": [
                    {"priority": 0, "src": "all", "table": "local"},
                    {"priority": 32766, "src": "all", "table": "main"},
                    {"priority": 32767, "src": "all", "table": "default"},
                ],
                "ipvs": set(),
            }

    def inode(self, namespace):
        return self.data[namespace]["inode"]

    def run(self, args):
        self.commands.append(args)
        ns_exec = args[1:3] == ["netns", "exec"]
        ns, cmd = (args[3], args[4:]) if ns_exec else (args[2], args[3:])
        state = self.data[ns]
        if cmd[0] == "sysctl":
            return (
                "2\n"
                if cmd[-1].endswith("arp_announce")
                else "1\n"
                if cmd[-1].endswith("arp_ignore")
                else "0\n"
            )
        if cmd == ["ipvsadm", "-Sn"]:
            return "\n".join(sorted(state["ipvs"]))
        if "show" in cmd:
            key = (
                "links"
                if "link" in cmd
                else "addresses"
                if "address" in cmd
                else "routes"
                if "route" in cmd
                else "rules"
            )
            return json.dumps(state[key])
        self.mutations += 1
        if self.mutations == self.fail_at:
            raise OSError("injected interruption before command")
        if cmd[0] == "ipvsadm":
            action, rest = cmd[1], cmd[2:]
            if action in ("-A", "-a"):
                line = " ".join(cmd[1:])
                if line in state["ipvs"]:
                    raise ValueError("duplicate service/destination")
                state["ipvs"].add(line)
            else:
                prefix = " ".join(["-A" if action == "-D" else "-a"] + rest)
                match = [
                    line for line in state["ipvs"] if line.startswith(prefix + " ")
                ]
                assert len(match) == 1, (prefix, match)
                state["ipvs"].remove(match[0])
        elif cmd[:2] == ["address", "del"]:
            assert cmd == ["address", "del", self.m["vip"] + "/32", "dev", "nxvip"]
            state["addresses"] = [
                x for x in state["addresses"] if x["ifname"] != "nxvip"
            ]
            state["routes"] = [x for x in state["routes"] if x["dev"] != "nxvip"]
        elif cmd[:2] == ["address", "add"]:
            state["addresses"].append(
                {
                    "ifname": "nxvip",
                    "addr_info": [
                        {
                            "family": "inet",
                            "local": cmd[2].split("/")[0],
                            "prefixlen": 32,
                        }
                    ],
                }
            )
            state["routes"].append(
                {
                    "dst": cmd[2].split("/")[0],
                    "dev": "nxvip",
                    "protocol": "kernel",
                    "scope": "host",
                    "prefsrc": cmd[2].split("/")[0],
                    "table": "local",
                    "type": "local",
                }
            )
        elif cmd[0] == "route":
            r = {
                "dst": cmd[2],
                "table": cmd[cmd.index("table") + 1],
                "dev": cmd[cmd.index("dev") + 1],
                "protocol": "186",
                "scope": "global" if "via" in cmd else "link",
            }
            if "via" in cmd:
                r["gateway"] = cmd[cmd.index("via") + 1]
            if "src" in cmd:
                r["prefsrc"] = cmd[cmd.index("src") + 1]
            if cmd[1] == "add":
                assert r not in state["routes"]
                state["routes"].append(r)
            else:
                state["routes"].remove(r)
        elif cmd[0] == "rule":
            r = {"priority": 186, "src": cmd[cmd.index("from") + 1], "table": 186}
            if cmd[1] == "add":
                state["rules"].append(r)
            else:
                state["rules"].remove(r)
        else:
            raise AssertionError(cmd)
        if self.mutations == self.fail_after:
            raise OSError("injected loss of command acknowledgement after commit")
        return ""


class AdapterTests(unittest.TestCase):
    """Exercise ownership, fencing and recovery across command failure boundaries."""

    def setUp(self):
        self.m = manifest()
        self.k = Kernel(self.m)
        self.a = Adapter(self.m, self.k)
        self.f = ClosedFence()

    def test_read_only_plan_and_exact_staging(self):
        plan = self.a.plan()
        self.assertEqual(self.k.mutations, 0)
        self.assertTrue(plan)
        self.assertFalse(any("flush" in c or "-C" in c or "up" in c for c in plan))
        self.assertFalse(any(c[2] == "nx-test-1" and "address" in c for c in plan))
        self.assertTrue(
            all("-g" in c and c[-2:] == ["-w", "0"] for c in plan if "-a" in c)
        )
        self.assertEqual(self.a.reconcile(self.f)["state"], "staged-fenced")
        self.assertEqual(self.a.reconcile(self.f)["commands"], 0)
        self.assertFalse(self.a.reconcile(self.f)["active"])

    def test_vip_baseline_is_required_and_never_mutated(self):
        mutations = [
            lambda s: s["links"].pop(),
            lambda s: s["links"][-1].pop("ifalias"),
            lambda s: s["links"][-1].update(ifalias="foreign"),
            lambda s: s["links"][-1].update(flags=["UP"]),
            lambda s: s["links"][-1].update(linkinfo={"info_kind": "veth"}),
        ]
        for mutate in mutations:
            for cleanup in (False, True):
                kernel = Kernel(self.m)
                # Last attachment proves all are checked before any mutation.
                mutate(kernel.data["nx-test-3"])
                baseline = copy.deepcopy(kernel.data)
                with self.assertRaisesRegex(ValueError, "owned VIP dummy required"):
                    Adapter(self.m, kernel).reconcile(self.f, cleanup)
                self.assertEqual(kernel.mutations, 0)
                self.assertEqual(kernel.data, baseline)
        baseline = copy.deepcopy(self.k.data)
        for cleanup in (False, True):
            self.assertFalse(any(cmd[3] == "link" for cmd in self.a.plan(cleanup)))
            self.a.reconcile(self.f, cleanup)
        self.assertEqual(self.k.data, baseline)

    def test_missing_or_rejected_fence_never_mutates(self):
        with self.assertRaises(ValueError):
            self.a.reconcile(None)

        class Reject:
            def run_closed(self, manifest, callback):
                raise ValueError("kernel fence not armed")

        with self.assertRaises(ValueError):
            self.a.reconcile(Reject())
        self.assertEqual(self.k.commands, [])

    def test_crash_at_every_apply_boundary_and_new_process_recovery(self):
        count = len(self.a.plan())
        for boundary in range(1, count + 1):
            with self.subTest(boundary=boundary):
                kernel = Kernel(self.m)
                baseline = copy.deepcopy(kernel.data)
                kernel.fail_at = boundary
                with self.assertRaises(OSError):
                    Adapter(self.m, kernel).reconcile(self.f)
                kernel.fail_at = None
                restarted = Adapter(self.m, kernel)
                restarted.reconcile(self.f)
                self.assertEqual(restarted.plan(), [])
                restarted.reconcile(self.f, cleanup=True)
                self.assertEqual(kernel.data, baseline)

    def test_cleanup_at_every_boundary_and_partial_apply_cleanup(self):
        self.a.reconcile(self.f)
        count = len(self.a.plan(cleanup=True))
        for boundary in range(1, count + 1):
            with self.subTest(boundary=boundary):
                kernel = Kernel(self.m)
                baseline = copy.deepcopy(kernel.data)
                adapter = Adapter(self.m, kernel)
                adapter.reconcile(self.f)
                kernel.fail_at = kernel.mutations + boundary
                with self.assertRaises(OSError):
                    adapter.reconcile(self.f, cleanup=True)
                kernel.fail_at = None
                Adapter(self.m, kernel).reconcile(self.f, cleanup=True)
                self.assertEqual(kernel.data, baseline)
                self.assertEqual(adapter.reconcile(self.f, cleanup=True)["commands"], 0)
        for boundary in range(1, len(Adapter(self.m, Kernel(self.m)).plan()) + 1):
            kernel = Kernel(self.m)
            baseline = copy.deepcopy(kernel.data)
            kernel.fail_at = boundary
            with self.assertRaises(OSError):
                Adapter(self.m, kernel).reconcile(self.f)
            kernel.fail_at = None
            Adapter(self.m, kernel).reconcile(self.f, cleanup=True)
            self.assertEqual(kernel.data, baseline)

    def test_unknown_resources_rejected_before_any_write_including_cleanup(self):
        mutations = [
            lambda s: s.update(inode=999),
            lambda s: s["links"][0].update(ifalias="foreign"),
            lambda s: s["links"][1].update(ifindex=99),
            lambda s: s["links"].append({"ifname": "management"}),
            lambda s: s["ipvs"].add("-A -t 192.168.10.139:53 -s rr"),
            lambda s: s["ipvs"].add("-a -t 192.168.10.136:53 -r 198.18.0.2:53 -g -w 1"),
            lambda s: s["routes"].append(
                {"dst": "0.0.0.0/0", "dev": "dr0", "protocol": "186"}
            ),
            lambda s: s["rules"].append(
                {"priority": 10, "src": "all", "table": "main"}
            ),
            lambda s: s["addresses"][0]["addr_info"].append(
                {"family": "inet", "local": "192.168.10.136", "prefixlen": 32}
            ),
        ]
        for mutate in mutations:
            for cleanup in (False, True):
                kernel = Kernel(self.m)
                mutate(kernel.data["nx-test-3"])
                with self.assertRaises(ValueError):
                    Adapter(self.m, kernel).reconcile(self.f, cleanup)
                self.assertEqual(kernel.mutations, 0)

    def test_changed_manifest_never_adopts_crash_resources(self):
        self.a.reconcile(self.f)
        for key, value in (("uid", "different"), ("run", "different")):
            other = copy.deepcopy(self.m)
            other[key] = value
            with self.assertRaises(ValueError):
                Adapter(other, self.k).reconcile(self.f, cleanup=True)

    def test_ambiguous_success_at_every_apply_and_cleanup_command(self):
        for cleanup in (False, True):
            probe = Kernel(self.m)
            if cleanup:
                Adapter(self.m, probe).reconcile(self.f)
            count = len(Adapter(self.m, probe).plan(cleanup))
            for boundary in range(1, count + 1):
                kernel = Kernel(self.m)
                baseline = copy.deepcopy(kernel.data)
                if cleanup:
                    Adapter(self.m, kernel).reconcile(self.f)
                kernel.fail_after = kernel.mutations + boundary
                with self.assertRaises(OSError):
                    Adapter(self.m, kernel).reconcile(self.f, cleanup)
                kernel.fail_after = None
                Adapter(self.m, kernel).reconcile(self.f, cleanup)
                Adapter(self.m, kernel).reconcile(self.f, cleanup=True)
                self.assertEqual(kernel.data, baseline)

    def test_named_route_protocol_representation(self):
        self.a.reconcile(self.f)
        for state in self.k.data.values():
            for route in state["routes"]:
                if route["protocol"] == "186":
                    route["protocol"] = "bgp"
        self.assertEqual(self.a.plan(), [])
        self.assertTrue(self.a.plan(cleanup=True))

    def test_missing_preloaded_kernel_support_cannot_invoke_loader(self):
        with (
            patch("adapter.sys.platform", "linux"),
            patch("adapter.os.path.isdir", return_value=False),
            patch("adapter.subprocess.run") as run,
        ):
            with self.assertRaises(ValueError):
                LinuxRunner().run(
                    ["ip", "netns", "exec", "nx-test-1", "ipvsadm", "-Sn"]
                )
            with self.assertRaises(ValueError):
                LinuxRunner().run(
                    ["ip", "-n", "nx-test-1", "link", "add", "nxvip", "type", "dummy"]
                )
            run.assert_not_called()

    def test_isolated_runner_rejects_unverified_child_before_any_command(self):
        spec = importlib.util.spec_from_file_location(
            "namespace_runner_test",
            Path(__file__).parent / "cmd" / "linux_namespace_test.py",
        )
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with (
            patch.object(module, "command") as command,
            patch.object(module.os, "getpid", return_value=2),
        ):
            with self.assertRaises(ValueError):
                module.isolated([100, 101])
            command.assert_not_called()
        with (
            patch.object(module, "command") as command,
            patch.object(module.os, "getpid", return_value=1),
            patch.object(module.os, "readlink", return_value="net:[same]"),
        ):
            with self.assertRaises(ValueError):
                module.isolated([100, 101])
            command.assert_not_called()

    def test_fixture_vip_provisioning_failure_cleans_namespaces_before_handover(self):
        spec = importlib.util.spec_from_file_location(
            "namespace_provisioner_test",
            Path(__file__).parent / "cmd" / "linux_namespace_test.py",
        )
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        # Three creates, then three explicit aliases. Model both command failure
        # and lost acknowledgement after mutation; neither may reach staging.
        for boundary in range(1, 8):
            for after in (False, True):
                calls, created, committed = [], [], []
                vip_commands = 0

                def run(
                    args,
                    calls=calls,
                    created=created,
                    committed=committed,
                    boundary=boundary,
                    after=after,
                ):
                    nonlocal vip_commands
                    calls.append(args)
                    if args[1:3] == ["netns", "add"]:
                        created.append(args[-1])
                    if "nxvip" in args and "link" in args:
                        vip_commands += 1
                        if vip_commands == boundary and not after:
                            raise OSError("before VIP provisioning mutation")
                        committed.append(args)
                        if vip_commands == boundary:
                            raise OSError("after VIP provisioning mutation")
                    if "show" in args:
                        return '[{"ifindex": 2}]'
                    return ""

                with (
                    patch.object(module, "LinuxRunner") as runner_type,
                    patch.object(module, "Adapter") as adapter_type,
                    patch.object(module, "command"),
                    patch.object(module.os, "getpid", return_value=1),
                    patch.object(
                        module.os,
                        "readlink",
                        side_effect=[
                            "net:[parent]",
                            "net:[child]",
                            "mnt:[parent]",
                            "mnt:[child]",
                        ],
                    ),
                    patch.object(module.Path, "read_text", return_value=""),
                    patch.object(module.Path, "exists", return_value=True),
                    patch.object(module.Path, "is_dir", return_value=True),
                    patch.object(module.Path, "is_symlink", return_value=False),
                ):
                    runner_type.return_value.run.side_effect = run
                    runner_type.return_value.inode.return_value = 101
                    adapter_type.return_value.inspect.side_effect = OSError(
                        "VIP baseline readback failed"
                    )
                    with self.assertRaises(OSError):
                        module.isolated([100, 101])
                    if boundary <= 6:
                        adapter_type.assert_not_called()
                    else:
                        adapter_type.return_value.inspect.assert_called_once()
                    adapter_type.return_value.reconcile.assert_not_called()
                self.assertEqual(
                    len(committed), min(6, boundary if after else boundary - 1)
                )
                self.assertEqual(
                    [c[-1] for c in calls if c[1:3] == ["netns", "delete"]],
                    list(reversed(created)),
                )
                self.assertFalse(any(c[3:5] == ["link", "del"] for c in calls))

    def test_foreign_main_local_routes_and_conflicting_returns(self):
        for route in (
            {
                "dst": "default",
                "gateway": "198.18.0.254",
                "dev": "dr0",
                "protocol": "static",
                "table": "main",
            },
            {
                "dst": "203.0.113.3",
                "dev": "lo",
                "protocol": "kernel",
                "table": "local",
                "type": "local",
                "scope": "host",
            },
        ):
            kernel = Kernel(self.m)
            kernel.data["nx-test-3"]["routes"].append(route)
            with self.assertRaises(ValueError):
                Adapter(self.m, kernel).reconcile(self.f)
            self.assertEqual(kernel.mutations, 0)
        self.m["attachments"][1]["returns"][0]["destination"] = "198.18.0.254/32"
        with self.assertRaises(ValueError):
            validate(self.m)

    def test_invalid_manifests(self):
        mutations = [
            lambda m: m.update(extra=True),
            lambda m: m.update(vip="192.168.10.140"),
            lambda m: m.update(run="x;id"),
            lambda m: m["services"].pop(),
            lambda m: m["backends"][1].update(slot="B"),
            lambda m: m["backends"][1].update(engine_uid="persistent-a"),
            lambda m: m["attachments"][0].update(namespace="../../host"),
            lambda m: m["attachments"][0].update(kind="bridge"),
            lambda m: m["attachments"][0].update(inode=True),
            lambda m: m["attachments"][1]["returns"][0].update(destination="0.0.0.0/0"),
            lambda m: m["attachments"][1]["returns"][0].update(gateway="203.0.113.1"),
        ]
        for mutate in mutations:
            other = copy.deepcopy(self.m)
            mutate(other)
            with self.assertRaises(ValueError):
                validate(other)

    def test_kernel_failure_and_sysctl_mismatch(self):
        run = self.k.run
        for mode in ("unsupported", "sysctl"):

            def broken(args, mode=mode):
                if mode == "unsupported" and "ipvsadm" in args:
                    raise OSError("IPVS unsupported")
                if mode == "sysctl" and "sysctl" in args:
                    return "9\n"
                return run(args)

            self.k.run = broken
            with self.assertRaises((OSError, ValueError)):
                self.a.reconcile(self.f)
            self.assertEqual(self.k.mutations, 0)


if __name__ == "__main__":
    unittest.main()
