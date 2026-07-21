package cli

import (
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestParseArgsAdvisorySync(t *testing.T) {
	req := mustParseArgs(t, []string{
		"advisory",
		"sync",
		"osv",
		"--cache-path", ".lopper/advisories",
		"--source-url", "https://example.test/osv.zip",
		"--output", "sync.json",
		"-o", "sync.json",
		"--enable-feature", report.AdvisoryOSVSyncPreviewFeature,
	})

	if req.Mode != app.ModeAdvisory {
		t.Fatalf(modeMismatchFmt, app.ModeAdvisory, req.Mode)
	}
	if req.Advisory.Command != "sync" || req.Advisory.Provider != "osv" || req.Advisory.SourceURL != "https://example.test/osv.zip" {
		t.Fatalf("unexpected advisory sync request: %#v", req.Advisory)
	}
	if req.Advisory.CachePath != ".lopper/advisories" || req.Advisory.OutputPath != "sync.json" {
		t.Fatalf("unexpected advisory paths: %#v", req.Advisory)
	}
	if !req.Advisory.Features.Enabled(report.AdvisoryOSVSyncPreviewFeature) {
		t.Fatalf("expected advisory preview feature to be enabled")
	}
}

func TestParseArgsAdvisoryStatus(t *testing.T) {
	req := mustParseArgs(t, []string{
		"advisory",
		"status",
		"--cache-path", ".lopper/advisories",
		"--enable-feature", report.AdvisoryOSVSyncPreviewFeature,
	})

	if req.Mode != app.ModeAdvisory || req.Advisory.Command != "status" || req.Advisory.Provider != "osv" {
		t.Fatalf("unexpected advisory status request: %#v", req)
	}
	if req.Advisory.SourceURL != "" {
		t.Fatalf("expected status not to set source URL, got %#v", req.Advisory)
	}
}

func TestParseArgsAdvisoryValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing subcommand", args: []string{"advisory"}, want: "requires sync or status"},
		{name: "unknown subcommand", args: []string{"advisory", "prune"}, want: "unknown advisory command"},
		{name: "wrong provider", args: []string{"advisory", "sync", "ghsa"}, want: "requires provider osv"},
		{name: "missing cache path", args: []string{"advisory", "status", "--cache-path", ""}, want: "--cache-path is required"},
		{name: "unexpected sync arg", args: []string{"advisory", "sync", "osv", "--cache-path", "cache", "extra"}, want: "unexpected arguments"},
		{name: "unexpected status arg", args: []string{"advisory", "status", "--cache-path", "cache", "extra"}, want: "unexpected arguments"},
		{name: "sync flag parse error", args: []string{"advisory", "sync", "osv", "--unknown"}, want: "flag provided but not defined"},
		{name: "status flag parse error", args: []string{"advisory", "status", "--unknown"}, want: "flag provided but not defined"},
		{name: "output conflict", args: []string{"advisory", "status", "--cache-path", "cache", "--output", "a.json", "-o", "b.json"}, want: "must match"},
		{name: "unknown feature", args: []string{"advisory", "status", "--cache-path", "cache", "--enable-feature", "missing-feature"}, want: "unknown feature"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := expectParseArgsError(t, tc.args, "expected advisory validation error")
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error to contain %q, got %v", tc.want, err)
			}
		})
	}
}
