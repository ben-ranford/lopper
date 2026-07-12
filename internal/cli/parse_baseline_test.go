package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
)

func TestParseBaselineList(t *testing.T) {
	t.Parallel()

	req := mustParseArgs(t, []string{
		"baseline", "list",
		"--store", "./snapshots",
		"--baseline-store", "./snapshots",
		"--format", "json",
		"--limit", "7",
	})
	if req.Mode != app.ModeBaseline || req.Baseline.Action != "list" {
		t.Fatalf("unexpected baseline mode/action: %#v", req.Baseline)
	}
	if req.Baseline.StorePath != "./snapshots" || req.Baseline.Format != "json" || req.Baseline.Limit != 7 {
		t.Fatalf("unexpected baseline list options: %#v", req.Baseline)
	}
	if !req.Baseline.Features.Enabled(app.BaselineStoreDiscoveryFeature) {
		t.Fatalf("expected stable baseline discovery default")
	}
}

func TestParseBaselineShowAndDefaults(t *testing.T) {
	t.Parallel()

	req := mustParseArgs(t, []string{"baseline", "show", "label:nightly", "--format", "table"})
	if req.Mode != app.ModeBaseline || req.Baseline.Action != "show" || req.Baseline.Key != "label:nightly" {
		t.Fatalf("unexpected baseline show request: %#v", req.Baseline)
	}
	if req.Baseline.StorePath != ".artifacts/lopper-baselines" || req.Baseline.Limit != 50 {
		t.Fatalf("unexpected baseline defaults: %#v", req.Baseline)
	}
	if !req.Baseline.Features.Enabled(app.BaselineStoreDiscoveryFeature) {
		t.Fatalf("expected stable baseline discovery default")
	}

	disabled := mustParseArgs(t, []string{
		"baseline", "show", "label:nightly",
		"--disable-feature", app.BaselineStoreDiscoveryFeature,
	})
	if disabled.Baseline.Features.Enabled(app.BaselineStoreDiscoveryFeature) {
		t.Fatalf("expected explicit disable to override baseline discovery default")
	}
}

func TestParseBaselineDeprecatedAliasPreservesDisableRollback(t *testing.T) {
	t.Parallel()

	const deprecatedName = "baseline-store-discovery-preview"
	req := mustParseArgs(t, []string{
		"baseline", "list",
		"--disable-feature", deprecatedName,
	})
	if req.Baseline.Features.Enabled(app.BaselineStoreDiscoveryFeature) || req.Baseline.Features.Enabled(deprecatedName) {
		t.Fatalf("expected deprecated alias to disable canonical baseline discovery feature")
	}
	warnings := req.Baseline.Features.DeprecationWarnings()
	want := `feature flag "baseline-store-discovery-preview" is deprecated; use "baseline-store-discovery" instead`
	if len(warnings) != 1 || warnings[0] != want {
		t.Fatalf("expected exact baseline discovery deprecation warning, got %#v", warnings)
	}
}

func TestParseBaselineValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"baseline", "unknown"}, want: "unknown baseline command"},
		{args: []string{"baseline", "list", "extra"}, want: "too many arguments"},
		{args: []string{"baseline", "list", "--limit", "0"}, want: "--limit must be greater than zero"},
		{args: []string{"baseline", "list", "--format", "yaml"}, want: "invalid baseline format"},
		{args: []string{"baseline", "list", "--store", "one", "--baseline-store", "two"}, want: "must match"},
		{args: []string{"baseline", "show"}, want: "requires a snapshot key"},
		{args: []string{"baseline", "show", "  "}, want: "requires a snapshot key"},
		{args: []string{"baseline", "show", "one", "two"}, want: "too many arguments"},
		{args: []string{"baseline", "show", "key", "--store"}, want: "flag needs an argument"},
	}
	for _, tc := range tests {
		if _, err := ParseArgs(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("ParseArgs(%v) error=%v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestParseBaselineHelp(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"baseline"}, {"baseline", "help"}, {"baseline", "list", "--help"}, {"baseline", "show", "--help"}} {
		if _, err := ParseArgs(args); !errors.Is(err, ErrHelpRequested) {
			t.Fatalf("ParseArgs(%v) error=%v, want help", args, err)
		}
	}
}
