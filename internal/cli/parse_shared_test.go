package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPatternListFlagSetMergesAndDedupes(t *testing.T) {
	flagValue := newPatternListFlag([]string{scopeGoGlob})
	if err := flagValue.Set("internal/**/*.go," + scopeGoGlob); err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if err := flagValue.Set("cmd/**/*.go"); err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if got := strings.Join(flagValue.Values(), ","); got != scopeIncludeCombined {
		t.Fatalf("unexpected merged pattern list: %q", got)
	}
	if flagValue.String() != scopeIncludeCombined {
		t.Fatalf("unexpected pattern list string form: %q", flagValue.String())
	}
}

func TestResolveScopePatternsUsesConfigWhenFlagNotVisited(t *testing.T) {
	configValues := []string{scopeGoGlob}
	got := resolveScopePatterns(map[string]bool{}, "include", []string{"ignored/**/*.go"}, configValues)
	if strings.Join(got, ",") != scopeGoGlob {
		t.Fatalf("expected config scope values when include flag not visited, got %q", strings.Join(got, ","))
	}
}

func TestResolveScopePatternsVisitedWithEmptyCLIValuesReturnsNil(t *testing.T) {
	got := resolveScopePatterns(map[string]bool{"include": true}, "include", nil, []string{scopeGoGlob})
	if len(got) != 0 {
		t.Fatalf("expected nil/empty scope patterns when include flag is visited with no values, got %#v", got)
	}
}

func TestMergePatternsWithEmptyNextKeepsExisting(t *testing.T) {
	existing := []string{scopeGoGlob}
	merged := mergePatterns(existing, nil)
	if strings.Join(merged, ",") != scopeGoGlob {
		t.Fatalf("expected merge with empty next to preserve existing patterns, got %#v", merged)
	}
}

func TestMergePatternsSkipsDuplicatesAlreadySeen(t *testing.T) {
	merged := mergePatterns([]string{scopeGoGlob}, []string{scopeGoGlob, "internal/**/*.go"})
	if strings.Join(merged, ",") != scopeAnalyseGoGlobs {
		t.Fatalf("expected duplicates to be skipped, got %#v", merged)
	}
}

func TestSplitPatternListSkipsEmptyAndDuplicateEntries(t *testing.T) {
	if got := splitPatternList(" , " + scopeGoGlob + ", " + scopeGoGlob + " , "); !reflect.DeepEqual(got, []string{scopeGoGlob}) {
		t.Fatalf("expected split pattern list to keep one trimmed value, got %#v", got)
	}
	if got := splitPatternList(" , , "); len(got) != 0 {
		t.Fatalf("expected nil pattern list when all values are blank, got %#v", got)
	}
}

func TestNormalizeArgsAndFlagNeedsValue(t *testing.T) {
	args, err := normalizeArgs([]string{"lodash", "--top", "5", "--format=json", "--", "--literal"})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if len(args) == 0 {
		t.Fatalf("expected normalized args")
	}
	if !flagNeedsValue(thresholdFailFlag) {
		t.Fatalf("expected threshold flag to require value")
	}
	if !flagNeedsValue(scoreWeightFlag) {
		t.Fatalf("expected score weight flag to require value")
	}
	if flagNeedsValue("--format=json") {
		t.Fatalf("expected equals-form flag not to require separate value")
	}
	if flagNeedsValue("--unknown-flag") {
		t.Fatalf("did not expect unknown flag to be treated as requiring value")
	}
}

func TestNormalizeArgsMissingFlagValueReturnsError(t *testing.T) {
	_, err := normalizeArgs([]string{"lodash", "--config"})
	if err == nil {
		t.Fatalf("expected missing flag value error")
	}
	if !strings.Contains(err.Error(), "flag needs an argument: -config") {
		t.Fatalf(unexpectedValidationErrFmt, err)
	}
}

func TestParseArgsErrorsAndHelp(t *testing.T) {
	const helpFlag = "--help"

	if _, err := ParseArgs([]string{"help"}); !errors.Is(err, ErrHelpRequested) {
		t.Fatalf("expected top-level help request error, got %v", err)
	}
	if _, err := ParseArgs([]string{"analyse", helpFlag}); !errors.Is(err, ErrHelpRequested) {
		t.Fatalf("expected analyse help request error, got %v", err)
	}
	if _, err := ParseArgs([]string{"tui", helpFlag}); !errors.Is(err, ErrHelpRequested) {
		t.Fatalf("expected tui help request error, got %v", err)
	}
	if _, err := ParseArgs([]string{"unknown"}); err == nil {
		t.Fatalf("expected unknown command error")
	}
}

func TestIsVersionArg(t *testing.T) {
	if !isVersionArg([]string{"--version"}) {
		t.Fatalf("expected --version to be recognized")
	}
	if isVersionArg([]string{"version"}) {
		t.Fatalf("did not expect bare version token to be recognized")
	}
	if isVersionArg([]string{"--version", "--help"}) {
		t.Fatalf("did not expect mixed args to be recognized as a version request")
	}
}

func TestParseArgsFlagParseAndConfigLoadErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "analyse_top_missing_value", args: []string{"analyse", "--top"}},
		{name: "analyse_invalid_format", args: []string{"analyse", "dep", formatFlagName, "invalid"}},
		{name: "analyse_missing_config", args: []string{"analyse", "--top", "1", "--config", "missing-config.yml"}},
		{name: "tui_top_missing_value", args: []string{"tui", "--top"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseArgs(tc.args); err == nil {
				t.Fatalf("expected parse/config error")
			}
		})
	}
}
