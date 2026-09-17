import copy
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("crosshost_lab", HERE / "lab.py")
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


class PlanTests(unittest.TestCase):
    def setUp(self):
        self.plan = json.loads((HERE / "plan.example.json").read_text())

    def test_example(self):
        self.assertEqual(lab.validate(self.plan), self.plan)

    def test_reject_unsafe_plans(self):
        cases = []
        for value in (
            "localhost",
            "127.0.0.1",
            "0.0.0.0",
            "224.0.0.1",
            "::1",
            "169.254.0.2",
            "192.0.2.11",
        ):
            p = copy.deepcopy(self.plan)
            p["hosts"]["left"] = value
            cases.append(p)
        for key, value in [
            ("run", "../oops!"),
            ("mtu", 1500),
            ("mtu", True),
            ("groups", []),
        ]:
            p = copy.deepcopy(self.plan)
            p[key] = value
            cases.append(p)
        for key in ("port", "vni", "name"):
            p = copy.deepcopy(self.plan)
            p["groups"][1][key] = p["groups"][0][key]
            cases.append(p)
        p = copy.deepcopy(self.plan)
        p["host_route"] = "default"
        cases.append(p)
        for p in cases:
            with self.subTest(plan=p), self.assertRaises((ValueError, TypeError)):
                lab.validate(p)

    def test_vni_and_size_fail_closed(self):
        frame = (
            b"\x08\0\0\0"
            + (710001).to_bytes(3, "big")
            + b"\0"
            + bytes(12)
            + b"\x08\0"
            + bytes(100)
        )
        self.assertTrue(lab.valid_frame(frame, 710001, 1400))
        self.assertFalse(lab.valid_frame(frame, 710002, 1400))
        self.assertFalse(lab.valid_frame(frame + bytes(1500), 710001, 1400))
        for index in (0, 1, 2, 3, 7, 20, 21):
            broken = bytearray(frame)
            broken[index] ^= 1
            self.assertFalse(lab.valid_frame(broken, 710001, 1400))

    def test_cleanup_attempts_every_owned_namespace(self):
        with tempfile.TemporaryDirectory() as d:
            instance = lab.Lab(Mock(evidence=d), self.plan)
            instance.owned = ["owned-first", "owned-second"]
            instance.created_base = True
            instance.cmd = Mock(side_effect=[RuntimeError("delete failed"), ""])
            with self.assertRaisesRegex(ValueError, "cleanup incomplete"):
                instance.cleanup()
            self.assertEqual(
                instance.cmd.call_args_list[0].args,
                ("ip", "netns", "del", "owned-second"),
            )
            self.assertEqual(
                instance.cmd.call_args_list[1].args,
                ("ip", "netns", "del", "owned-first"),
            )
            self.assertEqual(
                json.loads((Path(d) / "cleanup.json").read_text()),
                {"errors": ["delete failed"]},
            )

    def test_cleanup_does_not_touch_existing_evidence(self):
        with tempfile.TemporaryDirectory() as d:
            instance = lab.Lab(Mock(evidence=d), self.plan)
            instance.cmd = Mock()
            instance.cleanup()
            instance.cmd.assert_not_called()
            self.assertEqual(list(Path(d).iterdir()), [])

    def test_probe_cancellation_kills_process_group(self):
        pending = Mock(pid=12345)
        pending.poll.return_value = None
        with patch.object(lab.os, "killpg") as kill:
            lab.stop_probe(pending)
        kill.assert_called_once_with(12345, lab.signal.SIGTERM)
        pending.terminate.assert_not_called()
        pending.wait.assert_called_once_with(timeout=10)

    def test_missing_snat_tool_refuses_before_creating_resources(self):
        with tempfile.TemporaryDirectory() as d:
            base = Path(d) / "new"
            instance = lab.Lab(Mock(evidence=str(base), mode="snat"), self.plan)
            with (
                patch.object(lab.sys, "platform", "linux"),
                patch.object(lab.os, "geteuid", return_value=0),
                patch.object(lab.os, "getenv", return_value="yes"),
                patch.object(
                    lab.shutil,
                    "which",
                    side_effect=lambda name: (
                        None if name == "iptables" else "/usr/bin/" + name
                    ),
                ),
            ):
                with self.assertRaisesRegex(
                    ValueError, "missing required tool: iptables"
                ):
                    instance.setup()
            self.assertFalse(base.exists())
            self.assertEqual(instance.owned, [])

    def test_no_probe_retry(self):
        args = Mock(evidence="/tmp/unused")
        instance = lab.Lab(args, self.plan)
        instance.probed = True
        with self.assertRaisesRegex(ValueError, "already consumed"):
            instance.probe()


if __name__ == "__main__":
    unittest.main()
