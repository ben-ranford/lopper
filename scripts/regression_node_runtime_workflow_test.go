package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRegressionNodeRuntimeWorkflow(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	for _, jobName := range []string{"verify-checks", "regression-proof-windows"} {
		job := workflowJobByName(t, workflow.Jobs, jobName)
		last := "Capture duplication executables"
		if jobName == "regression-proof-windows" {
			last = "Test native Windows proof tools"
		}
		assertWorkflowStepOrder(t, job, "Setup Go", "Setup regression proof Node", "Provision restricted regression proof runtime", last)
		setup := workflowStepByName(t, workflow.Jobs, jobName, "Setup regression proof Node")
		if setup.Uses != "actions/setup-node@820762786026740c76f36085b0efc47a31fe5020" || setup.With["node-version"] != "24" || setup.With["package-manager-cache"] != "false" || setup.If != "" {
			t.Fatal("every native/ACT runner must provision the pinned Node action without repository cache inputs")
		}
		provision := workflowStepByName(t, workflow.Jobs, jobName, "Provision restricted regression proof runtime")
		if provision.Uses != "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3" || provision.If != "" || provision.ContinueOnError {
			t.Fatal("runtime provisioning must use the pinned action and fail closed")
		}
	}
}

func TestRegressionNodeRuntimeProvisioning(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "verify-checks", "Provision restricted regression proof runtime")
	for _, name := range []string{"success", "checkout shadow", "shadow control", "node escape", "npm escape", "existing destination", "wrong version", "launcher selects toolchain"} {
		t.Run(name, func(t *testing.T) {
			root, node, goBinary := regressionNodeRuntimeFixture(t, name)
			provision := step.With["script"]
			if name == "shadow control" {
				provision = strings.ReplaceAll(provision, "execFileSync(targetNode,", "execFileSync('node',")
			}
			script := regressionNodeRuntimeHarness + "\n(async function(io, core, require) {\n" + provision + "\n})(io, core, fixtureRequire).catch(error => { console.error(error); process.exitCode = 1; });\n"
			writeFixtureFile(t, root, "provision.cjs", script)
			cmd := exec.CommandContext(t.Context(), "node", filepath.Join(root, "provision.cjs"))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "RUNNER_TOOL_CACHE="+filepath.Join(root, "cache"), "RUNNER_TEMP="+root, "LOPPER_RUNTIME_MODE=capture", "FIXTURE_NODE="+node, "FIXTURE_GO="+goBinary, "FIXTURE_EFFECTIVE_GO="+filepath.Join(root, "go/bin", filepath.Base(goBinary)))
			output, err := cmd.CombinedOutput()
			assertRuntimeProvisioningResult(t, name, root, output, err)
		})
	}
}

func assertRuntimeProvisioningResult(t *testing.T, name, root string, output []byte, err error) {
	t.Helper()
	if name == "success" || name == "checkout shadow" || name == "launcher selects toolchain" {
		assertProvisionedNodeRuntime(t, root, output, err)
		return
	}
	if err == nil || !strings.Contains(string(output), regressionRuntimeDiagnostic(name)) {
		t.Fatalf("unexpected provisioning result: %v\n%s", err, output)
	}
	if name == "shadow control" {
		if _, err := os.Stat(filepath.Join(root, "shadow-executed")); err != nil {
			t.Fatalf("vulnerable lookup control did not select the checkout shadow: %v", err)
		}
	}
}

func regressionRuntimeDiagnostic(name string) string {
	switch name {
	case "node escape", "npm escape":
		return "outside the trusted distribution"
	case "existing destination":
		return "EEXIST"
	case "shadow control":
		return "Checkout executable selected"
	default:
		return "Proof Node version mismatch"
	}
}

func regressionNodeRuntimeFixture(t *testing.T, name string) (string, string, string) {
	t.Helper()
	return regressionNodeRuntimePlatformFixture(t, name, runtime.GOOS == "windows")
}

func regressionNodeRuntimePlatformFixture(t *testing.T, name string, windows bool) (string, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nodeRoot := "cache/node/24/arm64"
	nodePath, npmPath, goName := nodeRoot+"/bin/node", nodeRoot+"/lib/node_modules/npm", "go"
	if windows {
		nodePath, npmPath, goName = nodeRoot+"/node.exe", nodeRoot+"/node_modules/npm", "go.exe"
		writeFixtureFile(t, root, nodeRoot+"/npm.cmd", "trusted npm command shim\n")
	}
	if name == "node escape" {
		nodePath = "outside/node"
	}
	version := "v24.12.0"
	if name == "wrong version" {
		version = "v26.0.0"
	}
	writeFixtureFile(t, root, nodePath, version)
	writeFixtureFile(t, root, "go/bin/"+goName, "trusted Go fixture")
	writeFixtureFile(t, root, npmPath+"/bin/npm-cli.js", "11.0.0\n")
	writeFixtureFile(t, root, npmPath+"/node_modules/dependency/index.js", "trusted dependency\n")
	if name == "npm escape" {
		replaceRuntimeNpmWithEscape(t, root, npmPath)
	}
	if name == "existing destination" {
		writeFixtureFile(t, root, "go/bin/"+filepath.Base(nodePath), "do not replace\n")
	}
	writeFixtureFile(t, root, "node.exe", "v24.12.0")
	writeFixtureFile(t, root, "node", "v24.12.0")
	launcher := filepath.Join(root, "go/bin/"+goName)
	if name == "launcher selects toolchain" {
		writeFixtureFile(t, root, "launcher/"+goName, "launcher fixture")
		launcher = filepath.Join(root, "launcher", goName)
	}
	return root, filepath.Join(root, nodePath), launcher
}

func replaceRuntimeNpmWithEscape(t *testing.T, root, npmPath string) {
	t.Helper()
	npm := filepath.Join(root, npmPath)
	outside := filepath.Join(root, "outside-npm")
	if err := os.Rename(npm, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, npm); err != nil {
		t.Fatal(err)
	}
}

func assertProvisionedNodeRuntime(t *testing.T, root string, output []byte, err error) {
	t.Helper()
	if err != nil || !strings.Contains(string(output), "Provisioned Node v24.12.0, npm 11.0.0") {
		t.Fatalf("provision valid runtime: %v\n%s", err, output)
	}
	npm := filepath.Join(root, "go/bin/node_modules/npm/bin/npm-cli.js")
	data, err := os.ReadFile(npm)
	if err != nil || string(data) != "11.0.0\n" {
		t.Fatalf("npm CLI copy: %v, %q", err, data)
	}
	if _, err := os.Stat(filepath.Join(root, "shadow-executed")); !os.IsNotExist(err) {
		t.Fatalf("checkout executable was selected: %v", err)
	}
	if runtime.GOOS == "windows" {
		shim, err := os.ReadFile(filepath.Join(root, "go/bin/npm.cmd"))
		if err != nil || string(shim) != "trusted npm command shim\n" {
			t.Fatalf("npm command shim copy: %s, %v", shim, err)
		}
		return
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, "go/bin/npm"))
	if err != nil || resolved != npm {
		t.Fatalf("npm executable must resolve inside the provisioned bundle: %s, %v", resolved, err)
	}
}

// Only process execution is controlled; the workflow uses real filesystem paths,
// canonicalization, copies and hashes on every native test platform.
const regressionNodeRuntimeHarness = `
const fs = require('node:fs');
const path = require('node:path');
const io = { which: async name => process.env[name === 'node' ? 'FIXTURE_NODE' : 'FIXTURE_GO'] };
const core = { info: console.log, setOutput: () => {} };
function executeFixture(command, args, options) {
  const go = fs.realpathSync(process.env.FIXTURE_GO);
  const effectiveGo = process.env.FIXTURE_EFFECTIVE_GO || go;
  const bin = path.dirname(effectiveGo);
  if ((command === go || command === effectiveGo) && args.join(',') === 'env,GOROOT') return path.dirname(bin);
  if (!path.isAbsolute(command)) {
    fs.writeFileSync('shadow-executed', command);
    throw new Error('Checkout executable selected: ' + command);
  }
  const systemPath = process.platform === 'win32'
    ? String.raw` + "`" + `C:\mingw64\bin;C:\Program Files\Git\cmd;C:\Program Files\Git\mingw64\bin;C:\Program Files\Git\usr\bin;C:\Windows\System32;C:\Windows` + "`" + `
    : '/usr/bin:/bin:/usr/sbin:/sbin';
  if (options.env.PATH !== bin + path.delimiter + systemPath || options.cwd !== bin) {
    throw new Error('Runtime execution escaped restricted PATH or owned cwd');
  }
  if ('NODE_OPTIONS' in options.env || 'NODE_PATH' in options.env) throw new Error('Node loader input leaked');
  return fs.readFileSync(args[0] === '--version' ? command : args[0], 'utf8').trim();
}
const fixtureRequire = name => name === 'node:child_process' ? { execFileSync: executeFixture } : require(name);
`
