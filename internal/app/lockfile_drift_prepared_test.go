package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPyprojectSectionMatcherMatchesAndMissesSections(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pyprojectManifestName), "[tool.poetry]\nname = \"demo\"\n")

	matched, err := pyprojectSectionMatcher("tool.poetry")(repo, repo)
	if err != nil {
		t.Fatalf("match poetry section: %v", err)
	}
	if !matched {
		t.Fatal("expected Poetry section matcher to match")
	}

	matched, err = pyprojectSectionMatcher("tool.uv")(repo, repo)
	if err != nil {
		t.Fatalf("miss uv section: %v", err)
	}
	if matched {
		t.Fatal("expected uv section matcher to miss Poetry-only manifest")
	}
}

func TestLockfileFailFastBatchScannerHandleWalkEntryFlushesBeforeReturningErrors(t *testing.T) {
	t.Run("returns walk error after flushing buffered candidates", func(t *testing.T) {
		scanner := lockfileFailFastBatchScanner{}
		walkErr := errors.New("walk failed")

		err := scanner.handleWalkEntry(context.Background(), "", nil, walkErr, lockfileWalkState{})
		if !errors.Is(err, walkErr) {
			t.Fatalf("expected walk error, got %v", err)
		}
	})

	t.Run("returns processing error after flushing buffered candidates", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		scanner := lockfileFailFastBatchScanner{}
		err := scanner.handleWalkEntry(ctx, "", nil, nil, lockfileWalkState{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	})
}

func TestFindDotnetProjectLockfilesSortsResultsAndSkipsDirectories(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", "Zulu", dotnetProjectManifest), "<Project></Project>\n")
	writeFile(t, filepath.Join(repo, "src", "Zulu", dotnetLockfileName), "{}\n")
	writeFile(t, filepath.Join(repo, "src", "Alpha", dotnetProjectManifest), "<Project></Project>\n")
	writeFile(t, filepath.Join(repo, "src", "Alpha", dotnetLockfileName), "{}\n")
	if err := os.MkdirAll(filepath.Join(repo, "src", "nested-dir"), 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	lockfiles, err := findDotnetProjectLockfiles(repo)
	if err != nil {
		t.Fatalf("findDotnetProjectLockfiles: %v", err)
	}
	if len(lockfiles) != 2 {
		t.Fatalf("expected two project lockfiles, got %#v", lockfiles)
	}
	if lockfiles[0].name != filepath.ToSlash(filepath.Join("src", "Alpha", dotnetLockfileName)) {
		t.Fatalf("expected sorted Alpha lockfile first, got %#v", lockfiles)
	}
	if lockfiles[1].name != filepath.ToSlash(filepath.Join("src", "Zulu", dotnetLockfileName)) {
		t.Fatalf("expected sorted Zulu lockfile second, got %#v", lockfiles)
	}
}

func TestDirContainsDotnetProjectManifestSkipsSubdirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	writeFile(t, filepath.Join(dir, dotnetProjectManifest), "<Project></Project>\n")

	hasManifest, err := dirContainsDotnetProjectManifest(dir)
	if err != nil {
		t.Fatalf("dirContainsDotnetProjectManifest: %v", err)
	}
	if !hasManifest {
		t.Fatal("expected project manifest to be found after skipping subdirectories")
	}
}
