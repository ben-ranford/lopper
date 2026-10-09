package scripts

import "testing"

func TestCIRequiresNativeWindowsRegressionProof(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "regression-proof-windows")
	if job.RunsOn != "windows-latest" || job.If != "" || job.ContinueOnError {
		t.Fatal("Windows proof must execute on a required native Windows job")
	}
	assertWorkflowJobPermissions(t, job, "Windows regression proof", map[string]string{"contents": "read"})
	assertWorkflowStepOrder(t, job, "Setup Go", "Provision restricted regression proof runtime", "Test native Windows proof tools", "Prove Windows regression tests for fix PRs")
	native := workflowStepByName(t, workflow.Jobs, "regression-proof-windows", "Test native Windows proof tools")
	assertPinnedNodeConsumerEnvironment(t, native)
	if native.Env["REGRESSION_PROOF_GO_ROOT"] != "${{ steps.proof_runtime.outputs.go_root }}" {
		t.Fatal("Windows root must use authenticated compiler output")
	}
	toolchain := workflowStepByName(t, workflow.Jobs, "regression-proof-windows", "Provision restricted regression proof runtime")
	if toolchain.ID != "proof_runtime" || toolchain.Env["LOPPER_RUNTIME_MODE"] != "capture" {
		t.Fatal("Windows compiler root must come from the authenticated pre-execution capture")
	}
	checkout := workflowStepByName(t, workflow.Jobs, "regression-proof-windows", "Checkout")
	if checkout.With["ref"] != "${{ github.sha }}" || checkout.With["fetch-depth"] != "0" || checkout.With["persist-credentials"] != "false" {
		t.Fatal("Windows proof must use the same event source and full history without saved credentials")
	}
	linux := workflowStepByName(t, workflow.Jobs, "verify-checks", "Prove regression tests for fix PRs")
	windows := workflowStepByName(t, workflow.Jobs, "regression-proof-windows", "Prove Windows regression tests for fix PRs")
	if windows.If != linux.If || windows.ContinueOnError || windows.Shell != "pwsh" {
		t.Fatal("Windows proof must enforce the same PR scope and fail on errors")
	}
	assertWorkflowStepRunContainsAll(t, linux, "Linux proof partition", []string{"--target-os linux"})
	assertWorkflowStepRunContainsAll(t, windows, "Windows proof partition", []string{
		"& $env:LOPPER_PROOF_GO run ./tools/regressionproof", "--target-os windows", `--body-file "$env:PR_BODY_FILE"`, `--base-sha "$env:PR_BASE_SHA"`,
	})
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Require every verification job")
	for _, result := range []string{"success", "failure", "cancelled", "skipped", ""} {
		output, err := runShellCommand(t.TempDir(), gate.Run, map[string]string{
			"CHECKS_RESULT": "success", "TESTS_RESULT": "success", "WINDOWS_PROOF_RESULT": result, "DARWIN_PROOF_RESULT": "success",
		})
		if (err == nil) != (result == "success") {
			t.Fatalf("Windows result %q: %v\n%s", result, err, output)
		}
	}
}
