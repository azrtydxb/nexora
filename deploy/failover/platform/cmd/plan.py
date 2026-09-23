#!/usr/bin/env python3
"""Read-only manifest validation/inspection. No mutation or activation switches."""

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from adapter import Adapter, LinuxRunner, identity, load_manifest


def main():
    """Validate input or inspect Linux state without executing pending mutations."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest")
    parser.add_argument(
        "--check",
        action="store_true",
        help="read actual Linux resources and print staged reconciliation commands",
    )
    args = parser.parse_args()
    manifest = load_manifest(args.manifest)
    result = {
        "identity": identity(manifest),
        "state": "validated-only",
        "active": False,
    }
    if args.check:
        result.update(
            state="checked-only", commands=Adapter(manifest, LinuxRunner()).plan()
        )
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError) as exc:
        sys.exit(str(exc))
