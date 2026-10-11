package scripts

import (
	"strings"
	"testing"
)

func TestSuppressionTrackingDeadlineLeavesPublicationMargin(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")
	start := strings.Index(gate.Run, "job_deadline_ms=")
	if start < 0 {
		t.Fatal("missing shared suppression deadline")
	}
	script := `set -e
fingerprints=(first second)
date() { printf '%s\n' "$FIXTURE_NOW"; }
gh() { printf '%s\n' 42; }
sleep() { echo 'unexpected polling sleep' >&2; exit 97; }
trap 'printf "deadline=%s\n" "$job_deadline_ms"' EXIT
` + gate.Run[start:]
	for _, fixture := range []struct {
		name, now, message string
		success            bool
	}{
		{"published-before-deadline", "1109999", "Verified a trusted tracking issue", true},
		{"deadline-with-missing-records", "1110000", "No trusted inline suppression tracking issue", false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			output, err := runShellCommand(t.TempDir(), script, map[string]string{"JOB_START_MS": "0", "FIXTURE_NOW": fixture.now, "PR_NUMBER": "1749"})
			if (err == nil) != fixture.success || !strings.Contains(output, fixture.message) || !strings.Contains(output, "deadline=1110000") {
				t.Fatalf("unexpected deadline outcome: %v\n%s", err, output)
			}
		})
	}
	var timeout struct {
		Jobs map[string]struct {
			Minutes int `yaml:"timeout-minutes"`
		} `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/ci.yml", &timeout)
	if margin := timeout.Jobs["verify"].Minutes*60 - 1110; margin < 90 {
		t.Fatalf("suppression deadline leaves only %d seconds to publish the result", margin)
	}
}
