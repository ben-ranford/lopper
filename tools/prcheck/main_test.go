package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAcceptsEnvBody(t *testing.T) {
	var stderr bytes.Buffer
	env := mapEnv(map[string]string{
		"PR_TITLE":               "fix(release): validate PR metadata",
		"PR_HEAD_REF":            "bug/issue-000-pr-title-template-gate",
		"PR_HEAD_REPO_FULL_NAME": "ben-ranford/lopper",
		"PR_BODY":                validBody(),
		"REPOSITORY_FULL_NAME":   "ben-ranford/lopper",
	})
	code := run(nil, env, &stderr)
	if code != 0 {
		t.Fatalf("run exited with %d, stderr: %s", code, stderr.String())
	}
}

func TestRunAcceptsBodyFile(t *testing.T) {
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte(validBody()), 0o600); err != nil {
		t.Fatalf("write body file: %v", err)
	}
	var stderr bytes.Buffer
	args := []string{"--title", "fix(release): validate PR metadata", "--head-ref", "bug/issue-000-pr-title-template-gate", "--body-file", bodyFile}
	code := run(args, mapEnv(nil), &stderr)
	if code != 0 {
		t.Fatalf("run exited with %d, stderr: %s", code, stderr.String())
	}
}

func TestRunRejectsOversizedBodyFile(t *testing.T) {
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte(strings.Repeat("x", 1<<20+1)), 0o600); err != nil {
		t.Fatalf("write oversized body file: %v", err)
	}
	var stderr bytes.Buffer
	code := run([]string{"--title", "fix(release): validate PR metadata", "--head-ref", "bug/issue-000-pr-title-template-gate", "--body-file", bodyFile}, mapEnv(nil), &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "read PR body") {
		t.Fatalf("run exited with %d, stderr: %s", code, stderr.String())
	}
}

func TestRunRequiresMaintainerLabelForRegressionExemption(t *testing.T) {
	const declaration = "Regression-Test: ./tools/prcheck::TestValidateAcceptsCompletedTemplate"
	const exemption = "Regression-Test-Exemption: no deterministic local reproducer"
	body := strings.Replace(validBody(), declaration, exemption, 1)
	values := map[string]string{
		"PR_TITLE":               "fix(release): validate PR metadata",
		"PR_HEAD_REF":            "bug/issue-000-pr-title-template-gate",
		"PR_HEAD_REPO_FULL_NAME": "ben-ranford/lopper",
		"PR_BODY":                body,
		"REPOSITORY_FULL_NAME":   "ben-ranford/lopper",
	}

	var stderr bytes.Buffer
	if code := run(nil, mapEnv(values), &stderr); code != 1 {
		t.Fatalf("reason-only run exited with %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "maintainer-controlled regression-exempt label") {
		t.Fatalf("reason-only stderr = %q", stderr.String())
	}

	stderr.Reset()
	values["PR_REGRESSION_EXEMPT_LABEL"] = "true"
	if code := run(nil, mapEnv(values), &stderr); code != 0 {
		t.Fatalf("reason-and-label run exited with %d, stderr: %s", code, stderr.String())
	}
}

func TestRunAcceptsValidRepoPolicy(t *testing.T) {
	var stderr bytes.Buffer
	base := map[string]string{
		"PR_TITLE":               "fix(release): validate PR metadata",
		"PR_HEAD_REF":            "bug/issue-000-pr-title-template-gate",
		"PR_HEAD_REPO_FULL_NAME": "ben-ranford/lopper",
		"PR_BODY":                validBody(),
		"REPOSITORY_FULL_NAME":   "ben-ranford/lopper",
	}
	values := mergeMaps(base, validRepoPolicyEnv())
	env := mapEnv(values)
	code := run([]string{"--check-repo-policy"}, env, &stderr)
	if code != 0 {
		t.Fatalf("run exited with %d, stderr: %s", code, stderr.String())
	}
}

func TestRunRejectsInvalidRepoPolicy(t *testing.T) {
	var stderr bytes.Buffer
	base := map[string]string{
		"PR_TITLE":               "fix(release): validate PR metadata",
		"PR_HEAD_REF":            "bug/issue-000-pr-title-template-gate",
		"PR_HEAD_REPO_FULL_NAME": "ben-ranford/lopper",
		"PR_BODY":                validBody(),
		"REPOSITORY_FULL_NAME":   "ben-ranford/lopper",
	}
	policy := map[string]string{
		"REPO_ALLOW_MERGE_COMMIT":        "true",
		"REPO_ALLOW_REBASE_MERGE":        "false",
		"REPO_ALLOW_SQUASH_MERGE":        "true",
		"REPO_SQUASH_MERGE_COMMIT_TITLE": "COMMIT_OR_PR_TITLE",
	}
	values := mergeMaps(base, policy)
	env := mapEnv(values)
	code := run([]string{"--check-repo-policy"}, env, &stderr)
	if code != 1 {
		t.Fatalf("run exited with %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Repository must disable merge commits") {
		t.Fatalf("stderr did not explain repo policy failure: %s", stderr.String())
	}
}

func TestRunRejectsMissingRepoPolicyValue(t *testing.T) {
	var stderr bytes.Buffer
	base := map[string]string{
		"PR_TITLE":               "fix(release): validate PR metadata",
		"PR_HEAD_REF":            "bug/issue-000-pr-title-template-gate",
		"PR_HEAD_REPO_FULL_NAME": "ben-ranford/lopper",
		"PR_BODY":                validBody(),
		"REPOSITORY_FULL_NAME":   "ben-ranford/lopper",
	}
	policy := map[string]string{
		"REPO_ALLOW_MERGE_COMMIT": "false",
		"REPO_ALLOW_REBASE_MERGE": "false",
		"REPO_ALLOW_SQUASH_MERGE": "true",
	}
	values := mergeMaps(base, policy)
	code := run([]string{"--check-repo-policy"}, mapEnv(values), &stderr)
	if code != 1 {
		t.Fatalf("run exited with %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `expected "PR_TITLE", got empty value`) {
		t.Fatalf("stderr did not explain missing repo policy value: %s", stderr.String())
	}
}

func TestRunHandlesRepoPolicyValidationErrorWriteFailure(t *testing.T) {
	base := map[string]string{
		"PR_TITLE":               "fix(release): validate PR metadata",
		"PR_HEAD_REF":            "bug/issue-000-pr-title-template-gate",
		"PR_HEAD_REPO_FULL_NAME": "ben-ranford/lopper",
		"PR_BODY":                validBody(),
		"REPOSITORY_FULL_NAME":   "ben-ranford/lopper",
	}
	policy := map[string]string{
		"REPO_ALLOW_MERGE_COMMIT":        "false",
		"REPO_ALLOW_REBASE_MERGE":        "false",
		"REPO_ALLOW_SQUASH_MERGE":        "true",
		"REPO_SQUASH_MERGE_COMMIT_TITLE": "COMMIT_OR_PR_TITLE",
	}
	values := mergeMaps(base, policy)
	code := run([]string{"--check-repo-policy"}, mapEnv(values), &errWriter{})
	if code != 1 {
		t.Fatalf("run exited with %d", code)
	}
}

func TestRunRejectsMissingBodyFile(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"--title", "fix(release): validate PR metadata", "--body-file", filepath.Join(t.TempDir(), "missing.md")}
	code := run(args, mapEnv(nil), &stderr)
	if code != 1 {
		t.Fatalf("run exited with %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "read PR body") {
		t.Fatalf("stderr did not explain body read failure: %s", stderr.String())
	}
}

func TestRunHandlesBodyReadErrorWriteFailure(t *testing.T) {
	code := run([]string{"--body-file", filepath.Join(t.TempDir(), "missing.md")}, mapEnv(nil), &errWriter{})
	if code != 1 {
		t.Fatalf("run exited with %d", code)
	}
}

func TestRunRejectsInvalidFlags(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--unknown"}, mapEnv(nil), &stderr)
	if code != 2 {
		t.Fatalf("run exited with %d, stderr: %s", code, stderr.String())
	}
}

func TestRunReportsValidationFailures(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--title", "bug: invalid"}, mapEnv(map[string]string{"PR_BODY": validBody()}), &stderr)
	if code != 1 {
		t.Fatalf("run exited with %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "PR title must be") {
		t.Fatalf("stderr did not explain validation failure: %s", stderr.String())
	}
}

func TestRunHandlesValidationErrorWriteFailure(t *testing.T) {
	code := run([]string{"--title", "bug: invalid"}, mapEnv(map[string]string{"PR_BODY": validBody()}), &errWriter{})
	if code != 1 {
		t.Fatalf("run exited with %d", code)
	}
}

func TestValidateAcceptsCompletedTemplate(t *testing.T) {
	body := validBody()
	if err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", body, releasePleaseIdentity{}, false); err != nil {
		t.Fatalf("validate returned error: %v", err)
	}
}

func TestValidateAcceptsPreviewTitle(t *testing.T) {
	body := strings.Replace(validBody(), "\nRegression-Test: ./tools/prcheck::TestValidateAcceptsCompletedTemplate\n", "\n", 1)
	if err := validate("preview(runtime): add opt-in capture", "feat/preview-runtime-capture", body, releasePleaseIdentity{}, false); err != nil {
		t.Fatalf("validate returned error: %v", err)
	}
}

func TestValidateRejectsBreakingPreviewTitle(t *testing.T) {
	err := validate("preview(runtime)!: replace capture format", "feat/preview-runtime-capture", validBody(), releasePleaseIdentity{}, false)
	expectValidationError(t, err, "Preview PR titles must not use a breaking-change marker")
}

func TestValidateRejectsBugTitle(t *testing.T) {
	err := validate("bug: patch adapter parser", "bug/issue-123-parser", validBody(), releasePleaseIdentity{}, false)
	if err == nil {
		t.Fatal("validate succeeded for unsupported bug title")
	}
	if !strings.Contains(err.Error(), "use fix(scope)") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsNonConventionalTitle(t *testing.T) {
	err := validate("patch adapter parser", "bug/issue-123-parser", validBody(), releasePleaseIdentity{}, false)
	if err == nil {
		t.Fatal("validate succeeded for non-conventional title")
	}
}

func TestValidateRequiresTemplateSections(t *testing.T) {
	err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", strings.Replace(validBody(), "## Validation", "## Verification", 1), releasePleaseIdentity{}, false)
	expectValidationError(t, err, `missing required template section "Validation"`)
}

func TestValidateRejectsPlaceholderOnlySection(t *testing.T) {
	body := strings.Replace(validBody(), "Stops release-please from missing patch fixes.", "Describe the problem and the intent of this change.", 1)
	err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", body, releasePleaseIdentity{}, false)
	expectValidationError(t, err, `section "Summary" must be completed`)
}

func TestValidateRequiresRiskFields(t *testing.T) {
	body := strings.Replace(validBody(), "- Performance impact: None\n", "", 1)
	err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", body, releasePleaseIdentity{}, false)
	expectValidationError(t, err, `field "Performance impact"`)
}

func TestValidateRequiresCheckedChecklist(t *testing.T) {
	body := strings.Replace(validBody(), "- [x] Ready for review", "- [ ] Ready for review", 1)
	err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", body, releasePleaseIdentity{}, false)
	expectValidationError(t, err, `Checklist item "Ready for review"`)
}

func TestValidateRejectsBlankSectionWithCommentOnly(t *testing.T) {
	body := strings.Replace(validBody(), "Stops release-please from missing patch fixes.", "<!-- TODO -->", 1)
	err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", body, releasePleaseIdentity{}, false)
	if err == nil {
		t.Fatal("validate succeeded with comment-only Summary")
	}
}

func TestValidateExemptsGeneratedReleasePleaseBody(t *testing.T) {
	err := validate("chore(main): release 1.5.1", "release-please--branches--main", "## Changelog\n\n* fix: parser", trustedReleasePleaseIdentity(), false)
	if err != nil {
		t.Fatalf("validate returned error: %v", err)
	}
}

func TestValidateRejectsSpoofedReleasePleaseIdentity(t *testing.T) {
	err := validate("chore(main): release 1.5.1", "release-please--branches--main", "## Changelog\n\n* fix: parser", spoofedReleasePleaseIdentity(), false)
	expectValidationError(t, err, `missing required template section "Summary"`)
}

func TestValidateRejectsReleasePleasePrefixSpoof(t *testing.T) {
	err := validate("chore(main): release 1.5.1", "release-please--branches--main-attacker", "## Changelog\n\n* fix: parser", trustedReleasePleaseIdentity(), false)
	expectValidationError(t, err, `missing required template section "Summary"`)
}

func TestValidateAcceptsRepositoryOwnedReleasePRFromNonOwnerToken(t *testing.T) {
	identity := trustedReleasePleaseIdentity()
	err := validate("chore(main): release 1.5.1", "release-please--branches--main", "## Changelog\n\n* fix: parser", identity, false)
	if err != nil {
		t.Fatalf("validate returned error: %v", err)
	}
}

func TestValidateReleasePleaseExemptionStillRequiresReleaseTitle(t *testing.T) {
	err := validate("release 1.5.1", "release-please--branches--main", "## Changelog\n\n* fix: parser", trustedReleasePleaseIdentity(), false)
	if err == nil {
		t.Fatal("validate succeeded for generated release PR with non-conventional title")
	}
}

func validBody() string {
	return `## Summary

Stops release-please from missing patch fixes.

## Changes

- Adds a PR metadata validator.

## Validation

Commands and checks run:

` + "```bash" + `
go test ./tools/prcheck
` + "```" + `

Additional manual validation:

- Reviewed release-please title requirements.

Regression-Test: ./tools/prcheck::TestValidateAcceptsCompletedTemplate

## Risk and compatibility

- Breaking changes: None
- Migration required: None
- Performance impact: None
- Memory benchmark impact: None

## Checklist

- [x] Tests added/updated for behavior changes
- [x] Docs updated (README/docs/schema) if needed
- [x] ` + "`memory-approved`" + ` requested/applied if intentional memory benchmark regressions exceed CI thresholds
- [x] No unrelated changes included
- [x] Ready for review
`
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func mergeMaps(parts ...map[string]string) map[string]string {
	merged := make(map[string]string)
	for _, part := range parts {
		for key, value := range part {
			merged[key] = value
		}
	}
	return merged
}

func validRepoPolicyEnv() map[string]string {
	return map[string]string{
		"REPO_ALLOW_MERGE_COMMIT":        "false",
		"REPO_ALLOW_REBASE_MERGE":        "false",
		"REPO_ALLOW_SQUASH_MERGE":        "true",
		"REPO_SQUASH_MERGE_COMMIT_TITLE": "PR_TITLE",
	}
}

func trustedReleasePleaseIdentity() releasePleaseIdentity {
	return releasePleaseIdentity{
		headRepositoryFullName: "ben-ranford/lopper",
		repositoryFullName:     "ben-ranford/lopper",
	}
}

func spoofedReleasePleaseIdentity() releasePleaseIdentity {
	return releasePleaseIdentity{
		headRepositoryFullName: "attacker/lopper",
		repositoryFullName:     "ben-ranford/lopper",
	}
}

func expectValidationError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("validate succeeded, want error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("unexpected error: %v", err)
	}
}

type errWriter struct{}

func (*errWriter) Write([]byte) (int, error) {
	return 0, os.ErrClosed
}
