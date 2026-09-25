#!/usr/bin/env python3
"""Count committed Git blobs; never follow symlinks or scan untracked outputs."""
import argparse
import json
import os
import re
from pathlib import Path, PurePosixPath
import subprocess
import tempfile

CATEGORIES = ('production', 'tests', 'fixtures', 'generated_dependencies', 'configuration_docs', 'lockfiles')
SOURCE_SUFFIXES = frozenset('.go .py .js .jsx .ts .tsx .mjs .cjs .sh .bash .rb .c .h .cc .cpp .hpp .java .kt .kts .rs .cs .php .swift .dart .ex .exs .ps1 .vue .svelte .sql .scala .clj .lua .r .m .mm .fs .fsx .vb .psm1 .psd1 .cxx .hxx .hh .mts .cts .rake .gemspec .erl .hrl'.split())
DEPENDENCY_DIRS = frozenset(('vendor', 'node_modules', 'dist', 'build', 'bin', 'out', 'coverage', '__pycache__', '.venv', 'target', 'generated', '.next', '.nuxt'))
FIXTURE_DIRS = frozenset(('testdata', 'fixtures', '__fixtures__'))
TEST_DIRS = frozenset(('test', 'tests', '__tests__', 'testutil', 'testsupport'))
LOCK_NAMES = frozenset(('go.sum', 'package-lock.json', 'npm-shrinkwrap.json', 'yarn.lock', 'pnpm-lock.yaml', 'Gemfile.lock', 'Cargo.lock', 'composer.lock', 'poetry.lock', 'uv.lock', 'packages.lock.json', 'pubspec.lock', 'Package.resolved', 'bun.lock', 'bun.lockb', 'Pipfile.lock', 'mix.lock'))


def is_source(name, data):
    return PurePosixPath(name).suffix.lower() in SOURCE_SUFFIXES or data.startswith(b"#!")


def classify(name, data):
    path = PurePosixPath(name)
    parts = set(path.parts[:-1])
    if path.name in LOCK_NAMES or path.suffix == '.lock':
        return 'lockfiles'
    if (parts & DEPENDENCY_DIRS or name.endswith(('.min.js', '.min.css', '.map'))
            or b'Code generated' in data[:2048] and b'DO NOT EDIT' in data[:2048]):
        return 'generated_dependencies'
    if parts & FIXTURE_DIRS:
        return 'fixtures'
    if not is_source(name, data):
        return 'configuration_docs'
    if parts & TEST_DIRS or path.stem.startswith('test_') or path.stem.endswith(('_test', '.test', '.spec', '_spec')):
        return 'tests'
    return 'production'


def git(*args):
    return subprocess.check_output(['git', *args])


def snapshot(ref):
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_./~^{}@-]*', ref):
        raise ValueError('revision must be a Git reference without option prefixes or whitespace')
    revision = git('rev-parse', '--verify', '--end-of-options', ref + '^{commit}').decode().strip()
    entries = []
    for entry in git('ls-tree', '-rz', '--full-tree', revision).split(b'\0'):
        if entry:
            metadata, name = entry.split(b'\t', 1)
            mode, kind, oid = metadata.split()
            entries.append((name.decode('utf-8', 'surrogateescape'), mode, kind, oid))
    blobs = [entry for entry in entries if entry[2] == b'blob']
    result = subprocess.run(['git', 'cat-file', '--batch'], input=b''.join(entry[3] + b'\n' for entry in blobs), stdout=subprocess.PIPE, check=True).stdout
    files = []
    offset = 0
    for name, mode, _, _ in blobs:
        end = result.index(b'\n', offset)
        size = int(result[offset:end].split()[-1])
        data = result[end + 1:end + 1 + size]
        offset = end + size + 2
        files.append((name, mode, data))
    return revision, files, sum(entry[2] == b'commit' for entry in entries)


def counts(files):
    totals = {category: {'files': 0, 'source_files': 0, 'physical_lines': 0, 'nonblank_lines': 0, 'binary_files': 0, 'symlinks': 0} for category in CATEGORIES}
    for name, mode, data in files:
        bucket = totals[classify(name, data)]
        bucket['files'] += 1
        if mode == b'120000':
            bucket['symlinks'] += 1
        elif b'\0' in data:
            bucket['binary_files'] += 1
        else:
            bucket['source_files'] += is_source(name, data)
            lines = data.split(b'\n')
            if lines[-1] == b'':
                lines.pop()
            bucket['physical_lines'] += len(lines)
            bucket['nonblank_lines'] += sum(bool(line.strip()) for line in lines)
    return totals


def clone_context(finding, contents):
    """Label enclosing declarations heuristically; never prescribe extraction."""
    locations = re.findall(r'(?:^|duplicate of )(.+?):(\d+)-(\d+)', finding)
    contexts = []
    for name, start, _ in locations:
        preceding = contents[name].decode('utf-8', 'replace').splitlines()[:int(start)]
        declarations = [match.group(1) for line in preceding
                        if (match := re.match(r'^func\s+(?:\([^)]*\)\s*)?(\w+)\s*\(', line))]
        function = declarations[-1] if declarations else ''
        kind = 'behavioral_case' if function.startswith(('Test', 'Benchmark', 'Fuzz', 'Example')) else 'helper_candidate'
        contexts.append({'path': name, 'function': function, 'kind': kind if function else 'unclassified'})
    return {'finding': finding, 'contexts': contexts}


def test_clones(files, dupl_version, threshold):
    if not re.fullmatch(r'(?:[0-9a-f]{40}|v[0-9]+\.[0-9]+\.[0-9]+)', dupl_version):
        raise ValueError('dupl version must be a full commit hash or release version')
    threshold = int(threshold)
    if threshold <= 0:
        raise ValueError('clone threshold must be a positive integer')
    with tempfile.TemporaryDirectory(prefix='lopper-test-clones-') as directory:
        paths = []
        for name, mode, data in files:
            if mode != b'120000' and name.endswith('.go') and classify(name, data) == 'tests':
                path = Path(directory, name)
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(data)
                paths.append(str(path))
        if not paths:
            return []
        # Separate tool installation chatter from analyzer diagnostics. dupl can
        # emit parse errors on stderr while returning success.
        tool_directory = Path(directory, 'tool')
        tool_directory.mkdir()
        subprocess.run(['go', 'install', 'github.com/mibk/dupl@' + dupl_version],
                       env={**os.environ, 'GOBIN': str(tool_directory)}, check=True)
        result = subprocess.run([str(tool_directory / 'dupl'), '-t', str(threshold), '-plumbing', '-files'], input='\n'.join(paths) + '\n', text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
        if result.stderr.strip():
            raise RuntimeError('test clone analysis emitted diagnostics: ' + result.stderr.strip())
        contents = {name: data for name, _, data in files}
        return [clone_context(line, contents) for line in sorted(result.stdout.replace(directory + '/', '').splitlines())]


def build_report(base, head, dupl_version, threshold):
    base_sha, base_files, base_modules = snapshot(base)
    head_sha, head_files, head_modules = snapshot(head)
    before, after = counts(base_files), counts(head_files)
    return {
        'schema_version': 1,
        'baseline': base_sha,
        'head': head_sha,
        'methodology': (
            'Committed Git blobs; physical LF-delimited lines (final unterminated line included), '
            'nonblank byte-whitespace lines; not executable lines. Binary (NUL) blobs, symlinks '
            'and submodules have no line count. Unknown extensions remain visible as '
            'configuration/docs. Go-only structural test clones are advisory, not semantic '
            'equivalence; production gate unchanged.'
        ),
        'baseline_counts': before,
        'head_counts': after,
        'delta': {category: {key: after[category][key] - before[category][key]
                             for key in before[category]} for category in CATEGORIES},
        'submodules': {'baseline': base_modules, 'head': head_modules},
        'test_duplication': {
            'blocking': False,
            'tool': 'github.com/mibk/dupl@' + dupl_version,
            'token_threshold': threshold,
            'findings': test_clones(head_files, dupl_version, threshold),
            'triage': (
                'Review helper/harness declarations as reuse candidates separately from '
                'behavioral Test/Benchmark/Fuzz cases; filenames and structural matches '
                'alone cannot establish safe extraction.'
            ),
        },
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', required=True)
    parser.add_argument('--head', default='HEAD')
    parser.add_argument('--dupl-version', required=True)
    parser.add_argument('--threshold', type=int, default=55)
    args = parser.parse_args()
    print(json.dumps(build_report(args.base, args.head, args.dupl_version, args.threshold), indent=2, sort_keys=True))


if __name__ == '__main__':
    main()
