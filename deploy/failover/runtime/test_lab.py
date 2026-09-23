"""Nonprivileged regressions for the actual lab protocol and isolation guards."""

import contextlib
import importlib.util
import io
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "platform"))
sys.path.insert(0, str(ROOT / "platform/cmd"))
from adapter import Adapter
from stage_session import session
from test_adapter import ClosedFence, Kernel, manifest


def load(name, path):
    """Import only the source under review, with no privileged main entry."""
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class LabTests(unittest.TestCase):
    """Mocks here test control flow only; packet/Linux acceptance remains separate."""

    def test_pinned_namespace_uses_device_and_inode(self):
        lab = load("runtime_lab_identity", Path(__file__).with_name("lab.py"))
        current = SimpleNamespace(st_dev=4, st_ino=101)
        other = SimpleNamespace(st_dev=4, st_ino=102)
        cases = [
            (current, other, True),
            (other, other, False),
            (SimpleNamespace(st_dev=5, st_ino=101), other, False),
            (current, current, False),
        ]
        for pinned, initial, accepted in cases:
            with (
                self.subTest(pinned=pinned, initial=initial),
                patch.object(lab.os, "fstat", return_value=pinned),
                patch.object(lab.os, "stat", side_effect=[current, initial]),
                patch.object(
                    lab.os,
                    "readlink",
                    side_effect=AssertionError("pathname is not identity"),
                ),
            ):
                if accepted:
                    lab.require_detached_namespace(7)
                else:
                    with self.assertRaisesRegex(ValueError, "pinned detached"):
                        lab.require_detached_namespace(7)

    def test_partial_stage_never_acknowledges(self):
        for after in (False, True):
            m = manifest()
            kernel = Kernel(m)
            adapter = Adapter(m, kernel)
            count = len(adapter.plan())
            for operation in range(1, count + 1):
                with self.subTest(after=after, operation=operation):
                    kernel = Kernel(m)
                    adapter = Adapter(m, kernel)
                    baseline = [adapter.inspect(a) for a in m["attachments"]]
                    if after:
                        kernel.fail_after = operation
                    else:
                        kernel.fail_at = operation
                    output = io.StringIO()
                    with (
                        patch(
                            "stage_session.receive", return_value="STAGE " + "a" * 64
                        ),
                        contextlib.redirect_stdout(output),
                        self.assertRaises(OSError),
                    ):
                        session(adapter, ClosedFence(), baseline, count)
                    self.assertEqual(output.getvalue(), "")

    def test_partial_cleanup_never_acknowledges_cleaned(self):
        for after in (False, True):
            for operation in range(1, 26):
                with self.subTest(after=after, operation=operation):
                    m = manifest()
                    kernel = Kernel(m)
                    adapter = Adapter(m, kernel)
                    baseline = [adapter.inspect(a) for a in m["attachments"]]
                    requests = iter(["STAGE " + "a" * 64, "CLEAN " + "a" * 64])

                    def receive(
                        requests=requests,
                        after=after,
                        kernel=kernel,
                        operation=operation,
                        **kwargs,
                    ):
                        value = next(requests)
                        if value.startswith("CLEAN"):
                            if after:
                                kernel.fail_after = kernel.mutations + operation
                            else:
                                kernel.fail_at = kernel.mutations + operation
                        return value

                    output = io.StringIO()
                    with (
                        patch("stage_session.receive", side_effect=receive),
                        contextlib.redirect_stdout(output),
                        self.assertRaises(OSError),
                    ):
                        session(adapter, ClosedFence(), baseline, 25)
                    self.assertEqual(output.getvalue(), "STAGED " + "a" * 64 + "\n")

    def test_stale_cleanup_never_deletes(self):
        m = manifest()
        kernel = Kernel(m)
        adapter = Adapter(m, kernel)
        baseline = [adapter.inspect(a) for a in m["attachments"]]
        count = len(adapter.plan())
        output = io.StringIO()
        with (
            patch(
                "stage_session.receive",
                side_effect=["STAGE " + "a" * 64, "CLEAN " + "b" * 64],
            ),
            contextlib.redirect_stdout(output),
            self.assertRaisesRegex(ValueError, "stale"),
        ):
            session(adapter, ClosedFence(), baseline, count)
        self.assertFalse(adapter.plan())
        self.assertEqual(output.getvalue(), "STAGED " + "a" * 64 + "\n")

    def test_cleanup_ack_waits_for_namespace_cleanup(self):
        module = load("runtime_stage_main", ROOT / "platform/cmd/stage_session.py")
        output = io.StringIO()
        with (
            patch.object(
                module, "isolated", side_effect=ValueError("namespace cleanup failed")
            ),
            patch.object(module.os, "geteuid", return_value=0),
            patch.object(module.sys, "platform", "linux"),
            patch.object(
                module.sys,
                "argv",
                [
                    "stage_session.py",
                    "--execute-detached-stage",
                    "--isolated-fds",
                    "100",
                    "101",
                ],
            ),
            contextlib.redirect_stdout(output),
            self.assertRaisesRegex(ValueError, "namespace cleanup failed"),
        ):
            module.main()
        self.assertEqual(output.getvalue(), "")

    def test_detached_provider_binds_manifest_and_namespace(self):
        from linux_namespace_test import IsolatedFence

        m = manifest()
        kernel = Kernel(m)
        provider = IsolatedFence(kernel, "net:[original]", m)
        callback = lambda: self.fail("unverified fence invoked callback")
        altered = manifest()
        altered["run"] = "other-run"
        with self.assertRaisesRegex(ValueError, "manifest changed"):
            provider.run_closed(altered, callback)
        kernel.data[m["attachments"][0]["namespace"]]["inode"] += 1
        with self.assertRaisesRegex(ValueError, "namespace changed"):
            provider.run_closed(m, callback)

    def test_runtime_harness_requires_isolation_before_any_command(self):
        module = load("runtime_lab", Path(__file__).with_name("lab.py"))
        from argparse import Namespace

        with (
            patch.object(module.os, "getpid", return_value=100),
            patch.object(module, "command") as command,
        ):
            with self.assertRaisesRegex(ValueError, "PID1"):
                module.isolated(Namespace(isolated_fds=[100, 101]))
            command.assert_not_called()
        with (
            patch.object(module.os, "getpid", return_value=1),
            patch.object(module.os, "readlink", return_value="net:[same]"),
            patch.object(module, "command") as command,
        ):
            with self.assertRaisesRegex(ValueError, "distinct inherited"):
                module.isolated(Namespace(isolated_fds=[100, 101]))
            command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
