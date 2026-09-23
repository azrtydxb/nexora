#!/usr/bin/env python3
"""Offline bootstrap controls; no network, CA generation, or system trust edits; ephemeral test leaf only."""

import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/ci-trusted-ca.sh"
COUNTS = {"ci": 6, "fuzz": 1, "perf-gate": 2, "images": 2}


def wiring_errors(name, text):
    errors = []
    body = "".join(
        "          " + line + "\n" for line in SCRIPT.read_text().splitlines()[3:]
    )
    step = (
        "      - name: Install provisioned CI CA\n"
        "        env:\n"
        "          NEXORA_CI_CA_PEM: ${{ secrets.NEXORA_CI_CA_PEM }}\n"
        "        run: |\n" + body
    )
    if text.count(step) != COUNTS[name]:
        errors.append("bootstrap drift or missing secret")
    if re.search(
        r"curl\b[^\n]*(?:--insecure|\s-[A-Za-z]*k)|(?:insecure|http)\s*=\s*true|repository/public/cluster-ca",
        text,
    ):
        errors.append("insecure trust")
    if "    shell: bash\n" not in text:
        errors.append("shell failure propagation")
    if name == "images":
        for registry in ("192.168.10.131", "192.168.10.131:5000"):
            if (
                f'[registry."{registry}"]\n'
                "            http = false\n"
                "            insecure = false\n"
                '            ca = ["$NEXORA_CI_CA_FILE"]'
            ) not in text:
                errors.append("registry trust")
    return errors


class TrustedCATests(unittest.TestCase):
    def test_all_workflow_bootstraps_and_regressions(self):
        for name in COUNTS:
            text = (ROOT / f".github/workflows/{name}.yml").read_text()
            with self.subTest(workflow=name):
                self.assertEqual(wiring_errors(name, text), [])
                for mutation in (
                    "curl -sk https://example.invalid/ca",
                    "curl --insecure https://example.invalid/ca",
                    "insecure = true",
                    "http = true",
                ):
                    self.assertIn(
                        "insecure trust", wiring_errors(name, text + "\n" + mutation)
                    )
                self.assertTrue(
                    wiring_errors(
                        name,
                        text.replace("secrets.NEXORA_CI_CA_PEM", "secrets.MISSING"),
                    )
                )
                self.assertTrue(
                    wiring_errors(name, text.replace("openssl verify", "echo verify"))
                )
                for mutation in (
                    text.replace("-ext basicConstraints", "-text"),
                    text.replace(
                        "grep -E '^[[:space:]]*CA:TRUE(, pathlen:[0-9]+)?[[:space:]]*$'",
                        "grep 'CA:TRUE'",
                    ),
                ):
                    self.assertIn(
                        "bootstrap drift or missing secret",
                        wiring_errors(name, mutation),
                    )
                self.assertTrue(
                    wiring_errors(name, text.replace("shell: bash", "shell: sh"))
                )
        self.assertTrue(
            wiring_errors("images", text.replace('ca = ["$NEXORA_CI_CA_FILE"]', ""))
        )

    def run_bootstrap(self, value, public_bundle=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            envfile = root / "env"
            envfile.touch()
            env = {
                "PATH": os.environ["PATH"],
                "RUNNER_TEMP": directory,
                "GITHUB_ENV": str(envfile),
            }
            if value is not None:
                env["NEXORA_CI_CA_PEM"] = value
            if public_bundle:
                # Redirect only the Linux public bundle read on Darwin. No trust writes.
                bindir = root / "bin"
                bindir.mkdir()
                cat = bindir / "cat"
                cat.write_text(
                    "#!/bin/bash\nif [[ $1 == /etc/ssl/certs/ca-certificates.crt ]]; then\n"
                    '  shift\n  exec /bin/cat "$TEST_PUBLIC_BUNDLE" "$@"\n'
                    'fi\nexec /bin/cat "$@"\n'
                )
                cat.chmod(0o700)
                env["PATH"] = str(bindir) + ":" + env["PATH"]
                env["TEST_PUBLIC_BUNDLE"] = str(public_bundle)
            result = subprocess.run(
                ["/bin/bash", "-x", str(SCRIPT)],
                env=env,
                check=False,
                capture_output=True,
                text=True,
            )
            outputs = envfile.read_text()
            if result.returncode == 0:
                paths = dict(line.split("=", 1) for line in outputs.splitlines())
                self.assertEqual(
                    set(paths),
                    {
                        "NEXORA_CI_CA_FILE",
                        "NODE_EXTRA_CA_CERTS",
                        "CARGO_HTTP_CAINFO",
                        "SSL_CERT_FILE",
                        "GIT_SSL_CAINFO",
                        "CURL_CA_BUNDLE",
                    },
                )
                for path in paths.values():
                    self.assertTrue(Path(path).is_relative_to(root))
                    self.assertTrue(Path(path).is_file())
                self.assertIn(
                    "BEGIN CERTIFICATE", Path(paths["NEXORA_CI_CA_FILE"]).read_text()
                )
                self.assertTrue(
                    Path(paths["SSL_CERT_FILE"])
                    .read_bytes()
                    .startswith(public_bundle.read_bytes())
                )
            else:
                self.assertEqual(outputs, "")
                self.assertEqual(list(root.glob("nexora-ca.*")), [])
            if value:
                self.assertNotIn(value.strip(), result.stdout + result.stderr)
            return result

    def test_missing_and_malformed_fail_closed(self):
        for value in (
            None,
            "",
            "not-a-certificate",
            "-----BEGIN CERTIFICATE-----\nZmFrZQ==\n-----END CERTIFICATE-----",
            "-----BEGIN PRIVATE KEY-----\nnot-a-key\n-----END PRIVATE KEY-----",
        ):
            with self.subTest(value=value is not None):
                self.assertNotEqual(self.run_bootstrap(value).returncode, 0)

    def test_self_signed_non_ca_with_ca_true_in_cn_is_rejected(self):
        # Disposable leaf/key only: never a CA or infrastructure credential.
        with tempfile.TemporaryDirectory() as directory:
            cert = Path(directory) / "leaf.pem"
            key = Path(directory) / "leaf.key"
            subprocess.run(
                [
                    "openssl",
                    "req",
                    "-x509",
                    "-newkey",
                    "rsa:2048",
                    "-nodes",
                    "-keyout",
                    str(key),
                    "-out",
                    str(cert),
                    "-days",
                    "1",
                    "-subj",
                    "/CN=offline-test-CA:TRUE",
                    "-addext",
                    "basicConstraints=critical,CA:FALSE",
                ],
                check=True,
                capture_output=True,
            )
            # The old text grep and retained self-signature check both accept it.
            full_text = subprocess.run(
                ["openssl", "x509", "-in", str(cert), "-noout", "-text"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout
            self.assertIn("CA:TRUE", full_text)
            constraints = subprocess.run(
                [
                    "openssl",
                    "x509",
                    "-in",
                    str(cert),
                    "-noout",
                    "-ext",
                    "basicConstraints",
                ],
                check=True,
                capture_output=True,
                text=True,
            ).stdout
            self.assertIn("CA:FALSE", constraints)
            self.assertNotIn("CA:TRUE", constraints)
            subprocess.run(
                ["openssl", "verify", "-check_ss_sig", "-CAfile", str(cert), str(cert)],
                check=True,
                capture_output=True,
            )
            result = self.run_bootstrap(cert.read_text(), cert)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(
                "Trusted CI CA missing, invalid, or unavailable", result.stderr
            )

    def test_existing_public_root_and_rejected_extra_material(self):
        # Use an existing public OS root solely as test data; never generate CA material.
        bundle = next(
            p
            for p in (
                Path("/etc/ssl/certs/ca-certificates.crt"),
                Path("/etc/ssl/cert.pem"),
            )
            if p.is_file()
        )
        roots = re.findall(
            r"-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----",
            bundle.read_text(),
            re.DOTALL,
        )
        for root in roots:
            with tempfile.TemporaryDirectory() as directory:
                cert = Path(directory) / "root.pem"
                cert.write_text(root + "\n")
                result = subprocess.run(
                    [
                        "openssl",
                        "verify",
                        "-check_ss_sig",
                        "-CAfile",
                        str(cert),
                        str(cert),
                    ],
                    check=False,
                    capture_output=True,
                )
            if result.returncode == 0:
                break
        else:
            self.fail("No currently valid public OS root available for offline test")
        self.assertEqual(self.run_bootstrap(root, bundle).returncode, 0)
        for invalid in (
            root + "\n" + root,
            root + "\nextra-material",
            "extra-material\n" + root,
            root.replace("MI", "AA", 1),
        ):
            self.assertNotEqual(self.run_bootstrap(invalid, bundle).returncode, 0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
