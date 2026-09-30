'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { verifySuppressions, testables } = require('./queue_me_suppressions.js');

const HEAD = 'a'.repeat(40);
const POLICY = 'b'.repeat(40);
const TREE = 'c'.repeat(40);

function fixture(contents, options = {}) {
  const files = Object.entries(contents).map(([path, content], index) => ({
    path, body: Buffer.from(content), sha: (index + 1).toString(16).padStart(40, '0'),
  }));
  const directories = new Set();
  for (const file of files) {
    const parts = file.path.split('/');
    while (parts.length > 1) {
      parts.pop();
      directories.add(parts.join('/'));
    }
  }
  const entries = [
    ...[...directories].map((path) => ({ path, mode: '040000', type: 'tree', sha: TREE })),
    ...files.map(({ path, body, sha }) => ({ path, mode: '100644', type: 'blob', sha, size: body.length })),
  ];
  const calls = [];
  const github = {
    graphql: async (query, args) => {
      calls.push(['graphql', { query, ...args }]);
      if (options.graphqlError) throw options.graphqlError;
      const repository = { nameWithOwner: 'octo/lopper' };
      for (const match of query.matchAll(/(b\d+): object\(oid: "([a-f\d]{40})"\)/g)) {
        const file = files.find((item) => item.sha === match[2]);
        const binary = file.body.includes(0);
        const blob = { __typename: 'Blob', oid: file.sha, byteSize: file.body.length, isTruncated: false, isBinary: binary, text: binary ? null : file.body.toString('utf8') };
        repository[match[1]] = options.blob ? options.blob(blob, file) : blob;
      }
      return options.response ? options.response(repository) : { repository };
    },
    rest: { git: {
      getCommit: async (args) => { calls.push(['commit', args]); return { data: options.commit ?? { sha: HEAD, tree: { sha: TREE } } }; },
      getTree: async (args) => { calls.push(['tree', args]); return { data: options.tree ?? { sha: TREE, truncated: false, tree: entries } }; },
      getBlob: async (args) => {
        calls.push(['binary', args]);
        const file = files.find((item) => item.sha === args.file_sha);
        const data = { sha: file.sha, size: file.body.length, encoding: 'base64', content: file.body.toString('base64') };
        return { data: options.binary ? options.binary(data) : data };
      },
    } },
  };
  return { args: { github, owner: 'octo', repo: 'lopper', headSHA: HEAD, trustedPolicySHA: POLICY }, entries, files, calls };
}

function scan(path, content) {
  return testables.scanFile({ path, body: Buffer.from(content), mode: '100644' });
}

test('audits every immutable blob using one bounded batch and two tree API calls', async () => {
  const harness = fixture({ 'main.go': 'package main\n', 'renamed/unchanged.go': 'package lib\n' });
  const evidence = await verifySuppressions(harness.args);
  assert.deepEqual(evidence, { headSHA: HEAD, trustedPolicySHA: POLICY, treeSHA: TREE, count: 0, filesScanned: 2, findings: [], apiRequests: 3 });
  assert.equal(harness.calls.length, 3);
  assert.equal(harness.calls[0][1].commit_sha, HEAD);
  assert.equal(harness.calls[1][1].tree_sha, TREE);
  assert.equal(harness.calls[1][1].recursive, '1');
  assert.match(harness.calls[2][1].query, /object\(oid: "[a-f\d]{40}"\)/);
  assert.equal(harness.calls[2][1].owner, 'octo');
  assert.equal(harness.calls[2][1].repo, 'lopper');
});

test('unchanged/renamed sources and active executable test fixtures are audited', async () => {
  for (const path of ['unchanged.go', 'renamed/file.go', 'testdata/fixture.go']) {
    const harness = fixture({ [path]: 'package main\n//nolint:all\n' });
    await assert.rejects(verifySuppressions(harness.args), new RegExp(`${path}:2`));
  }
});

test('last immutable blob is scanned even after many complete batches', async () => {
  const contents = Object.fromEntries(Array.from({ length: 350 }, (_, index) => [`file${index}.go`, 'package main\n']));
  contents['last.go'] = 'package main\n//nolint:all\n';
  await assert.rejects(verifySuppressions(fixture(contents).args), /last.go:2/);
});

test('metadata never waives a suppression and malformed directives hold', () => {
  for (const line of [
    '//nolint:all // rationale=test; owner=@owner; remove-when=later',
    '//nolint-unknown',
    '# shellcheck disable',
  ]) {
    assert.equal(scan(line.startsWith('#') ? 'test.sh' : 'test.go', line).length, 1);
  }
});

test('ShellCheck and further lint/coverage directives are active', () => {
  const cases = [
    ['a.sh', '# shellcheck disable=SC2016'],
    ['a.sh', '# shellcheck source=/dev/null'],
    ['.githooks/pre-commit', '# shellcheck disable=SC2016'],
    ['a.js', '/* istanbul ignore next */\nrun();'],
    ['a.js', '/* c8 ignore next */\nrun();'],
    ['a.ts', '// @ts-nocheck'],
    ['a.ts', '// biome-ignore lint: reason'],
    ['a.py', 'value() # type: ignore'],
    ['a.py', '# pylint: disable=all'],
    ['a.rs', '#[allow(dead_code)]'],
    ['a.java', '@SuppressWarnings("all")'],
    ['a.kt', '@Suppress("UNUSED_VARIABLE")'],
    ['a.kt', '@file:Suppress("UNUSED_VARIABLE")'],
    ['a.cs', '#pragma warning disable CS0168'],
    ['a.c', '#pragma GCC diagnostic ignored "-Wformat"'],
    ['a.cs', '[SuppressMessage("Performance", "CA1801")]'],
    ['a.go', '//lint:ignore SA1000 reason'],
    ['a.go', '//revive:disable'],
    ['a.go', '// explanatory prose NOSONAR'],
    ['a.js', '/*\n * eslint-disable\n */'],
  ];
  for (const [path, content] of cases) assert.ok(scan(path, content).length, `${path}: ${content}`);
});

test('quoted Go raw fixtures and shell strings are inert but following markers remain active', () => {
  assert.equal(scan('fixture_test.go', 'package test\nconst fixture = `\n//nolint:all\n`\n').length, 0);
  assert.equal(scan('fixture.sh', 'value=\'\n# shellcheck disable=SC2016\n\'\n').length, 0);
  assert.equal(scan('fixture_test.go', 'const fixture = `//nolint:all`\n//nolint:all\n').length, 1);
  assert.equal(scan('fixture.js', 'const sample = "// eslint-disable";\n').length, 0);
});

test('comment apostrophes and JavaScript regex quotes cannot hide later directives', () => {
  for (const content of [
    "/* don't mask the next line */\n// eslint-disable-next-line no-undef\nmissing();",
    'const matcher = /"/;\n// eslint-disable-next-line no-undef\nmissing();',
    'const matcher = /"/; // eslint-disable-line no-undef',
  ]) assert.ok(scan('script.js', content).length, content);
});

test('multiline block directives do not require a decorative star', () => {
  for (const comment of [
    '/*\neslint-disable no-undef\n*/',
    '/*\n eslint-disable no-undef\n*/',
    '/*\n istanbul ignore next\n*/',
    '/*\r\n\t c8 ignore next\r\n*/',
  ]) assert.ok(scan('script.js', `${comment}\nmissing();`).length, comment);
  assert.equal(scan('fixture.js', 'const fixture = `/*\neslint-disable no-undef\n*/`;').length, 0);
});

test('marker-bearing executable interpolation is an ambiguity hold', () => {
  assert.throws(() => scan('script.js', 'const value = `${(() => {\n// eslint-disable-next-line no-undef\nreturn missing();\n})()}`;'), /interpolation/);
  assert.equal(scan('fixture.js', 'const value = `${trackedLine("nolint:all")}`;').length, 0);
  assert.throws(() => scan('script.sh', "sh -c '\n# shellcheck disable=SC2016\necho test\n'"), /interpreter/);
});

test('literal cat heredocs are data; shell interpreter heredocs and active following lines hold', () => {
  assert.equal(scan('fixture.sh', "cat <<'DATA'\n# shellcheck disable=SC2016\nDATA\n").length, 0);
  assert.equal(scan('fixture.sh', "bash <<'DATA'\n# shellcheck disable=SC2016\nDATA\n").length, 1);
  assert.equal(scan('fixture.sh', "cat <<'DATA'\n# shellcheck disable=SC2016\nDATA\n# shellcheck disable=SC2016").length, 1);
  assert.throws(() => scan('fixture.sh', "cat <<'DATA'\n# shellcheck disable=SC2016\n"), /unterminated/);
});

test('configuration exclusions and unsupported marker syntax remain actionable holds', () => {
  for (const [path, content] of [
    ['.sonarcloud.properties', 'sonar.exclusions=testdata/js/cjs/index.cjs,testdata/js/esm/index.js'],
    ['sonar-project.properties', 'sonar.issue.ignore.multicriteria=e1'],
    ['eslint.config.js', 'module.exports = { ignores: ["src/**"] };'],
    ['.eslintrc.json', '{"rules": {"no-eval": "off"}}'],
    ['.eslintrc.yml', 'rules:\n  no-eval: off\n'],
    ['eslint.config.js', 'export default [{ rules: { "no-eval": ["off"] } }];'],
    ['.eslintrc.json', '{"rules": {"no-eval": [0]}}'],
    ['.golangci.yml', 'linters:\n  exclusions:\n    paths: [src]'],
    ['.golangci.yml', 'linters:\n  disable:\n    - gosec\n'],
    ['pyproject.toml', '[tool.ruff.lint]\nignore = ["E501"]\n'],
    ['.shellcheckrc', 'disable=SC2016'],
    ['Makefile', 'GOSEC_EXCLUDE_RULES ?= internal/gitexec/gitexec\\.go:G204;tools/regressionproof/main\\.go:G204'],
    ['code.unknown', '# nolint:all'],
  ]) assert.ok(scan(path, content).length, path);
  assert.equal(scan('config_test.go', 'const fixture = `sonar.exclusions=src/**`').length, 0);
  assert.equal(scan('package.json', '{"minimum": 0}').length, 0);
});

test('candidate policy files are passive data and cannot bless their own scan', async () => {
  const harness = fixture({
    'scripts/queue_me_suppressions.js': "throw new Error('candidate code executed'); module.exports = { verifySuppressions: () => ({ count: 0 }) };",
    'unchanged.go': '//nolint:all',
  });
  await assert.rejects(verifySuppressions(harness.args), /unchanged.go:1/);
});

test('missing, truncated, malformed, oversized and unsupported trees fail closed before blobs', async () => {
  const invalid = [
    undefined,
    { sha: TREE, truncated: true, tree: [] },
    { sha: HEAD, truncated: false, tree: [] },
    { sha: TREE, truncated: false, tree: [{ path: '../a.go' }] },
    { sha: TREE, truncated: false, tree: [{ path: 'a.go', mode: '120000', type: 'blob', sha: HEAD, size: 1 }] },
    { sha: TREE, truncated: false, tree: [{ path: 'module', mode: '160000', type: 'commit', sha: HEAD }] },
    { sha: TREE, truncated: false, tree: [{ path: 'a.go', mode: '100644', type: 'blob', sha: HEAD, size: testables.MAX_BLOB_BYTES + 1 }] },
    { sha: TREE, truncated: false, tree: [{ path: 'a.go', mode: '100644', type: 'blob', sha: HEAD }] },
  ];
  for (const tree of invalid) assert.throws(() => testables.validateTree(tree, TREE), /Suppression audit held/);
  const harness = fixture({ 'a.go': 'x' }, { tree: invalid[1] });
  await assert.rejects(verifySuppressions(harness.args), /truncated/);
  assert.equal(harness.calls.length, 2);
});

test('missing parents and duplicate tree paths hold', () => {
  const blob = { path: 'folder/a.go', mode: '100644', type: 'blob', sha: HEAD, size: 1 };
  assert.throws(() => testables.validateTree({ sha: TREE, truncated: false, tree: [blob] }, TREE), /missing parent/);
  assert.throws(() => testables.validateTree({ sha: TREE, truncated: false, tree: [blob, blob] }, TREE), /duplicate/);
});

test('wrong commit, missing immutable evidence and GraphQL errors cannot pass', async () => {
  await assert.rejects(verifySuppressions(fixture({}, { commit: { sha: POLICY, tree: { sha: TREE } } }).args), /exact candidate head/);
  await assert.rejects(verifySuppressions({ ...fixture({}).args, trustedPolicySHA: 'main' }), /trusted policy/);
  await assert.rejects(verifySuppressions(fixture({ 'a.go': 'x' }, { graphqlError: new Error('API unavailable') }).args), /API unavailable/);
  for (const response of [() => ({}), () => ({ repository: null }), (repository) => ({ repository, errors: [{}] })]) {
    await assert.rejects(verifySuppressions(fixture({ 'a.go': 'x' }, { response }).args), /GraphQL errors/);
  }
});

test('blob responses must be complete, exact and lossless UTF-8', async () => {
  const mutations = [
    () => null,
    (blob) => ({ ...blob, __typename: 'Tree' }),
    (blob) => ({ ...blob, oid: POLICY }),
    (blob) => ({ ...blob, byteSize: 999 }),
    (blob) => ({ ...blob, isTruncated: true }),
    (blob) => ({ ...blob, isTruncated: undefined }),
    (blob) => ({ ...blob, isBinary: null }),
    (blob) => ({ ...blob, text: null }),
    (blob) => ({ ...blob, text: '' }),
    (blob) => ({ ...blob, text: '\ud800' }),
  ];
  for (const blob of mutations) {
    await assert.rejects(verifySuppressions(fixture({ 'a.go': 'abc' }, { blob }).args), /Suppression audit held/);
  }
});

test('late missing blob fails after successful earlier batches', async () => {
  const contents = Object.fromEntries(Array.from({ length: 101 }, (_, index) => [`file${index}.go`, 'package main']));
  const harness = fixture(contents, { blob: (blob, file) => file.path === 'file100.go' ? null : blob });
  await assert.rejects(verifySuppressions(harness.args), /file100.go/);
  assert.equal(harness.calls.filter(([kind]) => kind === 'graphql').length, 3);
});

test('binary data uses only its immutable REST blob and validates canonical bytes', async () => {
  const harness = fixture({ 'image.gif': Buffer.from('GIF89a\0'), 'a.go': 'package main' });
  const evidence = await verifySuppressions(harness.args);
  assert.equal(evidence.apiRequests, 4);
  assert.equal(evidence.filesScanned, 2);
  assert.equal(harness.calls.at(-1)[0], 'binary');
  assert.equal(harness.calls.at(-1)[1].file_sha, harness.files[0].sha);
  for (const binary of [
    (data) => ({ ...data, sha: POLICY }),
    (data) => ({ ...data, size: 99 }),
    (data) => ({ ...data, encoding: 'none' }),
    (data) => ({ ...data, content: data.content + '!' }),
    (data) => ({ ...data, content: '' }),
  ]) {
    await assert.rejects(verifySuppressions(fixture({ 'image.gif': Buffer.from('GIF89a\0') }, { binary }).args), /binary blob/);
  }
});

test('bounded batches deduplicate OIDs and respect both response-size and object limits', () => {
  const shared = { path: 'same.go', type: 'blob', sha: HEAD, size: 1 };
  assert.equal(testables.blobBatches([shared, { ...shared, path: 'renamed.go' }])[0].length, 1);
  const entries = Array.from({ length: 101 }, (_, index) => ({ ...shared, sha: String(index), size: 1 }));
  assert.deepEqual(testables.blobBatches(entries).map((batch) => batch.length), [50, 50, 1]);
  assert.deepEqual(testables.blobBatches(entries.slice(0, 3).map((entry) => ({ ...entry, size: testables.MAX_BLOB_BYTES }))).map((batch) => batch.length), [2, 1]);
});

test('binary source and ambiguous source encoding fail closed', () => {
  assert.throws(() => testables.scanFile({ path: 'src.go', body: Buffer.from([0xff]), mode: '100644' }), /non-UTF-8/);
  assert.throws(() => scan('src.go', 'package main\0'), /NUL/);
  assert.ok(scan('fake.png', '# shellcheck disable=SC2016').length);
  assert.ok(scan('fixture.txt', '# shellcheck disable=SC2016').length);
  assert.ok(scan('fixture.golden', '//nolint:all').length);
  assert.equal(testables.scanFile({ path: 'image.gif', body: Buffer.from('GIF89a\0'), mode: '100644' }).length, 0);
});
