package cli

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	unexpectedErrFmt            = "unexpected error: %v"
	unexpectedValidationErrFmt  = "unexpected validation error: %v"
	modeMismatchFmt             = "expected mode %q, got %q"
	languageFlagName            = "--language"
	includeFlagName             = "--include"
	excludeFlagName             = "--exclude"
	suggestOnlyFlag             = "--suggest-only"
	applyCodemodFlag            = "--apply-codemod"
	applyCodemodConfirmFlag     = "--apply-codemod-confirm"
	allowDirtyFlag              = "--allow-dirty"
	failAliasFlag               = "--fail-on-increase"
	thresholdFailFlag           = "--threshold-fail-on-increase"
	thresholdLowWarnFlag        = "--threshold-low-confidence-warning"
	scoreWeightFlag             = "--score-weight-usage"
	lockfileDriftPolicyFlagName = "--lockfile-drift-policy"
	scopeGoGlob                 = "src/**/*.go"
	scopeExcludeTestGlob        = "**/*_test.go"
	scopeVendorGlob             = "vendor/**"
	scopeAnalyseGoGlobs         = "src/**/*.go,internal/**/*.go"
	scopeIncludeCombined        = "src/**/*.go,internal/**/*.go,cmd/**/*.go"
	parseConfigFileName         = ".lopper.yml"
	repoFlagName                = "--repo"
	dashboardReposFlagName      = "--repos"
	dashboardOutputFlagName     = "--output"
	formatFlagName              = "--format"
	dashboardFormatFlagName     = formatFlagName
	dashboardConfigFlagName     = "--config"
	dashboardConfigFileName     = "lopper-org.yml"
	dashboardReportCSVFileName  = "report.csv"
	notifyOnFlag                = "--notify-on"
)

func mustParseArgs(t *testing.T, args []string) app.Request {
	t.Helper()

	req, err := ParseArgs(args)
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	return req
}

func expectParseArgsError(t *testing.T, args []string, wantMsg string) error {
	t.Helper()

	_, err := ParseArgs(args)
	if err == nil {
		t.Fatal(wantMsg)
	}
	return err
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	testutil.MustWriteFile(t, path, contents)
}
