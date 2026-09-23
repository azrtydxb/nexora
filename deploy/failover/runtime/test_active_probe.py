"""Verifier adversaries only: simulated packets never count as kernel evidence."""

import importlib.util
import io
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "active_probe_fixture", Path(__file__).parent / "cmd/active_probe.py"
)
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class ActivePacketVerifier(unittest.TestCase):
    def exercise(
        self, frontend, dr, *, leak=False, lose_management=False, wrong_vip=False
    ):
        names = ("fg0", "fp0", "bg0", "bp0", "mg0", "mp0")
        m = {
            "links": {
                name: {"mac": f"02:00:00:00:00:{i:02x}"}
                for i, name in enumerate(names, 1)
            }
        }
        frames = {}

        class Socket:
            def __enter__(self):
                return self

            def __exit__(self, *_args):
                pass

            def bind(self, pair):
                self.name = pair[0]

            def settimeout(self, _value):
                pass

            def send(self, frame):
                if self.name == "fg0" and frontend:
                    frames["fp0"] = frame
                if self.name == "mg0" and not lose_management:
                    frames["mp0"] = frame
                if self.name == "fp0" and (dr or leak):
                    # Model only the MAC rewrite; the destination IP stays VIP.
                    data = (
                        bytes.fromhex(m["links"]["bp0"]["mac"].replace(":", ""))
                        + frame[6:]
                    )
                    if wrong_vip:
                        data = data[:30] + b"\x00" * 4 + data[34:]
                    frames["bp0"] = data

            def recv(self, _size):
                if self.name not in frames:
                    raise TimeoutError
                return frames.pop(self.name)

        with (
            patch.object(probe.socket, "socket", side_effect=lambda *_args: Socket()),
            patch.object(probe.socket, "AF_PACKET", 17, create=True),
            redirect_stdout(io.StringIO()),
        ):
            probe.probe(m, frontend, dr)

    def test_allow_deny_and_frontend_only_sensitivity(self):
        self.exercise(True, True)
        self.exercise(False, False)
        self.exercise(False, True)

    def test_stale_mac_leak_cannot_pass_denial(self):
        with self.assertRaisesRegex(ValueError, "fp0->bp0"):
            self.exercise(False, False, leak=True)

    def test_management_failure_is_not_fencing_success(self):
        with self.assertRaisesRegex(ValueError, "mg0->mp0"):
            self.exercise(False, False, lose_management=True)

    def test_non_dr_destination_rewrite_cannot_pass(self):
        with self.assertRaisesRegex(ValueError, "fp0->bp0"):
            self.exercise(True, True, wrong_vip=True)


if __name__ == "__main__":
    unittest.main()
