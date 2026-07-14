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

func TestPersistDashboardOutputDoesNotFollowSymlinkTarget(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	outputPath := filepath.Join(workspace, "org-report.html")
	if err := os.Symlink(outside, outputPath); err != nil {
		t.Fatalf("create output symlink: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	if _, err := persistDashboardOutput("<html>report</html>", "org-report.html"); err != nil {
		t.Fatalf("persist dashboard output: %v", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if string(data) != "secret" {
		t.Fatalf("expected symlink target to remain unchanged, got %q", string(data))
	}
	written, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read dashboard output: %v", err)
	}
	if string(written) != "<html>report</html>" {
		t.Fatalf("expected report at output path, got %q", string(written))
	}
}

func TestPersistDashboardOutputRejectsSymlinkedParent(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "reports")); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	_, err = persistDashboardOutput(`{"report":true}`, filepath.Join("reports", "org-report.json"))
	if err == nil {
		t.Fatal("expected symlinked parent to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "org-report.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside report to remain absent, got err=%v", statErr)
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

func TestPersistDashboardOutputRejectsAbsoluteSymlinkedParent(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	outputPath := filepath.Join(workspace, "reports", "org-report.json")
	if err := os.Symlink(outside, filepath.Join(workspace, "reports")); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}

	_, err := persistDashboardOutput(`{"report":true}`, outputPath)
	if err == nil {
		t.Fatal("expected absolute symlinked parent to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "org-report.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside report to remain absent, got err=%v", statErr)
	}
}

func TestPersistDashboardOutputRejectsAbsoluteSymlinkedNestedParent(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir outside nested: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "reports")); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("canonicalize workspace: %v", err)
	}
	outputPath := filepath.Join(canonicalWorkspace, "reports", "nested", "org-report.json")

	_, err = persistDashboardOutput(`{"report":true}`, outputPath)
	if err == nil {
		t.Fatal("expected absolute nested symlinked parent to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "nested", "org-report.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside nested report to remain absent, got err=%v", statErr)
	}
}
