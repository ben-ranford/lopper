package scripts

import (
	"reflect"
	"testing"
)

func TestCIRequiresNativeDarwinRegressionProof(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "regression-proof-darwin")
	if job.RunsOn != "macos-26" || job.If != "" || job.ContinueOnError {
		t.Fatal("Darwin proof must use required native macOS arm64")
	}
	assertWorkflowJobPermissions(t, job, "Darwin proof", map[string]string{"contents": "read"})
	assertWorkflowStepOrder(t, job, "Setup Go", "Test native Darwin proof tools", "Prove Darwin regression tests for fix PRs")
	checkout := workflowStepByName(t, workflow.Jobs, "regression-proof-darwin", "Checkout")
	if checkout.With["ref"] != "${{ github.sha }}" || checkout.With["fetch-depth"] != "0" || checkout.With["persist-credentials"] != "false" {
		t.Fatal("Darwin proof needs exact event source/full history without credentials")
	}
	native := workflowStepByName(t, workflow.Jobs, "regression-proof-darwin", "Test native Darwin proof tools")
	assertWorkflowStepRunContainsAll(t, native, "Darwin native proof tests", []string{`"${LOPPER_PROOF_GO}" test ./tools/regressionproof`, "'^TestDarwinProof'", "-count=1"})
	linux := workflowStepByName(t, workflow.Jobs, "verify-checks", "Prove regression tests for fix PRs")
	darwin := workflowStepByName(t, workflow.Jobs, "regression-proof-darwin", "Prove Darwin regression tests for fix PRs")
	if darwin.If != linux.If || darwin.ContinueOnError || darwin.Shell != linux.Shell {
		t.Fatal("Darwin proof must enforce the Linux PR scope and errors")
	}
	assertWorkflowStepRunContainsAll(t, darwin, "Darwin partition", []string{`"${LOPPER_PROOF_GO}" run ./tools/regressionproof`, "--target-os darwin", `--body-file "$PR_BODY_FILE"`, `--base-sha "$PR_BASE_SHA"`})
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Require every verification job")
	for _, result := range []string{"success", "failure", "cancelled", "skipped", ""} {
		output, err := runShellCommand(t.TempDir(), gate.Run, map[string]string{"CHECKS_RESULT": "success", "TESTS_RESULT": "success", "WINDOWS_PROOF_RESULT": "success", "DARWIN_PROOF_RESULT": result})
		if (err == nil) != (result == "success") {
			t.Fatalf("Darwin result %q: %v\n%s", result, err, output)
		}
	}
}

func TestDarwinProofInheritsApprovedRuntimeCustody(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "regression-proof-darwin")
	assertWorkflowStepOrder(t, job, "Setup Go", "Setup regression proof Node", "Provision restricted regression proof runtime", "Test native Darwin proof tools", "Verify restricted regression proof runtime", "Prove Darwin regression tests for fix PRs")
	for _, name := range []string{"Setup regression proof Node", "Provision restricted regression proof runtime", "Verify restricted regression proof runtime"} {
		darwin := workflowStepByName(t, workflow.Jobs, "regression-proof-darwin", name)
		linux := workflowStepByName(t, workflow.Jobs, "verify-checks", name)
		if !reflect.DeepEqual(darwin, linux) {
			t.Fatalf("Darwin %s must reuse exact approved custody step", name)
		}
	}
	linux := workflowStepByName(t, workflow.Jobs, "verify-checks", "Prove regression tests for fix PRs")
	for _, name := range []string{"Test native Darwin proof tools", "Prove Darwin regression tests for fix PRs"} {
		step := workflowStepByName(t, workflow.Jobs, "regression-proof-darwin", name)
		assertPinnedNodeConsumerEnvironment(t, step)
		if step.Shell != linux.Shell {
			t.Fatal("Darwin consumers must sanitize before Bash starts")
		}
	}
}
