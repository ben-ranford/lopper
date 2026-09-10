#!/usr/bin/env python3
"""Add source-build Go requirements to the current release notes."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

from vscode_release_notes import INVALID_STABLE_TAG, STABLE_TAG, entry_version, git, release_entries


CHANGELOG_PATH = Path("CHANGELOG.md")
MANIFEST_PATH = Path(".release-please-manifest.json")
GO_DIRECTIVE = re.compile(r"^[ \t]*go[ \t]+(\d+\.\d+(?:\.\d+)?)[ \t]*(?://.*)?$", re.MULTILINE)
GENERATED_NOTE = re.compile(
    r"^\* Source builds require Go `[^`]+` or newer \(previously `[^`]+`\)\.\n{1,2}",
    re.MULTILINE,
)


def go_requirement(go_mod: str) -> str:
    directives = GO_DIRECTIVE.findall(go_mod)
    if len(directives) != 1:
        raise ValueError("go.mod must contain exactly one valid go directive")
    return directives[0]


def trusted_changelog(repo: Path) -> Path:
    repo_root = repo.resolve()
    changelog = repo / CHANGELOG_PATH
    resolved = changelog.resolve()
    if not resolved.is_relative_to(repo_root) or resolved != repo_root / CHANGELOG_PATH:
        raise ValueError("root changelog must not resolve outside the repository")
    return changelog


def write_changelog(repo: Path, content: str) -> None:
    repo_root = repo.resolve()
    changelog = repo_root / "CHANGELOG.md"
    resolved = changelog.resolve()
    if not resolved.is_relative_to(repo_root) or resolved != changelog:
        raise ValueError("root changelog must not resolve outside the repository")
    changelog.write_text(content, encoding="utf-8")


def manifest_version(repo: Path) -> str:
    manifest = json.loads((repo / MANIFEST_PATH).read_text(encoding="utf-8"))
    value = manifest.get(".") if isinstance(manifest, dict) else None
    if not isinstance(value, str) or not re.fullmatch(r"\d{1,9}\.\d{1,9}\.\d{1,9}", value):
        raise ValueError(".release-please-manifest.json must contain a root release version")
    return value


def latest_release_bounds(changelog: str, expected_version: str) -> tuple[int, int]:
    entries = release_entries(changelog)
    if not entries:
        raise ValueError("root changelog must contain a release entry")
    if entry_version(entries[0]) != expected_version:
        raise ValueError("root changelog newest entry must match .release-please-manifest.json")
    start = entries[0].start()
    end = entries[1].start() if len(entries) > 1 else len(changelog)
    return start, end


def build_note(previous: str, current: str) -> str | None:
    if previous == current:
        return None
    return f"* Source builds require Go `{current}` or newer (previously `{previous}`).\n"


def refresh_current_block(block: str, note: str | None) -> str:
    block = GENERATED_NOTE.sub("", block)
    if note is None:
        return block
    header_end = block.find("\n")
    if header_end < 0:
        raise ValueError("root changelog release entry must contain a heading")
    return block[:header_end + 1] + "\n" + note + block[header_end + 1:]


def generate(repo: Path, previous_tag: str) -> None:
    if not STABLE_TAG.fullmatch(previous_tag):
        raise ValueError(INVALID_STABLE_TAG)
    changelog_path = trusted_changelog(repo)
    changelog = changelog_path.read_text(encoding="utf-8")
    start, end = latest_release_bounds(changelog, manifest_version(repo))
    current = go_requirement((repo / "go.mod").read_text(encoding="utf-8"))
    previous = go_requirement(git(repo, "show", f"{previous_tag}:go.mod"))
    block = refresh_current_block(changelog[start:end], build_note(previous, current))
    write_changelog(repo, changelog[:start] + block + changelog[end:])


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--previous-tag", required=True)
    args = parser.parse_args(argv)
    try:
        generate(args.repo.resolve(), args.previous_tag)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"release build notes: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
