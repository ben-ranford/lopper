package scripts

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestCIMakeTargetsRetainAndPartitionTheCIPrerequisiteGraph(t *testing.T) {
	t.Parallel()

	const runtimePycacheCheck = "runtime-pycache-check"
	wantCI := []string{
		"fuzz-corpus-check", "benchdelta-cov", "automation-integrity", "format-check", "mod-check",
		"feature-flag-check", "lint", "actionlint", "shellcheck", "dup-check", "suppression-check",
		"security", "vuln-check", "test", "test-leaks", "test-race", "bench-gate", "build", "cov",
		runtimePycacheCheck,
	}
	wantTests := []string{"test", "test-leaks", runtimePycacheCheck}
	wantChecks := []string{
		"fuzz-corpus-check", "benchdelta-cov", "automation-integrity", "format-check", "mod-check",
		"feature-flag-check", "lint", "actionlint", "shellcheck", "dup-check", "suppression-check",
		"security", "vuln-check", "test-race", "bench-gate", "build", "cov", runtimePycacheCheck,
	}

	for _, tc := range []struct {
		name string
		want []string
	}{
		{name: "ci", want: wantCI},
		{name: "ci-tests", want: wantTests},
		{name: "ci-checks", want: wantChecks},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := makePrerequisites(t, tc.name, nil); !sameStrings(got, tc.want) {
				t.Fatalf("make %s prerequisites = %q, want %q", tc.name, got, tc.want)
			}
		})
	}

	combined := append(append([]string{}, wantTests...), wantChecks...)
	counts := make(map[string]int)
	for _, target := range combined {
		counts[target]++
	}
	for _, target := range wantCI {
		wantCount := 1
		if target == runtimePycacheCheck {
			wantCount = 2
		}
		if counts[target] != wantCount {
			t.Fatalf("partition count for %s = %d, want %d", target, counts[target], wantCount)
		}
	}
}

func TestCIMakePartitionsCannotBeOverridden(t *testing.T) {
	t.Parallel()

	overrides := map[string]string{
		"CI_TEST_TARGETS":  "skipped-test",
		"CI_CHECK_TARGETS": "skipped-check",
	}
	if got := makePrerequisites(t, "ci-tests", overrides); !sameStrings(got, []string{"test", "test-leaks", "runtime-pycache-check"}) {
		t.Fatalf("ci-tests prerequisites changed through overrides: %q", got)
	}
	if got := makePrerequisites(t, "ci-checks", overrides); strings.Contains(strings.Join(got, " "), "skipped-") {
		t.Fatalf("ci-checks prerequisites changed through overrides: %q", got)
	}
}

func makePrerequisites(t *testing.T, target string, overrides map[string]string) []string {
	t.Helper()

	// Do not name the target here: -n still allows recursive $(MAKE) commands
	// for named goals to run. The make database contains every target without
	// invoking a recursive CI prerequisite.
	cmd := exec.Command("make", "-prRn")
	cmd.Dir = repoPath(t, ".")
	cmd.Env = makeTestEnvironment(overrides)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read make database for %s: %v\n%s", target, err, output)
	}

	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `:\s*(.*)$`)
	match := pattern.FindStringSubmatch(string(output))
	if len(match) == 0 {
		t.Fatalf("make database has no %s target", target)
	}
	return strings.Fields(match[1])
}

func makeTestEnvironment(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides)+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "MAKEFLAGS=") ||
			strings.HasPrefix(entry, "MAKEOVERRIDES=") ||
			strings.HasPrefix(entry, "CI_TEST_TARGETS=") ||
			strings.HasPrefix(entry, "CI_CHECK_TARGETS=") {
			continue
		}
		environment = append(environment, entry)
	}
	// Exercise make's strongest environment precedence while keeping inherited
	// flags and propagated overrides from altering this graph assertion.
	environment = append(environment, "MAKEFLAGS=-e", "MAKEOVERRIDES=CI_TEST_TARGETS=skipped-test CI_CHECK_TARGETS=skipped-check")
	for name, value := range overrides {
		environment = append(environment, name+"="+value)
	}
	return environment
}

func sameStrings(got, want []string) bool {
	return strings.Join(got, "\x00") == strings.Join(want, "\x00")
}
