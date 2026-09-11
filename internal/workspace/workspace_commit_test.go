package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

func TestCurrentCommitSHASupportsSHA256References(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, gitDir string)
	}{
		{
			name: "loose ref",
			setup: func(t *testing.T, gitDir string) {
				mustWrite(t, filepath.Join(gitDir, "HEAD"), "ref: "+mainRefPath+"\n")
				mustWrite(t, filepath.Join(gitDir, "refs", "heads", "main"), sha256Main+"\n")
			},
		},
		{
			name: "packed ref",
			setup: func(t *testing.T, gitDir string) {
				mustWrite(t, filepath.Join(gitDir, "HEAD"), "ref: "+mainRefPath+"\n")
				mustWrite(t, filepath.Join(gitDir, packedRefsFile), sha256Main+" "+mainRefPath+"\n")
			},
		},
		{
			name: "detached HEAD",
			setup: func(t *testing.T, gitDir string) {
				mustWrite(t, filepath.Join(gitDir, "HEAD"), sha256Main+"\n")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			gitDir := filepath.Join(repo, ".git")
			tc.setup(t, gitDir)

			sha, err := CurrentCommitSHA(repo)
			if err != nil {
				t.Fatalf("resolve SHA-256 commit: %v", err)
			}
			if sha != sha256Main {
				t.Fatalf(expectedGotFmt, sha256Main, sha)
			}
		})
	}
}

func TestCurrentCommitSHASupportsSHA256Repository(t *testing.T) {
	assertCurrentCommitSHASupportsSHA256Repository(t)
}

func TestCurrentCommitSHASupportsSHA256RepositoryIgnoresInheritedSHA1Index(t *testing.T) {
	t.Setenv("GIT_INDEX_FILE", inheritedSHA1IndexFile(t))
	assertCurrentCommitSHASupportsSHA256Repository(t)
}

func assertCurrentCommitSHASupportsSHA256Repository(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	if output, err := runTestGit("init", "--object-format=sha256", repo); err != nil {
		t.Skipf("Git does not support SHA-256 repositories: %v\n%s", err, output)
	}
	for _, args := range [][]string{
		{"-C", repo, "config", "user.email", "test@example.com"},
		{"-C", repo, "config", "user.name", "Lopper Test"},
		{"-C", repo, "commit", "--allow-empty", "-m", "initial"},
	} {
		if output, err := runTestGit(args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}

	sha, err := CurrentCommitSHA(repo)
	if err != nil {
		t.Fatalf("resolve SHA-256 repository commit: %v", err)
	}
	if len(sha) != 64 || !validSHA(sha) {
		t.Fatalf("SHA-256 repository commit = %q, want valid 64-character object ID", sha)
	}
}

func inheritedSHA1IndexFile(t *testing.T) string {
	t.Helper()

	repo := t.TempDir()
	if output, err := runTestGit("init", repo); err != nil {
		t.Fatalf("create SHA-1 repository for inherited index: %v\n%s", err, output)
	}
	mustWrite(t, filepath.Join(repo, "fixture.txt"), "fixture\n")
	if output, err := runTestGit("-C", repo, "add", "fixture.txt"); err != nil {
		t.Fatalf("populate inherited SHA-1 index: %v\n%s", err, output)
	}
	return filepath.Join(repo, ".git", "index")
}

func runTestGit(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = sanitizedGitEnv()
	return cmd.CombinedOutput()
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

func TestResolveRefSHAErrorPaths(t *testing.T) {
	wantRefNotFound := "ref " + mainRefPath + " not found"

	t.Run("returns ref not found when loose ref invalid and packed refs are missing", func(t *testing.T) {
		gitDir := t.TempDir()
		mustWrite(t, filepath.Join(gitDir, "refs", "heads", "main"), "bad-sha\n")

		assertResolveRefSHAErrorContains(t, gitDir, mainRefPath, wantRefNotFound)
	})

	t.Run("returns ref not found when loose ref is missing and packed refs do not match", func(t *testing.T) {
		gitDir := t.TempDir()
		mustWrite(t, filepath.Join(gitDir, packedRefsFile), shaMain+" "+otherMainRef+"\n")

		assertResolveRefSHAErrorContains(t, gitDir, mainRefPath, wantRefNotFound)
	})

	t.Run("returns ref not found when loose ref invalid and packed refs do not match", func(t *testing.T) {
		gitDir := t.TempDir()
		mustWrite(t, filepath.Join(gitDir, "refs", "heads", "main"), "also-bad\n")
		mustWrite(t, filepath.Join(gitDir, packedRefsFile), shaMain+" "+otherMainRef+"\n")

		assertResolveRefSHAErrorContains(t, gitDir, mainRefPath, wantRefNotFound)
	})

	t.Run("returns ref not found when loose and packed refs are missing", func(t *testing.T) {
		gitDir := t.TempDir()

		assertResolveRefSHAErrorContains(t, gitDir, mainRefPath, wantRefNotFound)
	})

	t.Run("returns packed refs read error when packed refs path is unreadable", func(t *testing.T) {
		gitDir := t.TempDir()
		mustWrite(t, filepath.Join(gitDir, "refs", "heads", "main"), "bad-sha\n")
		if err := os.MkdirAll(filepath.Join(gitDir, packedRefsFile), 0o755); err != nil {
			t.Fatalf("mkdir packed-refs dir: %v", err)
		}

		assertResolveRefSHAErrorContains(t, gitDir, mainRefPath, packedRefsFile)
	})
}

func assertResolveRefSHAErrorContains(t *testing.T, gitDir, ref, wantErrPart string) {
	t.Helper()

	_, err := resolveRefSHA(gitDir, ref)
	if err == nil || !strings.Contains(err.Error(), wantErrPart) {
		t.Fatalf("expected error containing %q, got %v", wantErrPart, err)
	}
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
	if !validSHA(sha256Main) {
		t.Fatalf("expected valid SHA-256 commit ID")
	}
}
