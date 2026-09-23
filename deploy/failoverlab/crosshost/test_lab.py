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

    def test_duplicate_control_uses_deadline_not_reply_count(self):
        with tempfile.TemporaryDirectory() as d:
            base = Path(d)
            (base / "plan.json").write_text(json.dumps(self.plan))
            (base / "role").write_text("left")
            (base / "mode").write_text("duplicate")
            for group in self.plan["groups"]:
                (base / group["name"]).mkdir()
            replies = [
                Mock(
                    returncode=0,
                    stdout=(
                        f"reply [02:00:00:71:00:0{i}]\n"
                        f"reply [02:00:00:72:00:0{i}]\n"
                        f"reply [02:00:00:72:01:0{i}]\n"
                    ),
                )
                for i in (1, 2)
            ]
            with (
                patch.dict(lab.os.environ),
                patch.object(lab.subprocess, "run", side_effect=replies) as run,
            ):
                lab.probes(base, "/probe", "/fixture-tls")
            self.assertEqual(run.call_count, 2)
            for call in run.call_args_list:
                argv = call.args[0]
                self.assertNotIn("-c", argv)
                self.assertEqual(argv[argv.index("-w") + 1], "4")
                self.assertEqual(call.kwargs["timeout"], 6)

    def test_duplicate_control_requires_both_frontend_and_extra_responder(self):
        for output in (
            "reply [02:00:00:71:00:01]",
            "reply [02:00:00:99:00:01]\nreply [02:00:00:99:00:02]",
            "",
        ):
            with self.subTest(output=output), tempfile.TemporaryDirectory() as d:
                base = Path(d)
                (base / "plan.json").write_text(json.dumps(self.plan))
                (base / "role").write_text("left")
                (base / "mode").write_text("duplicate")
                (base / "g1").mkdir()
                with (
                    patch.dict(lab.os.environ),
                    patch.object(
                        lab.subprocess,
                        "run",
                        return_value=Mock(returncode=0, stdout=output),
                    ),
                    self.assertRaises(ValueError),
                ):
                    lab.probes(base, "/probe", "/fixture-tls")

    def test_no_probe_retry(self):
        args = Mock(evidence="/tmp/unused")
        instance = lab.Lab(args, self.plan)
        instance.probed = True
        with self.assertRaisesRegex(ValueError, "already consumed"):
            instance.probe()


if __name__ == "__main__":
    unittest.main()


class ControlTests(unittest.TestCase):
    def test_snat_requires_exact_observed_source(self):
        good = {
            "control": "source-mismatch",
            "transport": "udp",
            "expected": "198.18.0.10",
            "observed": "198.18.0.12",
        }
        lab.check_snat(
            Mock(returncode=1, stdout=json.dumps(good)),
            "udp",
            "198.18.0.10",
            "198.18.0.12",
        )
        for output in (
            "timeout",
            "TLS failure",
            json.dumps({**good, "observed": "198.18.0.120"}),
            json.dumps({**good, "transport": "doq"}),
            json.dumps(good) + "\n" + json.dumps(good),
        ):
            with self.subTest(output=output), self.assertRaises(ValueError):
                lab.check_snat(
                    Mock(returncode=1, stdout=output),
                    "udp",
                    "198.18.0.10",
                    "198.18.0.12",
                )
        with self.assertRaises(ValueError):
            lab.check_snat(
                Mock(returncode=0, stdout=json.dumps(good)),
                "udp",
                "198.18.0.10",
                "198.18.0.12",
            )

    def test_duplicate_requires_known_backend_responders(self):
        front = "[02:00:00:71:00:01]"
        backends = "[02:00:00:72:00:01] [02:00:00:72:01:01]"
        lab.check_arp(Mock(returncode=0, stdout=front), "g1", "dr")
        lab.check_arp(Mock(returncode=0, stdout=front + backends), "g1", "duplicate")
        for output in (front, front + "[aa:bb:cc:dd:ee:ff]", backends):
            with self.subTest(output=output), self.assertRaises(ValueError):
                lab.check_arp(Mock(returncode=0, stdout=output), "g1", "duplicate")
        with self.assertRaises(ValueError):
            lab.check_arp(Mock(returncode=0, stdout=front + backends), "g1", "dr")

    def test_child_cleanup_failure_still_deletes_owned_namespace(self):
        with tempfile.TemporaryDirectory() as d:
            instance = lab.Lab(Mock(evidence=d), {})
            child = Mock()
            child.poll.return_value = None
            child.terminate.side_effect = OSError("cannot terminate")
            instance.children = [child]
            instance.owned = ["owned"]
            instance.cmd = Mock()
            with self.assertRaisesRegex(ValueError, "cannot terminate"):
                instance.cleanup()
            instance.cmd.assert_called_once_with("ip", "netns", "del", "owned")

    def test_capture_signal_failure_still_deletes_owned_namespace(self):
        with tempfile.TemporaryDirectory() as d:
            instance = lab.Lab(Mock(evidence=d), {})
            cap = Mock()
            cap.poll.return_value = None
            cap.send_signal.side_effect = OSError("capture signal failed")
            instance.captures = [(cap, Path(d), "a")]
            instance.children = [cap]
            instance.owned = ["owned"]
            instance.cmd = Mock()
            with self.assertRaisesRegex(ValueError, "capture signal failed"):
                instance.cleanup()
            instance.cmd.assert_called_once_with("ip", "netns", "del", "owned")

    def test_engine_optin_and_controls_refuse_before_mutation(self):
        for enabled, mode in (("", "dr"), ("yes", "snat"), ("yes", "duplicate")):
            with tempfile.TemporaryDirectory() as d:
                instance = lab.Lab(
                    Mock(evidence=d + "/new", engine="/bin/engine", mode=mode), {}
                )
                with (
                    patch.object(lab.sys, "platform", "linux"),
                    patch.object(lab.os, "geteuid", return_value=0),
                    patch.object(
                        lab.os,
                        "getenv",
                        side_effect=lambda k, enabled=enabled: (
                            enabled if k == "FAILOVER_REAL_ENGINE_ENABLE" else "yes"
                        ),
                    ),
                    patch.object(lab.shutil, "which", return_value="/bin/tool"),
                ):
                    with self.assertRaises(ValueError):
                        instance.setup()
                self.assertFalse(instance.base.exists())
                self.assertEqual(instance.owned, [])
