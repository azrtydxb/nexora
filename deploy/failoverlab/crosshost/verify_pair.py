"""Merge immutable copies of two stopped hosts' evidence and reuse tuple verifier."""

import importlib.util
import json
import shutil
import sys
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "tuple_verifier", Path(__file__).resolve().parents[1] / "verify.py"
)
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


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
        report = verifier.verify(out)
        (out / "packet-verification.txt").write_text(report + "\n")
        print(group + ": " + report)


if __name__ == "__main__":
    verify(*(Path(p) for p in sys.argv[1:]))
