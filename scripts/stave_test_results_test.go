package scripts

import (
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestStaveTestResultsRequireExecutedPreviewAndRollback(t *testing.T) {
	t.Parallel()
	tests := []string{"TestStaveTUIFeatureFlagRendersAndQuitsInPTY", "TestStaveTUIResizeAndInterruptExitWithinBound", "TestStaveTUIProcessSignalsRestoreTerminal", "TestStaveTUIInteractiveNavigationFilterDetailAndHelp", "TestTUIWithoutStaveFlagUsesLegacyLinePath", "TestStaveTUIDumbTerminalErrorsAndHelpStayVisible"}
	const pkg = "github.com/ben-ranford/lopper/cmd/lopper"
	event := func(action, name, packageName string) string {
		value := map[string]string{"Action": action, "Package": packageName}
		if name != "" {
			value["Test"] = name
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(data) + "\n"
	}
	var passed string
	for _, name := range tests {
		passed += event("pass", name, pkg)
	}
	complete := passed + event("pass", "", pkg)
	cases := []struct {
		name, input string
		success     bool
	}{
		{"all executed", complete, true},
		{"empty selection", event("pass", "", pkg), false},
		{"skipped rollback", strings.Replace(complete, event("pass", tests[4], pkg), event("skip", tests[4], pkg), 1), false},
		{"missing preview", strings.Replace(complete, event("pass", tests[0], pkg), "", 1), false},
		{"other package", strings.ReplaceAll(complete, pkg, "example.com/other"), false},
		{"incomplete package", passed, false},
		{"failed package", passed + event("fail", "", pkg), false},
		{"invalid JSON", "not JSON\n", false},
		{"invalid event", "null\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("python3", repoPath(t, "scripts/check-stave-test-results.py"))
			cmd.Stdin = strings.NewReader(tc.input)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("result success=%v, want %v: %s (%v)", err == nil, tc.success, output, err)
			}
			if tc.name == "all executed" && string(output) != "Verified all 6 preview and legacy command tests executed and passed.\n" {
				t.Fatalf("default checker output changed: %q", output)
			}
		})
	}
}

func TestStaveParentCancellationTimingRequiresRepeatedElapsedEvidence(t *testing.T) {
	t.Parallel()
	validTimingWant := ""
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		validTimingWant = "timing evidence requires a native Linux or macOS runner"
	}
	valid := parentCancellationTimingFixture{
		repeats: 20, runEvents: 20, targetAction: "pass", targetElapsed: 0.05, targetElapsedPresent: true,
		packageAction: "pass", packageElapsed: 2.5, packageElapsedPresent: true,
	}
	cases := []struct {
		name    string
		fixture parentCancellationTimingFixture
		want    string
	}{
		{name: "twenty durations and package margin", fixture: valid, want: validTimingWant},
		{name: "missing iteration", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.repeats, f.runEvents = 19, 19 }), want: "ran 19 times"},
		{name: "extra iteration", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.repeats, f.runEvents = 21, 21 }), want: "ran 21 times"},
		{name: "missing run event", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.runEvents = 19 }), want: "ran 19 times"},
		{name: "skipped test", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.targetAction = "skip" }), want: "was skipped"},
		{name: "failed test", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.targetAction = "fail" }), want: "failed"},
		{name: "failed package", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.packageAction = "fail" }), want: "failed"},
		{name: "nonnumeric test elapsed", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.targetElapsed = "slow" }), want: "numeric elapsed"},
		{name: "boolean test elapsed", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.targetElapsed = true }), want: "numeric elapsed"},
		{name: "nonfinite test elapsed", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.nonFiniteTestElapsed = true }), want: "invalid elapsed"},
		{name: "negative test elapsed", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.targetElapsed = -0.1 }), want: "invalid elapsed"},
		{name: "missing test elapsed", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.targetElapsedPresent = false }), want: "numeric elapsed"},
		{name: "missing package completion", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.packageAction = "" }), want: "did not finish"},
		{name: "missing package elapsed", fixture: mutateParentTimingFixture(valid, func(f *parentCancellationTimingFixture) { f.packageElapsedPresent = false }), want: "numeric elapsed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertParentTimingCheckerCase(t, tc)
		})
	}
}

func assertParentTimingCheckerCase(t *testing.T, tc struct {
	name    string
	fixture parentCancellationTimingFixture
	want    string
}) {
	t.Helper()
	output, err := runParentTimingChecker(t, parentCancellationTimingInput(t, tc.fixture))
	if tc.want != "" {
		if err == nil || !strings.Contains(string(output), tc.want) {
			t.Fatalf("checker output = %q, want error containing %q (%v)", output, tc.want, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("checker rejected valid timing evidence: %s (%v)", output, err)
	}
	var summary parentCancellationTimingSummary
	if err := json.Unmarshal(output, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Package != "github.com/ben-ranford/lopper/internal/ui" || summary.RepeatCount != 20 || len(summary.SuccessfulElapsedSeconds) != 20 {
		t.Fatalf("timing summary identity/count = %+v", summary)
	}
	if summary.HostOS != "Linux" && summary.HostOS != "macOS" {
		t.Fatalf("timing summary host OS = %q", summary.HostOS)
	}
	if summary.MaximumTestElapsedSeconds != 0.05 || summary.PackageElapsedSeconds != 2.5 || summary.PackageTimeoutMarginSeconds != 297.5 {
		t.Fatalf("timing summary values = %+v", summary)
	}
}

type parentCancellationTimingSummary struct {
	Package                     string    `json:"package"`
	HostOS                      string    `json:"hostOS"`
	RepeatCount                 int       `json:"repeatCount"`
	SuccessfulElapsedSeconds    []float64 `json:"successfulElapsedSeconds"`
	MaximumTestElapsedSeconds   float64   `json:"maximumTestElapsedSeconds"`
	PackageElapsedSeconds       float64   `json:"packageElapsedSeconds"`
	PackageTimeoutMarginSeconds float64   `json:"packageTimeoutMarginSeconds"`
}

type parentCancellationTimingFixture struct {
	repeats, runEvents            int
	targetAction                  string
	targetElapsed, packageElapsed any
	targetElapsedPresent          bool
	packageAction                 string
	packageElapsedPresent         bool
	nonFiniteTestElapsed          bool
}

func mutateParentTimingFixture(fixture parentCancellationTimingFixture, mutate func(*parentCancellationTimingFixture)) parentCancellationTimingFixture {
	mutate(&fixture)
	return fixture
}

func parentCancellationTimingInput(t *testing.T, fixture parentCancellationTimingFixture) string {
	t.Helper()
	const pkg = "github.com/ben-ranford/lopper/internal/ui"
	const testName = "TestStavePreviewParentCancellationRestoresTerminalAndReturnsCause"
	var input strings.Builder
	for index := 0; index < fixture.repeats; index++ {
		if index < fixture.runEvents {
			input.WriteString(parentTimingEvent(t, "run", testName, pkg, nil, false))
		}
		input.WriteString(parentTimingEvent(t, fixture.targetAction, testName, pkg, fixture.targetElapsed, fixture.targetElapsedPresent))
	}
	if fixture.packageAction != "" {
		input.WriteString(parentTimingEvent(t, fixture.packageAction, "", pkg, fixture.packageElapsed, fixture.packageElapsedPresent))
	}
	result := input.String()
	if fixture.nonFiniteTestElapsed {
		result = strings.Replace(result, `"Elapsed":0.05`, `"Elapsed":NaN`, 1)
	}
	return result
}

func parentTimingEvent(t *testing.T, action, testName, pkg string, elapsed any, includeElapsed bool) string {
	t.Helper()
	value := map[string]any{"Action": action, "Package": pkg}
	if testName != "" {
		value["Test"] = testName
	}
	if includeElapsed {
		value["Elapsed"] = elapsed
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func runParentTimingChecker(t *testing.T, input string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("python3", repoPath(t, "scripts/check-stave-test-results.py"), "--parent-cancellation-timing")
	cmd.Stdin = strings.NewReader(input)
	return cmd.CombinedOutput()
}
