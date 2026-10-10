// Offline policy proof using the official pinned engine; no package installs here.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';
const repo = resolve(process.argv[2] ?? '.');
const engine = resolve(process.argv[3]);
const version = JSON.parse(readFileSync(join(engine, 'package.json'))).version;
assert.ok(['44.134.1', '44.145.1'].includes(version));
const load = (p) => import(pathToFileURL(join(engine, 'dist', p)));
const { getConfig } = await load('config/defaults.js');
const { branchifyUpgrades } = await load('workers/repository/updates/branchify.js');
const { applyPackageRules } = await load('util/package-rules/index.js');
const { compile } = await load('util/template/index.js');
const { mergeChildConfig } = await load('config/utils.js');
const config = JSON.parse(readFileSync(join(repo, 'renovate.json')));
assert.equal(config.semanticCommits, 'enabled');
const applied = async (dep) => applyPackageRules({ versioning: 'semver', ...dep, packageRules: config.packageRules }, 'explicit-policy');
const match = async (dep, key, want) => assert.deepEqual((await applied(dep))[key], want, JSON.stringify(dep));
await match({manager:'npm',depName:'@types/node',packageName:'@types/node'}, 'versioning', 'node');
await match({manager:'npm',depName:'unrelated',packageName:'unrelated'}, 'groupName', undefined);
await match({manager:'github-actions',packageName:'actions/attest-build-provenance',datasource:'github-tags',currentVersion:'4.0.0'}, 'replacementName', 'actions/attest');
for (const dep of [
 {manager:'github-actions',packageName:'actions/attest-build-provenance',datasource:'github-tags',currentVersion:'3.9.0'},
 {manager:'github-actions',packageName:'actions/attest-build-provenance',datasource:'npm',currentVersion:'4.0.0'},
 {manager:'github-actions',packageName:'unrelated',datasource:'github-tags',currentVersion:'4.0.0'},
]) await match(dep,'replacementName',undefined);
await match({packageName:'google-github-actions/release-please-action',datasource:'github-tags'},'replacementName','googleapis/release-please-action');
for (const dep of [{packageName:'google-github-actions/release-please-action',datasource:'npm'}, {packageName:'googleapis/release-please-action',datasource:'github-tags'}]) await match(dep,'replacementName',undefined);
for (const datasource of ['github-digest','github-releases','github-tags','git-refs','git-tags']) {
 const dep = {datasource,updateType:'digest',sourceUrl:'https://github.com/example/repo',currentDigest:'old',newDigest:'new'};
 const result = await applied(dep);
 assert.equal(compile(result.changelogUrl,dep),'https://github.com/example/repo/compare/old..new');
}
for (const dep of [{datasource:'git-tags',updateType:'digest',sourceUrl:'https://gitlab.com/example/repo'}, {datasource:'github-tags',updateType:'pinDigest',sourceUrl:'https://github.com/example/repo'}]) await match(dep,'changelogUrl',undefined);
for (const depName of ['golang.org/x/mod','example.org/other']) {
 const dep = {manager:'gomod',depName,updateType:'minor',displayFrom:'v1.0.0',displayTo:'v1.1.0',currentValue:'v1.0.0',newValue:'v1.1.0',depNameLinked:'[other](https://example.org/other)'};
 const rules = await applied(dep);
 assert.equal(compile(rules.prBodyDefinitions.Package,dep),depName.startsWith('golang.org/x/') ? `[${depName}](https://pkg.go.dev/${depName})` : dep.depNameLinked);
 assert.equal(compile(rules.prBodyDefinitions.Change,dep),depName.startsWith('golang.org/x/') ? '[`v1.0.0` → `v1.1.0`](https://cs.opensource.google/go/x/mod/+/refs/tags/v1.0.0...refs/tags/v1.1.0)' : '`v1.0.0` → `v1.1.0`');
}
const file = (manager, name, updateType, depType, datasource='npm') => ({ packageFile:`${manager}.json`,deps:[{depName:name,packageName:name,depType,datasource,currentValue:'1.0.0',currentVersion:'1.0.0',updates:[{newValue:'2.0.0',newVersion:'2.0.0',newMajor:2,updateType}]}] });
const branches = [];
for (const [label, files, group, title] of [
 ['VSCE major',{npm:[file('npm','@vscode/vsce','major','devDependencies')],regex:[file('regex','@vscode/vsce','major')]},'VSCE tooling',/^chore\(deps\)/],
 ['Artifact major',{'github-actions':[file('github-actions','actions/upload-artifact','major',undefined,'github-tags'),file('github-actions','actions/download-artifact','major',undefined,'github-tags')]},'GitHub Artifact Actions',/^chore\(deps\)/],
 ['Go special',{gomod:[file('gomod','go','minor','golang','golang-version')],regex:[file('regex','go','minor',undefined,'golang-version')]},'Go toolchain',/^chore\(deps\)/],
 ['Go runtime',{gomod:[file('gomod','golang.org/x/mod','minor','require','go')]},undefined,/^fix\(deps\)/],
 ['npm runtime',{npm:[file('npm','unrelated','minor','dependencies')]},undefined,/^fix\(deps\)/],
 ['Node types',{npm:[file('npm','@types/node','major','devDependencies')]},undefined,/^chore\(deps\)/],
]) {
 const result = await branchifyUpgrades({...getConfig(),...config,errors:[],warnings:[],repoIsOnboarded:true},files);
 assert.equal(result.branches.length,1,label);
 assert.equal(result.branches[0].upgrades.length,Object.values(files).flat().reduce((count, packageFile) => count + packageFile.deps.length, 0),label);
 const branch=result.branches[0];
 if (group) assert.ok(branch.branchName.endsWith(group.toLowerCase().replaceAll(' ', '-')), label);
 else assert.equal(branch.upgrades.length, 1, label);
 assert.match(branch.prTitle,title,label);
 assert.equal(branch.automerge,false,label);
 assert.equal(branch.platformAutomerge,false,label);
 branches.push({label,name:branch.branchName,title:branch.prTitle,group:branch.upgrades[0].groupName,automerge:branch.automerge,platformAutomerge:branch.platformAutomerge});
}
for (const updateType of ['minor','patch']) await match({manager:'github-actions',packageName:'actions/upload-artifact',updateType},'groupName',undefined);
assert.equal(mergeChildConfig({automerge:false},{force:{automerge:true}}).automerge,true,'external force exceeds local policy boundary');
const digestFile = file('github-actions', 'actions/checkout', 'digest', undefined, 'github-tags');
Object.assign(digestFile.deps[0], {sourceUrl:'https://github.com/actions/checkout', currentDigest:'old'});
Object.assign(digestFile.deps[0].updates[0], {newDigest:'new',newValue:'1.0.0',newVersion:'1.0.0'});
const digestResult = await branchifyUpgrades({...getConfig(),...config,errors:[],warnings:[],repoIsOnboarded:true},{'github-actions':[digestFile]});
assert.equal(digestResult.branches.length,1);
assert.equal(digestResult.branches[0].automerge,false);
assert.equal(digestResult.branches[0].platformAutomerge,false);
assert.equal(compile(digestResult.branches[0].upgrades[0].changelogUrl,digestResult.branches[0].upgrades[0]),'https://github.com/actions/checkout/compare/old..new');
console.log(JSON.stringify({version,branches,matchingAndTemplates:'pass',digestBranch:'pass',externalForceCounterexample:true},null,2));
