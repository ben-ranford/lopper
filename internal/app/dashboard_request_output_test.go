package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	config := dashboard.LoadedConfig{
		ConfigDir: configDir,
		Dashboard: dashboard.ConfigDashboard{
			Repos: []dashboard.ConfigRepo{
				{Path: "./worker"},
			},
		},
	}
	fromConfig, err := reposFromDashboardConfig(config, nil)
	if err != nil {
		t.Fatalf("repos from config: %v", err)
	}
	if len(fromConfig) != 1 || fromConfig[0].Name != "worker" || fromConfig[0].Path != filepath.Join(configDir, "worker") {
		t.Fatalf("expected config repo name inference and path resolution, got %#v", fromConfig)
	}

	absoluteBaselineStore := filepath.Join(t.TempDir(), "baselines")
	if got := resolveDashboardConfigPath(configDir, absoluteBaselineStore); got != absoluteBaselineStore {
		t.Fatalf("expected absolute dashboard config path to pass through, got %q", got)
	}
}

func TestDashboardOutputTrustedRootsDedupesAndSkipsBlankPaths(t *testing.T) {
	plan := dashboardExecutionPlan{
		initialResults: []dashboard.RepoAnalysis{
			{Input: dashboard.RepoInput{Path: "  "}},
			{Input: dashboard.RepoInput{Path: "/repo/a"}},
			{Input: dashboard.RepoInput{Path: "/repo/a"}},
			{Input: dashboard.RepoInput{Path: "/repo/b"}},
		},
	}

	roots := dashboardOutputTrustedRoots(plan)
	if len(roots) != 2 {
		t.Fatalf("expected two unique trusted roots, got %#v", roots)
	}
	if roots[0] != "/repo/a" || roots[1] != "/repo/b" {
		t.Fatalf("unexpected trusted roots: %#v", roots)
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
		writeBlockedFile(t, blocker)

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

func TestPersistDashboardOutputRejectsReadOnlyExistingFile(t *testing.T) {
	workspace := t.TempDir()
	outputPath := filepath.Join(workspace, "org-report.html")
	if err := os.WriteFile(outputPath, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed report file: %v", err)
	}
	if err := os.Chmod(outputPath, 0o400); err != nil {
		t.Fatalf("chmod report file read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(outputPath, 0o600); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore report file permissions: %v", err)
		}
	})

	probe, probeErr := os.OpenFile(outputPath, os.O_WRONLY, 0)
	if probeErr == nil {
		if err := probe.Close(); err != nil {
			t.Fatalf("close writability probe: %v", err)
		}
		t.Skip("effective privileges bypass read-only file permissions")
	}
	if !os.IsPermission(probeErr) {
		t.Skipf("read-only file semantics are not testable: %v", probeErr)
	}

	chdirCanonicalWorkspace(t, workspace)

	if _, err := persistDashboardOutput("<html>after</html>", "org-report.html"); err == nil {
		t.Fatal("expected read-only dashboard output to be rejected")
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read dashboard output: %v", err)
	}
	if string(data) != "before" {
		t.Fatalf("expected read-only dashboard output to remain unchanged, got %q", string(data))
	}
}

func TestPersistDashboardOutputAllowsAbsolutePathUnderSystemSymlinkPrefix(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "reports", "org-report.json")

	if _, err := persistDashboardOutput(`{"report":true}`, outputPath); err != nil {
		t.Fatalf("persist dashboard output: %v", err)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read dashboard output: %v", err)
	}
	if string(data) != `{"report":true}` {
		t.Fatalf("expected dashboard report content, got %q", string(data))
	}
	root, err := commandOutputRoot(outputPath)
	if err != nil {
		t.Fatalf("resolve command output root: %v", err)
	}
	if root == string(os.PathSeparator) {
		t.Fatalf("expected absolute output root to stop at existing parent directory, got %q", root)
	}
}

func TestExecuteDashboardRejectsAbsoluteOutputUnderRequestedRepoSymlinkOutsideWorkingDirectory(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir outside nested: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "reports")); err != nil {
		t.Fatalf("create reports symlink: %v", err)
	}
	chdirCanonicalWorkspace(t, t.TempDir())

	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			repo: {Dependencies: []report.DependencyReport{{Name: "dep"}}},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Format = "json"
	req.Dashboard.Repos = []DashboardRepo{{Name: "repo", Path: repo}}
	req.Dashboard.OutputPath = filepath.Join(repo, "reports", "nested", "org-report.json")

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "output root contains symlink") {
		t.Fatalf("expected dashboard repo-root symlink rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "nested", "org-report.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside dashboard output to remain absent, got err=%v", statErr)
	}
}
