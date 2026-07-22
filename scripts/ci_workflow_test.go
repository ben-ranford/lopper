package scripts

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestCIWorkflowPinsPrivilegedVerifyActions(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	verify := workflowJobByName(t, workflow.Jobs, "verify")
	assertWorkflowJobPermissions(t, verify, "ci verify", map[string]string{"contents": "read"})
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, verify, "ci verify")
	assertWorkflowStepOrder(t, verify, "Run coverage gate", "Stage PR report inputs", "Upload PR report inputs", "Upload binary artifact", "Fail workflow on coverage gate")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "ci verify trusted PR report output", got: verify.Outputs["pr_report_artifact_id"], want: "${{ steps.upload_pr_report_inputs.outputs.artifact-id }}"},
	})

	for _, check := range []struct {
		jobName   string
		stepName  string
		wantUses  string
		stepLabel string
	}{
		{"verify", "Checkout", "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", "verify checkout"},
		{"verify", "Setup Go", "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", "verify setup-go"},
		{"verify", "Upload PR report inputs", "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", "verify PR report upload"},
		{"verify", "Upload binary artifact", "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", "verify binary upload"},
		{"publish-pr-reports", "Download PR report inputs", "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c", "PR report download"},
		{"publish-pr-reports", "Comment memory benchmark report on PR", "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3", "memory benchmark comment"},
		{"publish-pr-reports", "Comment lopper report on PR", "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3", "lopper report comment"},
		{"publish-pr-reports", "Post SonarQube review comments (PR)", "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3", "Sonar review comment"},
		{"publish-pr-reports", "Comment on coverage failure", "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3", "coverage failure comment"},
	} {
		step := workflowStepByName(t, workflow.Jobs, check.jobName, check.stepName)
		if step.Uses != check.wantUses {
			t.Fatalf("%s uses %q, want %q", check.stepLabel, step.Uses, check.wantUses)
		}
	}

	immutableAction := regexp.MustCompile(`^[^@[:space:]]+@[0-9a-f]{40}$`)
	for _, jobName := range []string{"verify", "publish-pr-reports"} {
		for _, step := range workflow.Jobs[jobName].Steps {
			if step.Uses != "" && !immutableAction.MatchString(step.Uses) {
				t.Fatalf("%s step %q must use an immutable action SHA: %q", jobName, step.Name, step.Uses)
			}
		}
	}
}

func TestCIWorkflowIsolatesPRPublicationCredentials(t *testing.T) {
	t.Parallel()

	const hardenedShell = "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /bin/bash --noprofile --norc -euo pipefail {0}"

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	stageInputs := workflowStepByName(t, workflow.Jobs, "verify", "Stage PR report inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "PR report staging shell", got: stageInputs.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, stageInputs, "PR report staging", map[string]string{
		"LOPPER_BASE_OUTCOME":  "${{ steps.lopper_base.outcome }}",
		"LOPPER_DELTA_OUTCOME": "${{ steps.lopper_delta.outcome }}",
		"PATH":                 "/usr/bin:/bin",
	})
	assertWorkflowStepRunContainsAll(t, stageInputs, "ci PR report staging", []string{
		`write_bounded_output() {`,
		`copy_bounded_report() {`,
		`report_root="${RUNNER_TEMP}/pr-report-inputs"`,
		`write_bounded_output "${report_root}/lopper-base-outcome.txt" 64 "${LOPPER_BASE_OUTCOME}"`,
		`write_bounded_output "${report_root}/lopper-delta-outcome.txt" 64 "${LOPPER_DELTA_OUTCOME}"`,
		`src=".artifacts/${report}"`,
		`limit_bytes=1048576`,
		`coverage-package-failures.txt)`,
		`limit_bytes=131072`,
		`coverage-status.txt|coverage-total.txt|memory-bench-status.txt)`,
		`copy_bounded_report "${src}" "${report_root}/${report}" "${limit_bytes}"`,
	})
	uploadInputs := workflowStepByName(t, workflow.Jobs, "verify", "Upload PR report inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "PR report upload step id", got: uploadInputs.ID, want: "upload_pr_report_inputs"},
	})
	assertCIArtifactAction(t, uploadInputs, "PR report upload", "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", map[string]string{
		"name":              "pr-report-inputs",
		"path":              "${{ runner.temp }}/pr-report-inputs",
		"if-no-files-found": "error",
	})

	publication := workflowJobByName(t, workflow.Jobs, "publish-pr-reports")
	assertWorkflowJobNeeds(t, publication, "PR report publication", workflowJobNeeds{"verify"})
	assertWorkflowJobPermissions(t, publication, "PR report publication", map[string]string{
		"actions":       "read",
		"contents":      "read",
		"issues":        "write",
		"pull-requests": "write",
	})
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "PR report publication guard", got: publication.If, want: "${{ always() && github.event_name == 'pull_request' && needs.verify.outputs.pr_report_artifact_id != '' }}"},
	})
	assertWorkflowJobEnvEmpty(t, publication, "PR report publication")
	assertWorkflowJobOmitsCheckout(t, publication, "PR report publication")
	assertWorkflowJobStepRunsOmitAllFold(t, publication, "PR report publication", []string{
		"go run ./",
		"make ",
		"npm ",
		"npx ",
		"scripts/",
		"./extensions/",
		"git ",
	})
	assertWorkflowStepOrder(t, publication, "Download PR report inputs", "Validate PR report inputs", "Comment memory benchmark report on PR", "Comment lopper report on PR", "Post SonarQube review comments (PR)", "Comment on coverage failure")
	coverageComment := workflowStepByName(t, workflow.Jobs, "publish-pr-reports", "Comment on coverage failure")
	if !coverageComment.ContinueOnError {
		t.Fatal("coverage comment publication must not fail an otherwise-green CI run")
	}

	downloadInputs := workflowStepByName(t, workflow.Jobs, "publish-pr-reports", "Download PR report inputs")
	assertWorkflowArtifactDownloadByID(t, downloadInputs, workflowArtifactDownloadExpectation{
		label: "PR report download", wantID: "${{ needs.verify.outputs.pr_report_artifact_id }}", wantPath: "pr-report-inputs",
		wantRepo: "${{ github.repository }}", wantRunID: "${{ github.run_id }}", wantToken: "${{ github.token }}",
	})

	validateInputs := workflowStepByName(t, workflow.Jobs, "publish-pr-reports", "Validate PR report inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "PR report validation shell", got: validateInputs.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateInputs, "PR report validation", map[string]string{
		"PATH":        "/usr/bin:/bin",
		"REPORT_ROOT": "pr-report-inputs",
	})
	assertWorkflowStepRunContainsAll(t, validateInputs, "PR report validation", []string{
		`find -P "${REPORT_ROOT}" -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`allowed_files=(`,
		`required_files=(`,
		`path="${REPORT_ROOT}/${required}"`,
		`Unexpected PR report input: ${name}`,
		`PR report input exceeds the 1 MiB publication limit: ${name}`,
	})
	if got, want := shellArrayValues(t, validateInputs.Run, "required_files"), []string{"lopper-base-outcome.txt", "lopper-delta-outcome.txt"}; !slices.Equal(got, want) {
		t.Fatalf("required PR report inputs = %q, want %q", got, want)
	}

	sonar := workflowStepByName(t, workflow.Jobs, "publish-pr-reports", "Post SonarQube review comments (PR)")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "Sonar review comment action", got: sonar.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
		{label: "Sonar review comment condition", got: sonar.If, want: "${{ !env.ACT && env.SONAR_TOKEN != '' }}"},
	})
	assertWorkflowStepEnv(t, sonar, "Sonar review comment step", map[string]string{
		"SONAR_HOST_URL":    "https://sonarcloud.io",
		"SONAR_PROJECT_KEY": "ben-ranford_lopper",
		"SONAR_TOKEN":       "${{ secrets.SONAR_TOKEN }}",
	})
	assertWorkflowEnvKeyOnlyOnStep(t, workflow.Jobs, "SONAR_TOKEN", "publish-pr-reports", "Post SonarQube review comments (PR)")
}

func assertCIArtifactAction(t *testing.T, step workflowStepConfig, label string, wantUses string, wantInputs map[string]string) {
	t.Helper()
	if step.Uses != wantUses {
		t.Fatalf("%s action = %q, want %q", label, step.Uses, wantUses)
	}
	for name, want := range wantInputs {
		if got := step.With[name]; got != want {
			t.Fatalf("%s %s = %q, want %q", label, name, got, want)
		}
	}
}

func shellArrayValues(t *testing.T, script string, name string) []string {
	t.Helper()
	marker := name + "=("
	start := strings.Index(script, marker)
	if start == -1 {
		t.Fatalf("shell array %q is missing", name)
	}
	body := script[start+len(marker):]
	end := strings.Index(body, "\n)")
	if end == -1 {
		t.Fatalf("shell array %q is unterminated", name)
	}
	values := make([]string, 0)
	for _, line := range strings.Split(body[:end], "\n") {
		if value := strings.TrimSpace(line); value != "" {
			values = append(values, value)
		}
	}
	return values
}
