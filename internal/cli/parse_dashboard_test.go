package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
)

func TestParseArgsDashboardRepos(t *testing.T) {
	req := mustParseArgs(t, []string{
		"dashboard",
		dashboardReposFlagName, "./api, ./frontend,./api/,api/..//api",
		dashboardFormatFlagName, "html",
		"--top", "25",
		languageFlagName, "all",
		dashboardOutputFlagName, "org-report.html",
	})

	if req.Mode != app.ModeDashboard {
		t.Fatalf(modeMismatchFmt, app.ModeDashboard, req.Mode)
	}
	if len(req.Dashboard.Repos) != 2 {
		t.Fatalf("expected two repos after dedupe, got %#v", req.Dashboard.Repos)
	}
	if req.Dashboard.Repos[0].Path != filepath.Clean("./api") || req.Dashboard.Repos[1].Path != filepath.Clean("./frontend") {
		t.Fatalf("unexpected dashboard repo paths: %#v", req.Dashboard.Repos)
	}
	if req.Dashboard.Format != "html" {
		t.Fatalf("expected dashboard format html, got %q", req.Dashboard.Format)
	}
	if req.Dashboard.TopN != 25 {
		t.Fatalf("expected dashboard top 25, got %d", req.Dashboard.TopN)
	}
	if req.Dashboard.DefaultLanguage != "all" {
		t.Fatalf("expected dashboard default language all, got %q", req.Dashboard.DefaultLanguage)
	}
	if req.Dashboard.OutputPath != "org-report.html" {
		t.Fatalf("expected dashboard output path, got %q", req.Dashboard.OutputPath)
	}
}

func TestParseArgsDashboardRejectsBaselineStore(t *testing.T) {
	err := expectParseArgsError(t, []string{"dashboard", dashboardReposFlagName, "./api", "--baseline-store", "./baselines"}, "expected dashboard baseline-store rejection")
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("expected unknown flag error for baseline-store, got %v", err)
	}
}

func TestParseArgsDashboardConfig(t *testing.T) {
	req := mustParseArgs(t, []string{"dashboard", dashboardConfigFlagName, dashboardConfigFileName})
	if req.Mode != app.ModeDashboard {
		t.Fatalf(modeMismatchFmt, app.ModeDashboard, req.Mode)
	}
	if req.Dashboard.ConfigPath != dashboardConfigFileName {
		t.Fatalf("expected dashboard config path, got %q", req.Dashboard.ConfigPath)
	}
	if req.Dashboard.TopN != app.DefaultRequest().Dashboard.TopN {
		t.Fatalf("expected dashboard default top, got %d", req.Dashboard.TopN)
	}
}

func TestParseArgsDashboardOutputFlags(t *testing.T) {
	req := mustParseArgs(t, []string{"dashboard", dashboardConfigFlagName, dashboardConfigFileName, dashboardOutputFlagName, dashboardReportCSVFileName, "-o", dashboardReportCSVFileName})
	if req.Dashboard.OutputPath != dashboardReportCSVFileName {
		t.Fatalf("expected output path report.csv, got %q", req.Dashboard.OutputPath)
	}

	_, err := ParseArgs([]string{"dashboard", dashboardConfigFlagName, dashboardConfigFileName, dashboardOutputFlagName, "one.csv", "-o", "two.csv"})
	if err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("expected output mismatch validation error, got %v", err)
	}
}

func TestParseArgsDashboardValidation(t *testing.T) {
	err := expectParseArgsError(t, []string{"dashboard"}, "expected dashboard source validation error")
	if !strings.Contains(err.Error(), "--repos or --config") {
		t.Fatalf("unexpected dashboard source validation error: %v", err)
	}

	err = expectParseArgsError(t, []string{"dashboard", dashboardConfigFlagName, dashboardConfigFileName, "--top", "0"}, "expected dashboard top validation error")
	if !strings.Contains(err.Error(), "--top must be > 0") {
		t.Fatalf("unexpected dashboard top validation error: %v", err)
	}
}

func TestParseArgsDashboardRejectsUnexpectedArguments(t *testing.T) {
	_, err := ParseArgs([]string{"dashboard", dashboardReposFlagName, "./api", "extra"})
	if err == nil || !strings.Contains(err.Error(), "unexpected arguments for dashboard") {
		t.Fatalf("expected dashboard positional argument error, got %v", err)
	}
}
