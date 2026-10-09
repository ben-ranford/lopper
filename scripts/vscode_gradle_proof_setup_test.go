package scripts

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVSCodeGradleProofSetupFailures(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	nodeBinary, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	modules, err := filepath.Abs("../extensions/vscode-lopper/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if _, err := gradleProofCompiler(ctx, filepath.Dir(modules)); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct{ name, diagnostic string }{
		{"node", "find Gradle proof Node runner"},
		{"npm", "provision existing extension tooling"},
		{"compile", "compile checked-out extension"},
		{"fixture", "invoke Gradle proof fixture"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo, commandPath := gradleProofSetupFixture(t, testCase.name, nodeBinary, modules)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			// Match regressionproof's actual go-test JSON boundary: infrastructure
			// errors must fail the package before any named test outcome appears.
			cmd := exec.CommandContext(ctx, goBinary, "tool", "test2json", "-p", "github.com/ben-ranford/lopper/scripts", executable, "-test.v=test2json", "-test.run=^"+gradleInferenceTestName+"$")
			cmd.Dir = filepath.Join(repo, "scripts")
			cmd.Env = append(os.Environ(), "PATH="+commandPath)
			output, runErr := cmd.CombinedOutput()
			if runErr == nil || !strings.Contains(string(output), testCase.diagnostic) {
				t.Fatalf("expected %s infrastructure failure: %v\n%s", testCase.name, runErr, output)
			}
			assertNoGradleBehaviorOutcome(t, output)
		})
	}
}

func gradleProofSetupFixture(t *testing.T, name, nodeBinary, modules string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	extension := "extensions/vscode-lopper"
	writeFixtureFile(t, repo, "scripts/.keep", "")
	writeFixtureFile(t, repo, extension+"/tsconfig.json", readRepoFile(t, extension+"/tsconfig.json"))
	source := "export const fixture = true;"
	if name == "compile" {
		source = "const value: number = 'invalid';"
	}
	writeFixtureFile(t, repo, extension+"/src/languageConfiguration.ts", source)
	if name == "node" || name == "npm" {
		return repo, gradleProofMissingToolPath(t, repo, name, nodeBinary)
	}
	if err := os.Symlink(modules, filepath.Join(repo, extension, "node_modules")); err != nil {
		t.Fatal(err)
	}
	return repo, os.Getenv("PATH")
}

func gradleProofMissingToolPath(t *testing.T, repo, name, nodeBinary string) string {
	t.Helper()
	commandPath := filepath.Join(repo, "bin")
	if err := os.Mkdir(commandPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if name == "npm" {
		if err := os.Symlink(nodeBinary, filepath.Join(commandPath, filepath.Base(nodeBinary))); err != nil {
			t.Fatal(err)
		}
	}
	return commandPath
}

func assertNoGradleBehaviorOutcome(t *testing.T, output []byte) {
	t.Helper()
	packageFailed := false
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		var event struct{ Action, Test string }
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode test event: %v\n%s", err, line)
		}
		if event.Test == gradleInferenceTestName {
			t.Fatalf("setup failure emitted named behavioral event: %s", line)
		}
		if event.Test == "" && event.Action == "fail" {
			packageFailed = true
		}
	}
	if !packageFailed {
		t.Fatalf("setup failure must fail the package: %s", output)
	}
}

func TestVSCodeGradleProofUnrelatedSelections(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []string{"-test.run=^$", "-test.list=^TestVSCodeGradle"} {
		t.Run(selection, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, selection)
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), "PATH="+t.TempDir())
			output, err := cmd.CombinedOutput()
			if err != nil || strings.Contains(string(output), "prepare Gradle inference proof") {
				t.Fatalf("unrelated selection must not prepare the extension: %v\n%s", err, output)
			}
		})
	}
}

func TestVSCodeGradleProofNpmCommandShim(t *testing.T) {
	root := t.TempDir()
	writeFixtureFileMode(t, root, "bin/npm.cmd", "@echo off\r\nexit /b 9\r\n", 0o755)
	writeFixtureFile(t, root, "bin/node_modules/npm/bin/npm-cli.js", `
const fs = require('node:fs');
fs.writeFileSync('install.args', JSON.stringify(process.argv.slice(2)));
fs.mkdirSync('node_modules/typescript/bin', { recursive: true });
fs.writeFileSync('node_modules/typescript/bin/tsc', '// compiler fixture');
`)
	if err := os.Symlink(filepath.Join(root, "bin/npm.cmd"), filepath.Join(root, "bin/npm")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	compiler, err := gradleProofCompiler(t.Context(), root)
	if err != nil || compiler != filepath.Join(root, "node_modules/typescript/bin/tsc") {
		t.Fatalf("provision npm through its JavaScript CLI: %s, %v", compiler, err)
	}
	arguments, err := os.ReadFile(filepath.Join(root, "install.args"))
	if err != nil || string(arguments) != `["ci","--ignore-scripts","--no-audit","--no-fund"]` {
		t.Fatalf("npm CLI arguments = %s, %v", arguments, err)
	}
}
