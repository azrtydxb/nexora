import pathlib
import tempfile
import unittest

from verify import TRANSPORT, VIP, verify


class VerificationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = pathlib.Path(self.temp.name)
        captures = {n: [] for n in "abcd"}
        logs = {n: [] for n in "ab"}
        for index, (transport, (proto, service)) in enumerate(TRANSPORT.items()):
            for client, ip in [("c", "198.18.0.10"), ("d", "198.18.0.11")]:
                for round_number, backend in enumerate("ab"):
                    port = str(20000 + index * 100 + round_number)
                    suffix = (
                        "Flags [P.], length 10" if proto == "tcp" else "UDP, length 10"
                    )
                    request = f"1 IP {ip}.{port} > {VIP}.{service}: {suffix}\n"
                    reply = f"2 IP {VIP}.{service} > {ip}.{port}: {suffix}\n"
                    captures[client].extend([request, reply])
                    captures[backend].extend([request, reply])
                    logs[backend].append(
                        f"peer={ip}:{port} question={transport}.dsr-lab.test.\n"
                    )
        for n, lines in captures.items():
            (self.path / f"packets-{n}.txt").write_text("".join(lines))
            (self.path / f"capture-{n}.log").write_text("0 packets dropped by kernel\n")
        for n, lines in logs.items():
            (self.path / f"backend-{n}.log").write_text("".join(lines))

    def mutate(self, name, old, new):
        path = self.path / name
        path.write_text(path.read_text().replace(old, new))

    def test_complete(self):
        self.assertIn("PASS:", verify(self.path))

    def test_snat(self):
        self.mutate("packets-a.txt", "198.18.0.10", "198.18.0.2")
        with self.assertRaisesRegex(ValueError, "tuple mismatch"):
            verify(self.path)

    def test_source_port_loss(self):
        self.mutate("packets-b.txt", ".20001", ".30001")
        with self.assertRaisesRegex(ValueError, "tuple mismatch"):
            verify(self.path)

    def test_wrong_reply_ip(self):
        self.mutate("packets-c.txt", f"IP {VIP}", "IP 198.18.0.3")
        with self.assertRaisesRegex(ValueError, "reply tuple mismatch"):
            verify(self.path)

    def test_incomplete_capture(self):
        (self.path / "packets-d.txt").write_text("")
        with self.assertRaisesRegex(ValueError, "expected 20"):
            verify(self.path)

    def test_capture_drop(self):
        self.mutate("capture-a.log", "0 packets", "10 packets")
        with self.assertRaisesRegex(ValueError, "zero-drop"):
            verify(self.path)

    def test_socket_mismatch(self):
        self.mutate("backend-a.log", "198.18.0.10", "198.18.0.2")
        with self.assertRaisesRegex(ValueError, "socket identity"):
            verify(self.path)


if __name__ == "__main__":
    unittest.main()
