'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');

const trackInlineSuppressions = require('./inline_suppression_tracker.js');
const { testables } = trackInlineSuppressions;

function trackedLine(marker = 'nolint:staticcheck') {
  return `\t_ = 1 //${marker} // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard`;
}

function patchFor(line) {
  return `@@ -0,0 +1,5 @@\n+package main\n+\n+func main() {\n+${line}\n+}\n`;
}

function makeHarness(options = {}) {
  const pull = {
    number: 42,
    state: 'open',
    changed_files: options.changedFiles,
    base: { sha: 'base-sha', repo: { full_name: 'octo/lopper' } },
    head: {
      sha: 'head-sha',
      repo: {
        fork: true,
        full_name: 'fork/lopper',
      },
    },
    ...options.pull,
  };
  const files = options.files || [
    {
      filename: 'main.go',
      status: 'added',
      patch: patchFor(trackedLine()),
    },
  ];
  if (!Number.isInteger(pull.changed_files)) {
    pull.changed_files = files.length;
  }

  const calls = {
    created: [],
    infos: [],
    searches: [],
    updated: [],
  };
  const github = {
    rest: {
      issues: {
        create: async (input) => {
          calls.created.push(input);
          return { data: { number: 101 } };
        },
        update: async (input) => {
          calls.updated.push(input);
        },
      },
      pulls: {
        get: async () => ({ data: { changed_files: options.fetchedChangedFiles ?? files.length } }),
        listFiles: async () => {},
      },
      search: {
        issuesAndPullRequests: async (input) => {
          calls.searches.push(input);
          return {
            data: {
              items: options.existingIssue ? [{ number: options.existingIssue }] : [],
            },
          };
        },
      },
    },
    paginate: async (method) => {
      assert.equal(method, github.rest.pulls.listFiles);
      return files;
    },
  };
  return {
    args: {
      github,
      context: {
        repo: { owner: 'octo', repo: 'lopper' },
        payload: { pull_request: pull },
      },
      core: {
        info: (message) => calls.infos.push(message),
      },
    },
    calls,
  };
}

test('tracks fork pull request suppressions from trusted diff recomputation', async () => {
  const harness = makeHarness();

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.equal(harness.calls.updated.length, 0);
  const created = harness.calls.created[0];
  assert.equal(created.title, 'ci: track inline suppression in main.go:4');
  assert.match(created.body, /Location: `main\.go:4`/);
  assert.match(created.body, /Source: https:\/\/github\.com\/octo\/lopper\/blob\/head-sha\/main\.go#L4/);
  assert.match(created.body, /Rationale: temporary scanner false positive/);
  assert.match(harness.calls.infos.join('\n'), /Opened inline suppression tracking issue #101/);
});

test('recognizes supported inline suppression marker forms without matching quoted text', () => {
  const matchingLines = [
    '\t_ = 1 /' + '/no' + 'sec G404',
    '\t_ = 1 /' + '* NO' + 'SONAR */',
    '\t_ = 1 /' + '** NO' + 'SONAR */',
    '\t_ = 1 ' + '# no' + 'qa',
    '\t_ = 1 /' + '/ @ts-' + 'ignore',
    '\t_ = 1 /' + '/ @ts-' + 'expect-error',
    '\t_ = 1 /' + '/ eslint-' + 'disable-next-line no-console',
    '\t_ = 1 /' + '/ pragma: ' + 'no cover',
    '\t_ = 1 /' + '/ coverage: ' + 'ignore',
  ];
  for (const line of matchingLines) {
    assert.equal(testables.hasInlineSuppressionMarker(line), true, line);
  }

  const ignoredLines = [
    'const marker = "/' + '/no' + 'sec";',
    'const marker = "/' + '* NO' + 'SONAR */";',
    '\t_ = 1 /' + '/ no' + 'linter',
    '\t_ = 1 /' + '/ coverage: ' + 'ignored',
    '\t_ = 1 /' + '/ pragma: ' + 'no coverage',
  ];
  for (const line of ignoredLines) {
    assert.equal(testables.hasInlineSuppressionMarker(line), false, line);
  }
});

test('classifies source files without path validation side effects', () => {
  assert.equal(testables.isSourceFile('internal/main.go'), true);
  assert.equal(testables.isSourceFile('web/component.TSX'), true);
  assert.equal(testables.isSourceFile('.githooks/pre-commit'), true);
  assert.equal(testables.isSourceFile('docs/policy.md'), false);
});

test('parses metadata aliases without dynamic regular expressions', () => {
  const content = '/' + '/nolint //' + ' reason: false positive; owner = @security; removal-condition= analyzer fix';

  assert.equal(testables.metadataValue(content, ['rationale', 'reason']), 'false positive');
  assert.equal(testables.metadataValue(content, ['owner']), '@security');
  assert.equal(testables.metadataValue(content, ['remove-when', 'removal-condition']), 'analyzer fix');
});

test('updates an existing tracking issue by fingerprint', async () => {
  const harness = makeHarness({ existingIssue: 77 });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
  assert.equal(harness.calls.updated.length, 1);
  assert.equal(harness.calls.updated[0].issue_number, 77);
  assert.match(harness.calls.infos.join('\n'), /Updated inline suppression tracking issue #77/);
});

test('ignores forged artifact-shaped data because only pull files are read', async () => {
  const harness = makeHarness({
    files: [
      {
        filename: 'README.md',
        status: 'modified',
        patch: '@@ -1 +1 @@\n-doc\n+doc\n',
      },
    ],
    pull: {
      artifact: {
        suppressions: [
          {
            file: 'evil.go',
            content: trackedLine(),
          },
        ],
      },
    },
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
  assert.deepEqual(harness.calls.infos, ['No inline suppression records were produced.']);
});

test('fails closed when GitHub omits a changed file patch', async () => {
  const harness = makeHarness({
    files: [{ filename: 'main.go', status: 'modified' }],
  });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'TypeError',
      message: /diff patch is unavailable.*refusing to publish tracking mutations/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('fails closed when a trusted diff hunk is malformed', async () => {
  const harness = makeHarness({
    files: [{ filename: 'main.go', status: 'modified', patch: '@@ malformed @@\n+' + trackedLine() }],
  });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'SyntaxError',
      message: /Unable to parse inline suppression diff hunk for main\.go/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('fails closed when pagination does not match the authoritative file count', async () => {
  const harness = makeHarness({ changedFiles: 2 });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'RangeError',
      message: /saw 1 changed files but GitHub reports 2.*refusing to publish tracking mutations/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('fails closed above the GitHub 3000-file pull diff boundary', async () => {
  const harness = makeHarness({ changedFiles: testables.MAX_CHANGED_FILES + 1, files: [] });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'RangeError',
      message: /exceeds the 3000-file trusted diff limit.*refusing to publish tracking mutations/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('rejects path traversal before issue mutation', async () => {
  const harness = makeHarness({
    files: [
      {
        filename: '../main.go',
        status: 'added',
        patch: patchFor(trackedLine()),
      },
    ],
  });

  await assert.rejects(() => trackInlineSuppressions(harness.args), {
    name: 'TypeError',
    message: /Invalid inline suppression file path/,
  });
  assert.equal(harness.calls.created.length, 0);
});
