// Optional real Renovate fixture. Imports for ordinary Go controls use only builtins.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmodSync, cpSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { delimiter, dirname, isAbsolute, join, parse, relative, resolve, sep } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath, pathToFileURL } from 'node:url';

const sourcePath = 'scripts/release_workflow_config_test.go';
const manifestPath = 'extensions/vscode-lopper/package.json';
const lockPath = 'extensions/vscode-lopper/package-lock.json';
const workflowPath = '.github/workflows/release.yml';
const packageFiles = [manifestPath, sourcePath, workflowPath];
const directModules = [
 'modules/manager/custom/regex/index.js', 'modules/manager/npm/extract/index.js',
 'modules/manager/npm/update/dependency/index.js', 'workers/repository/update/branch/auto-replace.js',
 'workers/repository/extract/manager-files.js', 'workers/repository/updates/branchify.js',
 'config/defaults.js', 'config/global.js', 'util/string-match.js',
];
const manifestDigest = 'fb503b7e8e684c70fef3afd563a0109e194a9edb7cc8769cdafa0941c4408f94';
const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');
const byPath = (a, b) => a < b ? -1 : Number(a > b);

export function relativeLeaf(value) {
 assert.equal(typeof value, 'string');
 assert.ok(value && !isAbsolute(value) && !/[\\:\0\r\n]/.test(value), 'invalid relative leaf');
 assert.ok(value.split('/').every((part) => part && part !== '.' && part !== '..'), 'traversal or empty segment');
 return value;
}

export function canonicalRoot(root) {
 assert.equal(typeof root, 'string');
 assert.ok(isAbsolute(root) && !/[\0\r\n]/.test(root), 'root must be absolute');
 const anchor = parse(root).root;
 const parts = root.slice(anchor.length).split(sep);
 assert.ok(root === anchor || parts.every((part) => part && part !== '.' && part !== '..'), 'noncanonical root segments');
 let current = anchor;
 for (const part of root === anchor ? [] : parts) {
  current = join(current, part);
  const st = lstatSync(current);
  assert.ok(st.isDirectory() && !st.isSymbolicLink(), `non-directory/link root: ${current}`);
  // Windows offline controls have no POSIX UID/mode authority; REAL remains Darwin-only.
  if (process.platform !== 'win32') {
   assert.ok(st.uid === 0 || st.uid === process.getuid(), `foreign owner: ${current}`);
   const sharedTemp = ['/private/tmp', '/tmp'].includes(current) && st.uid === 0 && (st.mode & 0o1000);
   assert.ok(!(st.mode & 0o022) || sharedTemp, `writable root: ${current}`);
  }
 }
 assert.equal(realpathSync(root), root, 'root must be canonical');
 return root;
}

export function checkedPath(root, leaf, kind = 'file', create = false) {
 canonicalRoot(root);
 relativeLeaf(leaf);
 const target = join(root, leaf);
 const rel = relative(root, target);
 assert.ok(rel && !isAbsolute(rel) && rel !== '..' && !rel.startsWith(`..${sep}`), 'outside root');
 let current = root;
 const parts = leaf.split('/');
 for (const [index, part] of parts.entries()) {
  current = join(current, part);
  let st;
  try { st = lstatSync(current); } catch (error) {
   if (create && error.code === 'ENOENT') return target;
   throw error;
  }
  assert.ok(!st.isSymbolicLink(), `link path: ${current}`);
  if (process.platform !== 'win32') {
   assert.ok(st.uid === 0 || st.uid === process.getuid(), `foreign owner: ${current}`);
   assert.ok(!(st.mode & 0o022), `writable path: ${current}`);
  }
  const directory = index < parts.length - 1 || kind === 'directory';
  assert.ok(directory ? st.isDirectory() : st.isFile(), `wrong path type: ${current}`);
 }
 assert.equal(realpathSync(target), target, 'noncanonical target');
 return target;
}

export function readChecked(root, leaf) {
 return readFileSync(checkedPath(root, leaf), 'utf8');
}

export function writeChecked(root, leaf, value) {
 const target = checkedPath(root, leaf, 'file', true);
 mkdirSync(dirname(target), { recursive: true });
 const parent = relative(root, dirname(target)).split(sep).join('/');
 if (parent) checkedPath(root, parent, 'directory');
 writeFileSync(target, value);
 checkedPath(root, leaf);
}

export function requireProfile(platform, arch) {
 assert.ok(platform === 'darwin' && arch === 'arm64', 'optional real fixture supports only Darwin arm64');
}

export async function loadRenovateModule(root, module) {
 assert.ok(directModules.includes(module), 'unrecognized Renovate import');
 return import(pathToFileURL(checkedPath(root, `dist/${module}`)));
}

export function compareSelector(selector, knownRoot, cwd = process.cwd()) {
 if (selector === undefined) return;
 assert.equal(typeof selector, 'string');
 assert.ok(selector && !/[\0\r\n]/.test(selector), 'invalid root selector');
 assert.ok(selector === '.' || selector === knownRoot, 'selector must use canonical root spelling');
 assert.equal(resolve(cwd, selector), knownRoot, 'selector differs from module-derived root');
 canonicalRoot(knownRoot);
}

function inspectTree(root) {
 canonicalRoot(root);
 const rows = [];
 const rootStat = lstatSync(root);
 const identities = [['', rootStat.dev, rootStat.ino, rootStat.uid, rootStat.gid, rootStat.mode]];
 const visit = (dir, prefix) => {
  for (const name of readdirSync(dir).sort(byPath)) {
   const leaf = prefix ? `${prefix}/${name}` : name;
   relativeLeaf(leaf);
   const path = join(root, leaf);
   const st = lstatSync(path);
   identities.push([leaf, st.dev, st.ino, st.uid, st.gid, st.mode]);
   assert.ok(!st.isSymbolicLink(), `link inventory member: ${leaf}`);
   if (process.platform !== 'win32') {
    assert.ok(st.uid === 0 || st.uid === process.getuid(), `foreign inventory owner: ${leaf}`);
    assert.ok(!(st.mode & 0o022), `writable inventory member: ${leaf}`);
   }
   if (st.isDirectory()) {
    rows.push([leaf, 'd', 0, 0, '']); visit(path, leaf);
   } else {
    assert.ok(st.isFile(), `nonregular inventory member: ${leaf}`);
    rows.push([leaf, 'f', Number(Boolean(st.mode & 0o111)), st.size, sha256(readFileSync(path))]);
   }
  }
 };
 visit(root, '');
 rows.sort((a, b) => byPath(a[0], b[0]));
 return { inventory: { entries: rows.length, sha256: sha256(rows.map((row) => `${JSON.stringify(row)}\n`).join('')) }, identity: sha256(JSON.stringify(identities)) };
}

export function treeInventory(root) {
 return inspectTree(root).inventory;
}

export function admitTree(root, expected) {
 const snapshot = inspectTree(root);
 assert.deepEqual(snapshot.inventory, expected, 'runtime content inventory differs from official archive content');
 return snapshot.identity;
}

export function rejectUpwardPackages(packagesRoot) {
 for (let parent = dirname(packagesRoot); ; parent = dirname(parent)) {
  assert.throws(() => lstatSync(join(parent, 'node_modules')), (error) => error.code === 'ENOENT', `upward package resolution: ${parent}`);
  if (parent === dirname(parent)) break;
 }
}

export function copyChecked(sourceRoot, sourceLeaf, targetRoot, targetLeaf) {
 const source = checkedPath(sourceRoot, sourceLeaf);
 const target = checkedPath(targetRoot, targetLeaf, 'file', true);
 mkdirSync(dirname(target), { recursive: true });
 const parent = relative(targetRoot, dirname(target)).split(sep).join('/');
 if (parent) checkedPath(targetRoot, parent, 'directory');
 cpSync(source, target);
 checkedPath(targetRoot, targetLeaf);
}

export function assertUpgradeMembership(upgrades) {
 assert.equal(upgrades.length, 6);
 assert.deepEqual([...new Set(upgrades.map((u) => u.packageFile))].sort((a, b) => a.localeCompare(b)), [...packageFiles].sort((a, b) => a.localeCompare(b)));
 for (const upgrade of upgrades) relativeLeaf(upgrade.packageFile);
}

export function applySerial(upgrades, apply) {
 return upgrades.reduce((previous, upgrade) => previous.then(() => apply(upgrade)), Promise.resolve());
}

export function fixtureEnvironment(provider, fixture) {
 const windows = process.platform === 'win32';
 const systemRoot = windows ? process.env.SystemRoot : undefined;
 if (windows) assert.ok(systemRoot && isAbsolute(systemRoot), 'Windows offline child requires SystemRoot');
 const systemPaths = windows ? [join(systemRoot, 'System32'), systemRoot] : ['/usr/bin', '/bin', '/usr/sbin', '/sbin'];
 const temp = windows ? tmpdir() : '/private/tmp';
 return {
  ...(windows ? { SystemRoot: systemRoot } : {}),
  PATH: [join(provider, 'node/bin'), join(provider, 'go/bin'), ...systemPaths].join(delimiter),
  HOME: join(fixture, '.home'), XDG_CONFIG_HOME: join(fixture, '.home'),
  TMPDIR: temp, TMP: temp, TEMP: temp, LANG: 'C', LC_ALL: 'C',
  GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: windows ? 'NUL' : '/dev/null', GIT_TERMINAL_PROMPT: '0', GIT_OPTIONAL_LOCKS: '0',
  GIT_EXEC_PATH: '/Library/Developer/CommandLineTools/usr/libexec/git-core',
  GOROOT: join(provider, 'go'), GOTOOLCHAIN: 'local', GOENV: 'off', GOWORK: 'off', GOFLAGS: '', GOCACHEPROG: '', GOMAXPROCS: '1',
  GOCACHE: join(fixture, '.go-cache'), GOMODCACHE: join(fixture, '.go-modcache'), GOPROXY: 'https://proxy.golang.org', GOSUMDB: 'sum.golang.org',
  npm_config_registry: 'https://registry.npmjs.org/', npm_config_userconfig: join(fixture, '.home/.npmrc'),
  npm_config_globalconfig: join(fixture, '.home/empty-global-npmrc'), npm_config_cache: join(fixture, '.npm-cache'),
 };
}

export async function withEnvironment(env, operation) {
 const original = { ...process.env };
 try {
  for (const key of Object.keys(process.env)) delete process.env[key];
  Object.assign(process.env, env);
  return await operation();
 } finally {
  for (const key of Object.keys(process.env)) delete process.env[key];
  Object.assign(process.env, original);
 }
}

export async function withFixture(root, operation) {
 const fixture = mkdtempSync(join(canonicalRoot(root), 'lopper-vsce-contract-'));
 let failure;
 try { return await operation(fixture); } catch (error) { failure = error; throw error; } finally {
  try {
   // Go's private module cache contains read-only directories. Never follow links during cleanup.
   const writableDirectories = (directory) => {
    const st = lstatSync(directory);
    assert.ok(st.isDirectory() && !st.isSymbolicLink());
    if (process.platform !== 'win32') assert.equal(st.uid, process.getuid());
    chmodSync(directory, (st.mode & 0o777) | 0o700);
    for (const name of readdirSync(directory)) {
     const child = join(directory, name);
     const entry = lstatSync(child);
     if (entry.isDirectory() && !entry.isSymbolicLink()) writableDirectories(child);
    }
   };
   writableDirectories(fixture);
   rmSync(fixture, { recursive: true, force: true });
  } catch (error) {
   if (failure) throw new AggregateError([failure, error], 'fixture failed and cleanup failed');
   throw error;
  }
 }
}

export function commandFor(provider, name, args, cwd, env) {
 const tools = {
  git: '/Library/Developer/CommandLineTools/usr/bin/git', tar: '/usr/bin/bsdtar',
  npm: join(provider, 'node/bin/node'), go: join(provider, 'go/bin/go'),
 };
 assert.ok(Object.hasOwn(tools, name), 'unrecognized fixture command');
 assert.ok(isAbsolute(cwd), 'command cwd must be absolute');
 const prefix = name === 'npm' ? [join(provider, 'node/lib/node_modules/npm/bin/npm-cli.js')] : [];
 return { executable: tools[name], args: [...prefix, ...args], options: { cwd, env, shell: false, stdio: 'pipe' } };
}

function admitOSCapabilities() {
 const env = { PATH: '/usr/bin:/bin:/usr/sbin:/sbin', LANG: 'C', LC_ALL: 'C' };
 for (const path of ['/Library/Developer/CommandLineTools/usr/bin/git', '/usr/bin/bsdtar']) {
  const root = dirname(path); canonicalRoot(root);
  let current = sep;
  for (const part of root.split(sep).filter(Boolean)) {
   current = join(current, part); assert.equal(lstatSync(current).uid, 0, 'OS roots must be root-owned');
  }
  const st = lstatSync(checkedPath(root, relative(root, path)));
  assert.equal(st.uid, 0); assert.ok(st.mode & 0o111);
  execFileSync('/usr/bin/codesign', ['--verify', '--strict', '-R=anchor apple', path], { env, shell: false });
 }
 // Git's native libexec aliases belong to the installed root-owned CLT package.
 const libexec = canonicalRoot('/Library/Developer/CommandLineTools/usr/libexec/git-core');
 assert.equal(lstatSync(libexec).uid, 0);
 execFileSync('/usr/sbin/pkgutil', ['--pkg-info', 'com.apple.pkg.CLTools_Executables'], { env, shell: false });
}

export function readRuntimeManifest(repo) {
 const bytes = readFileSync(checkedPath(repo, 'scripts/testdata/renovate/runtime-integrity.json'));
 assert.equal(sha256(bytes), manifestDigest, 'changed upstream manifest');
 return JSON.parse(bytes);
}

function admitProvider(repo, provider) {
 const manifest = readRuntimeManifest(repo);
 assert.equal(manifest.profile, 'darwin-arm64');
 assert.equal(manifest.renovateVersion, '44.145.1');
 assert.deepEqual(readdirSync(canonicalRoot(provider)).sort(byPath), ['go', 'node', 'packages']);
 const identities = {};
 for (const name of ['node', 'go', 'packages']) identities[name] = admitTree(checkedPath(provider, name, 'directory'), manifest[name].inventory);
 rejectUpwardPackages(join(provider, 'packages'));
 assert.equal(process.execPath, checkedPath(provider, 'node/bin/node'), 'launch with the admitted absolute Node');
 checkedPath(provider, 'node/lib/node_modules/npm/bin/npm-cli.js');
 checkedPath(provider, 'go/bin/go');
 for (const name of ['node/bin', 'go/bin']) assert.equal(lstatSync(checkedPath(provider, name, 'directory')).mode & 0o222, 0, 'seal executable directories');
 return identities;
}

function validateArchive(repo, fixture, run) {
 // Inspect tracked paths and actual tar members/types before extraction.
 const safeGit = ['-c', 'core.fsmonitor=false', '-c', 'core.hooksPath=/dev/null', '-c', 'maintenance.auto=false', '-c', 'tar.umask=0022'];
 const tracked = run('git', [...safeGit, 'ls-tree', '-rz', 'HEAD'], repo).toString().split('\0').filter(Boolean);
 for (const row of tracked) {
  const [metadata, path] = row.split('\t');
  assert.match(metadata, /^100(?:644|755) blob [0-9a-f]+$/);
  relativeLeaf(path); assert.ok(!path.split('/').includes('.npmrc'), 'archived npm configuration is forbidden');
 }
 const archive = checkedPath(fixture, 'base.tar', 'file', true);
 run('git', [...safeGit, 'archive', 'HEAD', '-o', archive], repo);
 const names = run('tar', ['-tf', checkedPath(fixture, 'base.tar')], fixture).toString().trimEnd().split('\n');
 assert.equal(new Set(names).size, names.length, 'duplicate archive members');
 for (const name of names) relativeLeaf(name.replace(/\/$/, ''));
 const types = run('tar', ['-tvf', checkedPath(fixture, 'base.tar')], fixture).toString().trimEnd().split('\n');
 assert.equal(types.length, names.length);
 assert.ok(types.every((line) => line.startsWith('-') || line.startsWith('d')), 'archive links/special entries are forbidden');
 run('tar', ['-xf', checkedPath(fixture, 'base.tar'), '-C', fixture], fixture);
 rmSync(checkedPath(fixture, 'base.tar'));
}

async function runContract(repo, renovate, fixture, run) {
 assert.equal(JSON.parse(readChecked(renovate, 'package.json')).version, '44.145.1');
 const load = (module) => loadRenovateModule(renovate, module);
const { extractPackageFile: extractRegex } = await load('modules/manager/custom/regex/index.js');
const { extractPackageFile: extractNpm } = await load('modules/manager/npm/extract/index.js');
const { updateDependency } = await load('modules/manager/npm/update/dependency/index.js');
const { doAutoReplace } = await load('workers/repository/update/branch/auto-replace.js');
const { normalizeDepNames } = await load('workers/repository/extract/manager-files.js');
const { branchifyUpgrades } = await load('workers/repository/updates/branchify.js');
const { getConfig } = await load('config/defaults.js');
const { GlobalConfig } = await load('config/global.js');
const { matchRegexOrGlobList } = await load('util/string-match.js');
const config = JSON.parse(readChecked(repo, 'renovate.json'));
const source = readChecked(repo, sourcePath);
const independent = config.customManagers.filter((m) => matchRegexOrGlobList(sourcePath, m.managerFilePatterns));
assert.equal(independent.length, 1, 'exactly one manager selects the independent test');
const extracted = extractRegex(source, sourcePath, independent[0]);
assert.equal(extracted.deps.length, 1);
assert.equal(extracted.deps[0].depName, '@vscode/vsce');
assert.equal(extracted.deps[0].datasource, 'npm');
assert.equal(extracted.deps[0].currentValue, '4.0.0');
for (const path of ['scripts/other_test.go', 'scripts/testdata/release_workflow_config_test.go', `${sourcePath}.bak`]) {
 assert.equal(matchRegexOrGlobList(path, independent[0].managerFilePatterns), false, path);
}
const unrelated = '\nconst unrelatedVersion = "4.0.0"\nconst expectedMarketplaceVSCEVersionFixture = "4.0.0"\n// const expectedMarketplaceVSCEVersion = "4.0.0"\n';
assert.equal(extractRegex(source + unrelated, sourcePath, independent[0]).deps.length, 1);
const evidence = { renovateVersion: '44.145.1', extraction: extracted.deps, scenarios: [], mutationOrder: [] };
 validateArchive(repo, fixture, run);
 for (const path of ['renovate.json', sourcePath, 'scripts/renovate_vsce_test.go']) {
  copyChecked(repo, path, fixture, path);
 }
 const read = (path) => readChecked(fixture, path);
 const write = (path, value) => writeChecked(fixture, path, value);
 async function runScenario(oldVersion, newVersion, updateType) {
  write(manifestPath, readChecked(repo, manifestPath));
  write(lockPath, readChecked(repo, lockPath));
  run('npm', ['install', '--package-lock-only', '--ignore-scripts', '--no-audit', '--no-fund', '--save-dev', '--save-exact', `@vscode/vsce@${oldVersion}`], checkedPath(fixture, 'extensions/vscode-lopper', 'directory'));
  write(sourcePath, source.replace('const expectedMarketplaceVSCEVersion = "4.0.0"', `const expectedMarketplaceVSCEVersion = "${oldVersion}"`) + unrelated);
  write(workflowPath, readChecked(repo, workflowPath).replaceAll('4.0.0', oldVersion));
  GlobalConfig.set({ localDir: fixture, cacheDir: join(fixture, '.cache') });
  const npmFile = await extractNpm(read(manifestPath), manifestPath, {});
  assert.equal(npmFile.managerData.npmLock, lockPath, 'npm manager associates the manifest with its lockfile');
  npmFile.deps = npmFile.deps.filter((d) => d.depName === '@vscode/vsce');
  assert.equal(npmFile.deps.length, 1);
  npmFile.packageFile = manifestPath;
  const regexFiles = [];
  for (const manager of config.customManagers.filter((m) => m.depNameTemplate === '@vscode/vsce')) {
   for (const path of [sourcePath, workflowPath]) {
    if (!matchRegexOrGlobList(path, manager.managerFilePatterns)) continue;
    const file = extractRegex(read(path), path, manager);
    file.packageFile = path;
    regexFiles.push(file);
   }
  }
  assert.equal(regexFiles.find((f) => f.packageFile === sourcePath).deps.length, 1);
  assert.equal(regexFiles.find((f) => f.packageFile === workflowPath).deps.length, 4);
  for (const file of [npmFile, ...regexFiles]) {
   for (const dep of file.deps) {
    normalizeDepNames(dep);
    assert.equal(dep.currentValue, oldVersion);
    dep.updates = [{ newValue: newVersion, newVersion, newMajor: Number(newVersion.split('.')[0]), updateType }];
   }
  }
  const defaults = getConfig();
  const result = await branchifyUpgrades({ ...defaults, ...config, semanticCommits: 'disabled', errors: [], warnings: [], repoIsOnboarded: true }, { npm: [npmFile], regex: regexFiles });
  assert.equal(result.branches.length, 1, `${updateType}: manifest, workflow and test belong to one branch/PR`);
  const branch = result.branches[0];
  assert.equal(branch.automerge, false);
  assert.match(branch.branchName, /vsce-tooling/, 'real branch generation uses the VSCE tooling group');
  assertUpgradeMembership(branch.upgrades);
  await applySerial(branch.upgrades, async (upgrade) => {
   const content = read(upgrade.packageFile);
   const updated = upgrade.manager === 'npm'
    ? updateDependency({ fileContent: content, packageFile: upgrade.packageFile, upgrade })
    : await doAutoReplace(upgrade, content, false);
   assert.ok(updated && updated !== content, `real Renovate update changes ${upgrade.packageFile}`);
   write(upgrade.packageFile, updated);
   evidence.mutationOrder.push({ updateType, packageFile: upgrade.packageFile, before: sha256(content), after: sha256(updated) });
  });
  assert.ok(read(sourcePath).endsWith(unrelated), 'unrelated constants and fixtures remain unchanged');
  run('npm', ['install', '--package-lock-only', '--ignore-scripts', '--no-audit', '--no-fund'], checkedPath(fixture, 'extensions/vscode-lopper', 'directory'));
  const lock = JSON.parse(read(lockPath));
  assert.equal(JSON.parse(read(manifestPath)).devDependencies['@vscode/vsce'], newVersion);
  assert.equal(lock.packages[''].devDependencies['@vscode/vsce'], newVersion);
  assert.equal(lock.packages['node_modules/@vscode/vsce'].version, newVersion);
  assert.match(lock.packages['node_modules/@vscode/vsce'].integrity, /^sha512-.+/);
  // Run the actual Marketplace contract on the regenerated fixture, then corrupt it.
  const go = () => run('go', ['test', './scripts', '-run', '^TestReleaseWorkflowPreparesIntegrityBoundMarketplaceTooling$', '-count=1'], fixture).toString();
  assert.match(go(), /ok\s/);
  const validLock = read(lockPath);
  for (const [name, value] of [['mismatch', '999.0.0'], ['missing integrity', ''], ['wrong integrity prefix', 'sha256-invalid'], ['empty SHA-512 digest', 'sha512-']]) {
   const invalid = JSON.parse(validLock);
   invalid.packages['node_modules/@vscode/vsce'][name === 'mismatch' ? 'version' : 'integrity'] = value;
   write(lockPath, JSON.stringify(invalid));
   assert.throws(go, (err) => err.status === 1 && err.stdout.toString().includes('locked Marketplace tool'), name);
  }
  write(lockPath, validLock);
  evidence.scenarios.push({ oldVersion, newVersion, updateType, branchName: branch.branchName, automerge: branch.automerge, updatedFiles: [manifestPath, lockPath, sourcePath, workflowPath], marketplaceContract: 'pass', mismatchAndIntegrity: 'four behavioral failures' });
 }
 await runScenario('3.9.1', '3.9.2', 'patch');
 await runScenario('3.9.2', '4.0.0', 'major');
 return evidence;
}

export async function main() {
 requireProfile(process.platform, process.arch);
 const modulePath = fileURLToPath(import.meta.url);
 const suffix = '/scripts/testdata/renovate/vsce-contract.mjs';
 assert.ok(modulePath.endsWith(suffix), 'fixture must occupy its reviewed repository suffix');
 const repo = modulePath.slice(0, -suffix.length);
 canonicalRoot(repo);
 assert.equal(checkedPath(repo, 'scripts/testdata/renovate/vsce-contract.mjs'), modulePath);
 const provider = join(repo, '.artifacts/renovate-vsce-runtime');
 const renovate = join(provider, 'packages/node_modules/renovate');
 assert.ok(process.argv.length <= 4, 'only repository and Renovate comparison selectors are accepted');
 compareSelector(process.argv[2], repo);
 compareSelector(process.argv[3], renovate);
 // The outer runner authenticates source and sanitizes Node startup before this entry executes.
 assert.equal(process.env.NODE_OPTIONS, undefined, 'sanitize Node startup externally');
 assert.equal(process.env.NODE_PATH, undefined, 'sanitize Node search externally');
 const identities = admitProvider(repo, provider);
 admitOSCapabilities();
 let failure;
 try {
  return await withFixture('/private/tmp', async (fixture) => {
   writeChecked(fixture, '.home/.npmrc', '');
   writeChecked(fixture, '.home/empty-global-npmrc', '');
   const env = fixtureEnvironment(provider, fixture);
   const run = (name, args, cwd) => {
    canonicalRoot(cwd);
    const command = commandFor(provider, name, args, cwd, env);
    return execFileSync(command.executable, command.args, command.options);
   };
   return withEnvironment(env, () => runContract(repo, renovate, fixture, run));
  });
 } catch (error) { failure = error; throw error; } finally {
  try { assert.deepEqual(admitProvider(repo, provider), identities, 'runtime filesystem identities changed during consumption'); } catch (error) {
   if (failure) throw new AggregateError([failure, error], 'fixture failed and runtime custody changed');
   throw error;
  }
 }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
 console.log(JSON.stringify(await main(), null, 2));
}
