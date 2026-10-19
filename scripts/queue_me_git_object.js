'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const childProcess = require('node:child_process');

function assertEmpty(directory) {
  if (fs.readdirSync(directory).length !== 0) throw new Error('Git object verification changed its private directory');
}

function gitOutput(directory, argv, input, maxBuffer) {
  const options = { cwd: directory, shell: false, stdio: ['pipe', 'pipe', 'pipe'],
    timeout: 10000, killSignal: 'SIGKILL', maxBuffer,
    env: { PATH: '/usr/bin:/bin', LC_ALL: 'C', GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_SYSTEM: '/dev/null', GIT_CONFIG_GLOBAL: '/dev/null',
      GIT_NO_REPLACE_OBJECTS: '1', GIT_CEILING_DIRECTORIES: path.dirname(directory) } };
  if (input !== undefined) options.input = input;
  let output;
  let failure;
  try {
    const result = childProcess.spawnSync('/usr/bin/git', argv, options);
    output = checkedOutput(result, maxBuffer);
  } catch (error) { failure = error; }
  try { assertEmpty(directory); } catch (error) { failure = failure || error; }
  if (failure) throw failure;
  return output;
}

function checkedOutput(result, maxBuffer) {
  if (result.error || result.signal || result.status !== 0 ||
      !Buffer.isBuffer(result.stdout) || !Buffer.isBuffer(result.stderr)) {
    throw new Error('Git object verification process failed');
  }
  if (result.stdout.length > maxBuffer || result.stderr.length !== 0) {
    throw new Error('Git object verification returned invalid output');
  }
  return result.stdout;
}

function verifyBackend(directory) {
  const metadata = gitOutput(directory, ['version', '--build-options'], undefined, 65536);
  const declarations = metadata.toString('utf8').split('\n').filter(line => line.startsWith('SHA-1:'));
  if (declarations.length !== 1 || declarations[0] !== 'SHA-1: SHA1_DC') {
    throw new Error('Git object verification requires SHA1_DC');
  }
}

function blobIdentity(directory, bytes) {
  verifyBackend(directory);
  const output = gitOutput(directory, ['-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false',
    'hash-object', '-t', 'blob', '--stdin', '--no-filters'], bytes, 4096);
  const text = output.toString('ascii');
  if (output.length !== 41 || !output.equals(Buffer.from(text, 'ascii')) || !/^[a-f0-9]{40}\n$/.test(text)) {
    throw new Error('Git object verification returned a malformed identity');
  }
  return text.slice(0, 40);
}

function removeOwned(directory) {
  let failure;
  try { assertEmpty(directory); } catch (error) { failure = error; }
  try { fs.rmSync(directory, { recursive: true }); } catch (error) { failure = failure || error; }
  if (fs.existsSync(directory)) failure = failure || new Error('Git object verification cleanup failed');
  if (failure) throw failure;
}

function gitBlobIdentity(bytes) {
  if (!Buffer.isBuffer(bytes) || bytes.length > 2 * 1024 * 1024) {
    throw new Error('Git object verification requires a bounded Buffer');
  }
  if (!['linux', 'darwin'].includes(os.platform())) {
    throw new Error('Git object verification is unsupported on this platform');
  }
  const owned = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'lopper-git-object-'));
  let failure;
  let identity;
  try {
    fs.chmodSync(owned, 0o700);
    const directory = fs.realpathSync(owned);
    assertEmpty(directory);
    identity = blobIdentity(directory, bytes);
  } catch (error) { failure = error; }
  finally {
    try { removeOwned(owned); } catch (error) { failure = failure || error; }
  }
  if (failure) throw failure;
  return identity;
}

module.exports = { gitBlobIdentity };
