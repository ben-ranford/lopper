package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMarketplaceToolchainLockfileValidationUsesTrustedLock(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Validate Marketplace tooling lockfile")

	for _, fixture := range []struct {
		name      string
		lockfile  string
		wantError bool
	}{
		{name: "old supported version", lockfile: marketplaceLockfile("3.9.2", marketplaceIntegrity)},
		{name: "current version", lockfile: marketplaceLockfile("4.0.0", marketplaceIntegrity)},
		{name: "future version", lockfile: marketplaceLockfile("5.1.2", marketplaceIntegrity)},
		{name: "prerelease version", lockfile: marketplaceLockfile("5.1.2-rc.1", marketplaceIntegrity), wantError: true},
		{name: "missing version", lockfile: marketplaceLockfile("", marketplaceIntegrity), wantError: true},
		{name: "newline injected version", lockfile: marketplaceLockfile("5.1.2\ninjected", marketplaceIntegrity), wantError: true},
		{name: "range version", lockfile: marketplaceLockfile("^5.1.2", marketplaceIntegrity), wantError: true},
		{name: "missing integrity", lockfile: marketplaceLockfile("5.1.2", ""), wantError: true},
		{name: "wrong integrity algorithm", lockfile: marketplaceLockfile("5.1.2", "sha256-YWJj"), wantError: true},
		{name: "short SHA-512 integrity", lockfile: marketplaceLockfile("5.1.2", "sha512-YWJj"), wantError: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			repo := t.TempDir()
			lockfile := filepath.Join(repo, "extensions", "vscode-lopper", "package-lock.json")
			writeFile(t, lockfile, fixture.lockfile)

			output, err := runMarketplaceToolchainStep(t, step.Run, repo, nil)
			if fixture.wantError {
				if err == nil {
					t.Fatalf("lockfile validation accepted invalid lockfile: %s", output)
				}
				return
			}
			if err != nil {
				t.Fatalf("lockfile validation rejected trusted lockfile: %v\n%s", err, output)
			}
			wantOutput := marketplaceLockfileOutput(t, fixture.lockfile)
			if output != wantOutput {
				t.Fatalf("lockfile validation outputs = %q, want %q", output, wantOutput)
			}
		})
	}

	t.Run("checked-in lockfile", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, "extensions", "vscode-lopper", "package-lock.json"), readConfig(t, "extensions/vscode-lopper/package-lock.json"))

		output, err := runMarketplaceToolchainStep(t, step.Run, repo, nil)
		if err != nil {
			t.Fatalf("lockfile validation rejected the checked-in lockfile: %v\n%s", err, output)
		}
		if !strings.Contains(output, "vsce_version=") || !strings.Contains(output, "vsce_integrity=") {
			t.Fatalf("checked-in lockfile outputs = %q, want VSCE version and integrity", output)
		}
	})

	t.Run("symlink lockfile", func(t *testing.T) {
		repo := t.TempDir()
		outside := filepath.Join(t.TempDir(), "package-lock.json")
		writeFile(t, outside, marketplaceLockfile("5.1.2", marketplaceIntegrity))
		lockfile := filepath.Join(repo, "extensions", "vscode-lopper", "package-lock.json")
		if err := os.MkdirAll(filepath.Dir(lockfile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, lockfile); err != nil {
			t.Fatal(err)
		}

		output, err := runMarketplaceToolchainStep(t, step.Run, repo, nil)
		if err == nil {
			t.Fatalf("lockfile validation accepted symlink: %s", output)
		}
	})
}

func TestExtractedMarketplaceToolchainValidationBindsTrustedOutputs(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Validate extracted Marketplace tooling")

	for _, fixture := range []struct {
		name              string
		lockVersion       string
		lockIntegrity     string
		packageName       string
		packageVersion    string
		expectedVersion   string
		expectedIntegrity string
		wantError         bool
	}{
		{name: "old supported version", lockVersion: "3.9.2", lockIntegrity: marketplaceIntegrity, packageName: "@vscode/vsce", packageVersion: "3.9.2", expectedVersion: "3.9.2", expectedIntegrity: marketplaceIntegrity},
		{name: "current version", lockVersion: "4.0.0", lockIntegrity: marketplaceIntegrity, packageName: "@vscode/vsce", packageVersion: "4.0.0", expectedVersion: "4.0.0", expectedIntegrity: marketplaceIntegrity},
		{name: "future version", lockVersion: "5.1.2", lockIntegrity: marketplaceIntegrity, packageName: "@vscode/vsce", packageVersion: "5.1.2", expectedVersion: "5.1.2", expectedIntegrity: marketplaceIntegrity},
		{name: "lock version mismatch", lockVersion: "4.0.0", lockIntegrity: marketplaceIntegrity, packageName: "@vscode/vsce", packageVersion: "4.0.0", expectedVersion: "5.1.2", expectedIntegrity: marketplaceIntegrity, wantError: true},
		{name: "lock integrity mismatch", lockVersion: "5.1.2", lockIntegrity: marketplaceIntegrity, packageName: "@vscode/vsce", packageVersion: "5.1.2", expectedVersion: "5.1.2", expectedIntegrity: "sha512-ZGVm", wantError: true},
		{name: "installed name mismatch", lockVersion: "5.1.2", lockIntegrity: marketplaceIntegrity, packageName: "vsce", packageVersion: "5.1.2", expectedVersion: "5.1.2", expectedIntegrity: marketplaceIntegrity, wantError: true},
		{name: "installed version mismatch", lockVersion: "5.1.2", lockIntegrity: marketplaceIntegrity, packageName: "@vscode/vsce", packageVersion: "4.0.0", expectedVersion: "5.1.2", expectedIntegrity: marketplaceIntegrity, wantError: true},
		{name: "empty expected version", lockVersion: "5.1.2", lockIntegrity: marketplaceIntegrity, packageName: "@vscode/vsce", packageVersion: "5.1.2", expectedIntegrity: marketplaceIntegrity, wantError: true},
		{name: "empty expected integrity", lockVersion: "5.1.2", lockIntegrity: marketplaceIntegrity, packageName: "@vscode/vsce", packageVersion: "5.1.2", expectedVersion: "5.1.2", wantError: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			runnerTemp := t.TempDir()
			writeExtractedMarketplaceToolchain(t, runnerTemp, fixture.lockVersion, fixture.lockIntegrity, fixture.packageName, fixture.packageVersion)

			env := map[string]string{
				"RUNNER_TEMP":            runnerTemp,
				"TRUSTED_VSCE_VERSION":   fixture.expectedVersion,
				"TRUSTED_VSCE_INTEGRITY": fixture.expectedIntegrity,
			}
			output, err := runMarketplaceToolchainStep(t, step.Run, t.TempDir(), env)
			if fixture.wantError {
				if err == nil {
					t.Fatalf("extracted toolchain validation accepted invalid fixture: %s", output)
				}
				return
			}
			if err != nil {
				t.Fatalf("extracted toolchain validation rejected trusted fixture: %v\n%s", err, output)
			}
		})
	}
}

const marketplaceIntegrity = "sha512-NImwuLaenMmb5D5Jer9/lzi/F9ZQUBOp8Azhj/BVYcTFgixv8KehFXqEUDjQlD2tAiw2E6dDGyjTuAB//di60A=="

func marketplaceLockfile(version string, integrity string) string {
	return `{"packages":{"node_modules/@vscode/vsce":{"version":` + strconv.Quote(version) + `,"integrity":` + strconv.Quote(integrity) + `}}}`
}

func marketplaceLockfileOutput(t *testing.T, lockfile string) string {
	t.Helper()
	var parsed struct {
		Packages map[string]struct {
			Version   string `json:"version"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(lockfile), &parsed); err != nil {
		t.Fatalf("parse fixture lockfile: %v", err)
	}
	vsce := parsed.Packages["node_modules/@vscode/vsce"]
	return "vsce_version=" + vsce.Version + "\nvsce_integrity=" + vsce.Integrity + "\n"
}

func writeExtractedMarketplaceToolchain(t *testing.T, runnerTemp string, version string, integrity string, name string, packageVersion string) {
	t.Helper()
	toolchain := filepath.Join(runnerTemp, "vsce-toolchain")
	writeFile(t, filepath.Join(toolchain, "package-lock.json"), marketplaceLockfile(version, integrity))
	writeFile(t, filepath.Join(toolchain, "node_modules", "@vscode", "vsce", "package.json"), `{"name":"`+name+`","version":"`+packageVersion+`"}`)
}

func runMarketplaceToolchainStep(t *testing.T, script string, dir string, variables map[string]string) (string, error) {
	t.Helper()
	outputFile := filepath.Join(t.TempDir(), "github-output")
	command := exec.Command("bash", "-euo", "pipefail", "-c", script)
	command.Dir = dir
	command.Env = append(os.Environ(), "GITHUB_OUTPUT="+outputFile)
	for name, value := range variables {
		command.Env = append(command.Env, name+"="+value)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), err
	}
	contents, err := os.ReadFile(outputFile)
	if os.IsNotExist(err) {
		return string(output), nil
	}
	if err != nil {
		t.Fatalf("read GitHub output: %v", err)
	}
	return string(contents), nil
}
