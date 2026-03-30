#!/usr/bin/env python3

import json
import re
import sys
from pathlib import Path


SEMVER_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")


def normalize_version(raw: str) -> str:
    value = raw.strip()
    if value.startswith("v"):
        value = value[1:]
    if not SEMVER_RE.fullmatch(value):
        raise ValueError(f"invalid semver version: {raw!r}")
    return value


def load_json(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def write_json(path: Path, payload: dict) -> None:
    path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")


def sync_package_json(path: Path, version: str) -> None:
    payload = load_json(path)
    payload["version"] = version
    write_json(path, payload)


def sync_package_lock(path: Path, version: str) -> None:
    payload = load_json(path)
    payload["version"] = version
    packages = payload.get("packages")
    if isinstance(packages, dict):
        root = packages.get("")
        if isinstance(root, dict):
            root["version"] = version
    write_json(path, payload)


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: sync_extension_version.py <semver>", file=sys.stderr)
        return 1

    version = normalize_version(argv[1])
    repo_root = Path(__file__).resolve().parent.parent
    extension_dir = repo_root / "extensions" / "vscode-lopper"

    sync_package_json(extension_dir / "package.json", version)
    sync_package_lock(extension_dir / "package-lock.json", version)
    print(version)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
