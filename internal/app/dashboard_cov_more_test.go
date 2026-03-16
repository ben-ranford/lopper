package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestDashboardRequestAdditionalBranches(t *testing.T) {
	_, err := resolveDashboardRequest(DashboardRequest{
		ConfigPath: filepath.Join(t.TempDir(), "missing-dashboard.yml"),
	})
	if err == nil {
		t.Fatalf("expected missing config to fail request resolution")
	}

	repos := normalizedDashboardRepos([]DashboardRepo{
		{Path: "   "},
		{Path: " ./api ", Language: " go "},
	})
	if len(repos) != 1 {
		t.Fatalf("expected blank repo paths to be skipped, got %#v", repos)
	}
	if repos[0].Name != "api" || repos[0].Language != "go" {
		t.Fatalf("expected repo name/language normalization, got %#v", repos[0])
	}

	configDir := t.TempDir()
	fromConfig, err := reposFromDashboardConfig(dashboard.LoadedConfig{
		ConfigDir: configDir,
		Dashboard: dashboard.ConfigDashboard{
			Repos: []dashboard.ConfigRepo{
				{Path: "./worker"},
			},
		},
	})
	if err != nil {
		t.Fatalf("repos from config: %v", err)
	}
	if len(fromConfig) != 1 || fromConfig[0].Name != "worker" || fromConfig[0].Path != filepath.Join(configDir, "worker") {
		t.Fatalf("expected config repo name inference and path resolution, got %#v", fromConfig)
	}
}

func TestExecuteDashboardOutputPathErrors(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			singleRepoPath: {
				Dependencies: []report.DependencyReport{{Name: "dep"}},
			},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	t.Run("mkdir output directory failure", func(t *testing.T) {
		root := t.TempDir()
		blocker := filepath.Join(root, "blocked")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("write blocker: %v", err)
		}

		req := DefaultRequest()
		req.Mode = ModeDashboard
		req.Dashboard.Format = "csv"
		req.Dashboard.Repos = []DashboardRepo{{Path: singleRepoPath}}
		req.Dashboard.OutputPath = filepath.Join(blocker, "report.csv")

		if _, err := application.Execute(context.Background(), req); err == nil {
			t.Fatalf("expected executeDashboard to fail when output directory cannot be created")
		}
	})

	t.Run("write output file failure", func(t *testing.T) {
		outputDir := filepath.Join(t.TempDir(), "reports")
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatalf("mkdir output dir: %v", err)
		}

		req := DefaultRequest()
		req.Mode = ModeDashboard
		req.Dashboard.Format = "csv"
		req.Dashboard.Repos = []DashboardRepo{{Path: singleRepoPath}}
		req.Dashboard.OutputPath = outputDir

		if _, err := application.Execute(context.Background(), req); err == nil {
			t.Fatalf("expected executeDashboard to fail when output path is a directory")
		}
	})
}
