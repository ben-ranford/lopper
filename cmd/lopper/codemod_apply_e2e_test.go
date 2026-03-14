package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	e2eLodashPackageJSON = "{\n  \"main\": \"index.js\",\n  \"exports\": {\n    \".\": \"./index.js\",\n    \"./map\": \"./map.js\"\n  }\n}\n"
	e2eMapSource         = "import { map } from \"lodash\";\nmap([1], (x) => x)\n"
)

func TestRunAnalyseApplyCodemodE2E(t *testing.T) {
	repo, sourcePath := setupGitLodashFixture(t, e2eMapSource)

	var out bytes.Buffer
	var errOut bytes.Buffer
	args := []string{
		"analyse", "lodash",
		"--repo", repo,
		"--format", "json",
		"--apply-codemod",
		"--apply-codemod-confirm",
	}
	code := run(args, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("expected success exit code, got %d stderr=%q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", errOut.String())
	}

	content, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read updated source: %v", err)
	}
	if !strings.Contains(string(content), "import map from \"lodash/map\";") {
		t.Fatalf("expected source rewrite, got %q", string(content))
	}

	var payload report.Report
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode output report: %v", err)
	}
	if len(payload.Dependencies) != 1 || payload.Dependencies[0].Codemod == nil || payload.Dependencies[0].Codemod.Apply == nil {
		t.Fatalf("expected codemod apply summary in output, got %#v", payload.Dependencies)
	}
	apply := payload.Dependencies[0].Codemod.Apply
	if apply.AppliedFiles != 1 || apply.AppliedPatches != 1 {
		t.Fatalf("unexpected apply summary: %#v", apply)
	}
	if apply.BackupPath == "" {
		t.Fatalf("expected backup path in apply summary")
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(apply.BackupPath))); err != nil {
		t.Fatalf("expected rollback artifact to exist: %v", err)
	}
}

func TestRunAnalyseApplyCodemodDirtyWorktreeE2E(t *testing.T) {
	repo, sourcePath := setupGitLodashFixture(t, e2eMapSource)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	args := []string{
		"analyse", "lodash",
		"--repo", repo,
		"--format", "json",
		"--apply-codemod",
		"--apply-codemod-confirm",
	}
	code := run(args, strings.NewReader(""), &out, &errOut)
	if code != 1 {
		t.Fatalf("expected generic error exit code 1 for dirty worktree, got %d", code)
	}
	if !strings.Contains(errOut.String(), "clean git worktree") {
		t.Fatalf("expected dirty-worktree error on stderr, got %q", errOut.String())
	}

	content, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source after dirty-worktree refusal: %v", err)
	}
	if string(content) != e2eMapSource {
		t.Fatalf("expected source to remain unchanged, got %q", string(content))
	}
}

func setupGitLodashFixture(t *testing.T, source string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "index.js")
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	dependencyRoot := filepath.Join(repo, "node_modules", "lodash")
	if err := os.MkdirAll(dependencyRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dependencyRoot, "package.json"), []byte(e2eLodashPackageJSON), 0o644); err != nil {
		t.Fatalf("write dependency package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dependencyRoot, "index.js"), []byte("export { map } from './map.js'\n"), 0o644); err != nil {
		t.Fatalf("write dependency entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dependencyRoot, "map.js"), []byte("export default function map() {}\n"), 0o644); err != nil {
		t.Fatalf("write dependency map.js: %v", err)
	}

	testutil.RunGit(t, repo, "init")
	testutil.RunGit(t, repo, "config", "user.email", "codex@example.com")
	testutil.RunGit(t, repo, "config", "user.name", "Codex")
	testutil.RunGit(t, repo, "add", ".")
	testutil.RunGit(t, repo, "commit", "-m", "fixture")

	return repo, sourcePath
}
