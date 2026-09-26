"""Reviewed structural clone families; similarities never prove equivalence."""

import datetime
import hashlib
import itertools
import json
import os
import shutil
import subprocess


class PolicyError(ValueError):
    """Malformed or unapproved clone policy."""


def identity(function):
    return f"{function['path']}::{function['name']}@{function['shape']}"


def finding_id(pair):
    return hashlib.sha256(json.dumps(sorted(pair), separators=(",", ":")).encode()).hexdigest()


def function_index(repo, go):
    executable = shutil.which(go)
    if not executable or os.path.basename(executable).lower() not in ('go', 'go.exe'):
        raise PolicyError('Go command must name a Go executable, without embedded arguments')
    result = subprocess.run(["git", "ls-files", "-z", "--", "*.go"], cwd=repo, capture_output=True, check=True)
    paths = [path.decode() for path in result.stdout.split(b"\0") if path and not path.endswith(b"_test.go")]
    result = subprocess.run([executable, "run", "./scripts/duplication_index.go"], input=json.dumps(paths), cwd=repo,
                            capture_output=True, text=True, check=True)
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


def baseline_pairs(policy):
    validate_policy_schema(policy)
    pairs = set()
    members = set()
    for family in policy['families']:
        current = validate_family(family, members)
        members.update(current)
        pairs.update(tuple(sorted(pair)) for pair in itertools.combinations(current, 2))
    return pairs


def validate_policy_schema(policy):
    if not isinstance(policy, dict) or set(policy) != {'version', 'families', 'exceptions'} or policy['version'] != 1:
        raise PolicyError('Unsupported baseline schema')
    if not isinstance(policy['families'], list) or not isinstance(policy['exceptions'], list):
        raise PolicyError('Families and exceptions must be lists')


def validate_family(family, members):
    required = {'members', 'canonical_helper'}
    if not isinstance(family, dict) or set(family) != required or not isinstance(family['members'], list) or len(family['members']) < 2:
        raise PolicyError('A family needs members and canonical_helper')
    current = family['members']
    if not all(isinstance(member, str) and '::' in member and '@' in member for member in current):
        raise PolicyError('Invalid occurrence identity')
    if len(set(current)) != len(current) or members.intersection(current):
        raise PolicyError('Duplicate baseline occurrence')
    if family['canonical_helper'] is not None and family['canonical_helper'] not in current:
        raise PolicyError('Canonical helper must identify a family member')
    return current


def evaluate(pairs, policy, today=None):
    allowed = baseline_pairs(policy)
    today = today or datetime.date.today()
    exceptions = exception_index(policy['exceptions'], today)
    reports, used = finding_reports(pairs, policy['families'], allowed, exceptions)
    return {
        'version': 1,
        'findings': reports,
        'stale_exceptions': sorted(set(exceptions) - used),
        'removed_pairs': sorted(finding_id(pair) for pair in allowed - set(pairs)),
    }


def exception_index(exceptions, today):
    indexed = {}
    for exception in exceptions:
        key = validate_exception(exception, indexed)
        if datetime.date.fromisoformat(exception['expires']) < today:
            raise PolicyError(f'Expired exception: {key}')
        indexed[key] = exception
    return indexed


def validate_exception(exception, indexed):
    required = {'finding', 'rationale', 'owner', 'expires', 'review'}
    if not isinstance(exception, dict) or set(exception) != required:
        raise PolicyError('Exceptions require exact finding, rationale, owner, expiry and review')
    if not all(isinstance(value, str) and value.strip() for value in exception.values()):
        raise PolicyError('Empty exception metadata')
    key = exception['finding']
    if len(key) != 64 or any(char not in '0123456789abcdef' for char in key) or key in indexed:
        raise PolicyError('Invalid or duplicate exact finding exception')
    return key


def finding_reports(pairs, families, allowed, exceptions):
    reports = []
    used = set()
    for pair, functions in sorted(pairs.items()):
        key = finding_id(pair)
        status = finding_status(pair, key, allowed, exceptions, used)
        helper = canonical_helper(pair, families)
        reports.append({'id': key, 'status': status, 'functions': functions, 'canonical_helper': helper})
    return reports, used


def finding_status(pair, key, allowed, exceptions, used):
    if key in exceptions:
        used.add(key)
        return 'exception'
    return 'historical' if pair in allowed else 'violation'


def canonical_helper(pair, families):
    for family in families:
        if set(pair).intersection(family['members']) and family['canonical_helper']:
            return family['canonical_helper']
    return None


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


def propose_baseline(pairs):
    return {'version': 1, 'families': [{'members': sorted(group), 'canonical_helper': None}
                                     for group in connected_groups(pairs)], 'exceptions': []}


def validate_reduction(approved, proposed):
    approved_pairs = baseline_pairs(approved)
    proposed_pairs = baseline_pairs(proposed)
    if not proposed_pairs.issubset(approved_pairs):
        raise PolicyError('Baseline expansion requires the separately reviewed policy workflow (#1612)')
    if any(exception not in approved['exceptions'] for exception in proposed['exceptions']):
        raise PolicyError('Exception expansion requires the separately reviewed policy workflow (#1612)')
    approved_helpers = {family['canonical_helper'] for family in approved['families']}
    if any(family['canonical_helper'] and family['canonical_helper'] not in approved_helpers for family in proposed['families']):
        raise PolicyError('Canonical helper changes require policy review')


def validate_initial_baseline(pairs, proposed):
    approved = baseline_pairs(proposed)
    if proposed['exceptions']:
        raise PolicyError('An initial baseline cannot introduce exceptions before policy review')
    if approved != set(pairs):
        raise PolicyError('Initial baseline must exactly record the reviewed full-scan findings')


def render(report):
    lines = ['Structural similarity is not proof of semantic equivalence. Adopt/extract a helper or request a reviewed exact exception.']
    for finding in report['findings']:
        if finding['status'] == 'historical':
            continue
        locations = [f"{fn['path']}:{fn['start']} ({fn['name']})" for fn in finding['functions']]
        lines.append(f"{finding['status']}: {' <-> '.join(locations)} [{finding['id']}]")
        if finding['canonical_helper']:
            lines.append(f"  canonical helper: {finding['canonical_helper']}")
    lines.extend(f'Stale exception: {key}' for key in report['stale_exceptions'])
    violations = sum(finding['status'] == 'violation' for finding in report['findings'])
    lines.append(f"Production clone pairs: {len(report['findings'])}; violations: {violations}; removed historical pairs: {len(report['removed_pairs'])}")
    return '\n'.join(lines)
