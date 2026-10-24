package scripts

import (
	"regexp"
	"strings"
	"testing"
)

func TestMakefileCoverageTargetContract(t *testing.T) {
	t.Parallel()

	makefile := readConfig(t, "Makefile")
	covPattern := regexp.MustCompile(`(?m)^cov:[^\n]*\n(?:\t.*\n)+`)
	covTarget := covPattern.FindString(makefile)
	if covTarget == "" {
		t.Fatal("Makefile must define the cov target")
	}
	if !strings.Contains(covTarget, `cov-produce`) || !strings.Contains(covTarget, `@$(MAKE) cov-check`) {
		t.Fatalf("cov target must run the producer before the reusable checker, got:\n%s", covTarget)
	}

	covProducerPattern := regexp.MustCompile(`(?m)^cov-produce:\n(?:\t.*\n)+`)
	covProducer := covProducerPattern.FindString(makefile)
	if covProducer == "" {
		t.Fatal("Makefile must define the cov-produce target")
	}
	for _, want := range []string{
		`grep -Ev '/internal/app$$|/internal/testutil$$|/internal/testsupport$$|/tools/benchdelta$$'`,
		`mkdir -p $$(dirname "$(COVERAGE_DEFAULT_FILE)")`,
		`-coverprofile="$(COVERAGE_DEFAULT_FILE)"`,
		`mkdir -p $$(dirname "$(COVERAGE_FILE)")`,
		`awk 'NR==1 || FNR>1 { print }' "$(COVERAGE_DEFAULT_FILE)"`,
		`awk 'FNR>1 { print }' "$(COVERAGE_LOCKFILEDRIFT_HEAD_FILE)"`,
		`} > "$(COVERAGE_FILE)"`,
	} {
		if !strings.Contains(covProducer, want) {
			t.Fatalf("cov-produce target must contain %q", want)
		}
	}

	covCheckPattern := regexp.MustCompile(`(?m)^cov-check:\n(?:\t.*\n)+`)
	covCheck := covCheckPattern.FindString(makefile)
	if covCheck == "" {
		t.Fatal("Makefile must define the cov-check target")
	}
	for _, want := range []string{
		`mkdir -p .artifacts`,
		`$(GO_CMD) run ./tools/coveragegate`,
		`-coverprofile="$(COVERAGE_FILE)"`,
		`-min="$(COVERAGE_MIN)"`,
		`-package-min="$(COVERAGE_PACKAGE_MIN)"`,
		`-total-out=".artifacts/coverage-total.txt"`,
		`-packages-out=".artifacts/coverage-packages.txt"`,
		`-package-failures-out=".artifacts/coverage-package-failures.txt"`,
	} {
		if !strings.Contains(covCheck, want) {
			t.Fatalf("cov-check target must contain %q", want)
		}
	}
}

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
