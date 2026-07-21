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
	assertReadOnlyFeatureFlagEnforcementWorkflow(t, enforcementWorkflow, hardenedShell)

	var publicationWorkflow workflowConfig
	readYAMLConfig(t, ".github/workflows/feature-flag-comment-publish.yml", &publicationWorkflow)
	assertTrustedFeatureFlagPublicationWorkflow(t, publicationWorkflow, hardenedShell)
}

func assertReadOnlyFeatureFlagEnforcementWorkflow(t *testing.T, enforcementWorkflow workflowConfig, hardenedShell string) {
	t.Helper()

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
		{label: "feature flag input validation id", got: validateUpload.ID, want: "validate_comment_inputs"},
		{label: "feature flag input validation shell", got: validateUpload.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateUpload, "feature flag input validation", map[string]string{
		"COMMENT_DIR": "${{ runner.temp }}/feature-flag-comment-inputs",
		"PATH":        "/usr/bin:/bin",
	})
	assertWorkflowStepRunContainsAll(t, validateUpload, "feature flag input validation", []string{
		`find -P "${COMMENT_DIR}" -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`if [ -f "${summary_path}" ] && [ ! -s "${summary_path}" ]; then`,
		`rm -f -- "${summary_path}"`,
		`printf '1\n' > "${COMMENT_DIR}/payload-version.txt"`,
		`file_count="$(find -P "${COMMENT_DIR}" -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d '[:space:]')"`,
		`payload-version.txt`,
		`echo "::error::Comment payload version exceeds the 8-byte bound." >&2`,
		`feature-flag-enforcement.md|release-feature-flag-comment.md`,
		`stat --format=%s "${path}"`,
	})
	upload := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", "Upload bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment upload guard", got: upload.If, want: "${{ always() && steps.validate_comment_inputs.outcome == 'success' }}"},
		{label: "feature flag comment upload action", got: upload.Uses, want: "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"},
		{label: "feature flag comment upload name", got: upload.With["name"], want: "feature-flag-comment-inputs-${{ github.event.pull_request.number }}"},
		{label: "feature flag comment upload path", got: upload.With["path"], want: "${{ runner.temp }}/feature-flag-comment-inputs"},
		{label: "feature flag comment upload missing-file behavior", got: upload.With["if-no-files-found"], want: "error"},
	})

	enforcementText := readConfig(t, ".github/workflows/feature-flag-enforcement.yml")
	for _, forbidden := range []string{
		"issues: write",
		"pull-requests: write",
		"actions/github-script@",
		"publish-comments:",
		"enforcement_failed.txt",
		"feature_pr.txt",
		"release_pr.txt",
	} {
		if strings.Contains(enforcementText, forbidden) {
			t.Fatalf("untrusted feature flag workflow contains unsafe fragment %q", forbidden)
		}
	}

}

func assertTrustedFeatureFlagPublicationWorkflow(t *testing.T, publicationWorkflow workflowConfig, hardenedShell string) {
	t.Helper()

	publication := workflowJobByName(t, publicationWorkflow.Jobs, "publish-comments")
	assertWorkflowJobPermissions(t, publication, "feature flag comment publication", map[string]string{
		"actions":       "read",
		"issues":        "write",
		"pull-requests": "read",
	})
	assertWorkflowJobOmitsCheckout(t, publication, "feature flag comment publication")
	assertWorkflowJobStepRunsOmitAllFold(t, publication, "feature flag comment publication", []string{"go run ./", "make ", "npm ", "npx ", "git "})
	assertWorkflowStepOrder(t, publication, "Resolve triggering pull request and artifact", "Download bounded comment inputs", "Validate bounded comment inputs", "Sync feature flag enforcement comment on PR", "Sync release feature guidance comment on PR")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag publication event guard", got: publication.If, want: "${{ github.event.workflow_run.event == 'pull_request' }}"},
	})

	resolvePR := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Resolve triggering pull request and artifact")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag PR resolution id", got: resolvePR.ID, want: "resolve_pr"},
		{label: "feature flag PR resolution action", got: resolvePR.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
	})
	assertTextContainsAll(t, resolvePR.With["script"], "feature flag PR resolution", []string{
		"github.rest.actions.listWorkflowRunArtifacts",
		"/^feature-flag-comment-inputs-([1-9][0-9]*)$/",
		"github.rest.pulls.get",
		"pull.head.sha !== run.head_sha",
		"pull.head.ref !== run.head_branch",
		"pull.head.repo?.full_name?.toLowerCase() !== expectedHeadRepository",
		"github.rest.repos.listPullRequestsAssociatedWithCommit",
		"core.setOutput('artifact-name', artifact.name)",
		"core.exportVariable('PR_NUMBER', String(prNumber))",
		"String(run.conclusion !== 'success')",
		"String(/^feat(\\([^)]+\\))?(!)?:\\s+\\S/.test(pull.title))",
		"String(pull.head.ref.startsWith('release-please--branches--'))",
	})

	download := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Download bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment download action", got: download.Uses, want: "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c"},
		{label: "feature flag comment download name", got: download.With["name"], want: "${{ steps.resolve_pr.outputs.artifact-name }}"},
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
		`payload_version="$(tr -d '[:space:]' < "${path}")"`,
		`: "${payload_version:?missing comment payload version}"`,
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
	for _, forbidden := range []string{
		"actions/github-script@v",
		"actions/checkout@",
		"go run ./",
		"workflow_run.pull_requests[0]",
		"enforcement_failed.txt",
		"feature_pr.txt",
		"release_pr.txt",
	} {
		if strings.Contains(publicationText, forbidden) {
			t.Fatalf("trusted feature flag publisher contains unsafe fragment %q", forbidden)
		}
	}
}
