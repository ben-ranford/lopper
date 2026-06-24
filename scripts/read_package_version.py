#!/usr/bin/env python3

import json
import sys
from pathlib import Path


def resolve_package_path(raw_path: str) -> Path:
    requested_path = Path(raw_path)
    if requested_path.name != "package.json":
        raise ValueError(f"Invalid package path {raw_path}: expected a package.json file")

    repo_root = Path(__file__).resolve().parent.parent
    path = requested_path.resolve(strict=False) if requested_path.is_absolute() else (Path.cwd() / requested_path).resolve(strict=False)
    try:
        path.relative_to(repo_root)
    except ValueError as exc:
        raise ValueError(f"Invalid package path {raw_path}: must stay within {repo_root}") from exc

    return path


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: read_package_version.py <package.json>", file=sys.stderr)
        return 1

    try:
        path = resolve_package_path(argv[1])
    except ValueError as exc:
        print(exc, file=sys.stderr)
        return 1

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
