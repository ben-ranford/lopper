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

const (
	manifestFileName         = "package.json"
	lockfileName             = "package-lock.json"
	demoPackageJSON          = "{\n  \"name\": \"demo\"\n}\n"
	demoPackageJSONUpdated   = "{\n  \"name\": \"demo\",\n  \"version\": \"1.0.1\"\n}\n"
	demoPackageJSONUpdatedV2 = "{\n  \"name\": \"demo\",\n  \"version\": \"2.0.0\"\n}\n"
	nestedManifestPath       = "nested/package.json"
	gitBinaryPath            = "/usr/bin/git"
)

func TestDetectLockfileDriftGitManifestChangeWithoutLockfileChange(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, lockfileName), demoPackageJSON)
	initGitRepo(t, repo)

	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSONUpdated)

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
	writeFile(t, filepath.Join(repo, ".lopper-cache", "nested", manifestFileName), "{\n  \"name\": \"cache-only\"\n}\n")

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
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
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
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)

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
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
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
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	initGitRepo(t, repo)

	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSONUpdatedV2)
	writeFile(t, filepath.Join(repo, "new-untracked.txt"), "untracked\n")

	changed, hasGit, err := gitChangedFiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("gitChangedFiles in git repo: %v", err)
	}
	if !hasGit {
		t.Fatalf("expected hasGit=true for git repo")
	}
	if _, ok := changed[manifestFileName]; !ok {
		t.Fatalf("expected package.json to be detected as changed, got %#v", changed)
	}
	if _, ok := changed["new-untracked.txt"]; !ok {
		t.Fatalf("expected untracked file to be detected, got %#v", changed)
	}
}

func TestGitChangedFilesReturnsErrorWhenRepoHasNoHEAD(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")

	changed, hasGit, err := gitChangedFiles(context.Background(), repo)
	if err == nil {
		t.Fatalf("expected gitChangedFiles error when HEAD is missing")
	}
	if !hasGit {
		t.Fatalf("expected hasGit=true when inside git worktree")
	}
	if len(changed) != 0 {
		t.Fatalf("expected no changed files on error, got %#v", changed)
	}
}

func TestDetectLockfileDriftReturnsGitChangedFilesError(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	runGit(t, repo, "init")

	_, err := detectLockfileDrift(context.Background(), repo, false)
	if err == nil {
		t.Fatalf("expected detectLockfileDrift to return git changed-files error when HEAD is missing")
	}
}

func TestDetectDriftForRuleCases(t *testing.T) {
	repo := t.TempDir()
	manifest := filepath.Join(repo, manifestFileName)
	lock := filepath.Join(repo, lockfileName)
	writeFile(t, manifest, demoPackageJSON)
	writeFile(t, lock, demoPackageJSON)
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
		manifest:  manifestFileName,
		lockfiles: []string{lockfileName},
		remedy:    "run npm install",
	}
	files := map[string]fs.FileInfo{
		manifestFileName: manifestInfo,
		lockfileName:     lockInfo,
	}
	missingManifest := map[string]fs.FileInfo{lockfileName: lockInfo}
	missingLockfile := map[string]fs.FileInfo{manifestFileName: manifestInfo}
	cases := []struct {
		name         string
		files        map[string]fs.FileInfo
		changed      map[string]struct{}
		hasGit       bool
		wantWarnings int
		wantSubstr   string
	}{
		{name: "non-git-context", files: files, changed: map[string]struct{}{manifestFileName: {}}, hasGit: false, wantWarnings: 0},
		{name: "manifest-not-changed", files: files, changed: map[string]struct{}{lockfileName: {}}, hasGit: true, wantWarnings: 0},
		{name: "manifest-and-lockfile-changed", files: files, changed: map[string]struct{}{manifestFileName: {}, lockfileName: {}}, hasGit: true, wantWarnings: 0},
		{name: "manifest-only-changed", files: files, changed: map[string]struct{}{manifestFileName: {}}, hasGit: true, wantWarnings: 1, wantSubstr: "changed while no matching lockfile changed"},
		{name: "manifest-without-lockfile", files: missingLockfile, changed: nil, hasGit: false, wantWarnings: 1, wantSubstr: "no matching lockfile"},
		{name: "lockfile-without-manifest", files: missingManifest, changed: nil, hasGit: false, wantWarnings: 1, wantSubstr: "exists without package.json"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			warnings := detectDriftForRule(repo, repo, tc.files, rule, tc.changed, tc.hasGit)
			if len(warnings) != tc.wantWarnings {
				t.Fatalf("expected %d warnings, got %#v", tc.wantWarnings, warnings)
			}
			if tc.wantSubstr != "" && !strings.Contains(warnings[0], tc.wantSubstr) {
				t.Fatalf("expected warning containing %q, got %#v", tc.wantSubstr, warnings)
			}
		})
	}
}

func TestLockfileDriftHelpers(t *testing.T) {
	repo := t.TempDir()
	nestedDir := filepath.Join(repo, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	if got := relativeDir(repo, nestedDir); got != "nested" {
		t.Fatalf("expected relative dir nested, got %q", got)
	}
	if got := relativeFilePath(repo, nestedDir, manifestFileName); got != nestedManifestPath {
		t.Fatalf("expected relative file path nested/package.json, got %q", got)
	}
	if !isPathChanged(map[string]struct{}{nestedManifestPath: {}}, nestedManifestPath) {
		t.Fatalf("expected changed path to be detected")
	}
	if isPathChanged(map[string]struct{}{"other": {}}, nestedManifestPath) {
		t.Fatalf("expected unchanged path not to be detected")
	}

	lines := parseGitOutputLines([]byte("a\nb\n"))
	if len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("unexpected parsed git output lines: %#v", lines)
	}
	if got := parseGitOutputLines([]byte("")); len(got) != 0 {
		t.Fatalf("expected empty git output lines, got %#v", got)
	}

	manifest := filepath.Join(repo, manifestFileName)
	lock := filepath.Join(repo, lockfileName)
	writeFile(t, manifest, demoPackageJSON)
	writeFile(t, lock, demoPackageJSON)
	manifestInfo, err := os.Stat(manifest)
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	lockInfo, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("stat lockfile: %v", err)
	}
	files := map[string]fs.FileInfo{
		manifestFileName: manifestInfo,
		lockfileName:     lockInfo,
	}
	found := findRuleLockfiles(files, []string{lockfileName, "missing.lock"})
	if len(found) != 1 || found[0].name != lockfileName {
		t.Fatalf("unexpected lockfiles found: %#v", found)
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
	command := exec.Command(gitBinaryPath, commandArgs...)
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
