"""Synthetic validator regressions are not live engine evidence."""

import copy
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "pair", Path(__file__).with_name("verify_pair.py")
)
pair = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pair)


def attrs(values):
    return [{"key": k, "value": {"stringValue": v}} for k, v in values.items()]


class EngineEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.left, self.right = [Path(self.temp.name) / x for x in ("left", "right")]
        for base, role, round_number in ((self.left, "a", 0), (self.right, "b", 1)):
            out = base / "g1"
            out.mkdir(parents=True)
            (out / "management-health.txt").write_text("ready\n")
            records = []
            for client in ("198.18.0.10", "198.18.0.11"):
                for transport in ("udp", "tcp", "dot", "doh", "doq"):
                    for suffix in ("dsr-lab.test", "mtu.test"):
                        large = suffix == "mtu.test"
                        a = {
                            "client.address": client,
                            "nexora.transport": transport,
                            "nexora.policy.group": "g1-client-" + client.split(".")[-1],
                            "nexora.filter": "none" if large else "rewritten",
                            "nexora.filter.source": "" if large else "rewrite",
                            "dns.question.name": f"{transport}.r{round_number}.g1.{suffix}.",
                            "dns.question.type": "TXT" if large else "A",
                            "dns.response.code": "NOERROR",
                        }
                        records.append({"attributes": attrs(a)})
            batch = {
                "resourceLogs": [
                    {
                        "resource": {
                            "attributes": attrs(
                                {
                                    "service.name": "nexora-engine",
                                    "host.name": f"fx-abcdefgh-g1-{role}",
                                }
                            )
                        },
                        "scopeLogs": [{"logRecords": records}],
                    }
                ]
            }
            (out / "querylog.jsonl").write_text(json.dumps(batch) + "\n")
        packets = {role: [] for role in "abcd"}
        for base, client in ((self.left, "198.18.0.10"), (self.right, "198.18.0.11")):
            results = []
            for transport in ("udp", "tcp", "dot", "doh", "doq"):
                for r in (0, 1):
                    for suffix in ("dsr-lab.test", "mtu.test"):
                        result = {
                            "transport": transport,
                            "client": client,
                            "question": f"{transport}.r{r}.g1.{suffix}.",
                        }
                        proto, port = pair.verifier.TRANSPORT[transport]
                        local_port = 40000 + len(results)
                        result["question_type"] = "TXT" if suffix == "mtu.test" else "A"
                        result["socket"] = dict(
                            local_ip=client,
                            local_port=local_port,
                            remote_ip=pair.verifier.VIP,
                            remote_port=int(port),
                        )
                        marker = "Flags [P.]" if proto == "tcp" else "UDP, length 100"
                        request = f" IP {client}.{local_port} > {pair.verifier.VIP}.{port}: {marker}"
                        reply = f" IP {pair.verifier.VIP}.{port} > {client}.{local_port}: {marker}"
                        for role in (
                            ("a" if r == 0 else "b"),
                            ("c" if base == self.left else "d"),
                        ):
                            packets[role].extend([request, reply])
                        if suffix == "dsr-lab.test":
                            result["answer"] = "203.0.1." + client.split(".")[-1]
                        else:
                            result.update(
                                signed=transport != "udp",
                                truncated=transport == "udp",
                                bytes=100 if transport == "udp" else 2600,
                            )
                        results.append(json.dumps(result))
            (base / "g1" / "probe.log").write_text("\n".join(results))

        for base, roles in ((self.left, "ac"), (self.right, "bd")):
            for role in roles:
                (base / "g1" / f"packets-{role}.txt").write_text(
                    "\n".join(packets[role])
                )

    def verify(self):
        return pair.verify_engine(self.left, self.right, "g1", "abcdefgh")

    def test_complete_contract(self):
        self.assertIn("PASS: real engine", self.verify())

    def test_reject_swapped_backend_records(self):
        paths = [base / "g1" / "querylog.jsonl" for base in (self.left, self.right)]
        batches = [json.loads(p.read_text()) for p in paths]
        # Retain both resource identities and all original probes/captures.
        a, b = [x["resourceLogs"][0]["scopeLogs"][0] for x in batches]
        a["logRecords"], b["logRecords"] = b["logRecords"], a["logRecords"]
        for path, batch in zip(paths, batches):
            path.write_text(json.dumps(batch))
        with self.assertRaisesRegex(ValueError, "backend.*original client queries"):
            self.verify()

    def test_probe_tuple_and_query_mutations(self):
        path = self.left / "g1" / "probe.log"
        original = [json.loads(line) for line in path.read_text().splitlines()]
        mutations = [
            ("missing socket", lambda rows: rows[0].pop("socket")),
            (
                "source address",
                lambda rows: rows[0]["socket"].update(local_ip="198.18.0.11"),
            ),
            (
                "destination address",
                lambda rows: rows[0]["socket"].update(remote_ip="198.18.0.101"),
            ),
            ("source port", lambda rows: rows[0]["socket"].update(local_port=60000)),
            ("service port", lambda rows: rows[0]["socket"].update(remote_port=853)),
            ("zero port", lambda rows: rows[0]["socket"].update(local_port=0)),
            ("overflow port", lambda rows: rows[0]["socket"].update(local_port=65536)),
            ("string port", lambda rows: rows[0]["socket"].update(local_port="40000")),
            ("boolean port", lambda rows: rows[0]["socket"].update(local_port=True)),
            ("query", lambda rows: rows[0].update(question="udp.r0.g2.dsr-lab.test.")),
            ("type", lambda rows: rows[0].update(question_type="TXT")),
            ("transport", lambda rows: rows[0].update(transport="tcp")),
            ("collision", lambda rows: rows[1].update(socket=rows[0]["socket"])),
            ("duplicate", lambda rows: rows.__setitem__(-1, rows[0])),
        ]
        for label, mutate in mutations:
            with self.subTest(mutation=label):
                rows = copy.deepcopy(original)
                mutate(rows)
                path.write_text("\n".join(json.dumps(row) for row in rows))
                with self.assertRaises(ValueError):
                    self.verify()
        path.write_text("\n".join(json.dumps(row) for row in original))

    def test_swap_probe_ports_between_rounds_preserving_all_sets(self):
        path = self.left / "g1" / "probe.log"
        rows = [json.loads(line) for line in path.read_text().splitlines()]
        # Same client/transport/type, different backend: all port/query sets survive.
        rows[0]["socket"], rows[2]["socket"] = rows[2]["socket"], rows[0]["socket"]
        path.write_text("\n".join(json.dumps(row) for row in rows))
        with self.assertRaisesRegex(ValueError, "backend.*original client queries"):
            self.verify()

    def test_capture_ownership_mutations(self):
        a = self.left / "g1" / "packets-a.txt"
        b = self.right / "g1" / "packets-b.txt"
        c = self.left / "g1" / "packets-c.txt"
        originals = {p: p.read_text() for p in (a, b, c)}
        for mutation in (
            "absent",
            "both backends",
            "reply missing",
            "client missing",
            "extra flow",
            "wrong backend reply",
        ):
            with self.subTest(mutation=mutation):
                for path, text in originals.items():
                    path.write_text(text)
                lines = originals[a].splitlines()
                if mutation == "absent":
                    a.write_text("\n".join(lines[2:]))
                elif mutation == "both backends":
                    b.write_text(originals[b] + "\n" + "\n".join(lines[:2]))
                elif mutation == "wrong backend reply":
                    b.write_text(originals[b] + "\n" + lines[1])
                elif mutation == "reply missing":
                    a.write_text("\n".join([lines[0]] + lines[2:]))
                elif mutation == "client missing":
                    c.write_text("\n".join(originals[c].splitlines()[2:]))
                else:
                    a.write_text(
                        originals[a] + "\n" + lines[0].replace("40000", "60000")
                    )
                with self.assertRaises(ValueError):
                    self.verify()

    def test_backend_selection_comes_from_capture_not_round(self):
        # Move one complete transport/payload pair in each direction while keeping
        # per-backend coverage exact. Round numbers must not dictate ownership.
        paths = [base / "g1" / "querylog.jsonl" for base in (self.left, self.right)]
        batches = [json.loads(p.read_text()) for p in paths]
        a, b = [
            batch["resourceLogs"][0]["scopeLogs"][0]["logRecords"] for batch in batches
        ]
        a[:2], b[:2] = b[:2], a[:2]
        for path, batch in zip(paths, batches):
            path.write_text(json.dumps(batch))
        paths = [
            self.left / "g1" / "packets-a.txt",
            self.right / "g1" / "packets-b.txt",
        ]
        a, b = [path.read_text().splitlines() for path in paths]
        a[:4], b[:4] = b[:4], a[:4]
        for path, lines in zip(paths, (a, b)):
            path.write_text("\n".join(lines))
        self.assertIn("PASS: real engine", self.verify())

    def test_reject_attribution_mutations(self):
        path = self.left / "g1" / "querylog.jsonl"
        original = json.loads(path.read_text())
        for key, value in (
            ("client.address", "198.18.0.12"),
            ("nexora.policy.group", "g2-client-10"),
            ("nexora.filter", "blocked"),
            ("dns.question.name", "udp.r0.g2.dsr-lab.test."),
            ("dns.response.code", "SERVFAIL"),
            ("nexora.transport", "proxy"),
        ):
            with self.subTest(key=key):
                batch = copy.deepcopy(original)
                record = batch["resourceLogs"][0]["scopeLogs"][0]["logRecords"][0]
                next(a for a in record["attributes"] if a["key"] == key)["value"][
                    "stringValue"
                ] = value
                path.write_text(json.dumps(batch))
                with self.assertRaises(ValueError):
                    self.verify()
        path.write_text(json.dumps(original))

    def test_missing_duplicate_or_foreign_engine_log(self):
        path = self.left / "g1" / "querylog.jsonl"
        original = json.loads(path.read_text())
        for mutation in ("missing", "duplicate", "foreign"):
            with self.subTest(mutation=mutation):
                batch = copy.deepcopy(original)
                resource = batch["resourceLogs"][0]
                records = resource["scopeLogs"][0]["logRecords"]
                if mutation == "missing":
                    records.pop()
                elif mutation == "duplicate":
                    records[-1] = records[0]
                else:
                    resource["resource"]["attributes"] = attrs(
                        {"service.name": "echo", "host.name": "wrong"}
                    )
                path.write_text(json.dumps(batch))
                with self.assertRaises(ValueError):
                    self.verify()

    def test_management_loss_and_missing_signed_result(self):
        health = self.left / "g1" / "management-health.txt"
        health.write_text("not ready")
        with self.assertRaisesRegex(ValueError, "management"):
            self.verify()
        health.write_text("ready")
        path = self.left / "g1" / "probe.log"
        path.write_text(path.read_text().replace('"signed": true', '"signed": false'))
        with self.assertRaisesRegex(ValueError, "signed/MTU"):
            self.verify()


if __name__ == "__main__":
    unittest.main()
