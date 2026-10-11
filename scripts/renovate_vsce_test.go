package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestRenovateTracksIndependentMarketplaceExpectation(t *testing.T) {
	t.Parallel()
	var config struct {
		CustomManagers []struct {
			ManagerFilePatterns []string `json:"managerFilePatterns"`
			MatchStrings        []string `json:"matchStrings"`
			CustomType          string   `json:"customType"`
			DatasourceTemplate  string   `json:"datasourceTemplate"`
			DepNameTemplate     string   `json:"depNameTemplate"`
			VersioningTemplate  string   `json:"versioningTemplate"`
		} `json:"customManagers"`
	}
	readJSONConfig(t, "renovate.json", &config)
	for _, manager := range config.CustomManagers {
		if !slices.Contains(manager.ManagerFilePatterns, `/^scripts/release_workflow_config_test\.go$/`) {
			continue
		}
		if len(manager.ManagerFilePatterns) != 1 || len(manager.MatchStrings) != 1 || manager.CustomType != "regex" || manager.DatasourceTemplate != "npm" || manager.DepNameTemplate != "@vscode/vsce" || manager.VersioningTemplate != "npm" {
			t.Fatal("Marketplace expectation must use one narrowly scoped npm regex manager")
		}
		re := regexp.MustCompile(manager.MatchStrings[0])
		source := readConfig(t, "scripts/release_workflow_config_test.go")
		matches := re.FindAllStringSubmatch(source, -1)
		if len(matches) != 1 || re.SubexpIndex("currentValue") < 0 {
			t.Fatalf("Marketplace expectation matches = %v, want exactly one currentValue", matches)
		}
		for _, unrelated := range []string{
			`const unrelatedVersion = "4.0.0"`,
			`const expectedMarketplaceVSCEVersionFixture = "4.0.0"`,
			`// const expectedMarketplaceVSCEVersion = "4.0.0"`,
			`const expectedMarketplaceVSCEVersion = "4.0.0-invalid"`,
		} {
			if re.MatchString(unrelated) {
				t.Fatalf("Marketplace matcher selected unrelated assertion %q", unrelated)
			}
		}
		return
	}
	t.Fatal("Renovate does not track the independent Marketplace version expectation")
}

func TestMarketplaceToolingPinRejectsMismatchAndInvalidIntegrity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, version, integrity string
		valid                    bool
	}{
		{"coordinated current pin", expectedMarketplaceVSCEVersion, "sha512-bound", true},
		{"mismatched version", "999.0.0", "sha512-bound", false},
		{"missing integrity", expectedMarketplaceVSCEVersion, "", false},
		{"wrong algorithm", expectedMarketplaceVSCEVersion, "sha256-bound", false},
		{"empty digest", expectedMarketplaceVSCEVersion, "sha512-", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMarketplaceToolingPin(tc.version, tc.integrity, expectedMarketplaceVSCEVersion)
			if (err == nil) != tc.valid {
				t.Fatalf("pin validation = %v, valid = %v", err, tc.valid)
			}
		})
	}
}

const renovateFixtureControls = `
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
const f = await import(pathToFileURL(process.argv[2]));
const mode = process.argv[1];
const root = fs.realpathSync(process.argv[3]);
const windows = process.platform === 'win32';
const directoryLink = (target, link) => fs.symlinkSync(target, link, windows ? 'junction' : 'dir');
const write = (name, text) => { fs.mkdirSync(path.dirname(name), { recursive: true }); fs.writeFileSync(name, text); };
if (mode === 'boundaries') {
 f.requireProfile('darwin', 'arm64');
 assert.throws(() => f.requireProfile('linux', 'arm64'), /only Darwin arm64/);
 assert.throws(() => f.requireProfile('darwin', 'x64'), /only Darwin arm64/);
 const inside = path.join(root, 'inside'); const foreign = path.join(root, 'inside-other');
 fs.mkdirSync(inside); fs.mkdirSync(foreign);
 write(path.join(inside, 'file'), 'valid'); write(path.join(foreign, 'file'), 'foreign');
 assert.equal(f.canonicalRoot(inside), inside);
 f.compareSelector(inside, inside); f.compareSelector('.', inside, inside);
 for (const value of [inside + path.sep + '.', inside + path.sep + '..' + path.sep + path.basename(inside), inside + path.sep]) assert.throws(() => f.canonicalRoot(value));
 for (const value of [foreign, inside + '/.', inside + '//', inside + '/../inside', inside + '\\0', 'C:\\x']) assert.throws(() => f.compareSelector(value, inside));
 for (const value of ['../inside-other/file', '/etc/passwd', 'a/../file', 'C:/file', 'file\\0']) assert.throws(() => f.readChecked(inside, value));
 assert.equal(f.readChecked(inside, 'file'), 'valid');
 if (windows) directoryLink(foreign, path.join(inside, 'linked'));
 else fs.symlinkSync(path.join(foreign, 'file'), path.join(inside, 'linked'));
 const linkedLeaf = windows ? 'linked/file' : 'linked';
 assert.throws(() => f.readChecked(inside, linkedLeaf));
 assert.throws(() => f.copyChecked(inside, linkedLeaf, inside, 'out/file'));
 assert.equal(fs.existsSync(path.join(inside, 'out')), false);
 directoryLink(foreign, path.join(inside, 'parent'));
 assert.throws(() => f.canonicalRoot(path.join(inside, 'parent')));
 assert.throws(() => f.writeChecked(inside, 'parent/new', 'bad'));
 assert.equal(fs.existsSync(path.join(foreign, 'new')), false);
 fs.unlinkSync(path.join(inside, 'linked')); fs.unlinkSync(path.join(inside, 'parent'));
 f.copyChecked(inside, 'file', inside, 'out/file'); assert.equal(f.readChecked(inside, 'out/file'), 'valid');
 // Explicitly synthetic upstream tree; never real Renovate evidence.
 const pkg = path.join(root, 'packages'); fs.mkdirSync(pkg);
 write(path.join(pkg, 'package.json'), '{"name":"renovate","version":"44.145.1"}');
 write(path.join(pkg, 'dist', 'main.js'), 'export const value = 1;');
 write(path.join(pkg, 'node_modules', 'transitive', 'index.js'), 'export const value = 2;');
 const expected = f.treeInventory(pkg); f.admitTree(pkg, expected);
 for (const leaf of ['package.json', 'dist/main.js', 'node_modules/transitive/index.js']) {
  const original = fs.readFileSync(path.join(pkg, leaf));
  write(path.join(pkg, leaf), 'same-version fake content'); assert.throws(() => f.admitTree(pkg, expected));
  fs.writeFileSync(path.join(pkg, leaf), original);
 }
 write(path.join(pkg, 'extra.js'), 'extra'); assert.throws(() => f.admitTree(pkg, expected)); fs.unlinkSync(path.join(pkg, 'extra.js'));
 f.rejectUpwardPackages(pkg); fs.mkdirSync(path.join(root, 'node_modules')); assert.throws(() => f.rejectUpwardPackages(pkg)); fs.rmdirSync(path.join(root, 'node_modules'));
 directoryLink(path.join(root, 'absent'), path.join(root, 'node_modules')); assert.throws(() => f.rejectUpwardPackages(pkg)); fs.unlinkSync(path.join(root, 'node_modules'));
 const marker = path.join(root, 'attacker'); const attack = path.join(root, 'attack', 'attack.mjs');
 write(attack, 'import fs from "node:fs"; fs.writeFileSync(' + JSON.stringify(marker) + ', "executed");');
 const evil = path.join(pkg, windows ? 'evil/attack.mjs' : 'evil.mjs');
 if (windows) directoryLink(path.dirname(attack), path.join(pkg, 'evil'));
 else fs.symlinkSync(attack, evil);
 await assert.rejects(async () => { f.admitTree(pkg, expected); await import(pathToFileURL(evil)); });
 assert.equal(fs.existsSync(marker), false);
 // Joined removed-admission control: attacker code executes when the guard is removed.
 await import(pathToFileURL(evil)); assert.equal(fs.readFileSync(marker, 'utf8'), 'executed');
 assert.throws(() => f.readRuntimeManifest(root), /ENOENT/);
 write(path.join(root, 'scripts/testdata/renovate/runtime-integrity.json'), '{}');
 assert.throws(() => f.readRuntimeManifest(root), /changed upstream manifest/);
 await assert.rejects(() => f.loadRenovateModule(pkg, '../attack.mjs'));
} else if (mode === 'commands') {
 const poison = path.join(root, 'poison'); const marker = path.join(root, 'poison-executed'); fs.mkdirSync(poison);
 const poisonedNpm = path.join(poison, windows ? 'npm.exe' : 'npm');
 if (windows) fs.copyFileSync(process.execPath, poisonedNpm, fs.constants.COPYFILE_FICLONE);
 else {
  const quote = (value) => "'" + value.replace(/'/g, "'\\''") + "'";
  fs.writeFileSync(poisonedNpm, '#!/bin/sh\nexec ' + quote(process.execPath) + ' "$@"\n', { mode: 0o755 });
 }
 fs.chmodSync(poisonedNpm, 0o755);
 for (const name of ['git', 'tar', 'node', 'go']) fs.linkSync(poisonedNpm, path.join(poison, windows ? name + '.exe' : name));
 const sentinel = 'import fs from "node:fs"; fs.writeFileSync(' + JSON.stringify(marker) + ', "executed\\n");';
 const original = { ...process.env };
 const systemRoot = windows ? process.env.SystemRoot : undefined;
 Object.assign(process.env, { PATH: poison + path.delimiter, NODE_OPTIONS: '--require=evil', NODE_PATH: poison, GIT_CONFIG_COUNT: '99', npm_config_registry: 'http://evil', GOFLAGS: '-toolexec=evil', GOTOOLCHAIN: 'evil', DYLD_INSERT_LIBRARIES: 'evil', BASH_ENV: 'evil' });
 const provider = path.join(root, 'provider'); const env = f.fixtureEnvironment(provider, root);
 const systemPaths = windows ? [path.join(systemRoot, 'System32'), systemRoot] : ['/usr/bin','/bin','/usr/sbin','/sbin'];
 assert.equal(env.PATH, [path.join(provider, 'node/bin'), path.join(provider, 'go/bin'), ...systemPaths].join(path.delimiter));
 if (windows) assert.equal(env.SystemRoot, systemRoot);
 for (const key of ['NODE_OPTIONS','NODE_PATH','GIT_CONFIG_COUNT','DYLD_INSERT_LIBRARIES','BASH_ENV']) assert.equal(env[key], undefined);
 assert.equal(env.GOFLAGS, ''); assert.equal(env.GOTOOLCHAIN, 'local');
 const argv = ['install', '--package-lock-only', '--ignore-scripts', '--no-audit', '--no-fund'];
 const npm = f.commandFor(provider, 'npm', argv, root, env);
 assert.equal(npm.executable, path.join(provider, 'node/bin/node')); assert.deepEqual(npm.args, [path.join(provider, 'node/lib/node_modules/npm/bin/npm-cli.js'), ...argv]);
 assert.equal(npm.options.cwd, root); assert.equal(npm.options.shell, false); assert.equal(npm.options.env, env);
 for (const [name, executable] of [['git','/Library/Developer/CommandLineTools/usr/bin/git'],['tar','/usr/bin/bsdtar'],['go',path.join(provider, 'go/bin/go')]]) assert.equal(f.commandFor(provider,name,[],root,env).executable,executable);
 assert.throws(() => f.commandFor(provider, 'unknown', [], root, env));
 await assert.rejects(() => f.withEnvironment(env, async () => { assert.equal(process.env.NODE_OPTIONS, undefined); assert.equal(process.env.PATH, env.PATH); throw new Error('stop'); }), /stop/);
 assert.equal(process.env.NODE_OPTIONS, '--require=evil');
 // Actual child uses a trusted absolute executable + sanitized startup environment.
 execFileSync(process.execPath, ['--input-type=module', '-e', 'import assert from "node:assert/strict"; assert.equal(process.env.NODE_OPTIONS, undefined);'], { env, shell: false });
 assert.equal(fs.existsSync(marker), false);
 // Joined old PATH-selection control executes the sentinel.
 execFileSync(windows ? 'npm.exe' : 'npm', ['--input-type=module', '-e', sentinel], { env: { ...env, PATH: [poison, ...systemPaths].join(path.delimiter) }, shell: false });
 assert.equal(fs.readFileSync(marker, 'utf8'), 'executed\n');
 for (const key of Object.keys(process.env)) delete process.env[key]; Object.assign(process.env, original);
} else if (mode === 'serial') {
 const upgrades = ['manifest','source','workflow','workflow','workflow','workflow'].map((name) => ({ packageFile: ({manifest:'extensions/vscode-lopper/package.json',source:'scripts/release_workflow_config_test.go',workflow:'.github/workflows/release.yml'})[name] }));
 f.assertUpgradeMembership(upgrades);
 for (const bad of [upgrades.slice(1), [...upgrades, upgrades[0]], upgrades.map((u,i) => i === 0 ? {packageFile:'foreign'} : u)]) assert.throws(() => f.assertUpgradeMembership(bad));
 const trace = []; let current = ''; let inflight = 0; let max = 0; let release;
 const held = new Promise((resolve) => { release = resolve; });
 f.writeChecked(root, 'workflow', '');
 const apply = async (upgrade) => { inflight++; max = Math.max(max,inflight); const previous = f.readChecked(root, 'workflow'); trace.push('start:'+upgrade); if (upgrade === 'patch') await held; current = previous + upgrade; f.writeChecked(root, 'workflow', current); trace.push('write:'+current); inflight--; };
 const pending = f.applySerial(['patch','major'], apply);
 await Promise.resolve(); assert.deepEqual(trace, ['start:patch']); release(); await pending;
 assert.equal(current, 'patchmajor'); assert.equal(max, 1);
 // Joined eager-map control reads stale state and overlaps work.
 current = ''; max = 0; trace.length = 0;
 await Promise.all(['patch','major'].map(async (upgrade) => { inflight++; max = Math.max(max,inflight); const previous = current; await Promise.resolve(); current = previous + upgrade; inflight--; }));
 assert.equal(max, 2); assert.notEqual(current, 'patchmajor');
 let fixture; const envBefore = { ...process.env }; let calls = 0;
 await assert.rejects(() => f.withFixture(root, async (owned) => { fixture = owned; write(path.join(owned, '.go-modcache/readonly/file'), 'cached'); fs.chmodSync(path.join(owned, '.go-modcache/readonly'), 0o555); return f.withEnvironment({ONLY:'private'}, () => f.applySerial(['first','reject','never'], async (upgrade) => { calls++; write(path.join(owned,'output'), upgrade); if (upgrade === 'reject') throw new Error('replacement failed'); })); }), /replacement failed/);
 assert.equal(calls, 2); assert.equal(fs.existsSync(fixture), false); assert.deepEqual({...process.env}, envBefore);
 const result = {};
 assert.equal(await f.withFixture(root, async (owned) => { fixture = owned; return result; }), result);
 assert.equal(fs.existsSync(fixture), false);
 const originalFailure = new Error('original operation failure');
 await assert.rejects(() => f.withFixture(root, async () => { throw originalFailure; }), (error) => error === originalFailure);
 await f.withFixture(root, async () => { throw undefined; }).then(() => assert.fail('undefined rejection was lost'), (error) => assert.equal(error, undefined));
 const breakCleanup = (owned) => { fs.rmSync(owned, {recursive:true}); fs.writeFileSync(owned, 'non-directory'); };
 await assert.rejects(() => f.withFixture(root, async (owned) => { breakCleanup(owned); return result; }), (error) => error.code === 'ERR_ASSERTION' && !(error instanceof AggregateError));
 await assert.rejects(() => f.withFixture(root, async (owned) => { breakCleanup(owned); throw originalFailure; }), (error) => {
  assert.ok(error instanceof AggregateError); assert.equal(error.message, 'fixture failed and cleanup failed');
  assert.equal(error.errors.length, 2); assert.equal(error.errors[0], originalFailure); assert.equal(error.errors[1].code, 'ERR_ASSERTION'); return true;
 });
} else { throw new Error('unknown control'); }
console.log('offline control passed: ' + mode);
`

func TestRenovateFixtureOfflineControls(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required for Renovate fixture offline controls")
	}
	module, err := filepath.Abs("testdata/renovate/vsce-contract.mjs")
	if err != nil {
		t.Fatal(err)
	}
	env := renovateFixtureStartupEnvironment(os.Environ(), runtime.GOOS)
	for _, mode := range []string{"boundaries", "commands", "serial"} {
		t.Run(mode, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(node, "--input-type=module", "-e", renovateFixtureControls, mode, module, root)
			command.Env = env
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("%s control: %v\n%s", mode, err, output)
			}
		})
	}
}

func renovateFixtureStartupEnvironment(entries []string, goos string) []string {
	var env []string
	for _, entry := range entries {
		key, _, _ := strings.Cut(entry, "=")
		if goos == "windows" {
			key = strings.ToUpper(key)
		}
		if key == "NODE_OPTIONS" || key == "NODE_PATH" || key == "BASH_ENV" || key == "ENV" || strings.HasPrefix(key, "DYLD_") || strings.HasPrefix(key, "LD_") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func TestRenovateFixtureStartupEnvironmentKeys(t *testing.T) {
	t.Parallel()
	entries := []string{
		"NODE_OPTIONS=unsafe", "NODE_PATH=unsafe", "BASH_ENV=unsafe", "ENV=unsafe", "DYLD_INSERT_LIBRARIES=unsafe", "LD_PRELOAD=unsafe",
		"Node_Options=unsafe", "Node_Path=unsafe", "Bash_Env=unsafe", "Env=unsafe", "Dyld_Insert_Libraries=unsafe", "Ld_Preload=unsafe",
		"Fixture_Mixed=keep=verbatim", "PATH=trusted", "SystemRoot=native",
	}
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			want := entries[6:]
			if goos == "windows" {
				want = entries[12:]
			}
			got := renovateFixtureStartupEnvironment(entries, goos)
			if !slices.Equal(got, want) {
				t.Fatalf("startup environment = %q, want %q", got, want)
			}
		})
	}
}

func TestRenovateFixtureStartupEnvironmentControls(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required for Renovate fixture startup controls")
	}
	for _, key := range []string{"NODE_OPTIONS", "Node_Options", "NODE_PATH", "Node_Path"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "poison-executed")
			module := filepath.Join(root, "startup-poison")
			if err := os.Mkdir(module, 0o700); err != nil {
				t.Fatal(err)
			}
			poison := []byte(`require('node:fs').writeFileSync('poison-executed', 'executed');`)
			if err := os.WriteFile(filepath.Join(module, "index.js"), poison, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "poison.cjs"), poison, 0o600); err != nil {
				t.Fatal(err)
			}
			value := root
			probe := `try { require('startup-poison'); } catch (error) { if (error.code !== 'MODULE_NOT_FOUND') throw error; }`
			if strings.EqualFold(key, "NODE_OPTIONS") {
				value = "--require=./poison.cjs"
				probe = ""
			}
			entries := append(renovateFixtureStartupEnvironment(os.Environ(), "windows"), key+"="+value)
			command := exec.Command(node, "-e", `const assert = require('node:assert/strict'); for (const key of Object.keys(process.env)) assert.ok(!['NODE_OPTIONS', 'NODE_PATH'].includes(key.toUpperCase()), key); `+probe)
			command.Dir = root
			command.Env = renovateFixtureStartupEnvironment(entries, "windows")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("sanitized startup: %v\n%s", err, output)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("sanitized child executed poison or marker check failed: %v", err)
			}
			// Actual bypass child proves the module executes. On Unix, canonicalize
			// only this control's key to model Windows lookup; this is not native Windows evidence.
			unsafeEntries := slices.Clone(entries)
			if runtime.GOOS != "windows" {
				unsafeEntries[len(unsafeEntries)-1] = strings.ToUpper(key) + "=" + value
			}
			command = exec.Command(node, "-e", probe)
			command.Dir = root
			command.Env = unsafeEntries
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("startup bypass control: %v\n%s", err, output)
			}
			if text, err := os.ReadFile(marker); err != nil || string(text) != "executed" {
				t.Fatalf("bypass marker = %q, %v; want executed", text, err)
			}
		})
	}
}

// This protects CI routing and failure criteria; native child execution is separate evidence.
func TestRenovateFixtureWindowsCIQualificationContract(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	const jobName = "regression-proof-windows"
	const qualifyName = "Qualify native Windows Renovate offline controls"
	const uploadName = "Upload native Windows Renovate offline receipts"
	job := workflowJobByName(t, workflow.Jobs, jobName)
	assertRenovateWindowsJob(t, job, jobName, qualifyName, uploadName)
	qualify := workflowStepByName(t, workflow.Jobs, jobName, qualifyName)
	if qualify.If != "" || qualify.ContinueOnError || qualify.Shell != "pwsh" || qualify.ID != "renovate_windows_offline" || len(qualify.Env) != 1 || qualify.Env["QUALIFICATION_SOURCE_SHA"] != "${{ github.event.pull_request.head.sha || github.sha }}" {
		t.Fatal("exact head selection must be unconditional and bound through process environment")
	}
	assertWorkflowStepRunOmitsAll(t, qualify, "native qualification", []string{"${{", "merge-base", "continue-on-error", "npm install", "npm ci", "TestRenovateFixtureWindowsCIQualificationContract", "SilentlyContinue"})
	assertWorkflowStepRunContainsAll(t, qualify, "single native compiler selection", []string{
		"$gccCommands = @(Get-Command gcc -CommandType Application -All -ErrorAction Stop)",
		"$gccCommands.Count -eq 0 -or -not [IO.Path]::IsPathFullyQualified($gccCommands[0].Source)",
		"$gcc = (Resolve-Path -LiteralPath $gccCommands[0].Source -ErrorAction Stop).ProviderPath",
	})
	assertWorkflowStepRunOmitsAll(t, qualify, "no compiler array or fallback", []string{"$gcc = (Get-Command", "$gccCommands[1]", "Select-Object -Last"})
	assertWorkflowStepRunContainsAll(t, qualify, "single native archive-tool selection", []string{
		"function Resolve-NativeArchiveTool", "[ValidateSet('git','tar')][string]$Name",
		"$commands = @(Get-Command $Name -CommandType Application -All -ErrorAction Stop)",
		"$commands.Count -eq 0 -or -not [IO.Path]::IsPathFullyQualified($commands[0].Source)",
		"$resolved = Resolve-Path -LiteralPath $commands[0].Source -ErrorAction Stop", "$executable = $resolved.ProviderPath",
		"$executable -isnot [string]", "-not [IO.Path]::IsPathFullyQualified($executable)", "Test-Path -LiteralPath $executable -PathType Leaf -ErrorAction Stop",
		"$git = Resolve-NativeArchiveTool -Name 'git'", "$tar = Resolve-NativeArchiveTool -Name 'tar'",
	})
	assertWorkflowStepRunContainsAll(t, qualify, "native Git and tar chooser boundary controls", []string{
		"'archive-tool-choice-controls.json'", "'git-ordered-two'", "'tar-ordered-two'", "'git-alias-before-applications'", "'tar-alias-before-applications'",
		"'git-missing'", "'tar-missing'", "'git-relative'", "'tar-relative'", "'git-lookup-error'", "'tar-lookup-error'",
		"'git-canonical-error'", "'tar-canonical-error'", "'git-nonscalar'", "'tar-nonscalar'", "'git-relative-provider'", "'tar-relative-provider'", "'git-nonfile'", "'tar-nonfile'",
		"$Name -cne $case.tool", "Invalid archive-tool discovery invocation", "Wrong archive-tool provider selected", "Canonical native $Name executable file required",
	})
	assertWorkflowStepRunContainsAll(t, qualify, "native chooser boundary controls", []string{
		"'single'", "'ordered-two'", "'duplicates'", "'missing'", "'lookup-error'", "'relative'", "'alias-function-collision'", "'canonical-error'",
		"[System.Management.Automation.CommandTypes]$CommandType", "[System.Management.Automation.ActionPreference]$ErrorAction",
		"$Name -cne 'gcc'", "$CommandType -ne [System.Management.Automation.CommandTypes]::Application", "-or -not $All", "$ErrorAction -ne [System.Management.Automation.ActionPreference]::Stop",
		"$observed.discovery -ne 1 -or $observed.resolution -ne $case.resolves", "$gcc -isnot [string]", "$choiceError -cne $case.error",
		"$chooserEnvironmentAfter.Count -ne $chooserEnvironment.Count", "$chooserEnvironmentAfter[$key] -cne $chooserEnvironment[$key]", "$actualFunction -cne $chooserFunctions[$name]",
		"'gcc-choice-controls.json'", "'gcc-choice-restoration.json'", "nativeExecutableAuthentication = $false",
	})
	assertTextAppearsBefore(t, qualify.Run, "if ($null -ne $chooserFailure) { throw $chooserFailure }", "Set-ProcessEnvironment 'CC' $gcc", "native controls and restoration must precede real compiler use")
	assertTextAppearsBefore(t, qualify.Run, "$gcc = (Resolve-Path", "Set-ProcessEnvironment 'CC' $gcc", "scalar canonical compiler must precede CC")
	assertWorkflowStepRunContainsAll(t, qualify, "native source/runtime/offline custody", []string{
		"$ErrorActionPreference = 'Stop'", "$PSNativeCommandUseErrorActionPreference = $false",
		"if (-not $IsWindows)", "'go version go1.27.2 windows/amd64'", "'bin/go.exe'", "'node/24.21.0/x64/node.exe'", "'v24.21.0'",
		"$hostFields.GOHOSTOS -cne 'windows'", "$hostFields.GOOS -cne 'windows'", "$hostFields.GOHOSTARCH -cne 'amd64'", "$hostFields.GOARCH -cne 'amd64'",
		"$nodeHost.platform -cne 'win32'", "$nodeHost.arch -cne 'x64'", "Set-ProcessEnvironment 'CC' $gcc", "Set-ProcessEnvironment 'CGO_ENABLED' '1'", "'--print-file-name=libsynchronization.a'",
		"[IO.Path]::IsPathFullyQualified($env:SystemRoot)", "$drive.DriveFormat -ne 'NTFS'", "[IO.FileAttributes]::ReparsePoint",
		"$source = $env:QUALIFICATION_SOURCE_SHA", "'^[0-9a-f]{40}$'", "'rev-parse','--verify'", "${source}^{commit}", "-cne $source",
		"'core.autocrlf=false'", "'core.eol=lf'", "'archive','--format=tar'", "--output=$headArchive", "'-xf',$headArchive,'-C',$headRoot",
		"--output=$baseArchive", "'-xf',$baseArchive,'-C',$baseRoot", "'0c753332a26136aef94a78886d096f8dfd85f734'",
		"if ($hash -cne $bindings[$relative])", "Assert-ProviderAbsent $originalLocation.Path", "Assert-ProviderAbsent $root", "'renovate-vsce-runtime'",
		"'renovate.json', 'scripts/release_workflow_config_test.go', 'go.mod', 'go.sum'", "'source.json'", "eventSHA = $env:GITHUB_SHA", "runID = $env:GITHUB_RUN_ID", "runAttempt = $env:GITHUB_RUN_ATTEMPT",
		"headArchiveSHA256", "baseArchiveSHA256", "nodeSHA256", "goSHA256", "workingDirectory = (Get-Location).Path",
		"Require-Zero \"$tree-mod-download\" $go @('mod','download')", "Require-Zero \"$tree-mod-verify\" $go @('mod','verify')",
		"Set-ProcessEnvironment 'GOENV' 'off'", "Set-ProcessEnvironment 'GOWORK' 'off'", "Set-ProcessEnvironment 'GOTOOLCHAIN' 'local'", "Set-ProcessEnvironment 'GOFLAGS' '-buildvcs=false -mod=readonly'",
		"Set-ProcessEnvironment 'GOPROXY' 'off'", "Set-ProcessEnvironment 'GOSUMDB' 'off'", "Set-ProcessEnvironment 'GOVCS' '*:off'", "Set-ProcessEnvironment 'GONOPROXY' 'none'", "Set-ProcessEnvironment 'GONOSUMDB' 'none'", "Set-ProcessEnvironment 'GOPRIVATE' $null",
		"$comparison = $key.ToUpperInvariant()", "'NODE_OPTIONS','NODE_PATH','BASH_ENV','ENV'", "$comparison.StartsWith('LD_')", "$comparison.StartsWith('DYLD_')",
		"1> (Join-Path $receipt \"$name.stdout\") 2> (Join-Path $receipt \"$name.stderr\")", "$status = $LASTEXITCODE", "\"$name.exit\"", "ConvertFrom-Json -ErrorAction Stop",
		"$_.Action -eq 'fail' -or $_.Action -eq 'skip'", "$passes.Count -ne $expected.Count -or $packagePass.Count -ne 1", "$_.Test -ceq $test", "$expected -cnotcontains $pass.Test",
		"$expectedHead.Count -ne 20 -or $expectedBase.Count -ne 19", "@('normal','race')", "'test','./scripts','-json','-count=1','-timeout','2m','-run',$headRegex", "$arguments += '-race'",
		"Set-Location -LiteralPath $headRoot", "Set-Location -LiteralPath $baseRoot", "Copy-Item -LiteralPath (Join-Path $headRoot $relative)", "$baseOverlay = $true",
		"Original base input changed", "Base support changed", "Head input changed", "Assert-Passes 'base-compile' @()",
		"$negativeStatus -ne 1", "$failures.Count -ne 2", "$namedFailure.Count -ne 1", "$packageFailure.Count -ne 1", "$intendedOutput.Count -eq 0", "Renovate does not track the independent Marketplace version expectation",
		"Assert-Passes 'base-portable' $expectedBase", "catch {", "'failure.txt'", "finally {", "$receiptOwned", "$workOwned",
		"[Environment]::SetEnvironmentVariable($key, $originalEnvironment[$key], 'Process')", "Set-Location -LiteralPath $originalLocation.Path", "Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction Stop", "'restoration.json'", "$finalErrors.Count -ne 0",
	})
	for _, relative := range []string{"scripts/renovate_vsce_test.go", "scripts/testdata/renovate/vsce-contract.mjs", "scripts/testdata/renovate/README.md", "scripts/testdata/renovate/runtime-integrity.json"} {
		digest := sha256.Sum256([]byte(readConfig(t, relative)))
		binding := "'" + relative + "' = '" + hex.EncodeToString(digest[:]) + "'"
		if !strings.Contains(qualify.Run, binding) {
			t.Fatalf("qualification must bind current LF source: %q", relative)
		}
	}
	const headRegex = "^Test(RenovateTracksIndependentMarketplaceExpectation|MarketplaceToolingPinRejectsMismatchAndInvalidIntegrity|RenovateFixtureOfflineControls|RenovateFixtureStartupEnvironmentKeys|RenovateFixtureStartupEnvironmentControls)$"
	const baseRegex = "^Test(MarketplaceToolingPinRejectsMismatchAndInvalidIntegrity|RenovateFixtureOfflineControls|RenovateFixtureStartupEnvironmentKeys|RenovateFixtureStartupEnvironmentControls)$"
	assertWorkflowStepRunContainsAll(t, qualify, "exact behavioral selectors", []string{"$headRegex = '" + headRegex + "'", "$baseRegex = '" + baseRegex + "'"})
	for _, name := range []string{
		"TestMarketplaceToolingPinRejectsMismatchAndInvalidIntegrity", "TestRenovateFixtureOfflineControls", "TestRenovateFixtureStartupEnvironmentKeys", "TestRenovateFixtureStartupEnvironmentControls",
		"TestMarketplaceToolingPinRejectsMismatchAndInvalidIntegrity/coordinated_current_pin", "TestMarketplaceToolingPinRejectsMismatchAndInvalidIntegrity/mismatched_version", "TestMarketplaceToolingPinRejectsMismatchAndInvalidIntegrity/missing_integrity", "TestMarketplaceToolingPinRejectsMismatchAndInvalidIntegrity/wrong_algorithm", "TestMarketplaceToolingPinRejectsMismatchAndInvalidIntegrity/empty_digest",
		"TestRenovateFixtureOfflineControls/boundaries", "TestRenovateFixtureOfflineControls/commands", "TestRenovateFixtureOfflineControls/serial",
		"TestRenovateFixtureStartupEnvironmentKeys/windows", "TestRenovateFixtureStartupEnvironmentKeys/linux", "TestRenovateFixtureStartupEnvironmentKeys/darwin",
		"TestRenovateFixtureStartupEnvironmentControls/NODE_OPTIONS", "TestRenovateFixtureStartupEnvironmentControls/Node_Options", "TestRenovateFixtureStartupEnvironmentControls/NODE_PATH", "TestRenovateFixtureStartupEnvironmentControls/Node_Path",
	} {
		if strings.Count(qualify.Run, "'"+name+"'") != 1 {
			t.Fatalf("qualification must account for exactly one explicit control name: %q", name)
		}
	}
	assertTextAppearsBefore(t, qualify.Run, "Require-Zero 'base-compile'", "$negativeStatus = Invoke-Native", "genuine-base compile must precede the intended failure")
	assertTextAppearsBefore(t, qualify.Run, "Require-Zero \"$tree-mod-verify\"", "Set-ProcessEnvironment 'GOPROXY' 'off'", "module verification must finish before offline execution")
	upload := workflowStepByName(t, workflow.Jobs, jobName, uploadName)
	if upload.If != "${{ always() }}" || upload.ContinueOnError || upload.Uses != "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" || upload.With["name"] != "renovate-windows-offline-${{ github.run_id }}-${{ github.run_attempt }}" || upload.With["path"] != "${{ runner.temp }}/renovate-windows-offline-receipts" || upload.With["if-no-files-found"] != "error" {
		t.Fatal("native qualification must always upload raw receipts using the pinned action")
	}
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Require every verification job")
	assertWorkflowStepRunContainsAll(t, gate, "Windows required gate", []string{"${WINDOWS_PROOF_RESULT}", "success"})
}

func assertRenovateWindowsJob(t *testing.T, job workflowJobConfig, jobName, qualifyName, uploadName string) {
	t.Helper()
	if job.RunsOn != "windows-latest" || job.If != "" || job.ContinueOnError {
		t.Fatal("qualification requires the existing unconditional native Windows job")
	}
	assertWorkflowJobPermissions(t, job, jobName, map[string]string{"contents": "read"})
	var deadlines struct {
		Jobs map[string]struct {
			Timeout int `yaml:"timeout-minutes"`
		} `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/ci.yml", &deadlines)
	if deadlines.Jobs[jobName].Timeout != 20 {
		t.Fatal("qualification must retain the original 20-minute deadline")
	}
	wantNames := []string{"Checkout", "Setup Go", "Resolve native Go toolchain", "Test native Windows proof tools", "Verify native Windows runtime path compatibility", "Write PR body for regression proof", "Prove Windows regression tests for fix PRs", qualifyName, uploadName}
	if len(job.Steps) != len(wantNames) {
		t.Fatal("qualification must append only two steps to the original Windows job")
	}
	for i, name := range wantNames {
		if job.Steps[i].Name != name {
			t.Fatalf("Windows step %d = %q, want %q", i, job.Steps[i].Name, name)
		}
	}
	checkout := job.Steps[0]
	if checkout.Uses != "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1" || checkout.With["ref"] != "${{ github.sha }}" || checkout.With["fetch-depth"] != "0" || checkout.With["persist-credentials"] != "false" {
		t.Fatal("qualification must preserve the original event checkout")
	}
}
