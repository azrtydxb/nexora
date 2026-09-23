"""No privileged execution: isolation/partial-control refusal regression tests."""

import importlib.util
import io
import types
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "runtime_active_fixture", Path(__file__).parent / "active_lab.py"
)
harness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(harness)


class ActiveHarnessTests(unittest.TestCase):
    def test_direct_child_cannot_mutate_without_pid_isolation(self):
        with (
            patch.object(harness.os, "getpid", return_value=123),
            patch.object(harness, "command") as command,
        ):
            with self.assertRaisesRegex(ValueError, "private PID1"):
                harness.isolated(types.SimpleNamespace(isolated_fds=[7, 8]))
            command.assert_not_called()

    def test_partial_control_cannot_count_unrelated_failure_as_success(self):
        owner = types.SimpleNamespace(stdout=io.BytesIO(), stdin=None, poll=lambda: 1)
        args = types.SimpleNamespace(scenario="partial", executable="/trusted/owner")
        with (
            patch.object(harness.subprocess, "Popen", return_value=owner),
            patch.object(harness, "record", return_value="OTHER-FAILURE"),
            patch.object(harness, "command") as command,
        ):
            with self.assertRaisesRegex(ValueError, "partial injection not reached"):
                harness.exercise(args, ["/trusted/ip"], Path("/run/lab"), {})
            command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
