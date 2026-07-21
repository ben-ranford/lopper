package scripts

import (
	"strings"
	"testing"
)

func TestFeatureFlagEnforcementWorkflowSeparatesUntrustedExecutionFromPrivilegedPublication(t *testing.T) {
	t.Parallel()

	const hardenedShell = "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /bin/bash --noprofile --norc -euo pipefail {0}"

	var enforcementWorkflow workflowConfig
	readYAMLConfig(t, ".github/workflows/feature-flag-enforcement.yml", &enforcementWorkflow)

	enforce := workflowJobByName(t, enforcementWorkflow.Jobs, "enforce")
	assertWorkflowJobPermissions(t, enforce, "feature flag enforcement", map[string]string{"contents": "read"})
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, enforce, "feature flag enforcement")
	assertWorkflowStepOrder(t, enforce, "Checkout", "Setup Go", "Reset feature flag comment staging", "Fetch PR base", "Capture base feature catalog", "Classify PR", "Enforce feature flags on PRs", "Write feature flag summary", "Validate and stage bounded comment inputs", "Upload bounded comment inputs", "Fail on feature flag enforcement errors")
	if _, ok := enforcementWorkflow.Jobs["publish-comments"]; ok {
		t.Fatal("pull_request workflow must not contain a privileged comment publisher")
	}

	checkout := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", "Checkout")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag enforcement checkout persist-credentials", got: checkout.With["persist-credentials"], want: "false"},
	})

	validateUpload := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", "Validate and stage bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag input validation shell", got: validateUpload.Shell, want: hardenedShell},
	})
	assertWorkflowStepRunContainsAll(t, validateUpload, "feature flag input validation", []string{
		`status_path="${COMMENT_DIR}/$(printf '%s' "${status_name}" | tr '[:upper:]' '[:lower:]').txt"`,
		`stat --format=%s "${status_path}"`,
		`find -P "${COMMENT_DIR}" -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`file_count="$(find -P "${COMMENT_DIR}" -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d '[:space:]')"`,
		`feature-flag-enforcement.md|release-feature-flag-comment.md`,
		`stat --format=%s "${path}"`,
	})
	upload := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", "Upload bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment upload action", got: upload.Uses, want: "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"},
		{label: "feature flag comment upload name", got: upload.With["name"], want: "feature-flag-comment-inputs"},
		{label: "feature flag comment upload path", got: upload.With["path"], want: "${{ runner.temp }}/feature-flag-comment-inputs"},
		{label: "feature flag comment upload missing-file behavior", got: upload.With["if-no-files-found"], want: "error"},
	})

	enforcementText := readConfig(t, ".github/workflows/feature-flag-enforcement.yml")
	for _, forbidden := range []string{
		"issues: write",
		"pull-requests: write",
		"actions/github-script@",
		"publish-comments:",
	} {
		if strings.Contains(enforcementText, forbidden) {
			t.Fatalf("untrusted feature flag workflow contains unsafe fragment %q", forbidden)
		}
	}

	var publicationWorkflow workflowConfig
	readYAMLConfig(t, ".github/workflows/feature-flag-comment-publish.yml", &publicationWorkflow)
	publication := workflowJobByName(t, publicationWorkflow.Jobs, "publish-comments")
	assertWorkflowJobPermissions(t, publication, "feature flag comment publication", map[string]string{
		"actions": "read",
		"issues":  "write",
	})
	assertWorkflowJobOmitsCheckout(t, publication, "feature flag comment publication")
	assertWorkflowJobStepRunsOmitAllFold(t, publication, "feature flag comment publication", []string{"go run ./", "make ", "npm ", "npx ", "git "})
	assertWorkflowStepOrder(t, publication, "Download bounded comment inputs", "Validate bounded comment inputs", "Sync feature flag enforcement comment on PR", "Sync release feature guidance comment on PR")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag publication event guard", got: publication.If, want: "${{ github.event.workflow_run.event == 'pull_request' && github.event.workflow_run.pull_requests[0].number != null }}"},
		{label: "feature flag publication PR number", got: publication.Env["PR_NUMBER"], want: "${{ github.event.workflow_run.pull_requests[0].number }}"},
	})

	download := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Download bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment download action", got: download.Uses, want: "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c"},
		{label: "feature flag comment download name", got: download.With["name"], want: "feature-flag-comment-inputs"},
		{label: "feature flag comment download path", got: download.With["path"], want: "${{ runner.temp }}/feature-flag-comment-inputs"},
		{label: "feature flag comment download token", got: download.With["github-token"], want: "${{ github.token }}"},
		{label: "feature flag comment download repository", got: download.With["repository"], want: "${{ github.repository }}"},
		{label: "feature flag comment download run ID", got: download.With["run-id"], want: "${{ github.event.workflow_run.id }}"},
	})

	validateDownload := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Validate bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag publication validation shell", got: validateDownload.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateDownload, "feature flag publication validation", map[string]string{
		"COMMENT_DIR": "${{ runner.temp }}/feature-flag-comment-inputs",
		"PATH":        "/usr/bin:/bin",
	})
	assertWorkflowStepRunContainsAll(t, validateDownload, "feature flag publication validation", []string{
		`find -P "${COMMENT_DIR}" -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`file_count="$(find -P "${COMMENT_DIR}" -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d '[:space:]')"`,
		`status_value="$(tr -d '[:space:]' < "${path}")"`,
		`feature_summary_path=""`,
		`release_summary_path=""`,
		`printf 'FEATURE_SUMMARY_PATH=%s\n' "${feature_summary_path}"`,
		`printf 'RELEASE_SUMMARY_PATH=%s\n' "${release_summary_path}"`,
	})

	featureComment := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Sync feature flag enforcement comment on PR")
	releaseComment := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Sync release feature guidance comment on PR")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment action", got: featureComment.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
		{label: "release feature comment action", got: releaseComment.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
	})
	assertTextContainsAll(t, featureComment.With["script"], "feature flag comment publication", []string{"process.env.PR_NUMBER", "issue_number: prNumber"})
	assertTextContainsAll(t, releaseComment.With["script"], "release feature comment publication", []string{"process.env.PR_NUMBER", "issue_number: prNumber"})
	for _, script := range []string{featureComment.With["script"], releaseComment.With["script"]} {
		for _, forbidden := range []string{".artifacts/", "go run ./tools/featureflag"} {
			if strings.Contains(script, forbidden) {
				t.Fatalf("trusted feature flag comment publisher contains %q", forbidden)
			}
		}
	}

	publicationText := readConfig(t, ".github/workflows/feature-flag-comment-publish.yml")
	for _, required := range []string{
		"workflow_run:",
		"- feature flag enforcement",
		"permissions: {}",
	} {
		if !strings.Contains(publicationText, required) {
			t.Fatalf("trusted feature flag publisher is missing %q", required)
		}
	}
	for _, forbidden := range []string{"actions/github-script@v", "actions/checkout@", "go run ./"} {
		if strings.Contains(publicationText, forbidden) {
			t.Fatalf("trusted feature flag publisher contains unsafe fragment %q", forbidden)
		}
	}
}
