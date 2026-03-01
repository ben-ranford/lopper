package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

const (
	shaMain        = "1111111111111111111111111111111111111111"
	shaTopic       = "2222222222222222222222222222222222222222"
	shaHex         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	expectedGotFmt = "expected %q, got %q"
	packedRefsFile = "packed-refs"
	mainRefPath    = "refs/heads/main"
	topicRefPath   = "refs/heads/topic"
	otherMainRef   = "refs/heads/other"
)

func TestNormalizeRepoPath(t *testing.T) {
	got, err := NormalizeRepoPath("")
	if err != nil {
		t.Fatalf("normalize empty path: %v", err)
	}
	want, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs dot: %v", err)
	}
	if got != want {
		t.Fatalf(expectedGotFmt, want, got)
	}
}

func TestCurrentCommitSHA(t *testing.T) {
	sha, err := CurrentCommitSHA(".")
	if err != nil {
		t.Fatalf("current commit sha: %v", err)
	}
	if len(sha) < 7 {
		t.Fatalf("expected commit sha, got %q", sha)
	}
}

func TestCurrentCommitSHAErrorsForNonRepoPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_WORK_TREE", "")
	t.Setenv("GIT_CEILING_DIRECTORIES", tmp)

	_, err := CurrentCommitSHA(tmp)
	if err == nil {
		t.Fatalf("expected non-repo path to fail commit lookup")
	}
}

func TestCurrentCommitSHAFromNestedPathWithGitFile(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git-meta")
	mustWrite(t, filepath.Join(repo, ".git"), "gitdir: .git-meta\n")
	mustWrite(t, filepath.Join(gitDir, "HEAD"), "ref: "+mainRefPath+"\n")
	mustWrite(t, filepath.Join(gitDir, "refs", "heads", "main"), shaMain+"\n")

	nested := filepath.Join(repo, "internal", "module")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	sha, err := CurrentCommitSHA(nested)
	if err != nil {
		t.Fatalf("resolve nested sha: %v", err)
	}
	if sha != shaMain {
		t.Fatalf(expectedGotFmt, shaMain, sha)
	}
}

func TestCurrentCommitSHADetachedHead(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	mustWrite(t, filepath.Join(gitDir, "HEAD"), shaTopic+"\n")

	sha, err := CurrentCommitSHA(repo)
	if err != nil {
		t.Fatalf("resolve detached HEAD: %v", err)
	}
	if sha != shaTopic {
		t.Fatalf(expectedGotFmt, shaTopic, sha)
	}
}

func TestCurrentCommitSHAInvalidHeadCases(t *testing.T) {
	tests := []struct {
		name    string
		head    string
		errPart string
	}{
		{name: "invalid value", head: "not-a-sha\n", errPart: "invalid HEAD value"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			mustWrite(t, filepath.Join(repo, ".git", "HEAD"), tc.head)

			_, err := CurrentCommitSHA(repo)
			if err == nil || !strings.Contains(err.Error(), tc.errPart) {
				t.Fatalf("expected error containing %q, got %v", tc.errPart, err)
			}
		})
	}
}

func TestCurrentCommitSHAMissingHead(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	_, err := CurrentCommitSHA(repo)
	if err == nil || !strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("expected missing HEAD error, got %v", err)
	}
}

func TestResolveRefSHAFromPackedRefs(t *testing.T) {
	gitDir := t.TempDir()
	mustWrite(t, filepath.Join(gitDir, packedRefsFile), "# pack-refs\n"+shaMain+" "+mainRefPath+"\n")

	sha, err := resolveRefSHA(gitDir, mainRefPath)
	if err != nil {
		t.Fatalf("resolve packed ref: %v", err)
	}
	if sha != shaMain {
		t.Fatalf(expectedGotFmt, shaMain, sha)
	}
}

func TestResolveRefSHAFallsBackToCommonDir(t *testing.T) {
	parent := t.TempDir()
	gitDir := filepath.Join(parent, "worktree-git")
	commonDir := filepath.Join(parent, "common-git")

	mustWrite(t, filepath.Join(gitDir, "commondir"), "../common-git\n")
	mustWrite(t, filepath.Join(commonDir, "refs", "heads", "topic"), shaTopic+"\n")

	sha, err := resolveRefSHA(gitDir, topicRefPath)
	if err != nil {
		t.Fatalf("resolve common-dir ref: %v", err)
	}
	if sha != shaTopic {
		t.Fatalf(expectedGotFmt, shaTopic, sha)
	}
}

func TestInspectGitDirRejectsInvalidGitFile(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, ".git"), "bogus\n")

	_, _, err := inspectGitDir(repo)
	if err == nil || !strings.Contains(err.Error(), "invalid .git file format") {
		t.Fatalf("expected invalid .git file format error, got %v", err)
	}
}

func TestInspectGitDirScenarios(t *testing.T) {
	t.Run("git directory", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir .git dir: %v", err)
		}
		gitDir, found, err := inspectGitDir(repo)
		if err != nil {
			t.Fatalf("inspect .git dir: %v", err)
		}
		if !found || gitDir != filepath.Join(repo, ".git") {
			t.Fatalf("unexpected inspect result: found=%v dir=%q", found, gitDir)
		}
	})

	t.Run("no git entry", func(t *testing.T) {
		repo := t.TempDir()
		gitDir, found, err := inspectGitDir(repo)
		if err != nil {
			t.Fatalf("inspect empty dir: %v", err)
		}
		if found || gitDir != "" {
			t.Fatalf("expected no git entry, got found=%v dir=%q", found, gitDir)
		}
	})

	t.Run("empty gitdir path", func(t *testing.T) {
		repo := t.TempDir()
		mustWrite(t, filepath.Join(repo, ".git"), "gitdir:\n")
		_, _, err := inspectGitDir(repo)
		if err == nil || !strings.Contains(err.Error(), "empty gitdir path") {
			t.Fatalf("expected empty gitdir path error, got %v", err)
		}
	})
}

func TestResolveRefSHAErrorPaths(t *testing.T) {
	t.Run("returns packed refs read error when loose ref invalid", func(t *testing.T) {
		gitDir := t.TempDir()
		mustWrite(t, filepath.Join(gitDir, "refs", "heads", "main"), "bad-sha\n")
		_, err := resolveRefSHA(gitDir, mainRefPath)
		if err == nil || !strings.Contains(err.Error(), packedRefsFile) {
			t.Fatalf("expected packed-refs error, got %v", err)
		}
	})

	t.Run("returns loose ref read error when packed refs do not match", func(t *testing.T) {
		gitDir := t.TempDir()
		mustWrite(t, filepath.Join(gitDir, packedRefsFile), shaMain+" "+otherMainRef+"\n")
		_, err := resolveRefSHA(gitDir, mainRefPath)
		if err == nil || !strings.Contains(err.Error(), mainRefPath) {
			t.Fatalf("expected loose ref error, got %v", err)
		}
	})

	t.Run("returns ref not found when loose ref invalid and packed refs do not match", func(t *testing.T) {
		gitDir := t.TempDir()
		mustWrite(t, filepath.Join(gitDir, "refs", "heads", "main"), "also-bad\n")
		mustWrite(t, filepath.Join(gitDir, packedRefsFile), shaMain+" "+otherMainRef+"\n")
		_, err := resolveRefSHA(gitDir, mainRefPath)
		if err == nil || !strings.Contains(err.Error(), "ref "+mainRefPath+" not found") {
			t.Fatalf("expected ref-not-found error, got %v", err)
		}
	})
}

func TestResolveCommonGitDirScenarios(t *testing.T) {
	t.Run("absolute commondir", func(t *testing.T) {
		gitDir := t.TempDir()
		absCommon := filepath.Join(t.TempDir(), "common")
		mustWrite(t, filepath.Join(gitDir, "commondir"), absCommon+"\n")
		got, err := resolveCommonGitDir(gitDir)
		if err != nil {
			t.Fatalf("resolve absolute commondir: %v", err)
		}
		if got != absCommon {
			t.Fatalf(expectedGotFmt, absCommon, got)
		}
	})

	t.Run("blank commondir", func(t *testing.T) {
		gitDir := t.TempDir()
		mustWrite(t, filepath.Join(gitDir, "commondir"), "   \n")
		got, err := resolveCommonGitDir(gitDir)
		if err != nil {
			t.Fatalf("resolve blank commondir: %v", err)
		}
		if got != gitDir {
			t.Fatalf("expected fallback to gitDir "+expectedGotFmt, gitDir, got)
		}
	})
}

func TestResolveGitDirOpenError(t *testing.T) {
	_, err := resolveGitDir(filepath.Join(t.TempDir(), "missing", "repo"))
	if err == nil {
		t.Fatalf("expected resolveGitDir to fail for missing path")
	}
}

func TestReadGitPathErrors(t *testing.T) {
	_, err := readGitPath(filepath.Join(t.TempDir(), "missing-gitdir"), "HEAD")
	if err == nil {
		t.Fatalf("expected open root error for missing git dir")
	}

	gitDir := t.TempDir()
	_, err = readGitPath(gitDir, "HEAD")
	if err == nil || !strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("expected missing HEAD read error, got %v", err)
	}
}

func TestValidSHA(t *testing.T) {
	if !validSHA(shaMain) {
		t.Fatalf("expected valid sha")
	}
	if validSHA(strings.ToUpper(shaHex)) {
		t.Fatalf("expected uppercase sha to be invalid")
	}
	if validSHA("abc123") {
		t.Fatalf("expected short sha to be invalid")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestChangedFilesErrorsForNonRepoPath(t *testing.T) {
	tmp := t.TempDir()
	_, err := ChangedFiles(filepath.Join(tmp, "missing"))
	if err == nil {
		t.Fatalf("expected changed-files lookup to fail for non-repo path")
	}
}

func TestParseChangedFileHelpers(t *testing.T) {
	changed := parseChangedFileLines([]byte("packages/a/file.ts\npackages/a/file.ts\n"))
	if len(changed) != 1 || changed[0] != "packages/a/file.ts" {
		t.Fatalf("expected deduped changed lines, got %#v", changed)
	}

	porcelain := parsePorcelainChangedFiles([]byte("M  packages/a/file.ts\nR  old.ts -> packages/b/new.ts\n"))
	if len(porcelain) != 2 || porcelain[0] != "packages/a/file.ts" || porcelain[1] != "packages/b/new.ts" {
		t.Fatalf("expected parsed porcelain paths, got %#v", porcelain)
	}

	withShort := parsePorcelainChangedFiles([]byte("\n??\nA  file.txt\n"))
	if len(withShort) != 1 || withShort[0] != "file.txt" {
		t.Fatalf("expected short lines to be ignored, got %#v", withShort)
	}
}

func TestResolveGitBinaryPathMissing(t *testing.T) {
	original := resolveGitBinaryPathFn
	resolveGitBinaryPathFn = func() (string, error) {
		return "", errors.New("git executable not found")
	}
	t.Cleanup(func() {
		resolveGitBinaryPathFn = original
	})

	_, err := resolveGitBinaryPath()
	if err == nil {
		t.Fatalf("expected git lookup to fail when configured binaries are unavailable")
	}
}

func TestResolveGitBinaryPathUsesResolver(t *testing.T) {
	original := resolveGitBinaryPathFn
	t.Cleanup(func() {
		resolveGitBinaryPathFn = original
	})

	const expectedPath = "/tmp/fake-git"
	resolveGitBinaryPathFn = func() (string, error) {
		return expectedPath, nil
	}
	path, err := resolveGitBinaryPath()
	if err != nil || path != expectedPath {
		t.Fatalf("expected resolver path, got path=%q err=%v", path, err)
	}
}

func TestRunGitReturnsStderrInError(t *testing.T) {
	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		t.Skip("git binary not available")
	}
	_, err = runGit(gitPath, t.TempDir(), "hash-object", "--definitely-invalid-option")
	if err == nil {
		t.Fatalf("expected runGit to fail for invalid git option")
	}
	if !strings.Contains(err.Error(), "option") {
		t.Fatalf("expected stderr context in runGit error, got %v", err)
	}
}

func TestSanitizedGitEnvStripsGitOverrides(t *testing.T) {
	t.Setenv("GIT_DIR", "/tmp/fake-git-dir")
	t.Setenv("GIT_WORK_TREE", "/tmp/fake-worktree")
	t.Setenv("GIT_INDEX_FILE", "/tmp/fake-index")
	t.Setenv("PATH", "/tmp/fake-path")

	env := sanitizedGitEnv()
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_DIR=") || strings.HasPrefix(entry, "GIT_WORK_TREE=") || strings.HasPrefix(entry, "GIT_INDEX_FILE=") {
			t.Fatalf("expected git override env vars to be removed, found %q", entry)
		}
	}
	if !containsEnvEntry(env, gitexec.SafeSystemPath) {
		t.Fatalf("expected safe PATH entry in sanitized env, got %#v", env)
	}
}

func containsEnvEntry(env []string, target string) bool {
	for _, entry := range env {
		if entry == target {
			return true
		}
	}
	return false
}

func TestGitExecutableAvailable(t *testing.T) {
	if gitexec.ExecutableAvailable(t.TempDir()) {
		t.Fatalf("expected directory path to be non-executable")
	}

	file := filepath.Join(t.TempDir(), "git")
	mustWrite(t, file, "#!/bin/sh\n")
	if gitexec.ExecutableAvailable(file) {
		t.Fatalf("expected non-executable file to be unavailable")
	}
	if err := os.Chmod(file, 0o700); err != nil {
		t.Fatalf("chmod executable: %v", err)
	}
	if !gitexec.ExecutableAvailable(file) {
		t.Fatalf("expected executable file to be available")
	}
}
