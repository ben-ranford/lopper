package scripts

import (
	"regexp"
	"strings"
	"testing"
)

func TestParserFuzzWorkflowContract(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/parser-fuzz.yml", &workflow)

	job := workflowJobByName(t, workflow.Jobs, "discover")
	assertWorkflowJobPermissions(t, job, "parser fuzz discovery", map[string]string{"contents": "read"})
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, job, "parser fuzz discovery")
	assertWorkflowStepOrder(t, job, "Checkout", "Setup Go", "Run bounded parser fuzzing", "Upload discovered parser fuzz regressions")

	upload := workflowStepByName(t, workflow.Jobs, "discover", "Upload discovered parser fuzz regressions")
	if upload.Uses != "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" {
		t.Fatalf("parser fuzz upload action = %q, want immutable upload-artifact action", upload.Uses)
	}
	if upload.If != "${{ failure() }}" {
		t.Fatalf("parser fuzz upload condition = %q, want failure-only upload", upload.If)
	}

	workflowText := readConfig(t, ".github/workflows/parser-fuzz.yml")
	for _, fragment := range []string{
		"workflow_dispatch:",
		"schedule:",
		"cron: '17 4 * * 3'",
		"timeout-minutes: 15",
		"actions/checkout@v7",
		"actions/setup-go@v7",
		"go test ./internal/lang/golang -run '^$' -fuzz '^FuzzParseModFile$' -fuzztime=20s -parallel=1",
		"go test ./internal/lang/cpp -run '^$' -fuzz '^FuzzParseIncludes$' -fuzztime=20s -parallel=1",
		"go test ./internal/lang/swift -run '^$' -fuzz '^FuzzParseCarthageDependencies$' -fuzztime=20s -parallel=1",
		"name: parser-fuzz-regressions",
		"path: internal/lang/*/testdata/fuzz/**",
		"if-no-files-found: warn",
	} {
		if !strings.Contains(workflowText, fragment) {
			t.Fatalf("parser fuzz workflow missing %q", fragment)
		}
	}

	assertWorkflowJobOmitsText(t, job, "secrets.", "parser fuzz discovery must be secretless")
	if regexp.MustCompile(`\bsecrets\s*\[`).MatchString(workflowText) {
		t.Fatal("parser fuzz discovery must be secretless")
	}
	assertWorkflowJobOmitsText(t, job, "github.token", "parser fuzz discovery must not use github.token")
}
