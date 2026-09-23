"""Verifier negative controls only; simulated sockets are NOT kernel evidence."""

import errno
import io
import unittest
from contextlib import redirect_stdout
from unittest.mock import patch

import smoke


class PacketVerifier(unittest.TestCase):
    """Exercise evidence rejection without making any kernel execution claim."""

    def probe(self, expected, *, drop_front=False, drop_management=False, error=None):
        frames = {}

        class Socket:
            def __enter__(self):
                return self

            def __exit__(self, *_args):
                pass

            def bind(self, binding):
                self.interface = binding[0]

            def settimeout(self, _timeout):
                pass

            def send(self, frame):
                front = self.interface == "fg0"
                if front and error is not None:
                    raise OSError(error, "injected")
                if not (drop_front if front else drop_management):
                    frames["fp0" if front else "mp0"] = frame

            def recv(self, _size):
                if self.interface not in frames:
                    raise TimeoutError
                return frames.pop(self.interface)

        with patch.object(smoke.socket, "socket", return_value=None) as factory:
            factory.side_effect = lambda *_args: Socket()
            with (
                patch.object(smoke.socket, "AF_PACKET", 17, create=True),
                redirect_stdout(io.StringIO()),
            ):
                smoke.packet_probe(expected)

    def test_delivery_and_expected_denial(self):
        self.probe(True)
        self.probe(False, drop_front=True)

    def test_duplicate_advertising_or_data_leak_is_failure(self):
        with self.assertRaisesRegex(RuntimeError, "fg0 arp"):
            self.probe(False)

    def test_management_loss_is_failure(self):
        with self.assertRaisesRegex(RuntimeError, "mg0 arp"):
            self.probe(False, drop_front=True, drop_management=True)

    def test_authorized_data_loss_is_failure(self):
        with self.assertRaisesRegex(RuntimeError, "fg0 arp"):
            self.probe(True, drop_front=True)

    def test_kernel_drop_feedback_still_observes_peer(self):
        self.probe(False, error=errno.ENOBUFS)
        with self.assertRaises(OSError):
            self.probe(True, error=errno.ENOBUFS)

    def test_other_socket_errors_never_count_as_denial(self):
        with self.assertRaises(OSError):
            self.probe(False, error=errno.EPERM)


if __name__ == "__main__":
    unittest.main()
