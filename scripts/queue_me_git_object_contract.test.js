'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const cp = require('node:child_process');
const { gitBlobIdentity } = require('./queue_me_git_object');
const ci = require('./queue_me_ci');
const { harness, PATHS } = require('./testdata/queue_waiting/ci_fixture.cjs');
const { maintenanceFixture, maintenanceSource, simulateMaintenanceBackend } = require('./testdata/queue_waiting/maintenance.cjs');
const nativeSupported = ['linux', 'darwin'].includes(process.platform);
const emptyOID = 'e69de29bb2d1d6434b8b29ae775ad8c2e48c5391';
const normal = stdout => ({ status: 0, signal: null, stdout: Buffer.from(stdout), stderr: Buffer.alloc(0) });

function noEffects(t) {
  const temporary = t.mock.method(fs, 'mkdtempSync', () => assert.fail('unexpected temporary directory'));
  const process = t.mock.method(cp, 'spawnSync', () => assert.fail('unexpected backend process'));
  return () => {
    assert.equal(temporary.mock.callCount(), 0);
    assert.equal(process.mock.callCount(), 0);
  };
}

test('imports and ordinary API/readiness have zero backend effects on unsupported hosts', async t => {
  t.mock.method(os, 'platform', () => 'win32');
  const assertNone = noEffects(t);
  const filename = require.resolve('./queue_me_git_object');
  const prior = require.cache[filename];
  try {
    delete require.cache[filename];
    assert.equal(typeof require(filename).gitBlobIdentity, 'function');
    assert.deepEqual(Object.keys(ci).sort(), ['assertUnchangedCI', 'checkReadiness', 'isWaiting', 'testables', 'verifyCI']);
    await ci.verifyCI(harness().input);
    assert.equal((await ci.checkReadiness(harness().input)).state, 'READY');
    assert.equal(typeof require('./suppression_provenance'), 'function');
    assert.equal(typeof require('./suppression_provenance').isDeferred, 'function');
    assertNone();
  } finally { require.cache[filename] = prior; }
});

test('actual unsupported invocation and invalid inputs have zero effects', t => {
  const assertNone = noEffects(t);
  for (const input of [undefined, '', new Uint8Array(1), Buffer.alloc(2097153)]) {
    assert.throws(() => gitBlobIdentity(input), /bounded Buffer/);
  }
  if (nativeSupported) t.mock.method(os, 'platform', () => 'win32');
  assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), /unsupported/);
  assertNone();
});

test('trusted Git preserves independent literal and maximum-size object identities', () => {
  if (!nativeSupported) {
    assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), /unsupported/);
    return;
  }
  for (const [bytes, oid] of [[Buffer.alloc(0), emptyOID],
    [Buffer.from('hello\n'), 'ce013625030ba8dba906f756967f9e9ca394464a'],
    [Buffer.from('hello\r\n'), 'ef0493b275aa2080237f676d2ef6559246f56636'],
    [Buffer.from('00ff800d0a', 'hex'), 'f21a905eaf587067e88aadfe3cda06c916e532a6'],
    [Buffer.alloc(2097152, 0x78), 'c53659eed4add508990a03abebdebad2574b32e5']]) {
    assert.equal(gitBlobIdentity(bytes), oid);
  }
});

function rejectedProcess(t, metadata, object, expected) {
  t.mock.method(os, 'platform', () => 'linux');
  const directories = [];
  const spawn = t.mock.method(cp, 'spawnSync', (_executable, argv, options) => {
    directories.push(options.cwd);
    return argv[0] === 'version' ? metadata : object;
  });
  assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), expected);
  for (const directory of directories) assert.equal(fs.existsSync(directory), false, 'joined failure directory leaked');
  return spawn.mock.callCount();
}

test('backend admission rejects absent duplicate unsafe and malformed declarations before object hashing', async t => {
  for (const text of ['', 'SHA-1: openssl\n', 'SHA-1: SHA1_DC\nSHA-1: openssl\n',
    'SHA-1: SHA1_DC\nSHA-1: SHA1_DC\n', 'SHA-1: SHA1_DC\r\n']) {
    await t.test(JSON.stringify(text), child => {
      assert.equal(rejectedProcess(child, normal(text), normal(`${emptyOID}\n`), /requires SHA1_DC/), 1);
    });
  }
});

test('valid-looking identities cannot override process failures or output bounds', async t => {
  const failures = [ ['nonzero', { status: 1 }], ['null status', { status: null }],
    ['signal', { signal: 'SIGKILL' }], ['timeout', { error: Object.assign(new Error('timeout'), { code: 'ETIMEDOUT' }) }],
    ['stderr', { stderr: Buffer.from('private diagnostic') }], ['overflow', { stdout: Buffer.alloc(4097) }],
    ['nonbuffer', { stdout: emptyOID }] ];
  for (const [name, change] of failures) {
    await t.test(name, child => {
      assert.equal(rejectedProcess(child, normal('SHA-1: SHA1_DC\n'), { ...normal(`${emptyOID}\n`), ...change }, /verification/), 2);
    });
  }
  await t.test('metadata overflow', child => {
    assert.equal(rejectedProcess(child, normal(' '.repeat(65537)), normal(`${emptyOID}\n`), /invalid output/), 1);
  });
});

test('identity output requires exact lowercase ASCII and one LF', async t => {
  for (const output of [emptyOID, `${emptyOID}\r\n`, `${emptyOID}\n\n`, `${emptyOID.toUpperCase()}\n`,
    `${'a'.repeat(64)}\n`, `prefix${emptyOID}\n`, Buffer.from([...Buffer.from(`${emptyOID}\n`)].map(x => x | 0x80))]) {
    await t.test(Buffer.from(output).toString('hex'), child => {
      rejectedProcess(child, normal('SHA-1: SHA1_DC\n'), normal(output), /malformed identity/);
    });
  }
});

test('cleanup failure is fatal and original verification failure is preserved', async t => {
  for (const fails of [false, true]) {
    await t.test(fails ? 'primary preserved' : 'success cannot hide cleanup failure', child => {
      child.mock.method(os, 'platform', () => 'linux');
      let owned;
      child.mock.method(cp, 'spawnSync', (_exe, argv, options) => {
        owned = options.cwd;
        if (fails) return { ...normal('SHA-1: SHA1_DC\n'), status: 1 };
        return normal(argv[0] === 'version' ? 'SHA-1: SHA1_DC\n' : `${emptyOID}\n`);
      });
      const remove = fs.rmSync;
      child.mock.method(fs, 'rmSync', () => { throw new Error('injected cleanup refusal'); });
      try {
        assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), fails ? /process failed/ : /cleanup refusal/);
      } finally { if (owned) remove(owned, { recursive: true }); }
    });
  }
});

test('backend directory writes are rejected and removed after the joined call', t => {
  t.mock.method(os, 'platform', () => 'linux');
  let owned;
  t.mock.method(cp, 'spawnSync', (_exe, _argv, options) => {
    owned = options.cwd;
    fs.writeFileSync(path.join(owned, 'unexpected'), 'owned');
    return normal('SHA-1: SHA1_DC\n');
  });
  assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), /changed its private directory/);
  assert.equal(fs.existsSync(owned), false);
});

test('modeled maintenance backend enforces finite bytes and restores after an exception', async t => {
  const platform = os.platform;
  const spawn = cp.spawnSync;
  simulateMaintenanceBackend(t);
  try {
    await ci.verifyCI(maintenanceFixture().input);
    assert.throws(() => gitBlobIdentity(Buffer.from('unknown fixture bytes')), /unknown simulated corpus/);
  } finally { t.mock.restoreAll(); }
  assert.equal(os.platform, platform);
  assert.equal(cp.spawnSync, spawn);
  t.mock.method(os, 'platform', () => 'win32');
  const assertNone = noEffects(t);
  assert.throws(() => gitBlobIdentity(Buffer.from(maintenanceSource)), /unsupported/);
  assertNone();
});

function queueSource(bytes, oid, transform = value => value) {
  const fixture = maintenanceFixture();
  const read = fixture.input.github.rest.repos.getContent;
  fixture.input.github.rest.repos.getContent = async args => {
    const response = await read(args);
    if (args.path === PATHS[0]) response.data = transform({ ...response.data,
      content: bytes.toString('base64'), sha: oid });
    return response;
  };
  return fixture;
}

test('queue retains exact encoded bounds LF contribution and permissive binary decoding', async t => {
  if (!nativeSupported) {
    assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), /unsupported/);
    return;
  }
  const m = Buffer.from(maintenanceSource);
  const qmax = Buffer.concat([Buffer.alloc(1572864 - m.length, 0x20), m]);
  const qlf = Buffer.concat([Buffer.alloc(1572861 - m.length, 0x20), m]);
  await ci.verifyCI(queueSource(qmax, 'a1d4fed11700914da105cb4fda8971fb238c52d5').input);
  await ci.verifyCI(queueSource(qlf, 'd9622db23396161f09a567d9838605652f1a04d2', data => ({ ...data, content: data.content + '\n'.repeat(4) })).input);
  await ci.verifyCI(queueSource(Buffer.concat([Buffer.from([0xff]), m]), 'b4fda9e2779fd885ecdac470c47f79ee315a315e').input);
  for (const extra of ['A', '\n'.repeat(5)]) {
    const bytes = extra === 'A' ? qmax : qlf;
    await t.test(extra === 'A' ? 'encoded plus one' : 'fifth LF exceeds raw limit', async () => {
      await assert.rejects(ci.verifyCI(queueSource(bytes, 'a1d4fed11700914da105cb4fda8971fb238c52d5', data => ({ ...data, content: data.content + extra })).input), /missing bounded maintenance workflow source/);
    });
  }
});

test('queue keeps both independent identities and canonical changed-content rejection', async t => {
  if (!nativeSupported) { assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), /unsupported/); return; }
  for (const mode of ['advertised', 'selected source', 'changed bytes', 'noncanonical']) {
    await t.test(mode, async () => {
      let reads = 0;
      const fixture = queueSource(Buffer.from(mode === 'changed bytes' ? ` ${maintenanceSource}` : maintenanceSource),
        '2f6263b02f0b417799c71dd202f62920b3f648c3', data => {
          reads++;
          if ((mode === 'advertised' && reads > 3) || (mode === 'selected source' && reads <= 3)) data.sha = 'a'.repeat(40);
          if (mode === 'noncanonical') data.content += '!';
          return data;
        });
      await assert.rejects(ci.verifyCI(fixture.input), /source identity changed/);
    });
  }
});

function trustedMaterializer() {
  const workflow = fs.readFileSync(path.join(__dirname, '../.github/workflows/queue-me.yml'), 'utf8');
  const step = workflow.split('      - name: Materialize trusted queue controller\n')[1].split('\n      - name:')[0];
  const body = step.split('          script: |\n')[1].split('\n').map(line => line.slice(12)).join('\n');
  assert(body.includes("'queue_me_git_object.js'"));
  return `module.exports = async (github, context) => {\n${body}\n};\n`;
}

function restoreEnvironment(saved) {
  for (const [key, value] of saved) {
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  }
}

test('immutable trusted materialization includes private verifier and missing helper fails loading', async t => {
  const directory = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'lopper-materialized-git-')));
  const saved = ['QUEUE_CONTROLLER_PATH', 'TRUSTED_CONTROLLER_REF'].map(key => [key, process.env[key]]);
  const expectedRef = 'd'.repeat(40);
  process.env.QUEUE_CONTROLLER_PATH = path.join(directory, 'queue_me_controller.js');
  process.env.TRUSTED_CONTROLLER_REF = expectedRef;
  try {
    const loader = path.join(directory, 'materialize.cjs');
    fs.writeFileSync(loader, trustedMaterializer());
    const fetched = [];
    const github = { rest: { repos: { getContent: async args => {
      assert.equal(args.ref, expectedRef);
      assert.equal(args.owner, 'owner');
      assert.equal(args.repo, 'repo');
      assert(/^scripts\/[a-z_]+\.js$/.test(args.path));
      fetched.push(args.path);
      return { data: { type: 'file', encoding: 'base64', content: fs.readFileSync(path.join(__dirname, '..', args.path)).toString('base64') } };
    } } } };
    await require(loader)(github, { repo: { owner: 'owner', repo: 'repo' } });
    assert(fetched.includes('scripts/queue_me_git_object.js'));
    for (const name of fetched) require(path.join(directory, path.basename(name)));
    const materializedCI = require(path.join(directory, 'queue_me_ci.js'));
    t.mock.method(os, 'platform', () => 'win32');
    const spawn = t.mock.method(cp, 'spawnSync', () => assert.fail('ordinary materialized API started Git'));
    await materializedCI.verifyCI(harness().input);
    assert.equal(spawn.mock.callCount(), 0);
    t.mock.restoreAll();
    if (nativeSupported) await materializedCI.verifyCI(maintenanceFixture().input);
    clearMaterializedCache(directory);
    fs.unlinkSync(path.join(directory, 'queue_me_git_object.js'));
    assert.throws(() => require(path.join(directory, 'queue_me_ci.js')), error => {
      assert(['MODULE_NOT_FOUND', 'ENOENT'].includes(error.code));
      assert.match(error.message, /queue_me_git_object/);
      return true;
    });
  } finally {
    t.mock.restoreAll();
    clearMaterializedCache(directory);
    restoreEnvironment(saved);
    fs.rmSync(directory, { recursive: true });
  }
});

function clearMaterializedCache(directory) {
  for (const key of Object.keys(require.cache)) if (key.startsWith(`${directory}${path.sep}`)) delete require.cache[key];
}

test('native Git ignores hostile inherited configuration loader and enclosing repository filters', () => {
  if (!nativeSupported) { assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), /unsupported/); return; }
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'lopper-git-context-'));
  const saved = ['PATH', 'HOME', 'GIT_DIR', 'GIT_CONFIG_COUNT', 'GIT_CONFIG_PARAMETERS',
    'GIT_DEFAULT_HASH', 'LD_PRELOAD', 'DYLD_INSERT_LIBRARIES', 'TMPDIR'].map(key => [key, process.env[key]]);
  try {
    const initialized = cp.spawnSync('/usr/bin/git', ['init', '--object-format=sha256', directory], {
      env: { PATH: '/usr/bin:/bin', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' },
      timeout: 10000, killSignal: 'SIGKILL', maxBuffer: 65536 });
    assert.ifError(initialized.error);
    assert.equal(initialized.status, 0, initialized.stderr?.toString());
    const marker = path.join(directory, 'filter-executed');
    fs.writeFileSync(path.join(directory, '.gitattributes'), '* filter=hostile\n');
    fs.appendFileSync(path.join(directory, '.git/config'), `\n[filter "hostile"]\n\tclean = touch '${marker}'; cat\n`);
    for (const [key] of saved) process.env[key] = '/invalid/hostile';
    process.env.GIT_DIR = path.join(directory, '.git');
    process.env.GIT_DEFAULT_HASH = 'sha256';
    process.env.GIT_CONFIG_COUNT = '999';
    process.env.TMPDIR = directory;
    assert.equal(gitBlobIdentity(Buffer.from('hello\n')), 'ce013625030ba8dba906f756967f9e9ca394464a');
    assert.equal(fs.existsSync(marker), false);
    assert.deepEqual(fs.readdirSync(directory).sort(), ['.git', '.gitattributes']);
  } finally {
    restoreEnvironment(saved);
    fs.rmSync(directory, { recursive: true });
  }
});

test('queue translates backend failures into its existing bounded source error', async t => {
  const fixture = maintenanceFixture();
  const original = fixture.input.github.rest.repos.getContent;
  fixture.input.github.rest.repos.getContent = async args => args.path === PATHS[0] ? { data: {
    type: 'file', path: PATHS[0], encoding: 'base64', content: Buffer.from(maintenanceSource).toString('base64'),
    sha: '2f6263b02f0b417799c71dd202f62920b3f648c3' } } : original(args);
  t.mock.method(os, 'platform', () => 'linux');
  t.mock.method(cp, 'spawnSync', () => ({ ...normal('SHA-1: SHA1_DC\n'), status: 1, stderr: Buffer.from('private backend value') }));
  await assert.rejects(ci.verifyCI(fixture.input), error => {
    assert.match(error.message, /^CI audit paused: maintenance workflow source identity changed/);
    assert.doesNotMatch(error.message, /private backend value|lopper-git-object/);
    return true;
  });
});
