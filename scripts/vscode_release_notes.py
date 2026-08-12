#!/usr/bin/env python3
"""Generate and validate the VS Code extension release-notes contract."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from datetime import date
from pathlib import Path, PurePosixPath


EXTENSION_DIR = Path("extensions/vscode-lopper")
CHANGELOG_FILE_NAME = "CHANGELOG.md"
CHANGELOG_PATH = EXTENSION_DIR / CHANGELOG_FILE_NAME
PACKAGE_PATH = EXTENSION_DIR / "package.json"
LOCKFILE_PATH = EXTENSION_DIR / "package-lock.json"
ROOT_CHANGELOG_PATH = Path(CHANGELOG_FILE_NAME)
VERSION_HEADER = re.compile(
    r"^## (?:\[(?P<linked_version>\d{1,9}\.\d{1,9}\.\d{1,9})\]\([^)]+\)|(?P<version>\d{1,9}\.\d{1,9}\.\d{1,9})) \((?P<date>\d{4}-\d{2}-\d{2})\)$",
    re.MULTILINE,
)
STABLE_TAG = re.compile(r"v\d{1,9}\.\d{1,9}\.\d{1,9}\Z")


def read_json(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def release_entries(changelog: str) -> list[re.Match[str]]:
    return list(VERSION_HEADER.finditer(changelog))


def entry_version(entry: re.Match[str]) -> str:
    return entry.group("linked_version") or entry.group("version")


def trusted_extension_changelog(repo: Path) -> Path:
    repo_root = repo.resolve()
    changelog = (repo / CHANGELOG_PATH).resolve()
    try:
        changelog.relative_to(repo_root)
    except ValueError as exc:
        raise ValueError("VS Code changelog must not resolve outside the repository") from exc
    return changelog


def write_extension_changelog(repo: Path, content: str) -> None:
    repo_root = repo.resolve()
    changelog = repo_root / EXTENSION_DIR / CHANGELOG_FILE_NAME
    resolved_changelog = changelog.resolve()
    if not resolved_changelog.is_relative_to(repo_root) or resolved_changelog != changelog:
        raise ValueError("VS Code changelog must not resolve outside the repository")
    changelog.write_text(content, encoding="utf-8")


def validate(repo: Path) -> list[str]:
    package = read_json(repo / PACKAGE_PATH)
    lockfile = read_json(repo / LOCKFILE_PATH)
    version = package.get("version")
    lock_version = lockfile.get("version")
    root_lock_version = lockfile.get("packages", {}).get("", {}).get("version")
    errors: list[str] = []
    if not isinstance(version, str) or not version:
        errors.append("VS Code package.json must contain a version")
        return errors
    if lock_version != version or root_lock_version != version:
        errors.append("VS Code package.json and package-lock.json versions must match")

    entries = release_entries(trusted_extension_changelog(repo).read_text(encoding="utf-8"))
    matching = [entry for entry in entries if entry_version(entry) == version]
    if len(matching) != 1:
        errors.append(f"VS Code changelog must contain exactly one {version} entry")
    if not entries or entry_version(entries[0]) != version:
        errors.append("VS Code changelog newest entry must match the extension version")
    return errors


def git(repo: Path, *args: str) -> str:
    return subprocess.check_output(["git", "-C", str(repo), *args], text=True).strip()


def old_lockfile(repo: Path, previous_tag: str) -> dict:
    if not STABLE_TAG.fullmatch(previous_tag):
        raise ValueError("previous tag must be a stable vMAJOR.MINOR.PATCH tag")
    raw = git(repo, "show", f"{previous_tag}:{LOCKFILE_PATH.as_posix()}")
    return json.loads(raw)


def changed_dependency_notes(old: dict, current: dict) -> list[str]:
    old_packages = old.get("packages", {})
    current_packages = current.get("packages", {})
    old_production = old_packages.get("", {}).get("dependencies", {})
    production = current_packages.get("", {}).get("dependencies", {})
    notes: list[str] = []
    for name in sorted(set(old_production) | set(production)):
        package_path = f"node_modules/{name}"
        before = old_packages.get(package_path, {}).get("version")
        after = current_packages.get(package_path, {}).get("version")
        was_production = name in old_production
        is_production = name in production
        if not was_production and is_production and isinstance(after, str):
            notes.append(f"Added the bundled `{name}` dependency at {after}.")
        elif was_production and not is_production and isinstance(before, str):
            notes.append(f"Removed the bundled `{name}` dependency previously at {before}.")
        elif was_production and is_production and isinstance(before, str) and isinstance(after, str) and before != after:
            notes.append(f"Updated the bundled `{name}` dependency from {before} to {after}.")
    return notes


def root_release_block(changelog: str) -> str:
    starts = list(re.finditer(r"^## \[?\d{1,9}\.\d{1,9}\.\d{1,9}", changelog, re.MULTILINE))
    if not starts:
        return ""
    start = starts[0].start()
    next_start = changelog.find("\n## ", start + 1)
    return changelog[start:] if next_start < 0 else changelog[start:next_start]


def clean_root_note(line: str) -> str:
    line = line.removeprefix("* ").lstrip()
    while line.endswith(")"):
        reference_start = line.rfind(" (")
        if reference_start < 0 or "[" not in line[reference_start:]:
            break
        line = line[:reference_start]
    line = line.replace("**", "")
    if line.startswith("vscode: "):
        return "Updated VS Code behavior: " + line.removeprefix("vscode: ")
    return line


def visible_extension_commits(repo: Path, previous_tag: str) -> list[tuple[str, str]]:
    commits: list[tuple[str, str]] = []
    raw = git(repo, "log", "--format=%H%x00%s", f"{previous_tag}..HEAD", "--", EXTENSION_DIR.as_posix())
    for row in filter(None, raw.splitlines()):
        sha, subject = row.split("\x00", 1)
        paths = git(repo, "diff-tree", "--no-commit-id", "--name-only", "-r", sha).splitlines()
        source_paths = [
            path for path in paths
            if (path.startswith(f"{EXTENSION_DIR.as_posix()}/src/") and "/src/test/" not in path)
        ]
        manifest_changed = PACKAGE_PATH.as_posix() in paths and user_visible_manifest_change(repo, sha)
        marketplace_document_changed = marketplace_visible_package_document_changed(repo, sha, paths)
        if source_paths or manifest_changed or marketplace_document_changed:
            commits.append((sha, subject))
    return commits


def marketplace_visible_package_document_changed(repo: Path, sha: str, paths: list[str]) -> bool:
    marketplace_paths = {(EXTENSION_DIR / "README.md").as_posix()}
    icon_path = configured_marketplace_icon_path(repo, sha)
    if icon_path is not None:
        marketplace_paths.add(icon_path)
    return bool(marketplace_paths.intersection(paths))


def configured_marketplace_icon_path(repo: Path, sha: str) -> str | None:
    try:
        package = json.loads(git(repo, "show", f"{sha}:{PACKAGE_PATH.as_posix()}"))
    except (json.JSONDecodeError, subprocess.CalledProcessError):
        return None
    if not isinstance(package, dict):
        return None
    icon = package.get("icon")
    if not isinstance(icon, str) or not icon or "\\" in icon:
        return None
    icon_path = PurePosixPath(icon)
    if icon_path.is_absolute() or not icon_path.parts or ".." in icon_path.parts:
        return None
    return (EXTENSION_DIR / Path(*icon_path.parts)).as_posix()


def user_visible_manifest_change(repo: Path, sha: str) -> bool:
    try:
        previous = json.loads(git(repo, "show", f"{sha}^:{PACKAGE_PATH.as_posix()}"))
        current = json.loads(git(repo, "show", f"{sha}:{PACKAGE_PATH.as_posix()}"))
    except (json.JSONDecodeError, subprocess.CalledProcessError):
        return False
    for manifest in (previous, current):
        for key in ("version", "dependencies", "devDependencies"):
            manifest.pop(key, None)
    return previous != current


def source_notes(repo: Path, previous_tag: str) -> list[str]:
    root_block = root_release_block((repo / ROOT_CHANGELOG_PATH).read_text(encoding="utf-8"))
    notes: list[str] = []
    for sha, subject in visible_extension_commits(repo, previous_tag):
        matched = [clean_root_note(line) for line in root_block.splitlines() if line.startswith("*") and sha[:7] in line]
        notes.extend(matched or [f"Updated VS Code behavior: {subject}."])
    return list(dict.fromkeys(notes))


def render_entry(version: str, entry_date: str, notes: list[str]) -> str:
    body = "\n".join(f"- {note}" for note in notes) or "- No user-visible VS Code extension changes."
    return f"## {version} ({entry_date})\n\n{body}\n\n"


def generate(repo: Path, previous_tag: str, entry_date: str) -> None:
    if not STABLE_TAG.fullmatch(previous_tag):
        raise ValueError("previous tag must be a stable vMAJOR.MINOR.PATCH tag")
    errors = validate_versions(repo)
    if errors:
        raise ValueError("; ".join(errors))
    package = read_json(repo / PACKAGE_PATH)
    version = package["version"]
    notes = source_notes(repo, previous_tag)
    notes.extend(changed_dependency_notes(old_lockfile(repo, previous_tag), read_json(repo / LOCKFILE_PATH)))
    notes = list(dict.fromkeys(notes))
    changelog_path = trusted_extension_changelog(repo)
    changelog = changelog_path.read_text(encoding="utf-8")
    entries = release_entries(changelog)
    without_version = ""
    cursor = 0
    for index, entry in enumerate(entries):
        if entry_version(entry) != version:
            continue
        end = entries[index + 1].start() if index + 1 < len(entries) else len(changelog)
        without_version += changelog[cursor:entry.start()]
        cursor = end
    without_version += changelog[cursor:]
    marker = "# Changelog\n\n"
    if not without_version.startswith(marker):
        raise ValueError("VS Code changelog must start with '# Changelog'")
    write_extension_changelog(
        repo,
        marker + render_entry(version, entry_date, notes) + without_version[len(marker):],
    )


def validate_versions(repo: Path) -> list[str]:
    package = read_json(repo / PACKAGE_PATH)
    lockfile = read_json(repo / LOCKFILE_PATH)
    version = package.get("version")
    if not isinstance(version, str) or not version:
        return ["VS Code package.json must contain a version"]
    if lockfile.get("version") != version or lockfile.get("packages", {}).get("", {}).get("version") != version:
        return ["VS Code package.json and package-lock.json versions must match"]
    return []


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--previous-tag")
    parser.add_argument("--date", default=date.today().isoformat())
    parser.add_argument("--repo", type=Path)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args(argv)
    repo = args.repo.resolve() if args.repo else Path(__file__).resolve().parent.parent
    try:
        if args.check:
            errors = validate(repo)
            if errors:
                raise ValueError("; ".join(errors))
        else:
            if not args.previous_tag:
                raise ValueError("--previous-tag is required when generating release notes")
            generate(repo, args.previous_tag, args.date)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"VS Code release notes: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
