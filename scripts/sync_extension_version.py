#!/usr/bin/env python3

import json
import re
import sys
from pathlib import Path


SEMVER_RE = re.compile(r"^\d+\.\d+\.\d+$")
REPO_ROOT = Path(__file__).resolve().parent.parent
PACKAGE_JSON_PATH = REPO_ROOT / "extensions" / "vscode-lopper" / "package.json"
PACKAGE_LOCK_PATH = REPO_ROOT / "extensions" / "vscode-lopper" / "package-lock.json"


def normalize_version(raw: str) -> str:
    value = raw.strip()
    if value.startswith("v"):
        value = value[1:]
    if not SEMVER_RE.fullmatch(value):
        raise ValueError(f"invalid semver version: {raw!r}")
    return value


def load_json(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def write_package_json(payload: dict) -> None:
    PACKAGE_JSON_PATH.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")


def write_package_lock(payload: dict) -> None:
    PACKAGE_LOCK_PATH.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")


def sync_package_json(version: str) -> None:
    payload = load_json(PACKAGE_JSON_PATH)
    payload["version"] = version
    write_package_json(payload)


def sync_package_lock(version: str) -> None:
    payload = load_json(PACKAGE_LOCK_PATH)
    payload["version"] = version
    packages = payload.get("packages")
    if isinstance(packages, dict):
        root = packages.get("")
        if isinstance(root, dict):
            root["version"] = version
    write_package_lock(payload)


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: sync_extension_version.py <semver>", file=sys.stderr)
        return 1

    version = normalize_version(argv[1])
    sync_package_json(version)
    sync_package_lock(version)
    print(version)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
