package scripts

import (
	"strings"
	"testing"
)

func TestParserFuzzWorkflowContract(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/parser-fuzz.yml", &workflow)

	job := workflowJobByName(t, workflow.Jobs, "discover")
	assertWorkflowJobPermissions(t, job, "parser fuzz discovery", map[string]string{"contents": "read"})
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, job, "parser fuzz discovery")
	assertWorkflowStepOrder(t, job, "Checkout", "Setup Go", "Run bounded parser fuzzing")

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
	} {
		if !strings.Contains(workflowText, fragment) {
			t.Fatalf("parser fuzz workflow missing %q", fragment)
		}
	}

	assertWorkflowJobOmitsText(t, job, "secrets.", "parser fuzz discovery must be secretless")
	assertWorkflowJobOmitsText(t, job, "github.token", "parser fuzz discovery must not use github.token")
}
