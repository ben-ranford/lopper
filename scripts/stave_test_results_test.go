package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
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

func TestStaveRestorationTimingRequiresNativeRepeatedEvidence(t *testing.T) {
	valid := restorationTimingInput(t)
	nativeError := ""
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		nativeError = "restoration timing evidence requires a native Linux or macOS runner"
	}
	root := "TestStaveRestorationWaitsForReaderAcrossShutdown"
	subtest := root + "/quit"
	rootPass := restorationTimingEvent(t, "pass", root, 0.05, true)
	rootRun := restorationTimingEvent(t, "run", root, nil, false)
	subtestPass := restorationTimingEvent(t, "pass", subtest, 0.01, true)
	signalRoot := restorationTimingRoots[1]
	sigterm := signalRoot + "/SIGTERM"
	sigint := signalRoot + "/SIGINT"
	sigtermRun := restorationTimingEvent(t, "run", sigterm, nil, false)
	sigintPass := restorationTimingEvent(t, "pass", sigint, 0.01, true)
	packagePass := restorationTimingEvent(t, "pass", "", 2.5, true)
	cases := []struct{ name, input, want string }{
		{"twenty root summaries", valid, nativeError},
		{"missing iteration", strings.Replace(valid, rootRun, "", 1), "ran 19 times"},
		{"extra iteration", valid + rootRun + rootPass, "ran 21 times"},
		{"root skipped", strings.Replace(valid, rootPass, restorationTimingEvent(t, "skip", root, 0.05, true), 1), "was skipped"},
		{"root failed", strings.Replace(valid, rootPass, restorationTimingEvent(t, "fail", root, 0.05, true), 1), "failed"},
		{"subtest skipped", strings.Replace(valid, subtestPass, restorationTimingEvent(t, "skip", subtest, 0.01, true), 1), "was skipped"},
		{"subtest failed", strings.Replace(valid, subtestPass, restorationTimingEvent(t, "fail", subtest, 0.01, true), 1), "failed"},
		{"missing all first-root children", omitRestorationSubtests(valid, root), "quit ran 0 times"},
		{"missing SIGTERM run", strings.Replace(valid, sigtermRun, "", 1), "SIGTERM ran 19 times"},
		{"missing SIGINT pass", strings.Replace(valid, sigintPass, "", 1), "SIGINT passed 19 times"},
		{"duplicate child repeat", valid + sigtermRun + restorationTimingEvent(t, "pass", sigterm, 0.01, true), "SIGTERM ran 21 times"},
		{"unexpected test", valid + restorationTimingEvent(t, "run", "TestStaveOther", nil, false), "unexpected UI test"},
		{"unexpected subtest", valid + restorationTimingEvent(t, "run", root+"/unexpected", nil, false), "unexpected UI test"},
		{"missing root duration", strings.Replace(valid, rootPass, restorationTimingEvent(t, "pass", root, nil, false), 1), "numeric elapsed"},
		{"nonnumeric root duration", strings.Replace(valid, rootPass, restorationTimingEvent(t, "pass", root, "slow", true), 1), "numeric elapsed"},
		{"boolean root duration", strings.Replace(valid, rootPass, restorationTimingEvent(t, "pass", root, true, true), 1), "numeric elapsed"},
		{"nonfinite root duration", strings.Replace(valid, `"Elapsed":0.05`, `"Elapsed":NaN`, 1), "invalid elapsed"},
		{"negative root duration", strings.Replace(valid, rootPass, restorationTimingEvent(t, "pass", root, -0.1, true), 1), "invalid elapsed"},
		{"missing package completion", strings.Replace(valid, packagePass, "", 1), "did not finish"},
		{"duplicate package completion", valid + packagePass, "exactly once"},
		{"failed package", valid + restorationTimingEvent(t, "fail", "", nil, false), "failed"},
		{"missing package duration", strings.Replace(valid, packagePass, restorationTimingEvent(t, "pass", "", nil, false), 1), "numeric elapsed"},
		{"package over timeout", strings.Replace(valid, packagePass, restorationTimingEvent(t, "pass", "", 301.0, true), 1), "exceeded the five-minute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertRestorationTimingCheckerCase(t, tc) })
	}
}

func TestStaveRestorationTimingRejectsMissingSubtestEvidence(t *testing.T) {
	input := omitRestorationSubtests(restorationTimingInput(t), restorationTimingRoots[0])
	output, err := runRestorationTimingChecker(t, input)
	if err == nil || !strings.Contains(string(output), "quit ran 0 times") {
		t.Fatalf("checker output = %q, want missing-child rejection (%v)", output, err)
	}
}

func assertRestorationTimingCheckerCase(t *testing.T, tc struct{ name, input, want string }) {
	t.Helper()
	output, err := runRestorationTimingChecker(t, tc.input)
	if tc.want != "" {
		if err == nil || !strings.Contains(string(output), tc.want) {
			t.Fatalf("checker output = %q, want error containing %q (%v)", output, tc.want, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("checker rejected valid restoration evidence: %s (%v)", output, err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d summaries, want 3: %s", len(lines), output)
	}
	wantPhaseBounds := []map[string]float64{
		{"readerCancellation": 15, "readerStopped": 15, "readerClose": 15},
		{"signalHelperSubprocess": 15},
		{},
	}
	for index, line := range lines {
		assertRestorationTimingSummary(t, line, index, wantPhaseBounds[index])
	}
}

func assertRestorationTimingSummary(t *testing.T, line string, index int, wantPhaseBounds map[string]float64) {
	t.Helper()
	var summary restorationTimingSummary
	if err := json.Unmarshal([]byte(line), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Package != "github.com/ben-ranford/lopper/internal/ui" || summary.Test != restorationTimingRoots[index] || summary.RepeatCount != 20 || len(summary.SuccessfulElapsedSeconds) != 20 {
		t.Fatalf("summary identity/count = %+v", summary)
	}
	if summary.HostOS != "Linux" && summary.HostOS != "macOS" {
		t.Fatalf("summary host OS = %q", summary.HostOS)
	}
	if summary.MaximumTestElapsedSeconds != 0.05 || summary.PackageElapsedSeconds != 2.5 || summary.PackageTimeoutMarginSeconds != 297.5 {
		t.Fatalf("summary timing = %+v", summary)
	}
	if !reflect.DeepEqual(summary.RestorationPhaseBoundsSeconds, wantPhaseBounds) {
		t.Fatalf("restoration phase bounds = %v, want %v", summary.RestorationPhaseBoundsSeconds, wantPhaseBounds)
	}
	if summary.Subtests == nil {
		t.Fatal("subtest summary is missing")
	}
	wantSubtests := restorationTimingSubtests[summary.Test]
	if len(summary.Subtests) != len(wantSubtests) {
		t.Fatalf("subtest summaries = %v, want %v children", summary.Subtests, len(wantSubtests))
	}
	for _, child := range wantSubtests {
		evidence := summary.Subtests[child]
		if evidence.RunCount != 20 || evidence.PassCount != 20 || len(evidence.SuccessfulElapsedSeconds) != 20 || evidence.MaximumTestElapsedSeconds != 0.01 {
			t.Fatalf("subtest %s evidence = %+v", child, evidence)
		}
	}
}

var restorationTimingRoots = []string{
	"TestStaveRestorationWaitsForReaderAcrossShutdown",
	"TestStaveRestorationSignalProcess",
	"TestStaveRestorationBeforeInputStart",
}

type restorationTimingSummary struct {
	Package                       string                                     `json:"package"`
	Test                          string                                     `json:"test"`
	HostOS                        string                                     `json:"hostOS"`
	RepeatCount                   int                                        `json:"repeatCount"`
	SuccessfulElapsedSeconds      []float64                                  `json:"successfulElapsedSeconds"`
	MaximumTestElapsedSeconds     float64                                    `json:"maximumTestElapsedSeconds"`
	PackageElapsedSeconds         float64                                    `json:"packageElapsedSeconds"`
	PackageTimeoutMarginSeconds   float64                                    `json:"packageTimeoutMarginSeconds"`
	RestorationPhaseBoundsSeconds map[string]float64                         `json:"restorationPhaseBoundsSeconds"`
	Subtests                      map[string]restorationSubtestTimingSummary `json:"subtests"`
}

type restorationSubtestTimingSummary struct {
	RunCount                  int       `json:"runCount"`
	PassCount                 int       `json:"passCount"`
	SuccessfulElapsedSeconds  []float64 `json:"successfulElapsedSeconds"`
	MaximumTestElapsedSeconds float64   `json:"maximumTestElapsedSeconds"`
}

func restorationTimingInput(t *testing.T) string {
	t.Helper()
	var input strings.Builder
	for _, root := range restorationTimingRoots {
		for range 20 {
			input.WriteString(restorationTimingEvent(t, "run", root, nil, false))
			for _, child := range restorationTimingSubtests[root] {
				testName := root + "/" + child
				input.WriteString(restorationTimingEvent(t, "run", testName, nil, false))
				input.WriteString(restorationTimingEvent(t, "pass", testName, 0.01, true))
			}
			input.WriteString(restorationTimingEvent(t, "pass", root, 0.05, true))
		}
	}
	input.WriteString(restorationTimingEvent(t, "pass", "", 2.5, true))
	return input.String()
}

func restorationTimingEvent(t *testing.T, action, testName string, elapsed any, includeElapsed bool) string {
	t.Helper()
	value := map[string]any{"Action": action, "Package": "github.com/ben-ranford/lopper/internal/ui"}
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

func runRestorationTimingChecker(t *testing.T, input string) ([]byte, error) {
	t.Helper()
	checker := os.Getenv("LOPPER_STAVE_TEST_RESULTS_CHECKER")
	if checker == "" {
		checker = repoPath(t, "scripts/check-stave-test-results.py")
	}
	cmd := exec.Command("python3", checker, "--restoration-timing")
	cmd.Stdin = strings.NewReader(input)
	return cmd.CombinedOutput()
}

func omitRestorationSubtests(input, root string) string {
	var output strings.Builder
	prefix := `"Test":"` + root + "/"
	for _, line := range strings.Split(strings.TrimSpace(input), "\n") {
		if !strings.Contains(line, prefix) {
			output.WriteString(line + "\n")
		}
	}
	return output.String()
}

var restorationTimingSubtests = map[string][]string{
	"TestStaveRestorationWaitsForReaderAcrossShutdown": {"quit", "parent-cancel", "program-error", "close-error"},
	"TestStaveRestorationSignalProcess":                {"SIGINT", "SIGTERM"},
	"TestStaveRestorationBeforeInputStart":             {},
}
