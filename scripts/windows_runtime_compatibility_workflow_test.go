package scripts

import (
	"go/build"
	"slices"
	"strings"
	"testing"
)

func TestWindowsRuntimeSearchPathTestsRunNatively(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	const jobName = "regression-proof-windows"
	const stepName = "Test native Windows runtime search paths"
	step := workflowStepByName(t, workflow.Jobs, jobName, stepName)
	if step.If != "" || step.ContinueOnError || step.Shell != "pwsh" || strings.TrimSpace(step.Run) != "& $env:LOPPER_PROOF_GO test ./internal/runtime -run '^TestTrustedSearchDirsWindows' -count=1" {
		t.Fatal("Windows runtime search path tests must run natively and fail closed for every event")
	}
	assertPinnedNodeConsumerEnvironment(t, step)
	assertWorkflowStepOrder(t, workflow.Jobs[jobName], "Provision restricted regression proof runtime", "Test native Windows proof tools", "Verify restricted regression proof runtime", stepName, "Prove Windows regression tests for fix PRs")
}

func TestWindowsUNCFixtureRequiresExplicitIntegrationTag(t *testing.T) {
	context := build.Default
	context.GOOS = "windows"
	context.GOARCH = "amd64"
	context.CgoEnabled = false
	context.BuildTags = nil
	ordinary, err := context.ImportDir(repoPath(t, "internal/runtime"), 0)
	if err != nil {
		t.Fatal(err)
	}
	const fixture = "capture_searchdirs_unc_windows_test.go"
	if slices.Contains(ordinary.TestGoFiles, fixture) || !slices.Contains(ordinary.TestGoFiles, "capture_searchdirs_windows_test.go") {
		t.Fatalf("ordinary Windows selection must retain real drive checks without requiring a share: %v", ordinary.TestGoFiles)
	}
	context.BuildTags = []string{"lopper_native_windows_unc"}
	integration, err := context.ImportDir(repoPath(t, "internal/runtime"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(integration.TestGoFiles, fixture) {
		t.Fatal("explicit integration tag did not select the native UNC contract")
	}
	source := readConfig(t, "internal/runtime/"+fixture)
	if strings.Contains(source, "t.Skip") || !strings.Contains(source, `t.Fatal("native UNC fixture requires LOPPER_WINDOWS_TEST_UNC_DIR with an accessible share")`) {
		t.Fatal("explicit UNC integration must fail when its fixture is unavailable")
	}
}
