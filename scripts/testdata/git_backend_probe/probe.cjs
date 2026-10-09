'use strict';

// Test-only evidence acquisition. This is not the production blob verifier.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const childProcess = require('node:child_process');
const { createHash } = require('node:crypto');

const executable = '/usr/bin/git';
const objectArguments = ['-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false',
  'hash-object', '-t', 'blob', '--stdin', '--no-filters'];
const goldens = [
  { hex: '', oid: 'e69de29bb2d1d6434b8b29ae775ad8c2e48c5391' },
  { hex: '68656c6c6f0a', oid: 'ce013625030ba8dba906f756967f9e9ca394464a' },
  { hex: '68656c6c6f0d0a', oid: 'ef0493b275aa2080237f676d2ef6559246f56636' },
  { hex: '00ff800d0a', oid: 'f21a905eaf587067e88aadfe3cda06c916e532a6' },
  { byte: 0x78, count: 1048576, oid: 'fc26db1cf2fd25ac90dbf93eef0ebb92b51e8850' },
];

function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

function environment(cwd) {
  return { PATH: '/usr/bin:/bin', LC_ALL: 'C', GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_SYSTEM: '/dev/null', GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_NO_REPLACE_OBJECTS: '1', GIT_CEILING_DIRECTORIES: path.dirname(cwd) };
}

function checkResult(result, limit) {
  if (!Buffer.isBuffer(result.stdout) || !Buffer.isBuffer(result.stderr)) {
    throw new TypeError('Git returned non-buffer output');
  }
  if (result.error || result.signal || result.status !== 0) {
    throw new Error('Git process did not complete successfully');
  }
  if (result.stdout.length > limit || result.stderr.length > limit) {
    throw new Error('Git output exceeded its limit');
  }
  if (result.stderr.length !== 0) throw new Error('Git wrote unexpected stderr');
}

function execute(receipt, argv, input, limit) {
  const options = { cwd: receipt.cwd, env: environment(receipt.cwd), input,
    timeout: 10000, killSignal: 'SIGKILL', maxBuffer: limit };
  const result = childProcess.spawnSync(executable, argv, options);
  receipt.processes.push({ executable, argv, env: options.env, timeout: options.timeout,
    killSignal: options.killSignal, maxBuffer: limit, status: result.status,
    signal: result.signal, error: result.error ? String(result.error) : null,
    stdout: result.stdout?.subarray(0, limit).toString('base64'),
    stderr: result.stderr?.subarray(0, limit).toString('base64'),
    stdoutBytes: result.stdout?.length, stderrBytes: result.stderr?.length });
  receipt.afterProcess.push(fs.readdirSync(receipt.cwd));
  checkResult(result, limit);
  if (receipt.afterProcess.at(-1).length) throw new Error('Git wrote into its owned cwd');
  return result.stdout;
}

function checkBackend(bytes) {
  const declarations = bytes.toString('utf8').split('\n').filter(line => line.startsWith('SHA-1:'));
  if (declarations.length !== 1 || declarations[0] !== 'SHA-1: SHA1_DC') {
    throw new Error('Git must declare exactly one SHA1_DC backend');
  }
}

function checkObject(bytes, expected) {
  if (!/^[a-f0-9]{40}\n$/.test(bytes.toString('ascii')) ||
      !bytes.equals(Buffer.from(`${expected}\n`, 'ascii'))) {
    throw new Error('Git blob golden mismatch or malformed output');
  }
}

function fixedExecutable() {
  const stat = fs.lstatSync(executable);
  const realpath = fs.realpathSync(executable);
  const target = fs.statSync(realpath);
  if (!target.isFile() || target.uid !== 0 || (target.mode & 0o022) !== 0 || (target.mode & 0o111) === 0) {
    throw new Error('Fixed Git is not a root-owned non-writable regular executable');
  }
  return { path: executable, realpath, uid: stat.uid, mode: stat.mode,
    symlink: stat.isSymbolicLink(), targetUID: target.uid, targetMode: target.mode,
    sha256: sha256(fs.readFileSync(realpath)) };
}

function hostIdentity(platform) {
  const image = {};
  for (const key of ['ImageOS', 'ImageVersion', 'RUNNER_OS', 'RUNNER_ARCH']) {
    if (process.env[key] !== undefined) image[key] = process.env[key];
  }
  const identity = { platform, release: os.release(), arch: os.arch(), image };
  if (platform === 'linux') identity.osRelease = fs.readFileSync('/etc/os-release', 'utf8');
  return identity;
}

function collectObjects(receipt) {
  for (const golden of goldens) {
    const input = golden.hex === undefined ? Buffer.alloc(golden.count, golden.byte) : Buffer.from(golden.hex, 'hex');
    const output = execute(receipt, objectArguments, input, 4096);
    receipt.objects.push({ recipe: golden, bytes: input.length, sha256: sha256(input), output: output.toString('ascii') });
    checkObject(output, golden.oid);
  }
}

function cleanOwnedDirectory(receipt) {
  receipt.after = fs.readdirSync(receipt.cwd);
  // A failed no-write assertion still owns this one directory and must clean it.
  fs.rmSync(receipt.cwd, { recursive: true });
  receipt.cleaned = !fs.existsSync(receipt.cwd);
  if (receipt.after.length || !receipt.cleaned) throw new Error('Owned cwd cleanup contract failed');
}

function runProbe() {
  const platform = os.platform();
  if (!['linux', 'darwin'].includes(platform)) {
    throw new Error('Unsupported native Git backend platform');
  }
  const receipt = { admission: 'PENDING', host: hostIdentity(platform), executable: fixedExecutable(),
    processes: [], objects: [], afterProcess: [], cleaned: false };
  const owned = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'lopper-git-backend-'));
  receipt.cwd = owned;
  let failure;
  try {
    fs.chmodSync(owned, 0o700);
    receipt.cwd = fs.realpathSync(owned);
    receipt.mode = fs.statSync(receipt.cwd).mode & 0o777;
    receipt.before = fs.readdirSync(receipt.cwd);
    if (receipt.mode !== 0o700 || receipt.before.length) throw new Error('Owned cwd is not empty and private');
    checkBackend(execute(receipt, ['version', '--build-options'], Buffer.alloc(0), 65536));
    collectObjects(receipt);
  } catch (error) {
    failure = error;
  } finally {
    try { cleanOwnedDirectory(receipt); } catch (error) {
      receipt.cleanupError = String(error);
      failure = failure || error;
    }
  }
  if (failure) {
    receipt.admission = 'FAIL';
    failure.receipt = receipt;
    throw failure;
  }
  receipt.admission = 'PASS';
  return receipt;
}

module.exports = { runProbe, checkBackend, checkObject, checkResult, goldens };
