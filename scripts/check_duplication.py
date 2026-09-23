#!/usr/bin/env python3
"""Compare added Go lines with checked output from the pinned dupl detector."""

import argparse
import math
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tempfile


class AnalysisError(Exception):
    """The requested comparison did not complete."""


def checked(command, repo, *, environment=None):
    result = subprocess.run(command, cwd=repo, env=environment, capture_output=True, text=True)
    if result.returncode:
        raise AnalysisError(f"{command[0]} failed ({result.returncode}): {result.stderr.strip()}")
    return result


def comparison_base(repo, requested, environment):
    base = requested or environment.get("BASE_SHA")
    if not base:
        target = environment.get("GITHUB_BASE_REF") or environment.get("BASE_REF")
        base = f"refs/remotes/origin/{target}" if target else "origin/main"
    try:
        commit = checked(["git", "rev-parse", "--verify", "--end-of-options", f"{base}^{{commit}}"], repo).stdout.strip()
        merge_base = checked(["git", "merge-base", "--", commit, "HEAD"], repo).stdout.strip()
    except AnalysisError as error:
        raise AnalysisError(
            f"Cannot compare requested base {base!r}. Fetch the target and its history "
            "(git fetch --unshallow when shallow), then rerun with DUPLICATION_BASE=<target-ref-or-sha>. "
            f"No fallback comparison was used. {error}"
        ) from error
    if not re.fullmatch(r"[0-9a-f]{40,64}", merge_base):
        raise AnalysisError("Git returned an invalid merge base")
    return base, merge_base


def supported_path(raw, repo):
    if not raw or any(ord(char) < 32 for char in raw) or ": duplicate of " in raw:
        raise AnalysisError(f"Unsupported detector path: {raw!r}")
    # dupl emits native paths. Also accept relative Windows separators on POSIX
    # when validating portable fixtures; foreign absolute drives remain invalid.
    portable = raw.replace("\\", "/")
    if ":" in portable and not (os.name == "nt" and re.match(r"^[A-Za-z]:/", portable)):
        raise AnalysisError(f"Unsupported detector path: {raw!r}")
    path = (repo / portable).resolve()
    try:
        relative = path.relative_to(repo.resolve())
    except ValueError as error:
        raise AnalysisError(f"Detector path escapes repository: {raw!r}") from error
    if ".." in Path(portable).parts or not path.is_file() or path.suffix != ".go":
        raise AnalysisError(f"Unsupported or missing Go path: {raw!r}")
    return relative.as_posix()


def added_lines(repo, merge_base):
    output = checked(["git", "diff", "--name-only", "-z", "--no-renames", "--diff-filter=ACM", merge_base, "HEAD", "--", "*.go", ":(exclude)**/goleak_test.go"], repo).stdout
    if output and not output.endswith("\0"):
        raise AnalysisError("Truncated changed-file list from Git")
    added = set()
    for raw in output.split("\0")[:-1]:
        path = supported_path(raw, repo)
        diff = checked(["git", "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=0", merge_base, "HEAD", "--", raw], repo).stdout
        for line in diff.splitlines():
            if line.startswith(("Binary files ", "GIT binary patch")):
                raise AnalysisError(f"Cannot analyze binary Go diff: {raw!r}")
            if line.startswith("@@"):
                match = re.fullmatch(r"@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@.*", line)
                if not match:
                    raise AnalysisError(f"Malformed Git hunk for {raw!r}: {line!r}")
                start, count = int(match[1]), int(match[2] or 1)
                added.update((path, number) for number in range(start, start + count))
    return added


def finding_location(raw, start, end, repo, line_counts):
    path = supported_path(raw, repo)
    start, end = int(start), int(end)
    if path not in line_counts:
        line_counts[path] = len((repo / path).read_bytes().splitlines())
    if start < 1 or end < start or end > line_counts[path]:
        raise AnalysisError(f"Invalid detector line range: {raw}:{start}-{end}")
    return path, start, end


def parse_findings(output, repo):
    if output and not output.endswith("\n"):
        raise AnalysisError("Truncated detector output (missing final newline)")
    sources, destinations, duplicated = set(), set(), set()
    line_counts = {}
    for record in output.splitlines():
        match = re.fullmatch(r"(.+):(\d+)-(\d+): duplicate of (.+):(\d+)-(\d+)", record)
        if not match:
            raise AnalysisError(f"Malformed detector record: {record!r}")
        locations = []
        for offset in (1, 4):
            locations.append(finding_location(match[offset], match[offset + 1], match[offset + 2], repo, line_counts))
        if locations[0] == locations[1]:
            raise AnalysisError(f"Detector reported a self-duplicate: {record!r}")
        sources.add(locations[0])
        destinations.add(locations[1])
        path, start, end = locations[0]
        duplicated.update((path, line) for line in range(start, end + 1))
    # Plumbing output links every clone to the next clone in its group. Missing
    # source/destination records indicate partial output even at a line boundary.
    if sources != destinations:
        raise AnalysisError("Incomplete detector clone group")
    return duplicated


def scan(repo, go_command, version, threshold):
    with tempfile.TemporaryDirectory(prefix="lopper-dupl-") as directory:
        environment = dict(os.environ, GOBIN=directory)
        checked([*shlex.split(go_command), "install", f"github.com/mibk/dupl@{version}"], repo, environment=environment)
        executable = Path(directory) / ("dupl.exe" if os.name == "nt" else "dupl")
        result = checked([str(executable), "-t", str(threshold), "-plumbing", "."], repo)
        # dupl logs Go parse failures but can still exit zero. Never treat its
        # partially parsed input as a successful no-match scan.
        if result.stderr.strip():
            raise AnalysisError(f"Detector diagnostics indicate incomplete analysis: {result.stderr.strip()}")
        return parse_findings(result.stdout, repo)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default="")
    parser.add_argument("--go", default="go")
    parser.add_argument("--version", required=True)
    parser.add_argument("--threshold", type=int, default=55)
    parser.add_argument("--max", type=float, default=3)
    args = parser.parse_args(argv)
    try:
        if args.threshold < 1 or not math.isfinite(args.max) or not 0 <= args.max <= 100:
            raise AnalysisError("Threshold must be positive and maximum percentage must be finite within 0..100")
        repo = Path(checked(["git", "rev-parse", "--show-toplevel"], Path.cwd()).stdout.strip()).resolve()
        base, merge_base = comparison_base(repo, args.base, os.environ)
        added = added_lines(repo, merge_base)
        if not added:
            print(f"New-code duplication: no changed Go lines (base: {base}, merge base: {merge_base}); detector not required")
            return 0
        findings = scan(repo, args.go, args.version, args.threshold)
        duplicate_count = len(added & findings)
        percent = 100 * duplicate_count / len(added)
        print(f"New-code duplication: {percent:.2f}% (duplicated added lines: {duplicate_count} / {len(added)}, max: {args.max:g}%, threshold: {args.threshold} tokens, base: {base}, merge base: {merge_base})")
        if percent > args.max:
            print("Duplication gate failed: new-code duplication exceeds the configured maximum", file=sys.stderr)
            return 1
        return 0
    except (AnalysisError, OSError, UnicodeError) as error:
        print(f"Duplication analysis failed: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
