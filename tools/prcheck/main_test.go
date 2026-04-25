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
		"PR_TITLE":    "fix(release): validate PR metadata",
		"PR_HEAD_REF": "bug/issue-000-pr-title-template-gate",
		"PR_BODY":     validBody(),
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
	if err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", body); err != nil {
		t.Fatalf("validate returned error: %v", err)
	}
}

func TestValidateRejectsBugTitle(t *testing.T) {
	err := validate("bug: patch adapter parser", "bug/issue-123-parser", validBody())
	if err == nil {
		t.Fatal("validate succeeded for unsupported bug title")
	}
	if !strings.Contains(err.Error(), "use fix(scope)") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsNonConventionalTitle(t *testing.T) {
	err := validate("patch adapter parser", "bug/issue-123-parser", validBody())
	if err == nil {
		t.Fatal("validate succeeded for non-conventional title")
	}
}

func TestValidateRequiresTemplateSections(t *testing.T) {
	err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", strings.Replace(validBody(), "## Validation", "## Verification", 1))
	expectValidationError(t, err, `missing required template section "Validation"`)
}

func TestValidateRejectsPlaceholderOnlySection(t *testing.T) {
	body := strings.Replace(validBody(), "Stops release-please from missing patch fixes.", "Describe the problem and the intent of this change.", 1)
	err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", body)
	expectValidationError(t, err, `section "Summary" must be completed`)
}

func TestValidateRequiresRiskFields(t *testing.T) {
	body := strings.Replace(validBody(), "- Performance impact: None\n", "", 1)
	err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", body)
	expectValidationError(t, err, `field "Performance impact"`)
}

func TestValidateRequiresCheckedChecklist(t *testing.T) {
	body := strings.Replace(validBody(), "- [x] Ready for review", "- [ ] Ready for review", 1)
	err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", body)
	expectValidationError(t, err, `Checklist item "Ready for review"`)
}

func TestValidateRejectsBlankSectionWithCommentOnly(t *testing.T) {
	body := strings.Replace(validBody(), "Stops release-please from missing patch fixes.", "<!-- TODO -->", 1)
	err := validate("fix(release): validate PR metadata", "bug/issue-000-pr-title-template-gate", body)
	if err == nil {
		t.Fatal("validate succeeded with comment-only Summary")
	}
}

func TestValidateExemptsGeneratedReleasePleaseBody(t *testing.T) {
	err := validate("chore(main): release 1.5.1", "release-please--branches--main", "## Changelog\n\n* fix: parser")
	if err != nil {
		t.Fatalf("validate returned error: %v", err)
	}
}

func TestValidateReleasePleaseExemptionStillRequiresReleaseTitle(t *testing.T) {
	err := validate("release 1.5.1", "release-please--branches--main", "## Changelog\n\n* fix: parser")
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
