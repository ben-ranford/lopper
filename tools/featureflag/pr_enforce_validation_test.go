package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRunPREnforceRejectsInvalidEntriesAfterEarlyDuplicate(t *testing.T) {
	for _, duplicate := range []string{"code", "name"} {
		for _, tc := range []struct {
			name   string
			mutate func(*featureflags.Flag)
			want   string
		}{
			{"name", func(f *featureflags.Flag) { f.Name = "invalid name" }, "invalid feature name"},
			{"name markdown", func(f *featureflags.Flag) { f.Name = "flag`\n## injected" }, "invalid feature name"},
			{"code", func(f *featureflags.Flag) { f.Code = "BAD-0001" }, "invalid feature code"},
			{"code markdown", func(f *featureflags.Flag) { f.Code = "code`\n## injected" }, "invalid feature code"},
			{"lifecycle", func(f *featureflags.Flag) { f.Lifecycle = "unknown" }, "invalid feature lifecycle"},
			{"lifecycle markdown", func(f *featureflags.Flag) { f.Lifecycle = "preview`\n## injected" }, "invalid feature lifecycle"},
			{"deprecated name", func(f *featureflags.Flag) { f.DeprecatedNames = []string{"alias`\n## injected"} }, "invalid feature name"},
			{"stable release", func(f *featureflags.Flag) { f.FirstStableRelease = "v1.0.0`\n## injected" }, "invalid first stable release"},
		} {
			t.Run(duplicate+"/"+tc.name, func(t *testing.T) {
				flags := earlyDuplicateCatalogFlags(duplicate)
				tc.mutate(&flags[2])
				assertLaterCatalogEntryRejected(t, flags, tc.want)
			})
		}
	}
}

func earlyDuplicateCatalogFlags(duplicate string) []featureflags.Flag {
	flags := []featureflags.Flag{
		{Code: "LOP-FEAT-0001", Name: "first-flag", Lifecycle: featureflags.LifecyclePreview},
		{Code: "LOP-FEAT-0002", Name: "second-flag", Lifecycle: featureflags.LifecyclePreview},
		{Code: "LOP-FEAT-0001", Name: "first-flag", Lifecycle: featureflags.LifecyclePreview},
	}
	if duplicate == "code" {
		flags[1].Code = flags[0].Code
	} else {
		flags[1].Name = flags[0].Name
	}
	return flags
}

func assertLaterCatalogEntryRejected(t *testing.T, flags []featureflags.Flag, want string) {
	t.Helper()
	data, err := json.Marshal(flags)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeFeatureCatalog(t, root, string(data))
	previous := "previous-features.json"
	testutil.MustWriteFile(t, filepath.Join(root, previous), "[]")
	t.Chdir(root)

	output, err := captureStdout(t, func() error {
		return run([]string{"pr-enforce", "--pr-title", "fix(flags): validate catalog", "--previous-catalog", previous})
	})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected later entry validation error %q, got %v; report: %s", want, err, output)
	}
	if output != "" {
		t.Fatalf("invalid catalog must not produce a Markdown report, got %s", output)
	}
}

func TestPREnforcementPreservesValidDuplicateDiagnosticsAfterValidation(t *testing.T) {
	root := t.TempDir()
	writeFeatureCatalog(t, root, `[
		{"code":" LOP-FEAT-0001 ","name":" first-flag ","lifecycle":"experimental"},
		{"code":"LOP-FEAT-0001","name":"second-flag","lifecycle":"done"},
		{"code":"LOP-FEAT-0002","name":"second-flag","lifecycle":"preview"}
	]`)
	flags, violations, err := readCurrentCatalogForPREnforcement(root)
	if err != nil {
		t.Fatalf("valid entries must retain duplicate diagnostics, got %v", err)
	}
	if len(flags) != 0 || len(violations) != 2 {
		t.Fatalf("expected both duplicate diagnostics and no usable catalog, got flags=%#v violations=%#v", flags, violations)
	}
	report := formatPREnforcementReport(evaluatePREnforcement("fix(flags): validate catalog", flags, nil, violations))
	for _, want := range []string{
		"Check: failed",
		"Feature flag ids (`code`) must be unique: `LOP-FEAT-0001` is used by `first-flag`, `second-flag`.",
		"Feature flag names must be unique: `second-flag` is used by `LOP-FEAT-0001`, `LOP-FEAT-0002`.",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("expected report to contain %q, got %s", want, report)
		}
	}
}
