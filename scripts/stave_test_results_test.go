package scripts

import (
	"encoding/json"
	"os/exec"
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
		})
	}
}
