package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegressionNodeRuntimeCustody(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	provision := workflowStepByName(t, workflow.Jobs, "verify-checks", "Provision restricted regression proof runtime").With["script"]
	var verify string
	for _, step := range workflow.Jobs["verify-checks"].Steps {
		if step.Name == "Verify restricted regression proof runtime" {
			verify = step.With["script"]
		}
	}
	for _, scenario := range []string{"success", "node", "module", "extra", "symlink", "adapter", "joint node receipt", "joint adapter receipt", "verifier", "joint verifier receipt", "missing anchor", "malformed anchor", "malformed receipt", "ambient anchor", "compiler bytes", "joint compiler receipt", "compiler anchor", "missing compiler anchor"} {
		t.Run(scenario, func(t *testing.T) {
			for _, platform := range []string{"linux", "win32"} {
				t.Run(platform, func(t *testing.T) { assertRuntimeCustody(t, provision, verify, scenario, platform) })
			}
		})
	}
}

func assertRuntimeCustody(t *testing.T, provision, verify, scenario, platform string) {
	t.Helper()
	root, node, goBinary := regressionNodeRuntimePlatformFixture(t, "success", platform == "win32")
	// Exercise both provider layouts with real files and controlled process calls;
	// changing this marker is branch coverage, not native Windows execution.
	script := "Object.defineProperty(process, 'platform', { value: process.env.FIXTURE_PLATFORM });\n" + regressionNodeRuntimeHarness + runtimeCustodyHarness + "\n(async () => {\n" +
		"await (async function(io, core, require) {\n" + provision + "\n})(io, captureCore, fixtureRequire);\n" +
		"console.log('PROVISIONED'); mutateRuntime(); process.env.LOPPER_RUNTIME_MODE = 'verify';\n" +
		"await (async function(core, require) {\n" + verify + "\n})(core, require);\n" +
		"console.log('CONSUMER_REACHED');\n})().catch(error => { console.error(error); process.exitCode = 1; });\n"
	writeFixtureFile(t, root, "custody.cjs", script)
	cmd := exec.CommandContext(t.Context(), "node", filepath.Join(root, "custody.cjs"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "RUNNER_TOOL_CACHE="+filepath.Join(root, "cache"), "RUNNER_TEMP="+root, "LOPPER_RUNTIME_MODE=capture", "FIXTURE_NODE="+node, "FIXTURE_GO="+goBinary, "FIXTURE_CUSTODY="+scenario, "FIXTURE_PLATFORM="+platform)
	output, err := cmd.CombinedOutput()
	if !strings.Contains(string(output), "PROVISIONED") {
		t.Fatalf("provisioning failed before custody scenario: %v\n%s", err, output)
	}
	if scenario == "success" {
		if err != nil || !strings.Contains(string(output), "CONSUMER_REACHED") {
			t.Fatalf("valid runtime rejected: %v\n%s", err, output)
		}
		return
	}
	if err == nil || strings.Contains(string(output), "CONSUMER_REACHED") || !strings.Contains(string(output), "Runtime custody") {
		t.Fatalf("substitution reached consumer or failed outside custody check: %v\n%s", err, output)
	}
}

const runtimeCustodyHarness = `
const crypto = require('node:crypto');
const outputs = {};
const captureCore = { info: console.log, setOutput: (key, value) => { outputs[key] = value; } };
function mutateRuntimeFiles(kind, bin) {
  const target = path.join(bin, process.platform === 'win32' ? 'node.exe' : 'node');
  const modulePath = path.join(bin, 'node_modules/npm/node_modules/dependency/index.js');
  const adapter = path.join(bin, process.platform === 'win32' ? 'npm.cmd' : 'npm');
  if (kind.includes('compiler') && kind !== 'compiler anchor' && kind !== 'missing compiler anchor') fs.writeFileSync(process.env.FIXTURE_GO, 'substituted Go');
  if (kind.includes('node')) fs.writeFileSync(target, 'substituted Node');
  if (kind === 'module') fs.writeFileSync(modulePath, 'substituted npm dependency');
  if (kind === 'extra') fs.writeFileSync(path.join(bin, 'node_modules/npm/extra.js'), 'extra module');
  if (kind === 'symlink') {
    fs.unlinkSync(modulePath);
    fs.symlinkSync(target, modulePath);
  }
  if (kind.includes('adapter')) {
    fs.unlinkSync(adapter);
    fs.writeFileSync(adapter, 'substituted npm adapter');
  }
}
function rewriteRuntimeReceipt(bin) {
  const target = path.join(bin, process.platform === 'win32' ? 'node.exe' : 'node');
  const adapter = path.join(bin, process.platform === 'win32' ? 'npm.cmd' : 'npm');
  const receipt = JSON.parse(fs.readFileSync(outputs.receipt, 'utf8'));
  const digest = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
  receipt.nodeHash = digest(target);
  receipt.goHash = digest(process.env.FIXTURE_GO);
  receipt.adapterLink = fs.lstatSync(adapter).isSymbolicLink();
  receipt.adapterHash = receipt.adapterLink ? fs.readlinkSync(adapter) : digest(adapter);
  receipt.verifierHash = digest(outputs.verifier);
  fs.writeFileSync(outputs.receipt, JSON.stringify(receipt));
}
function mutateRuntime() {
  const kind = process.env.FIXTURE_CUSTODY;
  const bin = path.dirname(process.env.FIXTURE_GO);
  mutateRuntimeFiles(kind, bin);
  if (kind.includes('verifier') && outputs.verifier) fs.appendFileSync(outputs.verifier, '\n// substituted verifier');
  if (kind.startsWith('joint ') && outputs.receipt) rewriteRuntimeReceipt(bin);
  if (kind === 'malformed receipt' && outputs.receipt) fs.writeFileSync(outputs.receipt, '{bad JSON');
  if (kind === 'ambient anchor') {
    mutateRuntimeFiles('node', bin);
    process.env.LOPPER_RUNTIME_SHA256 = 'a'.repeat(64);
  }
  // Model explicit step env values from trusted outputs overriding inherited env.
  process.env.LOPPER_PROOF_GO = outputs.go || '';
  process.env.GOROOT = outputs.go_root || '';
  process.env.LOPPER_PROOF_GO_SHA256 = outputs.go_sha256 || '';
  process.env.LOPPER_RUNTIME_RECEIPT = outputs.receipt || '';
  process.env.LOPPER_RUNTIME_SHA256 = outputs.sha256 || '';
  process.env.LOPPER_RUNTIME_VERIFIER = outputs.verifier || '';
  process.env.LOPPER_RUNTIME_VERIFIER_SHA256 = outputs.verifier_sha256 || '';
  if (kind === 'compiler anchor') process.env.LOPPER_PROOF_GO = path.join(bin, 'alternate-go');
  if (kind === 'missing compiler anchor') delete process.env.LOPPER_PROOF_GO_SHA256;
  if (kind === 'missing anchor') delete process.env.LOPPER_RUNTIME_SHA256;
  if (kind === 'malformed anchor') process.env.LOPPER_RUNTIME_SHA256 = 'not-a-digest';
}
`

func TestRegressionNodeCustodyWorkflowOrder(t *testing.T) {
	var config workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &config)
	for _, item := range []struct{ job, intervening, proof string }{
		{"verify-checks", "Run CI target", "Prove regression tests for fix PRs"},
		{"regression-proof-windows", "Test native Windows proof tools", "Prove Windows regression tests for fix PRs"},
	} {
		t.Run(item.job, func(t *testing.T) {
			job := config.Jobs[item.job]
			assertWorkflowStepOrder(t, job, "Provision restricted regression proof runtime", item.intervening, "Verify restricted regression proof runtime", item.proof)
			capture := workflowStepByName(t, config.Jobs, item.job, "Provision restricted regression proof runtime")
			if capture.ID != "proof_runtime" {
				t.Fatal("runtime capture needs a stable pre-execution output identity")
			}
			verify := workflowStepByName(t, config.Jobs, item.job, "Verify restricted regression proof runtime")
			if verify.Env["LOPPER_RUNTIME_MODE"] != "verify" || capture.Env["LOPPER_RUNTIME_MODE"] != "capture" || verify.Uses != capture.Uses || verify.If != "" || verify.ContinueOnError {
				t.Fatal("runtime verification must always fail closed through the pinned trusted action")
			}
			for key, output := range map[string]string{"LOPPER_RUNTIME_RECEIPT": "receipt", "LOPPER_RUNTIME_SHA256": "sha256", "LOPPER_RUNTIME_VERIFIER": "verifier", "LOPPER_RUNTIME_VERIFIER_SHA256": "verifier_sha256", "LOPPER_PROOF_GO": "go", "GOROOT": "go_root", "LOPPER_PROOF_GO_SHA256": "go_sha256"} {
				if verify.Env[key] != "${{ steps.proof_runtime.outputs."+output+" }}" {
					t.Fatalf("%s must override inherited values with trusted capture output", key)
				}
			}
		})
	}
}

func TestRegressionNodePinnedConsumers(t *testing.T) {
	var config workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &config)
	for _, item := range []struct{ job, name, launch string }{
		{"verify-checks", "Prove regression tests for fix PRs", `"${LOPPER_PROOF_GO}" run`},
		{"regression-proof-windows", "Test native Windows proof tools", "& $env:LOPPER_PROOF_GO test"},
		{"regression-proof-windows", "Prove Windows regression tests for fix PRs", "& $env:LOPPER_PROOF_GO run"},
	} {
		t.Run(item.name, func(t *testing.T) {
			step := workflowStepByName(t, config.Jobs, item.job, item.name)
			assertPinnedNodeConsumerEnvironment(t, step)
			if !strings.HasPrefix(strings.TrimSpace(step.Run), item.launch) {
				t.Fatal("consumer must invoke the authenticated absolute compiler")
			}
			if item.job == "regression-proof-windows" && step.Env["REGRESSION_PROOF_GO_ROOT"] != "${{ steps.proof_runtime.outputs.go_root }}" {
				t.Fatal("Windows proof root must use the independently captured compiler root")
			}
		})
	}
	proof := workflowStepByName(t, config.Jobs, "verify-checks", "Prove regression tests for fix PRs")
	ci := workflowStepByName(t, config.Jobs, "verify-checks", "Run CI target")
	if proof.Shell != ci.Shell || !strings.Contains(proof.Shell, "-u BASH_ENV") {
		t.Fatal("proof must retain the established pre-start sanitized shell")
	}
}

func assertPinnedNodeConsumerEnvironment(t *testing.T, step workflowStepConfig) {
	t.Helper()
	for key, expected := range map[string]string{
		"LOPPER_PROOF_GO": "${{ steps.proof_runtime.outputs.go }}", "GOROOT": "${{ steps.proof_runtime.outputs.go_root }}",
		"GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off", "GOFLAGS": "", "GOCACHEPROG": "", "GOAUTH": "off",
	} {
		if actual, present := step.Env[key]; !present || actual != expected {
			t.Fatalf("consumer %s must be explicitly bound to %q", key, expected)
		}
	}
}
