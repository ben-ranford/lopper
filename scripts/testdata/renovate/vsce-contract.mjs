// Run with the external, pinned Renovate package; no repository dependency is added.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const repo = resolve(process.argv[2] ?? '.');
const renovate = resolve(process.argv[3]);
assert.equal(JSON.parse(readFileSync(join(renovate, 'package.json'))).version, '44.145.1');
const load = (path) => import(pathToFileURL(join(renovate, 'dist', path)));
const { extractPackageFile: extractRegex } = await load('modules/manager/custom/regex/index.js');
const { extractPackageFile: extractNpm } = await load('modules/manager/npm/extract/index.js');
const { updateDependency } = await load('modules/manager/npm/update/dependency/index.js');
const { doAutoReplace } = await load('workers/repository/update/branch/auto-replace.js');
const { normalizeDepNames } = await load('workers/repository/extract/manager-files.js');
const { branchifyUpgrades } = await load('workers/repository/updates/branchify.js');
const { getConfig } = await load('config/defaults.js');
const { GlobalConfig } = await load('config/global.js');
const { matchRegexOrGlobList } = await load('util/string-match.js');
const config = JSON.parse(readFileSync(join(repo, 'renovate.json')));
const sourcePath = 'scripts/release_workflow_config_test.go';
const manifestPath = 'extensions/vscode-lopper/package.json';
const lockPath = 'extensions/vscode-lopper/package-lock.json';
const workflowPath = '.github/workflows/release.yml';
const source = readFileSync(join(repo, sourcePath), 'utf8');
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
const evidence = { renovateVersion: '44.145.1', extraction: extracted.deps, scenarios: [] };
const fixture = mkdtempSync(join(tmpdir(), 'lopper-vsce-contract-'));
try {
 execFileSync('git', ['archive', 'HEAD', '-o', join(fixture, 'base.tar')], { cwd: repo });
 execFileSync('tar', ['-xf', join(fixture, 'base.tar'), '-C', fixture]);
 rmSync(join(fixture, 'base.tar'));
 for (const path of ['renovate.json', sourcePath, 'scripts/renovate_vsce_test.go']) {
  cpSync(join(repo, path), join(fixture, path));
 }
 const read = (path) => readFileSync(join(fixture, path), 'utf8');
 const write = (path, value) => { mkdirSync(dirname(join(fixture, path)), { recursive: true }); writeFileSync(join(fixture, path), value); };
 for (const [oldVersion, newVersion, updateType] of [['3.9.1', '3.9.2', 'patch'], ['3.9.2', '4.0.0', 'major']]) {
  write(manifestPath, readFileSync(join(repo, manifestPath), 'utf8'));
  write(lockPath, readFileSync(join(repo, lockPath), 'utf8'));
  execFileSync('npm', ['install', '--package-lock-only', '--ignore-scripts', '--no-audit', '--no-fund', '--save-dev', '--save-exact', `@vscode/vsce@${oldVersion}`], { cwd: join(fixture, 'extensions/vscode-lopper'), stdio: 'pipe' });
  write(sourcePath, source.replace('const expectedMarketplaceVSCEVersion = "4.0.0"', `const expectedMarketplaceVSCEVersion = "${oldVersion}"`) + unrelated);
  write(workflowPath, readFileSync(join(repo, workflowPath), 'utf8').replaceAll('4.0.0', oldVersion));
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
  assert.equal(branch.upgrades.length, 6);
  assert.equal(branch.automerge, false);
  assert.match(branch.branchName, /vsce-tooling/, 'real branch generation uses the VSCE tooling group');
  assert.deepEqual([...new Set(branch.upgrades.map((u) => u.packageFile))].sort(), [manifestPath, sourcePath, workflowPath].sort());
  for (const upgrade of branch.upgrades) {
   const content = read(upgrade.packageFile);
   const updated = upgrade.manager === 'npm'
    ? updateDependency({ fileContent: content, packageFile: upgrade.packageFile, upgrade })
    : await doAutoReplace(upgrade, content, false);
   assert.ok(updated && updated !== content, `real Renovate update changes ${upgrade.packageFile}`);
   write(upgrade.packageFile, updated);
  }
  assert.ok(read(sourcePath).endsWith(unrelated), 'unrelated constants and fixtures remain unchanged');
  execFileSync('npm', ['install', '--package-lock-only', '--ignore-scripts', '--no-audit', '--no-fund'], { cwd: join(fixture, 'extensions/vscode-lopper'), stdio: 'pipe' });
  const lock = JSON.parse(read(lockPath));
  assert.equal(JSON.parse(read(manifestPath)).devDependencies['@vscode/vsce'], newVersion);
  assert.equal(lock.packages[''].devDependencies['@vscode/vsce'], newVersion);
  assert.equal(lock.packages['node_modules/@vscode/vsce'].version, newVersion);
  assert.match(lock.packages['node_modules/@vscode/vsce'].integrity, /^sha512-.+/);
  // Run the actual Marketplace contract on the regenerated fixture, then corrupt it.
  const go = () => execFileSync('go', ['test', './scripts', '-run', '^TestReleaseWorkflowPreparesIntegrityBoundMarketplaceTooling$', '-count=1'], { cwd: fixture, stdio: 'pipe' }).toString();
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
 console.log(JSON.stringify(evidence, null, 2));
} finally {
 rmSync(fixture, { recursive: true, force: true });
}
