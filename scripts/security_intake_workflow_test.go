package scripts

import (
	"regexp"
	"strings"
	"testing"
)

func TestExistingWorkflowsKeepExplicitJobPermissions(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		".github/workflows/ci.yml",
		".github/workflows/release.yml",
		".github/workflows/release-orchestration.yml",
		".github/workflows/rolling.yml",
		".github/workflows/pr-metadata.yml",
		".github/workflows/parser-fuzz.yml",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			assertWorkflowDefaultDeniesToken(t, path)
		})
	}
}

func TestCodeQLWorkflowContract(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/codeql.yml", &workflow)
	assertWorkflowDefaultDeniesToken(t, ".github/workflows/codeql.yml")
	assertSecurityWorkflowPins(t, ".github/workflows/codeql.yml")

	goJob := workflowJobByName(t, workflow.Jobs, "analyze-go")
	jsJob := workflowJobByName(t, workflow.Jobs, "analyze-javascript-typescript")
	assertWorkflowJobPermissions(t, goJob, "CodeQL Go", map[string]string{
		"contents":        "read",
		"packages":        "read",
		"security-events": "write",
	})
	assertWorkflowJobPermissions(t, jsJob, "CodeQL javascript-typescript", map[string]string{
		"contents":        "read",
		"packages":        "read",
		"security-events": "write",
	})
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, goJob, "CodeQL Go")
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, jsJob, "CodeQL javascript-typescript")

	goInit := workflowStepByName(t, workflow.Jobs, "analyze-go", "Initialize CodeQL")
	jsInit := workflowStepByName(t, workflow.Jobs, "analyze-javascript-typescript", "Initialize CodeQL")
	if goInit.With["languages"] != "go" || goInit.With["build-mode"] != "autobuild" {
		t.Fatalf("Go CodeQL init = %#v", goInit.With)
	}
	if jsInit.With["languages"] != "javascript-typescript" || jsInit.With["build-mode"] != "none" {
		t.Fatalf("JavaScript CodeQL init = %#v", jsInit.With)
	}
	for _, fragment := range []string{
		"paths:",
		"- extensions/vscode-lopper",
		"- extensions/vscode-lopper/**/node_modules",
		"- extensions/vscode-lopper/out",
	} {
		if !strings.Contains(jsInit.With["config"], fragment) {
			t.Fatalf("JavaScript CodeQL config missing %q:\n%s", fragment, jsInit.With["config"])
		}
	}
	if strings.Contains(goInit.With["config"], "extensions/vscode-lopper") {
		t.Fatal("Go CodeQL analysis must not be limited to the VS Code extension")
	}

	goAnalyze := workflowStepByName(t, workflow.Jobs, "analyze-go", "Perform CodeQL analysis")
	jsAnalyze := workflowStepByName(t, workflow.Jobs, "analyze-javascript-typescript", "Perform CodeQL analysis")
	if goAnalyze.With["category"] != "/language:go" || jsAnalyze.With["category"] != "/language:javascript-typescript" {
		t.Fatalf("CodeQL categories = %q, %q", goAnalyze.With["category"], jsAnalyze.With["category"])
	}
}

func TestDependencyReviewWorkflowContract(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/dependency-review.yml", &workflow)
	text := readConfig(t, ".github/workflows/dependency-review.yml")
	if !strings.Contains(text, "on:\n  pull_request:\n") {
		t.Fatal("dependency review must run on pull_request")
	}
	for _, forbidden := range []string{"push:", "schedule:", "workflow_dispatch:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("dependency review must not declare %s", forbidden)
		}
	}
	assertWorkflowDefaultDeniesToken(t, ".github/workflows/dependency-review.yml")
	assertSecurityWorkflowPins(t, ".github/workflows/dependency-review.yml")

	job := workflowJobByName(t, workflow.Jobs, "dependency-review")
	assertWorkflowJobPermissions(t, job, "dependency review", map[string]string{"contents": "read"})
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, job, "dependency review")
	review := workflowStepByName(t, workflow.Jobs, "dependency-review", "Review dependency changes")
	if review.Uses != "actions/dependency-review-action@a1d282b36b6f3519aa1f3fc636f609c47dddb294" {
		t.Fatalf("dependency review action = %q", review.Uses)
	}
}

func TestScorecardWorkflowContract(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/scorecard.yml", &workflow)
	assertWorkflowDefaultDeniesToken(t, ".github/workflows/scorecard.yml")
	assertSecurityWorkflowPins(t, ".github/workflows/scorecard.yml")

	job := workflowJobByName(t, workflow.Jobs, "analysis")
	assertWorkflowJobPermissions(t, job, "Scorecard", map[string]string{
		"contents":        "read",
		"id-token":        "write",
		"security-events": "write",
	})
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, job, "Scorecard")

	analysis := workflowStepByName(t, workflow.Jobs, "analysis", "Run analysis")
	if analysis.With["results_file"] != "results.sarif" || analysis.With["results_format"] != "sarif" || analysis.With["publish_results"] != "true" {
		t.Fatalf("Scorecard analysis inputs = %#v", analysis.With)
	}
	upload := workflowStepByName(t, workflow.Jobs, "analysis", "Upload SARIF to code scanning")
	if upload.Uses != "github/codeql-action/upload-sarif@33d7b85da1a04171e211befe2c739303b9a4c066" || upload.With["sarif_file"] != "results.sarif" {
		t.Fatalf("Scorecard SARIF upload = uses %q with %#v", upload.Uses, upload.With)
	}

	text := readConfig(t, ".github/workflows/scorecard.yml")
	for _, fragment := range []string{
		"branch_protection_rule:",
		"cron: '20 7 * * 2'",
		"branches:\n      - main",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("scorecard workflow missing %q", fragment)
		}
	}
}

func assertWorkflowDefaultDeniesToken(t *testing.T, path string) {
	t.Helper()

	var workflow struct {
		Permissions map[string]string            `yaml:"permissions"`
		Jobs        map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, path, &workflow)
	if workflow.Permissions == nil || len(workflow.Permissions) != 0 {
		t.Fatalf("%s top-level permissions = %#v, want an explicit empty set", path, workflow.Permissions)
	}
	if len(workflow.Jobs) == 0 {
		t.Fatalf("%s has no jobs", path)
	}
	for name, job := range workflow.Jobs {
		if job.Permissions == nil {
			t.Fatalf("%s job %s does not set permissions and would inherit the workflow default", path, name)
		}
	}
}

func assertSecurityWorkflowPins(t *testing.T, path string) {
	t.Helper()

	text := readConfig(t, path)
	usesPattern := regexp.MustCompile(`(?m)^[ \t]*uses:[ \t]*(.+)$`)
	pinnedPattern := regexp.MustCompile(`\A[^\s@]+@[0-9a-f]{40} # v[0-9]+\.[0-9]+\.[0-9]+\z`)
	matches := usesPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		t.Fatalf("%s has no actions", path)
	}
	for _, match := range matches {
		uses := strings.TrimSpace(match[1])
		if strings.HasPrefix(uses, "./") {
			continue
		}
		if !pinnedPattern.MatchString(uses) {
			t.Fatalf("%s action %q must be a 40-character SHA with a version comment", path, uses)
		}
	}
}
