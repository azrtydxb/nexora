"""Merge immutable copies of two stopped hosts' evidence and reuse tuple verifier."""

import importlib.util
from collections import Counter
import json
import shutil
import sys
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "tuple_verifier", Path(__file__).resolve().parents[1] / "verify.py"
)
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


def attributes(values):
    result = {}
    for item in values:
        verifier.require(item["key"] not in result, "duplicate OTLP attribute")
        result[item["key"]] = item["value"].get("stringValue")
    return result


def verify_engine(left, right, group, run):
    require = verifier.require
    observed = Counter()
    captures = {
        role: verifier.flows((base / group / f"packets-{role}.txt").read_text())
        for base, roles in ((left, "ac"), (right, "bd"))
        for role in roles
    }
    used = set()
    transports = {"udp", "tcp", "dot", "doh", "doq"}
    for base, role in ((left, "a"), (right, "b")):
        require(
            (base / group / "management-health.txt").read_text().strip() == "ready",
            "management attachment unhealthy",
        )
        records = []
        for line in (base / group / "querylog.jsonl").read_text().splitlines():
            batch = json.loads(line)
            for resource in batch.get("resourceLogs", []):
                attrs = attributes(resource["resource"]["attributes"])
                require(
                    attrs.get("service.name") == "nexora-engine"
                    and attrs.get("host.name") == f"fx-{run}-{group}-{role}",
                    "wrong engine resource",
                )
                for scope in resource.get("scopeLogs", []):
                    records.extend(scope.get("logRecords", []))
        require(
            len(records) == 20,
            f"{group}/{role}: expected twenty real engine query logs, got {len(records)}",
        )
        coverage = Counter()
        for record in records:
            a = attributes(record["attributes"])
            client, transport = a.get("client.address"), a.get("nexora.transport")
            require(
                client in verifier.CLIENTS and transport in transports,
                "engine source/transport mismatch",
            )
            policy = group + "-client-" + client.split(".")[-1]
            require(
                a.get("nexora.policy.group") == policy,
                "engine policy attribution mismatch",
            )
            name = a.get("dns.question.name", "")
            large = name.endswith(".mtu.test.")
            suffix = "mtu.test" if large else "dsr-lab.test"
            require(
                a.get("dns.question.type") == ("TXT" if large else "A")
                and a.get("dns.response.code") == "NOERROR",
                "engine DNS result mismatch",
            )
            require(
                a.get("nexora.filter") == ("none" if large else "rewritten"),
                "engine filter mismatch",
            )
            if not large:
                require(
                    a.get("nexora.filter.source") == "rewrite",
                    "missing rewrite attribution",
                )
            require(
                name in {f"{transport}.r{r}.{group}.{suffix}." for r in (0, 1)},
                "cross-group/unexpected query",
            )
            coverage[client, transport, large] += 1
            observed[role, client, policy, transport, name, a["dns.question.type"]] += 1
        require(
            coverage
            == Counter(
                {
                    (c, t, large): 1
                    for c in verifier.CLIENTS
                    for t in transports
                    for large in (False, True)
                }
            ),
            "backend did not serve each client/transport/payload exactly once",
        )
    expected = Counter()
    for base, client in ((left, "198.18.0.10"), (right, "198.18.0.11")):
        results = [
            json.loads(line)
            for line in (base / group / "probe.log").read_text().splitlines()
            if line.startswith("{")
        ]
        require(len(results) == 20, "missing original engine probe results")
        local = Counter()
        for result in results:
            require(result["client"] == client, "probe source mismatch")
            transport = result["transport"]
            require(transport in transports, "invalid probe transport")
            large = result["question"].endswith(".mtu.test.")
            qtype = "TXT" if large else "A"
            require(result.get("question_type") == qtype, "probe query type mismatch")
            socket = result.get("socket")
            require(isinstance(socket, dict), "missing original probe socket")
            require(
                socket.get("local_ip") == client
                and socket.get("remote_ip") == verifier.VIP,
                "probe socket address mismatch",
            )
            for field in ("local_port", "remote_port"):
                port = socket.get(field)
                require(
                    type(port) is int and 1 <= port <= 65535,
                    "invalid probe socket port",
                )
            proto, service = verifier.TRANSPORT[transport]
            require(
                str(socket["remote_port"]) == service, "probe service port mismatch"
            )
            flow = (proto, client, str(socket["local_port"]), verifier.VIP, service)
            require(flow not in used, "original probe socket collision/reuse")
            used.add(flow)
            client_role = "c" if base == left else "d"
            require(
                flow in captures[client_role],
                "original probe tuple absent from client capture",
            )
            owners = [role for role in "ab" if flow in captures[role]]
            require(
                len(owners) == 1,
                "original probe tuple must resolve to exactly one backend",
            )
            role = owners[0]
            reverse = (proto, verifier.VIP, service, client, str(socket["local_port"]))
            require(
                reverse in captures[role] and reverse in captures[client_role],
                "original probe reply tuple missing from captured backend/client",
            )
            policy = group + "-client-" + client.split(".")[-1]
            expected[role, client, policy, transport, result["question"], qtype] += 1
            if large:
                udp = result["transport"] == "udp"
                require(
                    result["signed"] is (not udp)
                    and result["truncated"] is udp
                    and (result["bytes"] <= 1232 if udp else result["bytes"] >= 2400),
                    "invalid large signed/MTU acceptance",
                )
            else:
                require(
                    result["answer"] == f"203.0.{group[1:]}.{client.split('.')[-1]}",
                    "probe policy mismatch",
                )
            local[client, result["transport"], result["question"]] += 1
        want = Counter(
            {
                (client, t, f"{t}.r{r}.{group}.{suffix}."): 1
                for t in transports
                for r in (0, 1)
                for suffix in ("dsr-lab.test", "mtu.test")
            }
        )
        require(local == want, "duplicate/missing original probe")
    replies = {(proto, dst, dp, src, sp) for proto, src, sp, dst, dp in used}
    for roles in ("ab", "cd"):
        captured = Counter(
            flow
            for role in roles
            for flow in captures[role]
            if flow[1] == verifier.VIP or flow[3] == verifier.VIP
        )
        require(
            captured == Counter({flow: 1 for flow in used | replies}),
            "captured tuples do not match exact original probes",
        )
    require(
        observed == expected, "backend engine logs do not match original client queries"
    )
    return "PASS: real engine source-IP, client/group rewrite policy and socket-to-backend OTLP attribution on both backends, all five transports; large signed payloads and UDP truncation; independent management attachment healthy."


def verify(left, right, output):
    require = verifier.require
    plans = [json.loads((p / "plan.json").read_text()) for p in (left, right)]
    require(plans[0] == plans[1], "host plans differ")
    require(
        (left / "role").read_text() == "left"
        and (right / "role").read_text() == "right",
        "incorrect host roles",
    )
    for base in (left, right):
        require(
            (base / "run-complete").read_text() == "0", "supervisor did not complete"
        )
        require(
            (base / "mode").read_text() == "dr", "SNAT control is not positive evidence"
        )
        require(
            (base / "probe-exit").read_text() == "0",
            "missing successful original probe",
        )
        require(
            json.loads((base / "cleanup.json").read_text()) == {"errors": []},
            "cleanup failed",
        )
    kinds = [
        (p / "backend").read_text() if (p / "backend").exists() else "echo"
        for p in (left, right)
    ]
    require(
        kinds[0] == kinds[1] and kinds[0] in ("echo", "engine"),
        "backend modes differ/invalid",
    )
    backend = kinds[0]
    if backend == "engine":
        builds = [
            json.loads((p / "engine-binary.json").read_text()) for p in (left, right)
        ]
        require(builds[0]["sha256"] == builds[1]["sha256"], "engine binaries differ")
        require(
            len(builds[0]["sha256"]) == 64
            and builds[0]["version"].startswith("nexora-engine "),
            "invalid engine build provenance",
        )
    output.mkdir(mode=0o700)
    for group in ("g1", "g2"):
        out = output / group
        out.mkdir()
        for base, roles in ((left, "ac"), (right, "bd")):
            for role in roles:
                for name in (
                    f"packets-{role}.txt",
                    f"capture-{role}.log",
                    f"{role}.pcap",
                ):
                    shutil.copyfile(base / group / name, out / name)
                if role in "ab":
                    shutil.copyfile(
                        base / group / f"backend-{role}.log",
                        out / f"backend-{role}.log",
                    )
        report = verifier.verify(out, backend)
        if backend == "engine":
            report += " " + verify_engine(left, right, group, plans[0]["run"])
            for base, role in ((left, "a"), (right, "b")):
                shutil.copyfile(
                    base / group / "querylog.jsonl", out / f"querylog-{role}.jsonl"
                )
        (out / "packet-verification.txt").write_text(report + "\n")
        print(group + ": " + report)


if __name__ == "__main__":
    verify(*(Path(p) for p in sys.argv[1:]))
