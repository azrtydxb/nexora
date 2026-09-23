#!/usr/bin/env python3
"""Parent-only detached runtime/real API-server harness; no serving frontend."""

import argparse
import json
import os
import secrets
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path


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


def require_detached_namespace(fd):
    """Compare pinned kernel identities, not procfs names of bind-mounted handles."""
    pinned = os.fstat(fd)
    current = os.stat("/proc/self/ns/net")
    initial = os.stat("/proc/1/ns/net")

    def key(info):
        return info.st_dev, info.st_ino

    require(
        key(pinned) == key(current) != key(initial),
        "pinned detached API namespace required",
    )


def bootstrap(args):
    """Create synthetic resources solely on this namespace's new API server."""
    require_detached_namespace(args.lab_net_fd)
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
        "Mode": "detached-lab-v1",
        "Driver": {
            "Enabled": True,
            "DriverPath": args.driver,
            "ObjectPath": args.object,
            "Interface": "fg0",
            "Alias": (root / "alias").read_text(),
            "UID": 0,
            "BootID": Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
            "NetworkNamespace": os.readlink("/proc/self/ns/net"),
            "TimeNamespace": os.readlink("/proc/self/ns/time"),
            "IOTimeout": 2_000_000_000,
            "ReapTimeout": 2_000_000_000,
        },
        "Stage": {
            "Python": str(Path(sys.executable).resolve()),
            "Script": str(
                Path(__file__).resolve().parents[1] / "platform/cmd/stage_session.py"
            ),
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
    alias = "nexora-fence:" + secrets.token_hex(16)
    private_file(root / "alias", alias)
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
        for front, peer in (("fg0", "fp0"), ("mg0", "mp0")):
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
        command(["/usr/sbin/ip", "-n", namespace, "link", "set", "fg0", "alias", alias])
        for link in ("lo", "fp0", "mg0", "mp0"):
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
        if args.scenario == "runtime":
            argv = [
                args.executable,
                "--execute-detached-lab",
                "--config",
                str(root / "config.json"),
            ]
        else:
            argv = [
                args.executable,
                "-test.run=^TestIntegrationActualDriverAPI$",
                "-test.v",
                "-test.timeout=90s",
            ]
            env.update(
                NEXORA_FG05_REAL_API="yes",
                NEXORA_FG05_LAB_CONFIG=str(root / "config.json"),
            )
        command([*prefix, *argv], timeout=120, env=env)
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
        "PASS: detached " + args.scenario + " lab and owned cleanup; NOT HA acceptance",
        flush=True,
    )


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
    parser.add_argument("--scenario", choices=("runtime", "driver"), default="runtime")
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
