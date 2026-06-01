package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/dashboard"
)

func TestResolveDashboardRequestConfigRelativeRepo(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "lopper-org.yml")
	config := "dashboard:\n  repos:\n    - path: ./api\n      name: API Service\n      language: go\n  output: html\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	resolved, err := resolveDashboardRequest(DashboardRequest{
		ConfigPath: configPath,
	})
	if err != nil {
		t.Fatalf("resolve dashboard request from config: %v", err)
	}
	if len(resolved.repos) != 1 {
		t.Fatalf("expected one repo from config, got %#v", resolved.repos)
	}
	if resolved.repos[0].Path != filepath.Join(tmpDir, "api") {
		t.Fatalf("expected config-relative repo path, got %#v", resolved.repos)
	}
	if resolved.format != dashboard.FormatHTML {
		t.Fatalf("expected config output format html, got %q", resolved.format)
	}
}

func TestResolveDashboardRequestConfigRepoURLNotSupported(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "lopper-org.yml")
	config := "dashboard:\n  repos:\n    - repoUrl: https://github.com/org/worker\n  output: json\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := resolveDashboardRequest(DashboardRequest{
		ConfigPath: configPath,
	})
	if err == nil {
		t.Fatalf("expected unsupported repoUrl error")
	}
	if !strings.Contains(err.Error(), "repoUrl") {
		t.Fatalf("expected repoUrl error message, got %v", err)
	}
}

func TestResolveDashboardRequestRequiresRepos(t *testing.T) {
	_, err := resolveDashboardRequest(DashboardRequest{})
	if err == nil || !strings.Contains(err.Error(), "requires at least one repo") {
		t.Fatalf("expected missing repos error, got %v", err)
	}
}

func TestResolveDashboardRequestAppliesDefaultLanguageAndOutputTrim(t *testing.T) {
	resolved, err := resolveDashboardRequest(DashboardRequest{
		Repos:           []DashboardRepo{{Path: "./api"}},
		DefaultLanguage: "go",
		Format:          "json",
		OutputPath:      " ./out/report.json ",
	})
	if err != nil {
		t.Fatalf("resolve dashboard request: %v", err)
	}
	if len(resolved.repos) != 1 {
		t.Fatalf("expected one resolved repo, got %#v", resolved.repos)
	}
	if resolved.repos[0].Language != "go" {
		t.Fatalf("expected default language to be applied, got %#v", resolved.repos[0])
	}
	if resolved.outputPath != "./out/report.json" {
		t.Fatalf("expected output path to be trimmed, got %q", resolved.outputPath)
	}
}

func TestReposFromDashboardConfigMissingPath(t *testing.T) {
	_, err := reposFromDashboardConfig(dashboard.LoadedConfig{
		ConfigDir: t.TempDir(),
		Dashboard: dashboard.ConfigDashboard{
			Repos: []dashboard.ConfigRepo{
				{Name: "broken-repo"},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "missing path") {
		t.Fatalf("expected missing path error, got %v", err)
	}
}

func TestInferDashboardRepoNameRootPath(t *testing.T) {
	if got := inferDashboardRepoName(string(filepath.Separator)); got != string(filepath.Separator) {
		t.Fatalf("expected root path repo name fallback, got %q", got)
	}
}

func TestLoadDashboardConfigError(t *testing.T) {
	_, hasConfig, err := loadDashboardConfig(filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil {
		t.Fatalf("expected config load error for missing file")
	}
	if hasConfig {
		t.Fatalf("expected hasConfig=false when load fails")
	}
}

func TestLoadDashboardConfigEmptyPath(t *testing.T) {
	loaded, hasConfig, err := loadDashboardConfig("   ")
	if err != nil {
		t.Fatalf("expected empty config path to be a no-op, got %v", err)
	}
	if hasConfig {
		t.Fatalf("expected hasConfig=false for empty config path")
	}
	if loaded.Path != "" || loaded.ConfigDir != "" || len(loaded.Dashboard.Repos) != 0 {
		t.Fatalf("expected empty loaded config, got %#v", loaded)
	}
}
