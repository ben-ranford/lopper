package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectLockfileDriftGitManifestChangeWithoutLockfileChange(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	writeFile(t, filepath.Join(repo, "package-lock.json"), "{\n  \"name\": \"demo\"\n}\n")
	initGitRepo(t, repo)

	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\",\n  \"version\": \"1.0.1\"\n}\n")

	warnings, err := detectLockfileDrift(context.Background(), repo, false)
	if err != nil {
		t.Fatalf("detect lockfile drift: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %#v", warnings)
	}
	if !strings.Contains(warnings[0], "package.json changed while no matching lockfile changed") {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
}

func TestDetectLockfileDriftSkipsLopperCache(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".lopper-cache", "nested", "package.json"), "{\n  \"name\": \"cache-only\"\n}\n")

	warnings, err := detectLockfileDrift(context.Background(), repo, false)
	if err != nil {
		t.Fatalf("detect lockfile drift: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings from .lopper-cache contents, got %#v", warnings)
	}
}

func TestEvaluateLockfileDriftPolicyFailFormatsSinglePrefix(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	writeFile(t, filepath.Join(repo, "composer.lock"), "{}\n")

	warnings, err := evaluateLockfileDriftPolicy(context.Background(), repo, "fail")
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected ErrLockfileDrift, got %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected fail policy to stop after first warning, got %#v", warnings)
	}
	if strings.Count(err.Error(), "lockfile drift detected") != 1 {
		t.Fatalf("expected single lockfile drift prefix in error, got %q", err.Error())
	}
}

func TestEvaluateLockfileDriftPolicyOff(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")

	warnings, err := evaluateLockfileDriftPolicy(context.Background(), repo, "off")
	if err != nil {
		t.Fatalf("evaluate off policy: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for off policy, got %#v", warnings)
	}
}

func TestFormatLockfileDriftErrorNoWarnings(t *testing.T) {
	err := formatLockfileDriftError(nil)
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected ErrLockfileDrift for empty warnings, got %v", err)
	}
}

func TestSanitizedGitEnvPinsSafePath(t *testing.T) {
	t.Setenv("PATH", "/tmp/user-bin:/Users/test/bin")
	t.Setenv("GIT_DIR", "/tmp/fake-git-dir")
	t.Setenv("GIT_WORK_TREE", "/tmp/fake-worktree")
	t.Setenv("GIT_INDEX_FILE", "/tmp/fake-index")
	t.Setenv("KEEP_ME", "1")

	env := sanitizedGitEnv()

	if !containsEnv(env, safeSystemPath) {
		t.Fatalf("expected safe path %q in env, got %#v", safeSystemPath, env)
	}
	if containsEnvPrefix(env, "PATH=") && !containsEnv(env, safeSystemPath) {
		t.Fatalf("expected only pinned PATH in env, got %#v", env)
	}
	if containsEnvPrefix(env, "GIT_DIR=") || containsEnvPrefix(env, "GIT_WORK_TREE=") || containsEnvPrefix(env, "GIT_INDEX_FILE=") {
		t.Fatalf("expected git override vars to be stripped, got %#v", env)
	}
	if !containsEnv(env, "KEEP_ME=1") {
		t.Fatalf("expected unrelated env vars to be preserved, got %#v", env)
	}
}

func TestDetectLockfileDriftStopOnFirst(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	writeFile(t, filepath.Join(repo, "composer.lock"), "{}\n")

	warnings, err := detectLockfileDrift(context.Background(), repo, true)
	if err != nil {
		t.Fatalf("detect lockfile drift stop on first: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning in stop-on-first mode, got %#v", warnings)
	}
}

func TestDetectLockfileDriftContextCancelled(t *testing.T) {
	repo := t.TempDir()
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := detectLockfileDrift(cancelledCtx, repo, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestDetectLockfileDriftInvalidRepoPath(t *testing.T) {
	_, err := detectLockfileDrift(context.Background(), filepath.Join(t.TempDir(), "missing"), false)
	if err == nil {
		t.Fatalf("expected normalize/walk error for missing repo path")
	}
}

func TestReadDirectoryFilesMissingPath(t *testing.T) {
	_, err := readDirectoryFiles(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatalf("expected readDirectoryFiles to fail for missing path")
	}
}

func TestDetectDriftForRuleNonGitContextSkipsManifestOnlyChange(t *testing.T) {
	repo := t.TempDir()
	manifest := filepath.Join(repo, "package.json")
	lock := filepath.Join(repo, "package-lock.json")
	writeFile(t, manifest, "{\n  \"name\": \"demo\"\n}\n")
	writeFile(t, lock, "{\n  \"name\": \"demo\"\n}\n")
	manifestInfo, err := os.Stat(manifest)
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	lockInfo, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("stat lockfile: %v", err)
	}

	rule := lockfileRule{
		manager:   "npm",
		manifest:  "package.json",
		lockfiles: []string{"package-lock.json"},
		remedy:    "run npm install",
	}
	files := map[string]fs.FileInfo{
		"package.json":      manifestInfo,
		"package-lock.json": lockInfo,
	}
	warnings := detectDriftForRule(repo, repo, files, rule, map[string]struct{}{"package.json": {}}, false)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings without git context, got %#v", warnings)
	}
}

func TestGitHelperErrors(t *testing.T) {
	repo := t.TempDir()
	if _, err := gitTrackedChanges(context.Background(), repo); err == nil {
		t.Fatalf("expected tracked changes command to fail outside git repo")
	}
	if _, err := gitUntrackedFiles(context.Background(), repo); err == nil {
		t.Fatalf("expected untracked files command to fail outside git repo")
	}
	if isGitWorktree(context.Background(), repo) {
		t.Fatalf("expected non-git temp dir to not be worktree")
	}
	if _, err := gitTrackedChanges(nil, repo); err == nil {
		t.Fatalf("expected tracked changes command with nil context to fail outside git repo")
	}
	if _, err := gitUntrackedFiles(nil, repo); err == nil {
		t.Fatalf("expected untracked files command with nil context to fail outside git repo")
	}
	if isGitWorktree(nil, repo) {
		t.Fatalf("expected non-git temp dir to not be worktree with nil context")
	}
}

func TestGitChangedFilesOutsideGitRepo(t *testing.T) {
	repo := t.TempDir()
	changed, hasGit, err := gitChangedFiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("gitChangedFiles outside git repo: %v", err)
	}
	if hasGit {
		t.Fatalf("expected hasGit=false for non-git repo")
	}
	if len(changed) != 0 {
		t.Fatalf("expected no changed files for non-git repo, got %#v", changed)
	}
}

func TestGitChangedFilesInGitRepo(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	initGitRepo(t, repo)

	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\",\n  \"version\": \"2.0.0\"\n}\n")
	writeFile(t, filepath.Join(repo, "new-untracked.txt"), "untracked\n")

	changed, hasGit, err := gitChangedFiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("gitChangedFiles in git repo: %v", err)
	}
	if !hasGit {
		t.Fatalf("expected hasGit=true for git repo")
	}
	if _, ok := changed["package.json"]; !ok {
		t.Fatalf("expected package.json to be detected as changed, got %#v", changed)
	}
	if _, ok := changed["new-untracked.txt"]; !ok {
		t.Fatalf("expected untracked file to be detected, got %#v", changed)
	}
}

func TestDetectDriftForRuleManifestAndLockfileChangedNoWarning(t *testing.T) {
	repo := t.TempDir()
	manifest := filepath.Join(repo, "package.json")
	lock := filepath.Join(repo, "package-lock.json")
	writeFile(t, manifest, "{\n  \"name\": \"demo\"\n}\n")
	writeFile(t, lock, "{\n  \"name\": \"demo\"\n}\n")
	manifestInfo, err := os.Stat(manifest)
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	lockInfo, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("stat lockfile: %v", err)
	}

	rule := lockfileRule{
		manager:   "npm",
		manifest:  "package.json",
		lockfiles: []string{"package-lock.json"},
		remedy:    "run npm install",
	}
	files := map[string]fs.FileInfo{
		"package.json":      manifestInfo,
		"package-lock.json": lockInfo,
	}
	changed := map[string]struct{}{
		"package.json":      {},
		"package-lock.json": {},
	}
	warnings := detectDriftForRule(repo, repo, files, rule, changed, true)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings when both manifest and lockfile changed, got %#v", warnings)
	}
}

func TestDetectDriftForRuleManifestWithoutLockfileWarns(t *testing.T) {
	rule := lockfileRule{
		manager:   "npm",
		manifest:  "package.json",
		lockfiles: []string{"package-lock.json"},
		remedy:    "run npm install",
	}
	files := map[string]fs.FileInfo{
		"package.json": mustStatFixtureFile(t, "package.json"),
	}
	warnings := detectDriftForRule("/repo", "/repo", files, rule, nil, false)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "no matching lockfile") {
		t.Fatalf("expected missing-lockfile warning, got %#v", warnings)
	}
}

func TestDetectDriftForRuleLockfileWithoutManifestWarns(t *testing.T) {
	rule := lockfileRule{
		manager:   "npm",
		manifest:  "package.json",
		lockfiles: []string{"package-lock.json"},
		remedy:    "run npm install",
	}
	files := map[string]fs.FileInfo{
		"package-lock.json": mustStatFixtureFile(t, "package-lock.json"),
	}
	warnings := detectDriftForRule("/repo", "/repo", files, rule, nil, false)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "exists without package.json") {
		t.Fatalf("expected stale-lockfile warning, got %#v", warnings)
	}
}

func TestDetectDriftForRuleManifestChangedWarnsWithGitContext(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	writeFile(t, filepath.Join(repo, "package-lock.json"), "{\n  \"name\": \"demo\"\n}\n")
	manifestInfo, err := os.Stat(filepath.Join(repo, "package.json"))
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	lockInfo, err := os.Stat(filepath.Join(repo, "package-lock.json"))
	if err != nil {
		t.Fatalf("stat lockfile: %v", err)
	}

	rule := lockfileRule{
		manager:   "npm",
		manifest:  "package.json",
		lockfiles: []string{"package-lock.json"},
		remedy:    "run npm install",
	}
	files := map[string]fs.FileInfo{
		"package.json":      manifestInfo,
		"package-lock.json": lockInfo,
	}
	warnings := detectDriftForRule(repo, repo, files, rule, map[string]struct{}{"package.json": {}}, true)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "changed while no matching lockfile changed") {
		t.Fatalf("expected manifest-only-change warning, got %#v", warnings)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func initGitRepo(t *testing.T, repo string) {
	t.Helper()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "init")
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", repo}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = sanitizedGitEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}

func containsEnv(env []string, expected string) bool {
	for _, entry := range env {
		if entry == expected {
			return true
		}
	}
	return false
}

func containsEnvPrefix(env []string, prefix string) bool {
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func mustStatFixtureFile(t *testing.T, name string) fs.FileInfo {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	writeFile(t, path, "{}\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", name, err)
	}
	return info
}
