package scripts

import (
	"strings"
	"testing"
)

func TestCIWorkflowChecksExistingCoverageProfiles(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "verify-checks")
	assertWorkflowStepOrder(t, job, "Run CI target", "Run coverage gate")
	step := workflowStepByName(t, workflow.Jobs, "verify-checks", "Run coverage gate")
	if strings.Contains(step.Run, "make cov ") || strings.Count(step.Run, "make cov-check ") != 2 {
		t.Fatalf("coverage gate must reuse profiles with make cov-check in both execution paths:\n%s", step.Run)
	}
	for _, name := range []string{"COVERAGE_MIN", "COVERAGE_PACKAGE_MIN"} {
		if step.Env[name] != "98" {
			t.Errorf("coverage gate %s = %q, want 98", name, step.Env[name])
		}
	}
	for _, required := range []string{"coverage-status.txt", "coverage-total.txt", "coverage-package-failures.txt"} {
		if !strings.Contains(step.Run, required) {
			t.Errorf("coverage gate step missing %q", required)
		}
	}
}
