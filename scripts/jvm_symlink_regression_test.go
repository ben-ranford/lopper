package scripts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/language"
)

func TestJVMGradleSymlinkedInputIsNotConsumed(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "build.gradle")

	writeFile(t, outside, "dependencies { implementation 'org.junit.jupiter:junit-jupiter-api:5.10.0' }\n")
	if err := os.Symlink(outside, filepath.Join(repo, "build.gradle")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	detection, err := jvm.NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected escaping build.gradle symlink to be ignored, got %#v", detection)
	}
}

func TestJVMEscapingSymlinkFloodStillDetectsLaterLegitimateFile(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "Outside.java")

	writeFile(t, outside, "class Outside {}\n")
	for index := 0; index < 1024; index++ {
		linkPath := filepath.Join(repo, fmt.Sprintf("escape-%04d.java", index))
		if err := os.Symlink(outside, linkPath); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
	}
	writeFile(t, filepath.Join(repo, "module", "src", "main", "java", "Main.java"), "class Main {}\n")

	detection, err := jvm.NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected later legitimate JVM file to remain detectable after escaping symlink flood, got %#v", detection)
	}
}

func TestJVMEscapingSymlinkedDirectoryIsNotTraversed(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside-module")

	writeFile(t, filepath.Join(outside, "src", "main", "java", "Outside.java"), "class Outside {}\n")
	if err := os.Symlink(outside, filepath.Join(repo, "linked-module")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	detection, err := jvm.NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected escaping symlinked directory to stay outside rooted traversal, got %#v", detection)
	}
}

func TestJVMSymlinkedRepoRootFailsClosed(t *testing.T) {
	t.Parallel()

	canonicalRepo := t.TempDir()
	writeFile(t, filepath.Join(canonicalRepo, "src", "main", "java", "Main.java"), "class Main {}\n")

	repoLink := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(canonicalRepo, repoLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := jvm.NewAdapter().DetectWithConfidence(context.Background(), repoLink)
	if err == nil {
		t.Fatal("expected symlinked repo root to be rejected")
	}
}

func TestJVMSymlinkedRepoAncestorFailsClosed(t *testing.T) {
	t.Parallel()

	canonicalParent := filepath.Join(t.TempDir(), "canonical-parent")
	repo := filepath.Join(canonicalParent, "repo")
	writeFile(t, filepath.Join(repo, "src", "main", "java", "Main.java"), "class Main {}\n")

	ancestorLink := filepath.Join(t.TempDir(), "ancestor-link")
	if err := os.Symlink(canonicalParent, ancestorLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := jvm.NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(ancestorLink, "repo"))
	if err == nil {
		t.Fatal("expected symlinked repo ancestor to be rejected")
	}
}

func TestJVMRelativeRepoPathDetectsInDefaultTempDir(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	writeFile(t, filepath.Join(repo, "src", "main", "java", "Main.java"), "class Main {}\n")

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})
	if err := os.Chdir(parent); err != nil {
		t.Fatalf("chdir %s: %v", parent, err)
	}

	detection, err := jvm.NewAdapter().DetectWithConfidence(context.Background(), "repo")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected relative repo path to detect JVM sources, got %#v", detection)
	}
}

func TestJVMAnalyseRejectsSymlinkedRepoRootAndAcceptsRealRoots(t *testing.T) {
	t.Parallel()

	canonicalRepo := t.TempDir()
	writeFile(t, filepath.Join(canonicalRepo, "src", "main", "java", "Main.java"), "class Main {}\n")

	t.Run("real raw tempdir root", func(t *testing.T) {
		if _, err := jvm.NewAdapter().Analyse(context.Background(), language.Request{RepoPath: canonicalRepo, TopN: 1}); err != nil {
			t.Fatalf("analyse raw tempdir root: %v", err)
		}
	})

	t.Run("real canonical root", func(t *testing.T) {
		realRepo, err := filepath.EvalSymlinks(canonicalRepo)
		if err != nil {
			t.Fatalf("eval symlinks: %v", err)
		}
		if _, err := jvm.NewAdapter().Analyse(context.Background(), language.Request{RepoPath: realRepo, TopN: 1}); err != nil {
			t.Fatalf("analyse canonical root: %v", err)
		}
	})

	t.Run("symlinked repo root", func(t *testing.T) {
		repoLink := filepath.Join(t.TempDir(), "repo-link")
		if err := os.Symlink(canonicalRepo, repoLink); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}

		if _, err := jvm.NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repoLink, TopN: 1}); err == nil {
			t.Fatal("expected analyse to reject symlinked repo root")
		}
	})

	t.Run("symlinked repo ancestor", func(t *testing.T) {
		canonicalParent := filepath.Join(t.TempDir(), "canonical-parent")
		realRepo := filepath.Join(canonicalParent, "repo")
		writeFile(t, filepath.Join(realRepo, "src", "main", "java", "Main.java"), "class Main {}\n")

		ancestorLink := filepath.Join(t.TempDir(), "ancestor-link")
		if err := os.Symlink(canonicalParent, ancestorLink); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}

		if _, err := jvm.NewAdapter().Analyse(context.Background(), language.Request{RepoPath: filepath.Join(ancestorLink, "repo"), TopN: 1}); err == nil {
			t.Fatal("expected analyse to reject symlinked repo ancestor")
		}
	})
}
