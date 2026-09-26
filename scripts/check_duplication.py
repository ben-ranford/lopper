#!/usr/bin/env python3
"""Compare added Go lines with checked output from the pinned dupl detector."""

import argparse
import math
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

import duplication_policy as policy


class AnalysisError(Exception):
    """The requested comparison did not complete."""


CANONICAL_BASELINE = ".github/duplication-baseline.json"


def checked(command, repo, *, environment=None):
    result = subprocess.run(command, cwd=repo, env=environment, capture_output=True, text=True)
    if result.returncode:
        raise AnalysisError(f"{command[0]} failed ({result.returncode}): {result.stderr.strip()}")
    return result


def comparison_base(repo, requested, environment):
    target = None
    base = requested or environment.get("BASE_SHA")
    if not base:
        target = environment.get("GITHUB_BASE_REF") or environment.get("BASE_REF")
        base = target or "origin/main"
    if not re.fullmatch(r"\w[\w./-]*", base, flags=re.ASCII):
        raise AnalysisError(
            f"Unsupported comparison base {base!r}. Use a named ref containing letters, digits, "
            "underscores, dots, slashes, or hyphens (not a leading hyphen), or a commit SHA. "
            "No fallback comparison was used."
        )
    if target:
        base = f"refs/remotes/origin/{base}"
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
    output = checked(["git", "diff", "--find-renames", "--name-status", "-z", "--diff-filter=ACMRT", merge_base, "HEAD", "--", "*.go", ":(exclude)**/goleak_test.go"], repo).stdout
    if output and not output.endswith("\0"):
        raise AnalysisError("Truncated changed-file list from Git")
    added = set()
    fields = output.split("\0")[:-1]
    index = 0
    while index < len(fields):
        status = fields[index]
        index += 1
        if status.startswith(("R", "C")):
            if index + 1 >= len(fields):
                raise AnalysisError("Truncated renamed-file record from Git")
            previous, raw = fields[index : index + 2]
            index += 2
            pathspecs = [f":(literal){previous}", f":(literal){raw}"]
        else:
            raw = fields[index]
            index += 1
            pathspecs = [f":(literal){raw}"]
        path = supported_path(raw, repo)
        diff = checked(["git", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--find-renames", "--unified=0", merge_base, "HEAD", "--", *pathspecs], repo).stdout
        added.update(changed_hunk_lines(diff, path))
    return added


def changed_hunk_lines(diff, path):
    added = set()
    for line in diff.splitlines():
        if line.startswith(("Binary files ", "GIT binary patch")):
            raise AnalysisError(f"Cannot analyze binary Go diff: {path!r}")
        if line.startswith("@@"):
            match = re.fullmatch(r"@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@.*", line)
            if not match:
                raise AnalysisError(f"Malformed Git hunk for {path!r}: {line!r}")
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


def parse_location(location, repo, line_counts):
    raw, separator, line_range = location.rpartition(":")
    start, dash, end = line_range.partition("-")
    if not separator or not dash or not start.isascii() or not end.isascii() or not start.isdecimal() or not end.isdecimal():
        raise AnalysisError(f"Malformed detector location: {location!r}")
    return finding_location(raw, start, end, repo, line_counts)


def parse_findings(output, repo, *, records=None):
    if output and not output.endswith("\n"):
        raise AnalysisError("Truncated detector output (missing final newline)")
    sources, destinations, duplicated = set(), set(), set()
    line_counts = {}
    for record in output.splitlines():
        endpoints = record.split(": duplicate of ")
        if len(endpoints) != 2:
            raise AnalysisError(f"Malformed detector record: {record!r}")
        locations = [parse_location(location, repo, line_counts) for location in endpoints]
        if locations[0] == locations[1]:
            raise AnalysisError(f"Detector reported a self-duplicate: {record!r}")
        if records is not None:
            records.append(tuple(locations))
        sources.add(locations[0])
        destinations.add(locations[1])
        path, start, end = locations[0]
        duplicated.update((path, line) for line in range(start, end + 1))
    # Plumbing output links every clone to the next clone in its group. Missing
    # source/destination records indicate partial output even at a line boundary.
    if sources != destinations:
        raise AnalysisError("Incomplete detector clone group")
    return duplicated


def scan(repo, go_command, version, threshold, *, records=None):
    if not re.fullmatch(r"[0-9a-f]{40}", version):
        raise AnalysisError("Detector version must be pinned to a full lowercase commit SHA")
    go_executable = shutil.which(go_command)
    if not go_executable or Path(go_executable).name not in ("go", "go.exe"):
        raise AnalysisError("Go command must name a Go executable, without embedded arguments")
    with tempfile.TemporaryDirectory(prefix="lopper-dupl-") as directory:
        environment = dict(os.environ, GOBIN=directory)
        checked([go_executable, "install", "--", f"github.com/mibk/dupl@{version}"], repo, environment=environment)
        executable = Path(directory) / ("dupl.exe" if os.name == "nt" else "dupl")
        result = checked([str(executable), "-t", str(threshold), "-plumbing", "."], repo)
        # dupl logs Go parse failures but can still exit zero. Never treat its
        # partially parsed input as a successful no-match scan.
        if result.stderr.strip():
            raise AnalysisError(f"Detector diagnostics indicate incomplete analysis: {result.stderr.strip()}")
        return parse_findings(result.stdout, repo, records=records)


def occurrence_gate(repo, merge_base, args):
    if args.baseline is not None:
        validate_occurrence_settings(repo, merge_base, args)
    records = []
    scan(repo, args.go, args.version, args.threshold, records=records)
    functions = policy.function_index(repo, args.go)
    pairs = policy.clone_pairs(records, functions)
    if args.propose_baseline:
        output_path = repository_path(repo, args.propose_baseline, "Baseline proposal")
        output_path.write_text(json.dumps(policy.propose_baseline(pairs), indent=2) + "\n")
        print("Baseline proposal written; it does not authorize new clones")
    if args.baseline is None:
        return 0
    baseline_path = CANONICAL_BASELINE
    baseline_file = repo / baseline_path
    if baseline_file.is_symlink():
        raise AnalysisError("Baseline policy file must not be a symlink")
    proposed = json.loads(baseline_file.read_text())
    # The protected target, never the contributor's new policy, grants exceptions.
    target_entry = checked(["git", "ls-tree", "-z", merge_base, "--", baseline_path], repo).stdout
    if target_entry:
        approved = json.loads(checked(["git", "show", f"{merge_base}:{baseline_path}"], repo).stdout)
        policy.validate_reduction(approved, proposed)
    else:
        policy.validate_initial_baseline(pairs, proposed)
        print("Initial baseline seed exactly matches the reviewed full-scan findings")
    report = policy.evaluate(pairs, proposed)
    if args.report:
        report_path = repository_path(repo, args.report, "Report")
        report_path.write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
    print(policy.render(report))
    return int(bool(report['stale_exceptions']) or any(finding['status'] == 'violation' for finding in report['findings']))


def protected_make_variable(repo, reference, name):
    makefile = checked(["git", "show", f"{reference}:Makefile"], repo).stdout
    pattern = re.compile(rf"^[ \t]*{re.escape(name)}[ \t]*(?:\?=|:=|=)[ \t]*([^\s#]+)[ \t]*(?:#.*)?$", re.MULTILINE)
    values = pattern.findall(makefile)
    if len(values) != 1:
        raise AnalysisError(f"Protected Makefile must define {name} exactly once")
    return values[0]


def validate_occurrence_settings(repo, merge_base, args):
    if args.baseline != CANONICAL_BASELINE:
        raise AnalysisError(f"Occurrence enforcement must use the protected baseline path {CANONICAL_BASELINE!r}")
    protected_version = protected_make_variable(repo, merge_base, "DUPL_VERSION")
    protected_threshold = protected_make_variable(repo, merge_base, "DUPLICATION_TOKEN_THRESHOLD")
    try:
        protected_threshold = int(protected_threshold)
    except ValueError as error:
        raise AnalysisError("Protected duplication token threshold must be an integer") from error
    if args.version != protected_version:
        raise AnalysisError("Detector version must match the protected target Makefile")
    if args.threshold != protected_threshold:
        raise AnalysisError("Detector threshold must match the protected target Makefile")


def repository_path(repo, value, label):
    candidate = Path(value)
    root = repo.resolve()
    if candidate.is_absolute() or ".." in candidate.parts:
        raise AnalysisError(f"{label} path must stay within the repository")
    try:
        resolved = (root / candidate).resolve()
    except (OSError, RuntimeError) as error:
        raise AnalysisError(f"{label} path could not be safely resolved") from error
    try:
        resolved.relative_to(root)
    except ValueError as error:
        raise AnalysisError(f"{label} path must stay within the repository") from error
    return resolved


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default="")
    parser.add_argument("--go", default="go")
    parser.add_argument("--version", required=True)
    parser.add_argument("--threshold", type=int, default=55)
    parser.add_argument("--max", type=float, default=3)
    parser.add_argument("--baseline", help="Reviewed baseline path; enables occurrence enforcement")
    parser.add_argument("--report", help="Write deterministic occurrence report JSON")
    parser.add_argument("--propose-baseline", help="Write a baseline proposal for separate review, never authorize it")
    args = parser.parse_args(argv)
    try:
        if args.threshold < 1 or not math.isfinite(args.max) or not 0 <= args.max <= 100:
            raise AnalysisError("Threshold must be positive and maximum percentage must be finite within 0..100")
        repo = Path(checked(["git", "rev-parse", "--show-toplevel"], Path.cwd()).stdout.strip()).resolve()
        base, merge_base = comparison_base(repo, args.base, os.environ)
        if args.baseline is not None or args.propose_baseline:
            return occurrence_gate(repo, merge_base, args)
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
    except (AnalysisError, OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"Duplication analysis failed: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
