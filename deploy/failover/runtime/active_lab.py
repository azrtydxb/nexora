"""Parent-only ISOLATED active dual-egress lab. No production activation."""

import argparse
import json
import os
import secrets
import select
import signal
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "platform"))
from active_lab import NAMES, PAIRS, digest, forbidden_offloads, trusted
from active_lab import alias as lab_alias


def require(ok, reason):
    """Assertions stay enabled under optimized Python too."""
    if not ok:
        raise ValueError(reason)


def command(argv, timeout=20, **kwargs):
    """Bound one explicit argv operation; no shell, package/module installation."""
    return subprocess.run(argv, check=True, timeout=timeout, **kwargs)


def private_file(path, data):
    """Create only fresh files in the private mount; never overwrite caller data."""
    with path.open("x", encoding="utf-8") as stream:
        os.chmod(path, 0o600)
        stream.write(data)


def bootstrap(args):
    """Create synthetic resources solely on this namespace's new API server."""
    require(
        os.readlink(f"/proc/self/fd/{args.lab_net_fd}")
        == os.readlink("/proc/self/ns/net")
        != os.readlink("/proc/1/ns/net"),
        "pinned detached API namespace required",
    )
    root = Path("/run/nexora-runtime-lab")
    cert = root / "certs/apiserver.crt"
    until = time.monotonic() + 45
    context = None
    # Readiness polling is explicit startup scheduling, never a retry of a
    # failed Lease write or a hidden DNS/data-plane retry.
    while time.monotonic() < until:
        if cert.exists():
            context = ssl.create_default_context(cafile=str(cert))
            try:
                request = urllib.request.Request(
                    "https://127.0.0.1:6443/readyz",
                    headers={"Authorization": "Bearer " + (root / "token").read_text()},
                )
                with urllib.request.urlopen(
                    request, context=context, timeout=1
                ) as reply:
                    if reply.status == 200:
                        break
            except (OSError, urllib.error.URLError):
                pass
        time.sleep(0.1)
    else:
        raise ValueError("real API-server readiness deadline")

    def create(path, document):
        request = urllib.request.Request(
            "https://127.0.0.1:6443" + path,
            data=json.dumps(document).encode(),
            headers={
                "Authorization": "Bearer " + (root / "token").read_text(),
                "Content-Type": "application/json",
            },
            method="POST",
        )
        with urllib.request.urlopen(request, context=context, timeout=5) as reply:
            require(reply.status == 201, "real API create was not acknowledged")

    create(
        "/api/v1/namespaces",
        {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "fg-lab"}},
    )
    create(
        "/apis/coordination.k8s.io/v1/namespaces/fg-lab/leases",
        {
            "apiVersion": "coordination.k8s.io/v1",
            "kind": "Lease",
            "metadata": {
                "name": "isolated",
                "namespace": "fg-lab",
                "annotations": {
                    "failover.nexora.io/protocol": "nexora-kernel-ticket-v1",
                    "failover.nexora.io/epoch": "0",
                    "failover.nexora.io/nonce": secrets.token_hex(32),
                },
            },
            "spec": {"holderIdentity": ""},
        },
    )
    cfg = {
        "Mode": "isolated-active-lab-v1",
        "Driver": {
            "Enabled": True,
            "DriverPath": args.driver,
            "ObjectPath": args.object,
            "Interface": "fg0",
            "Alias": (root / "alias").read_text(),
            "LabBackendInterface": "bg0",
            "LabBackendAlias": (root / "backend-alias").read_text(),
            "UID": 0,
            "BootID": Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
            "NetworkNamespace": os.readlink("/proc/self/ns/net"),
            "TimeNamespace": os.readlink("/proc/self/ns/time"),
            "IOTimeout": 2_000_000_000,
            "ReapTimeout": 2_000_000_000,
        },
        "Stage": {
            "Python": str(Path(sys.executable).resolve()),
            "Script": str(Path(__file__).resolve().parent / "cmd/active_probe.py"),
            "UID": 0,
            "Timeout": 30_000_000_000,
        },
        "Endpoint": "https://127.0.0.1:6443",
        "Namespace": "fg-lab",
        "Name": "isolated",
        "TokenFile": str(root / "token"),
        "CAFile": str(cert),
        # LAB ONLY: a scheduling margin, not measured clock/drain evidence.
        "Margin": 100_000_000,
        "Interval": 250_000_000,
        "Duration": 12_000_000_000,
    }
    private_file(root / "config.json", json.dumps(cfg))


def isolated(args):
    """Prove private mounts/network/PID before creating any resource."""
    require(os.getpid() == 1, "private PID1 required")
    for kind, fd in zip(("net", "mnt"), args.isolated_fds):
        original = os.readlink(f"/proc/self/fd/{fd}")
        require(
            original.startswith(kind + ":[")
            and original != os.readlink("/proc/self/ns/" + kind),
            "distinct inherited isolation descriptors required",
        )
    require(
        not any(
            " shared:" in line
            for line in Path("/proc/self/mountinfo").read_text().splitlines()
        ),
        "private mount propagation required",
    )
    require(
        Path("/run").is_dir() and not Path("/run").is_symlink(), "real /run required"
    )
    # /run becomes private before journals, namespaces, keys or server data exist.
    command(
        ["/usr/bin/mount", "-t", "tmpfs", "-o", "mode=0755,size=256m", "tmpfs", "/run"]
    )
    Path("/run/netns").mkdir(mode=0o755)
    root = Path("/run/nexora-runtime-lab")
    root.mkdir(mode=0o700)
    (root / "certs").mkdir(mode=0o700)
    token = secrets.token_hex(32)  # synthetic lab token; no existing credentials
    private_file(root / "token", token)
    private_file(root / "tokens.csv", token + ",lab,lab,system:masters\n")
    # Manifest-derived aliases are written after exact fresh-link inventory.
    command(
        [
            args.openssl,
            "genpkey",
            "-algorithm",
            "RSA",
            "-pkeyopt",
            "rsa_keygen_bits:2048",
            "-out",
            str(root / "service-account.key"),
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    os.chmod(root / "service-account.key", 0o600)
    namespace = "nx-runtime-" + secrets.token_hex(6)
    owned_inode = None
    servers = []
    logs = []
    try:
        command(["/usr/sbin/ip", "netns", "add", namespace])
        owned_inode = os.stat("/run/netns/" + namespace).st_ino
        prefix = ["/usr/sbin/ip", "netns", "exec", namespace]
        command(
            [
                *prefix,
                "/usr/sbin/sysctl",
                "-qw",
                "net.ipv6.conf.all.disable_ipv6=1",
                "net.ipv6.conf.default.disable_ipv6=1",
            ]
        )
        for front, peer in PAIRS:
            command(
                [
                    "/usr/sbin/ip",
                    "-n",
                    namespace,
                    "link",
                    "add",
                    front,
                    "type",
                    "veth",
                    "peer",
                    "name",
                    peer,
                ]
            )
        links = json.loads(
            subprocess.check_output(
                ["/usr/sbin/ip", "-n", namespace, "-j", "-d", "link", "show"],
                timeout=10,
            )
        )
        m = {
            "version": "isolated-dual-v1",
            "run": secrets.token_hex(16),
            "netns": os.readlink("/proc/self/ns/net"),
            "links": {},
        }
        # readlink on the pinned namespace FD, never on a caller-supplied name.
        netfd = os.open("/run/netns/" + namespace, os.O_RDONLY)
        try:
            m["netns"] = os.readlink(f"/proc/self/fd/{netfd}")
        finally:
            os.close(netfd)
        for link in links:
            if link["ifname"] in NAMES:
                m["links"][link["ifname"]] = {
                    "index": link["ifindex"],
                    "peer": link["link_index"],
                    "mac": link["address"],
                }
        private_file(root / "manifest.json", json.dumps(m))
        private_file(root / "alias", lab_alias(m, "fg0"))
        private_file(root / "backend-alias", lab_alias(m, "bg0"))
        command(
            [
                "/usr/sbin/ip",
                "-n",
                namespace,
                "link",
                "set",
                "lo",
                "alias",
                "nexora-lab:" + digest(m),
            ]
        )
        for name in NAMES:
            command(
                [
                    "/usr/sbin/ip",
                    "-n",
                    namespace,
                    "link",
                    "set",
                    name,
                    "alias",
                    lab_alias(m, name),
                ]
            )
            command(
                [
                    *prefix,
                    "/usr/sbin/ethtool",
                    "-K",
                    name,
                    "gro",
                    "off",
                    "gso",
                    "off",
                    "tso",
                    "off",
                ]
            )
            features = subprocess.check_output(
                [*prefix, "/usr/sbin/ethtool", "-k", name], text=True, timeout=10
            )
            for feature in forbidden_offloads(features):
                command([*prefix, "/usr/sbin/ethtool", "-K", name, feature, "off"])
            command(
                [
                    *prefix,
                    "/usr/sbin/sysctl",
                    "-qw",
                    f"net.ipv4.conf.{name}.rp_filter=0",
                ]
            )
        command(
            [
                *prefix,
                "/usr/sbin/sysctl",
                "-qw",
                "net.ipv4.conf.all.rp_filter=0",
                "net.ipv4.ip_forward=0",
            ]
        )
        # No frontend/serving IP is assigned. Synthetic local route feeds the
        # namespace's real IPVS input hook; raw backend receiver has no address.
        command(
            [
                "/usr/sbin/ip",
                "-n",
                namespace,
                "route",
                "add",
                "local",
                "198.18.100.53/32",
                "dev",
                "lo",
            ]
        )
        command(
            [
                "/usr/sbin/ip",
                "-n",
                namespace,
                "route",
                "add",
                "198.18.101.2/32",
                "dev",
                "bg0",
            ]
        )
        command(
            [
                "/usr/sbin/ip",
                "-n",
                namespace,
                "neigh",
                "add",
                "198.18.101.2",
                "lladdr",
                m["links"]["bp0"]["mac"],
                "nud",
                "permanent",
                "dev",
                "bg0",
            ]
        )
        require(
            Path("/proc/net/ip_vs").exists(),
            "preloaded IPVS required; no module installation",
        )
        command(
            [*prefix, "/usr/sbin/ipvsadm", "-A", "-u", "198.18.100.53:53", "-s", "rr"]
        )
        command(
            [
                *prefix,
                "/usr/sbin/ipvsadm",
                "-a",
                "-u",
                "198.18.100.53:53",
                "-r",
                "198.18.101.2:53",
                "-g",
                "-w",
                "1",
            ]
        )
        for link in ("lo", "fp0", "bp0", "mg0", "mp0"):
            command(["/usr/sbin/ip", "-n", namespace, "link", "set", link, "up"])
        env = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C"}
        server_commands = [
            [
                args.etcd,
                "--name=lab",
                "--data-dir=" + str(root / "etcd"),
                "--listen-client-urls=http://127.0.0.1:2379",
                "--advertise-client-urls=http://127.0.0.1:2379",
                "--listen-peer-urls=http://127.0.0.1:2380",
                "--initial-advertise-peer-urls=http://127.0.0.1:2380",
                "--initial-cluster=lab=http://127.0.0.1:2380",
                "--initial-cluster-token=" + secrets.token_hex(16),
            ],
            [
                args.apiserver,
                "--etcd-servers=http://127.0.0.1:2379",
                "--bind-address=127.0.0.1",
                "--advertise-address=127.0.0.1",
                "--secure-port=6443",
                "--service-cluster-ip-range=10.254.0.0/24",
                "--authorization-mode=AlwaysAllow",
                "--anonymous-auth=false",
                "--token-auth-file=" + str(root / "tokens.csv"),
                "--service-account-issuer=https://nexora.invalid",
                "--service-account-signing-key-file="
                + str(root / "service-account.key"),
                "--service-account-key-file=" + str(root / "service-account.key"),
                "--cert-dir=" + str(root / "certs"),
            ],
        ]
        for index, argv in enumerate(server_commands):
            log = (root / f"server-{index}.log").open("xb")
            logs.append(log)
            servers.append(
                subprocess.Popen(
                    [*prefix, *argv], stdout=log, stderr=subprocess.STDOUT, env=env
                )
            )
        fd = os.open("/run/netns/" + namespace, os.O_RDONLY)
        try:
            command(
                [
                    *prefix,
                    sys.executable,
                    "-I",
                    "-B",
                    str(Path(__file__).resolve()),
                    *forward(args),
                    "--bootstrap",
                    "--lab-net-fd",
                    str(fd),
                ],
                timeout=60,
                pass_fds=(fd,),
                env=env,
            )
        finally:
            os.close(fd)
        exercise(args, prefix, root, env)

    finally:
        errors = []
        for server in reversed(servers):
            if server.poll() is None:
                server.terminate()
                try:
                    server.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    server.kill()
                    server.wait(timeout=5)
                    errors.append("API fixture required kill escalation")
        for log in logs:
            log.close()
            with Path(log.name).open("rb") as source:
                data = source.read((4 << 20) + 1)
            if len(data) > 4 << 20:
                errors.append("API fixture log exceeds preserved output bound")
                data = data[: 4 << 20]
            print(f"--- real API fixture {Path(log.name).name} ---", flush=True)
            sys.stdout.buffer.write(data)
            sys.stdout.buffer.flush()
        if owned_inode is not None:
            require(
                os.stat("/run/netns/" + namespace).st_ino == owned_inode,
                "refuse replaced namespace cleanup",
            )
            command(["/usr/sbin/ip", "netns", "delete", namespace])
        require(not errors, "; ".join(errors))
    print(
        "PASS: isolated active candidate "
        + args.scenario
        + " lab and owned cleanup; NOT HA acceptance",
        flush=True,
    )


def record(pipe, timeout=15):
    """Unbuffered private bounded parent/owner acknowledgement."""
    out = bytearray()
    end = time.monotonic() + timeout
    while len(out) < 128:
        require(
            time.monotonic() < end
            and select.select([pipe], [], [], max(0, end - time.monotonic()))[0],
            "owner acknowledgement timeout",
        )
        byte = os.read(pipe.fileno(), 1)
        require(byte, "owner EOF before acknowledgement")
        if byte == b"\n":
            return out.decode("ascii")
        out += byte
    raise ValueError("oversized owner acknowledgement")


def exercise(args, prefix, root, env):
    manifest = str(root / "manifest.json")
    probe = str(Path(__file__).resolve().parent / "cmd/active_probe.py")

    def packets(mode):
        command(
            [
                *prefix,
                sys.executable,
                "-I",
                "-B",
                probe,
                "--manifest",
                manifest,
                "--phase",
                mode,
            ],
            timeout=10,
            env=env,
        )

    owner = None
    try:
        if args.scenario == "attach-partial":
            attempt = subprocess.run(
                [
                    *prefix,
                    args.driver,
                    "--isolated-dual-lab",
                    "fg0",
                    (root / "alias").read_text(),
                    "bg0",
                    (root / "backend-alias").read_text(),
                    args.object,
                ],
                input=b"",
                check=False,
                capture_output=True,
                timeout=15,
                env=env,
            )
            require(
                attempt.returncode == 42
                and not attempt.stdout
                and b"INJECTED_SECOND_ATTACH_FAILURE" in attempt.stderr,
                "second-attach injection not reached",
            )
            links = json.loads(
                subprocess.check_output(
                    [*prefix, "/usr/sbin/ip", "-j", "link", "show"], timeout=10
                )
            )
            require(
                all(
                    "UP" not in x.get("flags", [])
                    for x in links
                    if x["ifname"] in ("fg0", "bg0")
                ),
                "partial attach enabled a data link",
            )
            first = json.loads(
                subprocess.check_output(
                    [
                        *prefix,
                        "/usr/sbin/tc",
                        "-j",
                        "filter",
                        "show",
                        "dev",
                        "fg0",
                        "egress",
                    ],
                    timeout=10,
                )
            )
            second = json.loads(
                subprocess.check_output(
                    [
                        *prefix,
                        "/usr/sbin/tc",
                        "-j",
                        "filter",
                        "show",
                        "dev",
                        "bg0",
                        "egress",
                    ],
                    timeout=10,
                )
            )
            require(len(first) == 1 and not second, "not an actual partial attachment")
            packets("deny")
            # Restart must fail BEFORE the injection: existing first hook is
            # never adopted, replaced or detached, including after process death.
            again = subprocess.run(
                [
                    *prefix,
                    args.driver,
                    "--isolated-dual-lab",
                    "fg0",
                    (root / "alias").read_text(),
                    "bg0",
                    (root / "backend-alias").read_text(),
                    args.object,
                ],
                input=b"",
                check=False,
                capture_output=True,
                timeout=15,
                env=env,
            )
            require(
                again.returncode not in (0, 42) and not again.stdout,
                "partial hook was adopted",
            )
            return
        if args.scenario == "frontend-only":
            # Deliberately incomplete gate: sensitivity control for stale-MAC
            # DR. Never ARM, so no capture or CAS is performed in this control.
            owner = subprocess.Popen(
                [
                    *prefix,
                    args.driver,
                    "fg0",
                    (root / "alias").read_text(),
                    args.object,
                ],
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                env=env,
                bufsize=0,
            )
            require(
                record(owner.stdout).startswith("READY ifindex="),
                "negative driver failed",
            )
            for name in ("fg0", "bg0"):
                command([*prefix, "/usr/sbin/ip", "link", "set", name, "up"])
            packets("leak")
            owner.stdin.write(b"DENY\n")
            require(record(owner.stdout) == "DENIED", "negative driver deny failed")
            owner.stdin.close()
            require(owner.wait(timeout=5) == 0, "negative driver exit failed")
            return
        owner = subprocess.Popen(
            [
                *prefix,
                "/usr/bin/setpriv",
                "--bounding-set=-net_raw",
                "--inh-caps=-all",
                "--ambient-caps=-all",
                "--no-new-privs",
                args.executable,
                "--execute-isolated-active-lab",
                "--config",
                str(root / "config.json"),
                "--manifest",
                manifest,
                "--scenario",
                args.scenario,
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            env=env,
            bufsize=0,
        )
        if args.scenario == "partial":
            require(
                record(owner.stdout) == "PARTIAL-REFUSED",
                "partial injection not reached",
            )
            require(
                owner.wait(timeout=20) != 0, "partial activation unexpectedly succeeded"
            )
            packets("deny")
            return
        require(record(owner.stdout) == "QUARANTINED", "missing actual quarantine")
        packets("deny")
        line = record(owner.stdout)
        if args.scenario == "delayed":
            require(line == "DELAYED-REFUSED", "delayed acknowledgement renewed TTL")
            require(owner.wait(timeout=10) == 0, "delayed negative failed")
            packets("deny")
            return
        fields = line.split()
        require(
            len(fields) == 3
            and fields[0] == "ARMED"
            and all(x.isdecimal() for x in fields[1:]),
            "invalid ARM record",
        )
        captured, deadline = map(int, fields[1:])
        require(
            0 < captured < deadline and deadline - captured == 5_000_000_000,
            "nonabsolute window",
        )
        packets("allow")
        if args.scenario == "stop":
            owner.send_signal(signal.SIGSTOP)
            stopped = time.monotonic() + 2
            while True:
                status = Path(f"/proc/{owner.pid}/status").read_text()
                if any(
                    line.startswith("State:") and "T (stopped)" in line
                    for line in status.splitlines()
                ):
                    break
                require(
                    time.monotonic() < stopped and owner.poll() is None,
                    "owner did not enter SIGSTOP",
                )
                time.sleep(0.01)
        elif args.scenario == "death":
            owner.kill()
            require(
                owner.wait(timeout=5) == -signal.SIGKILL, "owner death not observed"
            )
        # Parent's exact same zero-offset boot clock, independent of owner timers.
        while time.clock_gettime_ns(time.CLOCK_BOOTTIME) < deadline:
            time.sleep(0.01)
        packets("deny")
        if args.scenario == "stop":
            owner.send_signal(signal.SIGCONT)
        if args.scenario != "death":
            require(owner.wait(timeout=20) == 0, "owner shutdown failed")
    finally:
        if owner is not None and owner.poll() is None:
            owner.kill()
            owner.wait(timeout=5)
        if owner is not None and owner.stdout:
            owner.stdout.close()
        if owner is not None and owner.stdin and not owner.stdin.closed:
            owner.stdin.close()


def forward(args):
    """Preserve only explicit executable inputs across isolation."""
    out = ["--execute", "--scenario", args.scenario]
    for name in ("driver", "object", "executable", "etcd", "apiserver", "openssl"):
        out += ["--" + name, getattr(args, name)]
    return out


def main():
    """No default execution or capability fallback."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute", action="store_true")
    parser.add_argument(
        "--scenario",
        choices=(
            "expiry",
            "stop",
            "death",
            "delayed",
            "partial",
            "frontend-only",
            "attach-partial",
        ),
        required=True,
    )
    for name in ("driver", "object", "executable", "etcd", "apiserver", "openssl"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--isolated-fds", type=int, nargs=2)
    parser.add_argument("--bootstrap", action="store_true")
    parser.add_argument("--lab-net-fd", type=int)
    args = parser.parse_args()
    require(
        args.execute and sys.platform == "linux" and os.geteuid() == 0,
        "explicit opt-in and Linux root required",
    )
    for name in ("driver", "object", "executable", "etcd", "apiserver", "openssl"):
        path = Path(getattr(args, name))
        require(
            path.is_absolute()
            and path.is_file()
            and str(path.resolve()) == str(path)
            and not path.is_relative_to("/run"),
            "canonical existing executable/object paths required",
        )
    for name in ("driver", "object", "executable", "etcd", "apiserver", "openssl"):
        trusted(getattr(args, name), name != "object")
    trusted("/usr/bin/setpriv", True)
    trusted(str(Path(sys.executable).resolve()), True)
    for path in (
        Path(__file__).resolve(),
        Path(__file__).resolve().parents[1] / "platform/active_lab.py",
        Path(__file__).resolve().parent / "cmd/active_probe.py",
    ):
        trusted(str(path))
    if args.bootstrap:
        bootstrap(args)
    elif args.isolated_fds:
        isolated(args)
    else:
        fds = [os.open("/proc/self/ns/" + kind, os.O_RDONLY) for kind in ("net", "mnt")]
        try:
            command(
                [
                    "/usr/bin/unshare",
                    "--mount",
                    "--net",
                    "--pid",
                    "--fork",
                    "--kill-child=SIGKILL",
                    "--mount-proc",
                    "--propagation",
                    "private",
                    sys.executable,
                    "-I",
                    "-B",
                    str(Path(__file__).resolve()),
                    *forward(args),
                    "--isolated-fds",
                    *map(str, fds),
                ],
                timeout=240,
                pass_fds=fds,
            )
        finally:
            for fd in fds:
                os.close(fd)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as exc:
        sys.exit(str(exc))
