"""Zero production clone pairs; similarities never prove equivalence."""

import contextlib
import hashlib
import itertools
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import tempfile

FIXTURE_DIRS = frozenset(('testdata', 'fixtures', 'test-fixtures', '__fixtures__'))


class PolicyError(ValueError):
    """Malformed or unapproved clone policy."""


def identity(function):
    return f"{function['path']}::{function['name']}@{function['shape']}"


def finding_id(pair):
    return hashlib.sha256(json.dumps(sorted(pair), separators=(",", ":")).encode()).hexdigest()


def git_executable():
    executable = os.environ.get("LOPPER_DUPLICATION_GIT", "git")
    if "LOPPER_DUPLICATION_GIT" in os.environ and not os.path.isabs(executable):
        raise PolicyError("Trusted Git executable must be an absolute path")
    return executable


def git_environment(environment):
    # Every Git command must inspect the explicitly selected checkout and index,
    # not repository selectors or configuration injected by earlier tooling.
    clean = {name: value for name, value in environment.items() if not name.startswith("GIT_")}
    # Global filters can rewrite immutable blobs during checkout; replacement
    # objects can forge the protected commit's ancestry and policy contents.
    clean.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_SYSTEM=os.devnull,
                 GIT_CONFIG_GLOBAL=os.devnull, GIT_NO_REPLACE_OBJECTS="1")
    return clean


@contextlib.contextmanager
def isolated_checkout(repo, revision):
    if not re.fullmatch(r"[0-9a-f]{40,64}", revision):
        raise PolicyError("Checked revision must be a full immutable commit SHA")
    environment = git_environment(os.environ)
    with tempfile.TemporaryDirectory(prefix="lopper-duplication-checkout-") as directory:
        checkout = Path(directory) / "source"
        subprocess.run([git_executable(), "clone", "--no-local", "--no-checkout", "--", str(repo), str(checkout)],
                       env=environment, capture_output=True, check=True)
        # A protected caller may have fetched the immutable PR head only into a
        # remote-tracking ref. Local clone copies branch refs, not that ref, so
        # explicitly fetch the already-validated commit into the isolated repo.
        subprocess.run([git_executable(), "-c", "core.hooksPath=/dev/null", "fetch", "--no-tags", "origin", revision],
                       cwd=checkout, env=environment, capture_output=True, check=True)
        subprocess.run([git_executable(), "-c", "core.hooksPath=/dev/null", "checkout", "--detach", revision],
                       cwd=checkout, env=environment, capture_output=True, check=True)
        yield checkout


def function_index(repo, go, records):
    executable = shutil.which(go)
    if not executable or os.path.basename(executable).lower() not in ('go', 'go.exe'):
        raise PolicyError('Go command must name a Go executable, without embedded arguments')
    # Keep every detector edge for clone-group reconstruction, but only index
    # production members; fixture endpoints cannot hide two production members.
    paths = sorted({path for record in records for path, _, _ in record
                    if path.endswith('.go') and not path.endswith('_test.go')
                    and not (set(PurePosixPath(path).parts[:-1]) & FIXTURE_DIRS)})
    # Build flags and persisted settings can replace indexer source via overlays.
    environment = dict(os.environ, GOFLAGS="", GOENV="off", GO111MODULE="off")
    environment.pop("GOROOT", None)
    environment.pop("GOCACHEPROG", None)
    result = subprocess.run([executable, "run", str(Path(__file__).resolve().with_name("duplication_index.go"))], input=json.dumps(paths), cwd=repo,
                            env=environment, capture_output=True, text=True, check=True)
    return json.loads(result.stdout)


def clone_pairs(records, functions):
    """Promote validated dupl fragment matches to enclosing named functions."""
    indexed = {}
    for function in functions:
        indexed.setdefault(function['path'], []).append(function)
    # dupl emits a cyclic chain, not every pair. Preserve the complete group
    # before excluding test-only endpoints, otherwise test members could hide
    # the relationship between two production functions.
    groups = connected_groups(records)
    pairs = {}
    for group in groups:
        functions_by_id = {}
        for path, start, end in sorted(group):
            for function in indexed.get(path, []):
                if function['start'] <= end and function['end'] >= start:
                    functions_by_id[identity(function)] = function
        for pair in itertools.combinations(sorted(functions_by_id), 2):
            pairs[pair] = [functions_by_id[key] for key in pair]
    return pairs


def validate_policy_schema(policy):
    if (not isinstance(policy, dict) or set(policy) != {'version', 'families', 'exceptions'}
            or type(policy['version']) is not int or policy['version'] != 1):
        raise PolicyError('Unsupported baseline schema')
    if not isinstance(policy['families'], list) or not isinstance(policy['exceptions'], list):
        raise PolicyError('Families and exceptions must be lists')
    if policy['families'] or policy['exceptions']:
        raise PolicyError('Clone families and exceptions are not permitted; remove the underlying production pairs')


def evaluate(pairs, policy):
    validate_policy_schema(policy)
    reports = [{'id': finding_id(pair), 'status': 'violation', 'functions': functions,
                'canonical_helper': None} for pair, functions in sorted(pairs.items())]
    return {'version': 1, 'findings': reports, 'stale_exceptions': [], 'removed_pairs': []}


def connected_groups(edges):
    groups = []
    for edge in sorted(edges):
        merged = set(edge)
        remaining = []
        for group in groups:
            if merged.intersection(group):
                merged.update(group)
            else:
                remaining.append(group)
        groups = remaining + [merged]
    return sorted(groups, key=lambda group: sorted(group))


def validate_reduction(approved, proposed):
    # Neither a protected historical allowance nor a candidate exception grants
    # authority to suppress a finding under the zero-production-pair policy.
    validate_policy_schema(approved)
    validate_policy_schema(proposed)


def validate_initial_baseline(_pairs, proposed):
    # Initial installation accepts only the empty policy. The normal evaluation
    # below still reports and rejects every detected pair, including old ones.
    validate_policy_schema(proposed)


def render(report):
    lines = ['Structural similarity is not proof of semantic equivalence. Review the behavior and adopt or extract a shared helper.']
    for finding in report['findings']:
        locations = [f"{fn['path']}:{fn['start']} ({fn['name']})" for fn in finding['functions']]
        lines.append(f"{finding['status']}: {' <-> '.join(locations)} [{finding['id']}]")
    lines.append(f"Production clone pairs: {len(report['findings'])}; violations: {len(report['findings'])}")
    return '\n'.join(lines)
