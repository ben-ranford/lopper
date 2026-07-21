package scripts

import (
	"strings"
	"testing"
)

func TestFeatureFlagEnforcementWorkflowSeparatesUntrustedExecutionFromPrivilegedPublication(t *testing.T) {
	t.Parallel()

	const hardenedShell = "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /bin/bash --noprofile --norc -euo pipefail {0}"

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/feature-flag-enforcement.yml", &workflow)

	enforce := workflowJobByName(t, workflow.Jobs, "enforce")
	assertWorkflowJobPermissions(t, enforce, "feature flag enforcement", map[string]string{"contents": "read"})
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, enforce, "feature flag enforcement")
	assertWorkflowStepOrder(t, enforce, "Checkout", "Setup Go", "Reset feature flag comment staging", "Fetch PR base", "Capture base feature catalog", "Classify PR", "Enforce feature flags on PRs", "Write feature flag summary", "Validate and stage bounded comment inputs", "Upload bounded comment inputs", "Fail on feature flag enforcement errors")

	checkout := workflowStepByName(t, workflow.Jobs, "enforce", "Checkout")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag enforcement checkout persist-credentials", got: checkout.With["persist-credentials"], want: "false"},
	})

	validateUpload := workflowStepByName(t, workflow.Jobs, "enforce", "Validate and stage bounded comment inputs")
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
	upload := workflowStepByName(t, workflow.Jobs, "enforce", "Upload bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment upload action", got: upload.Uses, want: "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"},
		{label: "feature flag comment upload name", got: upload.With["name"], want: "feature-flag-comment-inputs"},
		{label: "feature flag comment upload path", got: upload.With["path"], want: "${{ runner.temp }}/feature-flag-comment-inputs"},
		{label: "feature flag comment upload missing-file behavior", got: upload.With["if-no-files-found"], want: "error"},
	})

	publication := workflowJobByName(t, workflow.Jobs, "publish-comments")
	assertWorkflowJobNeeds(t, publication, "feature flag comment publication", workflowJobNeeds{"enforce"})
	assertWorkflowJobPermissions(t, publication, "feature flag comment publication", map[string]string{"issues": "write"})
	assertWorkflowJobOmitsCheckout(t, publication, "feature flag comment publication")
	assertWorkflowJobStepRunsOmitAllFold(t, publication, "feature flag comment publication", []string{"go run ./", "make ", "npm ", "npx ", "git "})
	assertWorkflowStepOrder(t, publication, "Download bounded comment inputs", "Validate bounded comment inputs", "Sync feature flag enforcement comment on PR", "Sync release feature guidance comment on PR")

	download := workflowStepByName(t, workflow.Jobs, "publish-comments", "Download bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment download action", got: download.Uses, want: "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c"},
		{label: "feature flag comment download name", got: download.With["name"], want: "feature-flag-comment-inputs"},
		{label: "feature flag comment download path", got: download.With["path"], want: "${{ runner.temp }}/feature-flag-comment-inputs"},
	})

	validateDownload := workflowStepByName(t, workflow.Jobs, "publish-comments", "Validate bounded comment inputs")
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

	featureComment := workflowStepByName(t, workflow.Jobs, "publish-comments", "Sync feature flag enforcement comment on PR")
	releaseComment := workflowStepByName(t, workflow.Jobs, "publish-comments", "Sync release feature guidance comment on PR")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment action", got: featureComment.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
		{label: "release feature comment action", got: releaseComment.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
	})
	assertWorkflowStepRunOmitsAll(t, featureComment, "feature flag comment publication", []string{".artifacts/", "go run ./tools/featureflag"})
	assertWorkflowStepRunOmitsAll(t, releaseComment, "release feature comment publication", []string{".artifacts/", "go run ./tools/featureflag"})

	workflowText := readConfig(t, ".github/workflows/feature-flag-enforcement.yml")
	for _, forbidden := range []string{
		"permissions:\n      contents: read\n      issues: write",
		"permissions:\n      contents: read\n      pull-requests: write",
		"actions/github-script@v",
		"publish-comments:\n    if: ${{ always() && !cancelled() }}\n    needs:\n      - enforce\n    runs-on: ubuntu-latest\n    permissions:\n      issues: write\n    steps:\n      - name: Checkout",
	} {
		if strings.Contains(workflowText, forbidden) {
			t.Fatalf("feature flag enforcement workflow contains unsafe fragment %q", forbidden)
		}
	}
}
