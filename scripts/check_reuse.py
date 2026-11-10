#!/usr/bin/env python3
"""Run protected reuse detectors against one immutable prospective merge tree."""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile


BASELINE = ".github/duplication-baseline.json"
REQUIRED_FILES = (
    BASELINE, "scripts/check_duplication.py", "scripts/duplication_policy.py",
    "scripts/duplication_index.go", "tools/reusecheck/main.go",
)
TRUSTED_PATHS = ("Makefile", "go.mod", "go.sum", "go.work", "go.work.sum",
                 ".gitattributes", ":(glob)**/.gitattributes", "scripts",
                 "internal/reusecheck", "tools/reusecheck", BASELINE)
POLICY_PATHS = (
    "Makefile", "go.mod", "go.sum", "go.work", "go.work.sum", "CODEOWNERS",
    ".github/", "scripts/check_reuse", "scripts/check_duplication",
    "scripts/duplication_", "internal/reusecheck/", "tools/reusecheck/",
)


class AnalysisError(ValueError):
    """Analysis did not establish a passing reuse result."""


def immutable_sha(value):
    if not re.fullmatch(r"[0-9a-f]{40}", value):
        raise AnalysisError("Base and revision must be full immutable commit SHAs")
    return value


def trusted_environment():
    environment = {key: value for key, value in os.environ.items()
                   if not key.startswith(("GIT_", "GO", "LOPPER_DUPLICATION_"))}
    environment.update(
        GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_SYSTEM=os.devnull,
        GIT_CONFIG_GLOBAL=os.devnull, GIT_NO_REPLACE_OBJECTS="1",
        GOENV="off", GOFLAGS="", GOWORK="off", GOTOOLCHAIN="local",
        GOAUTH="off", GOPROXY="https://proxy.golang.org", GOSUMDB="sum.golang.org",
    )
    # Analysis needs no API credentials. Callers must also use a read-only job;
    # deleting variables cannot remove a runner's ambient GitHub permissions.
    for key in ("GH_TOKEN", "GITHUB_TOKEN", "ACTIONS_RUNTIME_TOKEN"):
        environment.pop(key, None)
    return environment


def checked(command, root, environment):
    return subprocess.run(command, cwd=root, env=environment, check=True,
                          capture_output=True, text=True).stdout


def validate_base(root, base, git, environment):
    if checked([git, "rev-parse", "HEAD"], root, environment).strip() != base:
        raise AnalysisError("Execute the runner from the selected protected base checkout")
    for name in REQUIRED_FILES:
        path = root / name
        if path.is_symlink() or not path.is_file():
            raise AnalysisError(f"Missing protected reuse dependency: {name}")
    checked([git, "diff", "--exit-code", "HEAD", "--", *TRUSTED_PATHS], root, environment)
    indexed = checked([git, "ls-files", "-v", "-z", "--", *TRUSTED_PATHS], root, environment)
    if any(not entry.startswith("H ") for entry in indexed.split("\0") if entry):
        raise AnalysisError("Protected tool paths must not hide changes through index flags")
    # Go compiles untracked (even ignored) files in a package. A clean tracked
    # diff alone does not establish that the build uses only protected source.
    untracked = checked([git, "ls-files", "--others", "-z", "--", *TRUSTED_PATHS], root, environment)
    if untracked:
        raise AnalysisError("Untracked files in protected tool or policy paths")
    reject_baseline_allowances(root / BASELINE)


def reject_baseline_allowances(path):
    if path.is_symlink():
        raise AnalysisError("Reuse policy must not be a symlink")
    policy = json.loads(path.read_text())
    if not isinstance(policy, dict) or any(policy.get(name) != [] for name in ("families", "exceptions")):
        raise AnalysisError("Reuse baseline families and exceptions must be empty lists under the no-suppression policy")


def setting(root, name):
    matches = re.findall(r"^" + re.escape(name) + r"\s*\?=\s*([^\s#]+)\s*$",
                         (root / "Makefile").read_text(), re.MULTILINE)
    if len(matches) != 1:
        raise AnalysisError(f"Missing or ambiguous protected setting: {name}")
    return matches[0]


def changed_paths(root, base, revision, git, environment):
    output = checked([git, "diff", "--no-renames", "--name-only", "-z", base, revision, "--"],
                     root, environment)
    if output and not output.endswith("\0"):
        raise AnalysisError("Truncated changed-path response")
    return output.split("\0")[:-1]


def relevant(paths):
    return any(path.endswith(".go") or path.startswith(POLICY_PATHS)
               or Path(path).name == ".gitattributes" for path in paths)


def has_symlinks(root, revisions, git, environment):
    # A non-Go path can be the referent of a Go symlink. Until the detectors
    # provide a dependency inventory, conservatively scan trees with any links.
    return any(record.startswith("120000 ") for revision in revisions
               for record in checked([git, "ls-tree", "-r", "-z", revision],
                                     root, environment).split("\0"))


def run_detectors(root, base, revision, go, git, environment, policy_source=None):
    # Imported only after validating the protected checkout. The candidate's
    # Python modules, Go module/workspace, workflows and Makefile never execute.
    import duplication_policy

    with tempfile.TemporaryDirectory(prefix="lopper-reuse-tool-") as directory:
        binary = Path(directory) / "reusecheck"
        build_environment = dict(environment, GOMODCACHE=str(Path(directory) / "modules"))
        checked([go, "build", "-trimpath", "-o", str(binary), "./tools/reusecheck"],
                root, build_environment)
        with duplication_policy.isolated_checkout(root, revision) as source:
            reject_baseline_allowances(source / BASELINE)
            command = [sys.executable, "-E", "-S", "-B", str(root / "scripts/check_duplication.py"),
                       "--base", base, "--go", setting(root, "GO"),
                       "--version", setting(root, "DUPL_VERSION"),
                       "--threshold", setting(root, "DUPLICATION_TOKEN_THRESHOLD"),
                       "--max", setting(root, "DUPLICATION_MAX"), "--baseline", BASELINE]
            if policy_source is not None:
                command.extend(["--policy-source", policy_source])
            detector_environment = dict(environment, LOPPER_DUPLICATION_GO=go,
                                        LOPPER_DUPLICATION_GIT=git)
            duplication = subprocess.run(command, cwd=source, env=detector_environment, check=False)
            helper = subprocess.run([str(binary), "-root", str(source), "-legacy-advisory=false"],
                                    cwd=root, env=environment, check=False)
            return combine_results(duplication.returncode, helper.returncode)


def combine_results(duplication, helper):
    results = (duplication, helper)
    if any(result not in (0, 1) for result in results):
        return 2
    return int(any(results))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--policy-source", help="Immutable protected executable and policy commit")
    args = parser.parse_args(argv)
    try:
        base, revision = immutable_sha(args.base), immutable_sha(args.revision)
        root = Path(__file__).resolve().parents[1]
        environment = trusted_environment()
        go, git = shutil.which("go"), shutil.which("git")
        if not go or not git:
            raise AnalysisError("Trusted Go and Git executables are required")
        environment["LOPPER_DUPLICATION_GIT"] = git
        # The protected isolation helper also launches Git. Give it exactly the
        # same credential-free environment as the detector subprocesses.
        os.environ.clear()
        os.environ.update(environment)
        protected = immutable_sha(args.policy_source) if args.policy_source else base
        validate_base(root, protected, git, environment)
        if args.policy_source:
            import duplication_policy
            with duplication_policy.isolated_checkout(root, base) as comparison:
                reject_baseline_allowances(comparison / BASELINE)
        checked([git, "cat-file", "-e", f"{revision}^{{commit}}"], root, environment)
        checked([git, "merge-base", "--is-ancestor", base, revision], root, environment)
        paths = changed_paths(root, base, revision, git, environment)
        print(f"Reuse analysis: base={base} revision={revision}", flush=True)
        if not relevant(paths) and (not paths or not has_symlinks(root, (base, revision), git, environment)):
            print("Reuse check passed: no relevant source or policy changes")
            return 0
        result = run_detectors(root, base, revision, go, git, environment, args.policy_source)
        print(f"Reuse check {'passed' if result == 0 else 'failed'}: exit={result}", flush=True)
        return result
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"Reuse analysis failed: {error}", file=sys.stderr)
        if isinstance(error, subprocess.CalledProcessError) and error.stderr:
            print(error.stderr, file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
