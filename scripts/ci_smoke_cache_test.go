package scripts

import (
	"slices"
	"strings"
	"testing"
)

func TestCISmokeCachesFollowTheirWorkloads(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	osSetup := workflowStepByName(t, workflow.Jobs, "os-smoke", "Setup Go")
	vscodeSetup := workflowStepByName(t, workflow.Jobs, "vscode-smoke", "Setup Go")
	for jobName, setup := range map[string]workflowStepConfig{
		"os-smoke":     osSetup,
		"vscode-smoke": vscodeSetup,
	} {
		if !strings.HasPrefix(setup.Uses, "actions/setup-go@") {
			t.Fatalf("%s setup-go action = %q, want pinned setup-go action", jobName, setup.Uses)
		}
		if setup.With["go-version-file"] != "go.mod" {
			t.Fatalf("%s go-version-file = %q, want go.mod", jobName, setup.With["go-version-file"])
		}
	}
	if osSetup.Uses != vscodeSetup.Uses {
		t.Fatalf("smoke setup-go actions differ: os-smoke=%q vscode-smoke=%q", osSetup.Uses, vscodeSetup.Uses)
	}

	wantOS := []string{
		"go.sum",
		"Makefile",
		".github/workflows/ci.yml",
		"scripts/bench_fixture_cache_main_test.go",
	}
	wantVSCode := []string{
		"go.sum",
		"extensions/vscode-lopper/package-lock.json",
	}
	osPaths := strings.Fields(osSetup.With["cache-dependency-path"])
	vscodePaths := strings.Fields(vscodeSetup.With["cache-dependency-path"])
	for _, paths := range [][]string{osPaths, vscodePaths} {
		for _, dependency := range paths {
			readFile(t, repoPath(t, dependency))
		}
	}
	slices.Sort(osPaths)
	slices.Sort(vscodePaths)
	slices.Sort(wantOS)
	slices.Sort(wantVSCode)
	if !slices.Equal(osPaths, wantOS) {
		t.Fatalf("os-smoke cache dependencies = %v, want %v", osPaths, wantOS)
	}
	if !slices.Equal(vscodePaths, wantVSCode) {
		t.Fatalf("vscode-smoke cache dependencies = %v, want %v", vscodePaths, wantVSCode)
	}
	if slices.Equal(osPaths, vscodePaths) {
		t.Fatal("smoke jobs must use distinct cache dependency scopes")
	}
}
