#!/usr/bin/env python3
"""W4-15 wiring regressions, not evidence of supported product execution.

--check-root checks a saved pre-change fixture or another checkout without executing it.
Default mode also proves each omitted coverage group is rejected by mutation.
"""

import argparse
import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path


def findings(root, replacements=()):
    workflow = (root / ".github/workflows/ci.yml").read_text()
    make = (root / "Makefile").read_text()
    for old, new in replacements:
        workflow = workflow.replace(old, new)
        make = make.replace(old, new)
    errors = []
    requirements = {
        "full E2E/browser": [
            "make ci-e2e",
            "ci-e2e:",
            "$(MAKE) e2e-build",
            "go test -json -count=1 -timeout 120m ./e2e/... ./mgmt/internal/querylog/e2e/...",
            "pnpm exec playwright install chromium",
            "bash scripts/ci-prerequisites.sh e2e",
        ],
        "deployment/control race": [
            "make ci-race",
            "ci-race:",
            "go test -json -race -count=1 -timeout 30m $(GO_PKGS) ./e2e/harness/...",
            "foreach d,mgmt gen bench deploy",
            "bash scripts/ci-prerequisites.sh race",
        ],
        "failover contracts": [
            "make ci-failover",
            "ci-failover:",
            "python3 -O -m unittest discover -s deploy/failoverlab -v",
            "python3 -O -m unittest discover -s deploy/failoverlab/crosshost -v",
            "python3 -B -m unittest discover -s deploy/failover/platform -v",
            "$(MAKE) -C deploy/failover/fence test",
        ],
        "M8 units": [
            "make web-test web-unit-test",
            "web-unit-test:",
            "pnpm exec playwright test e2e/unit --output test-results/unit",
        ],
    }
    for group, tokens in requirements.items():
        if any(token not in workflow + make for token in tokens):
            errors.append(group)
    if workflow.count("if: always()") < 4 or "web/test-results/" not in workflow:
        errors.append("artifact retention")
    if "shell: bash" not in workflow or re.search(
        r"continue-on-error:\s*true|\|\|\s*true", workflow
    ):
        errors.append("failure propagation")
    if "oapi-codegen@v2.8.0" not in make:
        errors.append("generator pin")
    return errors


ROOT = Path(__file__).resolve().parents[1]


class CoverageTests(unittest.TestCase):
    def test_trusted_ca_controls(self):
        subprocess.run(
            [os.sys.executable, str(ROOT / "scripts/ci-trusted-ca-test.py")], check=True
        )

    def test_current_wiring(self):
        self.assertEqual(findings(ROOT), [])

    def test_omissions_are_red(self):
        mutations = {
            "full E2E/browser": "make ci-e2e",
            "deployment/control race": "foreach d,mgmt gen bench deploy",
            "failover contracts": "$(MAKE) -C deploy/failover/fence test",
            "M8 units": "make web-test web-unit-test",
            "artifact retention": "if: always()",
            "failure propagation": "shell: bash",
            "generator pin": "oapi-codegen@v2.8.0",
        }
        for group, token in mutations.items():
            with self.subTest(group=group):
                self.assertIn(group, findings(ROOT, [(token, "")]))

    def test_every_contract_suite_is_required(self):
        for suite in (
            "deploy/failoverlab",
            "deploy/failoverlab/crosshost",
            "deploy/failover/platform",
        ):
            with self.subTest(suite=suite):
                self.assertIn(
                    "failover contracts", findings(ROOT, [(f"-s {suite} -v", "")])
                )

    def test_prerequisites_fail_closed(self):
        # Fake only uname; no Linux product execution is claimed by these controls.
        with tempfile.TemporaryDirectory() as directory:
            uname = Path(directory) / "uname"
            uname.write_text("#!/bin/sh\necho Darwin\n")
            uname.chmod(0o700)
            env = {"PATH": directory}
            command = [
                "/bin/bash",
                str(ROOT / "scripts/ci-prerequisites.sh"),
                "contracts",
            ]
            result = subprocess.run(
                command, env=env, check=False, capture_output=True, text=True
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("requires Linux", result.stderr)
            uname.write_text("#!/bin/sh\necho Linux\n")
            supported = subprocess.run(
                command[:-1] + ["linux"],
                env=env,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(supported.returncode, 0)
            result = subprocess.run(
                command, env=env, check=False, capture_output=True, text=True
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Missing prerequisite: python3", result.stderr)
            env["NEXORA_KW_DNS_ADDR"] = "must-not-contact.invalid"
            result = subprocess.run(
                command, env=env, check=False, capture_output=True, text=True
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Unexpected live/lab environment", result.stderr)

    def test_raw_log_does_not_hide_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "failure.log"
            result = subprocess.run(
                [
                    "/bin/bash",
                    "-e",
                    "-o",
                    "pipefail",
                    "-c",
                    '{ echo expected-failure; exit 17; } 2>&1 | tee "$1"',
                    "ci-test",
                    str(log),
                ],
                env=os.environ.copy(),
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 17)
            self.assertEqual(log.read_text(), "expected-failure\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--check-root", type=Path)
    args = parser.parse_args()
    if args.check_root:
        errors = findings(args.check_root)
        print("\n".join(errors) if errors else "coverage wiring present")
        raise SystemExit(bool(errors))
    unittest.main(argv=[__file__], verbosity=2)
