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

	for _, check := range []struct {
		jobName   string
		stepName  string
		wantUses  string
		stepLabel string
	}{
		{
			jobName:   "verify",
			stepName:  "Checkout",
			wantUses:  "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
			stepLabel: "verify checkout",
		},
		{
			jobName:   "verify",
			stepName:  "Setup Go",
			wantUses:  "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e",
			stepLabel: "verify setup-go",
		},
		{
			jobName:   "verify",
			stepName:  "Upload PR report inputs",
			wantUses:  "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a",
			stepLabel: "verify PR report upload",
		},
		{
			jobName:   "verify",
			stepName:  "Upload binary artifact",
			wantUses:  "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a",
			stepLabel: "verify binary upload",
		},
		{
			jobName:   "publish-pr-reports",
			stepName:  "Download PR report inputs",
			wantUses:  "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c",
			stepLabel: "PR report download",
		},
		{
			jobName:   "publish-pr-reports",
			stepName:  "Comment memory benchmark report on PR",
			wantUses:  "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3",
			stepLabel: "memory benchmark comment",
		},
		{
			jobName:   "publish-pr-reports",
			stepName:  "Comment lopper report on PR",
			wantUses:  "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3",
			stepLabel: "lopper report comment",
		},
		{
			jobName:   "publish-pr-reports",
			stepName:  "Post SonarQube review comments (PR)",
			wantUses:  "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3",
			stepLabel: "Sonar review comment",
		},
		{
			jobName:   "publish-pr-reports",
			stepName:  "Comment on coverage failure",
			wantUses:  "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3",
			stepLabel: "coverage failure comment",
		},
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
	assertWorkflowStepRunContainsAll(t, stageInputs, "ci PR report staging", []string{
		`report_root="${RUNNER_TEMP}/pr-report-inputs"`,
		`printf '%s\n' "${LOPPER_BASE_OUTCOME}" > "${report_root}/lopper-base-outcome.txt"`,
		`printf '%s\n' "${LOPPER_DELTA_OUTCOME}" > "${report_root}/lopper-delta-outcome.txt"`,
		`src=".artifacts/${report}"`,
		`cp -- "${src}" "${report_root}/${report}"`,
	})
	uploadInputs := workflowStepByName(t, workflow.Jobs, "verify", "Upload PR report inputs")
	assertCIArtifactAction(t, uploadInputs, "PR report upload", "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", map[string]string{
		"name":              "pr-report-inputs",
		"path":              "${{ runner.temp }}/pr-report-inputs",
		"if-no-files-found": "error",
	})

	publication := workflowJobByName(t, workflow.Jobs, "publish-pr-reports")
	assertWorkflowJobNeeds(t, publication, "PR report publication", workflowJobNeeds{"verify"})
	assertWorkflowJobPermissions(t, publication, "PR report publication", map[string]string{
		"contents":      "read",
		"issues":        "write",
		"pull-requests": "write",
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
	assertCIArtifactAction(t, downloadInputs, "PR report download", "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c", map[string]string{
		"name": "pr-report-inputs",
		"path": "pr-report-inputs",
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
