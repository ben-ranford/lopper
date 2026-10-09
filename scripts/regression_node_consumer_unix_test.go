//go:build !windows

package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegressionNodeConsumerBoundary(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "verify-checks", "Prove regression tests for fix PRs")
	for _, scenario := range []string{"positive", "alternate path", "shell startup"} {
		t.Run(scenario, func(t *testing.T) {
			for _, compiler := range []bool{false, true} {
				t.Run(map[bool]string{false: "npm install", true: "existing compiler"}[compiler], func(t *testing.T) { assertNodeConsumerBoundary(t, step, scenario, compiler) })
			}
		})
	}
}

func assertNodeConsumerBoundary(t *testing.T, step workflowStepConfig, scenario string, compiler bool) {
	t.Helper()
	root, env := nodeConsumerFixture(t, compiler)
	bin := filepath.Join(root, "a/bin")
	if scenario == "alternate path" {
		bin = filepath.Join(root, "b/bin")
	}
	env = append(env, "PATH="+bin+":/usr/bin:/bin:/usr/sbin:/sbin")
	if scenario == "shell startup" {
		env = append(env, "BASH_ENV="+filepath.Join(root, "startup.sh"))
	}
	// Explicit workflow bindings replace mutable inherited values at step startup.
	for key, value := range step.Env {
		if strings.HasPrefix(value, "${{ steps.proof_runtime.outputs.") {
			env = append(env, key+"="+nodeConsumerOutput(root, value))
		} else if !strings.HasPrefix(value, "${{") {
			env = append(env, key+"="+value)
		}
	}
	consumer := filepath.Join(root, "consumer.sh")
	writeFile(t, consumer, step.Run)
	shell := strings.Fields(step.Shell)
	if len(shell) == 0 {
		shell = []string{"/bin/bash", "-e"}
	}
	if shell[len(shell)-1] == "{0}" {
		shell[len(shell)-1] = consumer
	} else {
		shell = append(shell, consumer)
	}
	command := exec.CommandContext(t.Context(), shell[0], shell[1:]...)
	command.Dir = filepath.Join(root, "scripts")
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "REAL_NODE_CONSUMER_CONTROL") || strings.Contains(string(output), "SUBSTITUTED_NODE_EXECUTED") {
		t.Fatalf("verified runtime was not consumed: %v\n%s", err, output)
	}
	assertReleaseMarkerAbsent(t, filepath.Join(root, "startup-executed"))
}

func nodeConsumerOutput(root, value string) string {
	switch value {
	case "${{ steps.proof_runtime.outputs.go }}":
		return filepath.Join(root, "a/bin/go")
	case "${{ steps.proof_runtime.outputs.go_root }}":
		return filepath.Join(root, "a")
	default:
		return ""
	}
}

func nodeConsumerFixture(t *testing.T, compiler bool) (string, []string) {
	t.Helper()
	root := t.TempDir()
	bridge, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFileMode(t, root, "cache/node/24/bin/node", "#!/bin/sh\nexec \"$FIXTURE_REAL_NODE\" \"$@\"\n", 0o755)
	badNode := "#!/bin/sh\nprintf 'SUBSTITUTED_NODE_EXECUTED\\n'\n"
	writeFixtureFileMode(t, root, "b/bin/node", badNode, 0o755)
	for _, name := range []string{"a", "b"} {
		// This dispatcher models proofGoEnv's existing selected-root PATH contract;
		// the full unchanged Go bridge/TestMain runs as the actual child consumer.
		launcher := "#!/bin/sh\nPATH=\"$FIXTURE_ROOT/" + name + "/bin:/usr/bin:/bin:/usr/sbin:/sbin\"\nexport PATH\nexec \"$FIXTURE_BRIDGE\" '-test.run=^TestVSCodeGradleInferenceBounds$' -test.v\n"
		writeFixtureFileMode(t, root, name+"/bin/go", launcher, 0o755)
	}
	extension := "extensions/vscode-lopper"
	writeFixtureFile(t, root, extension+"/tsconfig.json", "{}")
	if compiler {
		writeFixtureFile(t, root, extension+"/node_modules/typescript/bin/tsc", "// native Node compiler control\n")
	}
	writeFixtureFileMode(t, root, "cache/node/24/lib/node_modules/npm/bin/npm-cli.js", `const fs=require('node:fs');fs.mkdirSync('node_modules/typescript/bin',{recursive:true});fs.writeFileSync('node_modules/typescript/bin/tsc','// compiler control');`, 0o755)
	verifyNodeConsumerFixture(t, root)
	if err := os.Symlink(filepath.Join(root, "a/bin/node_modules/npm/bin/npm-cli.js"), filepath.Join(root, "b/bin/npm")); err != nil {
		t.Fatal(err)
	}

	writeFixtureFile(t, root, "scripts/testdata/vscode-gradle-inference.cjs", "console.log('REAL_NODE_CONSUMER_CONTROL');\n")
	writeFixtureFile(t, root, "startup.sh", "printf executed > \"$FIXTURE_ROOT/startup-executed\"\ncp \"$FIXTURE_ROOT/b/bin/node\" \"$FIXTURE_ROOT/a/bin/node\"\n")
	env := append(os.Environ(), "FIXTURE_ROOT="+root, "FIXTURE_BRIDGE="+bridge, "FIXTURE_REAL_NODE="+node, "PR_BODY_FILE=fixture", "PR_TITLE=fixture", "PR_BASE_SHA=fixture", "PR_REGRESSION_EXEMPT_LABEL=false", "GOTOOLCHAIN=ambient-invalid", "GOFLAGS=-invalid", "GOROOT="+filepath.Join(root, "b"))
	return root, env
}

func assertReleaseMarkerAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("shell startup executed after verification: %v", err)
	}
}

func verifyNodeConsumerFixture(t *testing.T, root string) {
	t.Helper()
	var config workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &config)
	provision := workflowStepByName(t, config.Jobs, "verify-checks", "Provision restricted regression proof runtime").With["script"]
	source := nodeConsumerBootstrap + "\n(async function(require) {\n" + provision + "\n})(fixtureRequire).then(() => fs.writeFileSync(process.env.FIXTURE_CAPTURE,JSON.stringify(outputs))).catch(error=>{console.error(error);process.exitCode=1});\n"
	writeFixtureFile(t, root, "bootstrap.cjs", source)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "RUNNER_TOOL_CACHE="+filepath.Join(root, "cache"), "RUNNER_TEMP="+root, "FIXTURE_ROOT="+root, "FIXTURE_CAPTURE="+filepath.Join(root, "capture.json"))
	command := exec.CommandContext(t.Context(), node, filepath.Join(root, "bootstrap.cjs"))
	command.Env = append([]string(nil), env...)
	command.Env = append(command.Env, "LOPPER_RUNTIME_MODE=capture")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("capture consumer fixture: %v\n%s", err, output)
	}
	var outputs map[string]string
	data, err := os.ReadFile(filepath.Join(root, "capture.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &outputs); err != nil {
		t.Fatal(err)
	}
	for key, name := range map[string]string{"LOPPER_RUNTIME_RECEIPT": "receipt", "LOPPER_RUNTIME_SHA256": "sha256", "LOPPER_RUNTIME_VERIFIER": "verifier", "LOPPER_RUNTIME_VERIFIER_SHA256": "verifier_sha256", "LOPPER_PROOF_GO": "go", "GOROOT": "go_root", "LOPPER_PROOF_GO_SHA256": "go_sha256"} {
		env = append(env, key+"="+outputs[name])
	}
	command = exec.CommandContext(t.Context(), node, filepath.Join(root, "bootstrap.cjs"))
	command.Env = append([]string(nil), env...)
	command.Env = append(command.Env, "LOPPER_RUNTIME_MODE=verify")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("verify actual consumer fixture before launch: %v\n%s", err, output)
	}
}

const nodeConsumerBootstrap = `
const fs=require('node:fs');const path=require('node:path');
const root=process.env.FIXTURE_ROOT;const outputs={};
const io={which:async name=>path.join(root,name==='node'?'cache/node/24/bin/node':'a/bin/go')};
const core={info:console.log,setOutput:(key,value)=>{outputs[key]=value}};
const fixtureRequire=name=>name==='node:child_process'?{execFileSync:(file,args)=>{
 if(args.join(',')==='env,GOROOT')return path.join(root,'a');
 return args[0]==='--version'?'v24.12.0':'11.0.0';
}}:require(name);
`
