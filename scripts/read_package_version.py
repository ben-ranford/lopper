#!/usr/bin/env python3

import json
import sys
from pathlib import Path


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: read_package_version.py <package.json>", file=sys.stderr)
        return 1

    path = Path(argv[1])
    try:
        package = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        print(f"Missing {path}", file=sys.stderr)
        return 1
    except json.JSONDecodeError as exc:
        print(f"Invalid JSON in {path}: {exc}", file=sys.stderr)
        return 1

    version = package.get("version")
    if not isinstance(version, str) or version == "":
        print(f"Missing string version in {path}", file=sys.stderr)
        return 1

    print(version)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
