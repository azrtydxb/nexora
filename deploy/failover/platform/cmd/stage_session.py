#!/usr/bin/env python3
"""Private bounded stage protocol for the opt-in detached runtime lab only."""

import argparse
import os
import re
import select
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from linux_namespace_test import isolated


def receive(timeout=30):
    """Read one bounded ASCII record; never buffer a later acknowledgement."""
    until = time.monotonic() + timeout
    data = bytearray()
    while len(data) < 128:
        left = until - time.monotonic()
        if left <= 0 or not select.select([0], [], [], left)[0]:
            raise ValueError("stage protocol deadline")
        byte = os.read(0, 1)
        if byte == b"\n":
            return data.decode("ascii")
        if len(byte) != 1 or not 32 <= byte[0] <= 126:
            raise ValueError("invalid stage protocol")
        data.extend(byte)
    raise ValueError("oversized stage protocol")


def session(adapter, fence, baseline, expected):
    """The isolated namespace provider, not a reply boolean, contains traffic."""
    line = receive()
    if not re.fullmatch(r"STAGE [0-9a-f]{64}", line):
        raise ValueError("expected unique STAGE request")
    nonce = line.split()[1]
    adapter.reconcile(fence)
    if adapter.plan():
        raise ValueError("stage did not converge")
    print("STAGED " + nonce, flush=True)
    if receive(timeout=150) != "CLEAN " + nonce:
        raise ValueError("stale or invalid cleanup request")
    adapter.reconcile(fence, cleanup=True)
    if [adapter.inspect(a) for a in adapter.m["attachments"]] != baseline:
        raise ValueError("owned baseline not restored")
    # CLEANED is emitted by main only AFTER namespace cleanup succeeds.
    session.nonce = nonce


def main():
    """Re-exec into new namespaces, retaining original identity descriptors."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute-detached-stage", action="store_true")
    parser.add_argument("--isolated-fds", type=int, nargs=2)
    args = parser.parse_args()
    if not args.execute_detached_stage or sys.platform != "linux" or os.geteuid() != 0:
        raise ValueError("explicit detached-stage opt-in and Linux root required")
    if args.isolated_fds:
        isolated(args.isolated_fds, exercise=session)
        print("CLEANED " + session.nonce, flush=True)
        return
    fds = [os.open("/proc/self/ns/" + kind, os.O_RDONLY) for kind in ("net", "mnt")]
    for fd in fds:
        os.set_inheritable(fd, True)
    os.execv(
        "/usr/bin/unshare",
        [
            "unshare",
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
            "--execute-detached-stage",
            "--isolated-fds",
            *map(str, fds),
        ],
    )


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError) as exc:
        sys.exit(str(exc))
