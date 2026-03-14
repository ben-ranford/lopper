package app

import (
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/dashboard"
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
