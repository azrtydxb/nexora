"""Host-independent adversaries; no Linux or privileged execution."""

import copy
import unittest
from unittest.mock import patch

import active_lab as bridge


def fixture():
    m = {
        "version": "isolated-dual-v1",
        "run": "a" * 32,
        "netns": "net:[123]",
        "links": {},
    }
    for i, name in enumerate(bridge.NAMES, 2):
        m["links"][name] = {
            "index": i,
            "peer": i + (1 if i % 2 == 0 else -1),
            "mac": f"02:00:00:00:00:{i:02x}",
        }
    links = [
        {
            "ifname": "lo",
            "link_type": "loopback",
            "ifalias": "nexora-lab:" + bridge.digest(m),
        }
    ]
    for name, a in m["links"].items():
        links.append(
            {
                "ifname": name,
                "ifindex": a["index"],
                "link_index": a["peer"],
                "address": a["mac"],
                "mtu": 1500,
                "ifalias": bridge.alias(m, name),
                "flags": ["UP"] if name not in bridge.GATED else [],
                "linkinfo": {"info_kind": "veth"},
            }
        )
    return m, links


class ActiveBridgeTests(unittest.TestCase):
    def test_segmentation_subfeatures_cannot_hide_behind_gso_off(self):
        self.assertEqual(
            bridge.forbidden_offloads(
                "generic-segmentation-offload: off\n tx-udp-segmentation: on\nrx-gro-list: on\n"
            ),
            ["tx-udp-segmentation", "rx-gro-list"],
        )
        with self.assertRaises(ValueError):
            bridge.forbidden_offloads("hw-tc-offload: unknown")

    def test_exact_inventory_and_manifest_binding(self):
        m, links = fixture()
        bridge.validate(m, links, {"fp0", "bp0", "mg0", "mp0"})
        changed = copy.deepcopy(m)
        changed["run"] = "b" * 32
        with self.assertRaises(ValueError):
            bridge.validate(changed, links, {"fp0", "bp0", "mg0", "mp0"})

    def test_every_attachment_replacement_bypass_and_unexpected_up_refused(self):
        m, links = fixture()
        for index in range(1, 7):
            for key, value in (
                ("ifindex", 999),
                ("link_index", 999),
                ("ifalias", "foreign"),
                ("master", "br0"),
                ("link_netnsid", 0),
                ("xdp", {"attached": True}),
                ("addr_info", [{"local": "198.18.0.1"}]),
                ("mtu", 9000),
            ):
                with self.subTest(index=index, key=key):
                    bad = copy.deepcopy(links)
                    bad[index][key] = value
                    with self.assertRaises(ValueError):
                        bridge.validate(m, bad, {"fp0", "bp0", "mg0", "mp0"})
        with self.assertRaises(ValueError):
            bridge.validate(m, links + [{"ifname": "extra"}], set())
        links[1]["flags"] = ["UP"]
        with self.assertRaises(ValueError):
            bridge.validate(m, links, {"fp0", "bp0", "mg0", "mp0"})

    def test_failures_at_every_activation_boundary_never_continue(self):
        m, _ = fixture()
        for fail in range(4):
            events = []

            def event(*args, events=events, fail=fail):
                events.append(args)
                if len(events) == fail + 1:
                    raise ValueError("injected boundary")

            candidate = bridge.ActiveLabBridge(m)
            with (
                patch.object(candidate, "inspect", side_effect=event),
                patch.object(bridge, "run", side_effect=event),
                self.assertRaisesRegex(ValueError, "injected boundary"),
            ):
                candidate.activate_closed()
            self.assertEqual(len(events), fail + 1)

    def test_partial_control_reaches_only_first_up(self):
        m, _ = fixture()
        candidate = bridge.ActiveLabBridge(m)
        with patch.object(candidate, "inspect"), patch.object(bridge, "run") as run:
            with self.assertRaisesRegex(ValueError, "injected partial activation"):
                candidate.activate_closed(True)
            self.assertEqual(
                run.call_args_list,
                [unittest.mock.call("/usr/sbin/ip", "link", "set", "fg0", "up")],
            )


if __name__ == "__main__":
    unittest.main()
