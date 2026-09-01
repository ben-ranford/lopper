package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
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

func TestPreparedLockfileNameHelpersPreserveOrder(t *testing.T) {
	present := []presentLockfile{
		{name: "package-lock.json"},
		{name: "poetry.lock"},
	}
	names := preparedLockfileNames(present)
	wantNames := []string{"package-lock.json", "poetry.lock"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("unexpected prepared names:\n got: %#v\nwant: %#v", names, wantNames)
	}

	roundTrip := preparedPresentLockfiles(names)
	if !reflect.DeepEqual(roundTrip, present) {
		t.Fatalf("unexpected prepared present lockfiles:\n got: %#v\nwant: %#v", roundTrip, present)
	}

	if got := preparedPresentLockfiles(nil); len(got) != 0 {
		t.Fatalf("expected nil names to return no lockfiles, got %#v", got)
	}
	if got := preparedLockfileNames(nil); len(got) != 0 {
		t.Fatalf("expected nil lockfiles to return no names, got %#v", got)
	}
}

func TestEvaluatePreparedReplayRuleNormalPackageBranches(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	snapshot := lockfileDirSnapshot{repoPath: repo, path: repo, relDir: "."}
	dir := lockfilePreparedDir{repoPath: repo, path: repo, relDir: "."}

	t.Run("missing lockfile", func(t *testing.T) {
		prepared := lockfilePreparedRule{
			replay: &lockfilePreparedRuleReplay{
				rule:      lockfileRule{manager: "npm", manifest: manifestFileName},
				manifests: []string{manifestFileName},
			},
		}

		finding, found, err := evaluatePreparedReplayRule(dir, prepared, lockfileGitContext{}, newLockfileManifestCache(snapshot))
		if err != nil {
			t.Fatalf("evaluate missing lockfile replay: %v", err)
		}
		if !found || finding.kind != lockfileDriftMissingLockfile || finding.manifest != manifestFileName {
			t.Fatalf("expected missing lockfile finding, got found=%v finding=%#v", found, finding)
		}
	})

	t.Run("stale lockfile", func(t *testing.T) {
		prepared := lockfilePreparedRule{
			replay: &lockfilePreparedRuleReplay{
				rule:      lockfileRule{manager: "npm", manifest: manifestFileName},
				lockfiles: []string{lockfileName},
			},
		}

		finding, found, err := evaluatePreparedReplayRule(dir, prepared, lockfileGitContext{}, newLockfileManifestCache(snapshot))
		if err != nil {
			t.Fatalf("evaluate stale lockfile replay: %v", err)
		}
		if !found || finding.kind != lockfileDriftStaleLockfile || len(finding.lockfiles) != 1 || finding.lockfiles[0].name != lockfileName {
			t.Fatalf("expected stale lockfile finding, got found=%v finding=%#v", found, finding)
		}
	})

	t.Run("without replay inputs", func(t *testing.T) {
		finding, found, err := evaluatePreparedReplayRule(dir, lockfilePreparedRule{replay: &lockfilePreparedRuleReplay{
			rule: lockfileRule{manager: "custom", manifest: "custom.toml"},
		}}, lockfileGitContext{}, newLockfileManifestCache(snapshot))
		if err != nil {
			t.Fatalf("evaluate empty replay: %v", err)
		}
		if found || finding.kind != 0 || finding.manifest != "" || len(finding.lockfiles) != 0 {
			t.Fatalf("expected no finding without replay inputs, got found=%v finding=%#v", found, finding)
		}
	})
}

func TestAppendPreparedReplayRuleHandlesManifestReadErrors(t *testing.T) {
	repo := t.TempDir()
	dir := lockfilePreparedDir{repoPath: repo, path: repo, relDir: "."}
	rule := lockfilePreparedRule{
		replay: &lockfilePreparedRuleReplay{
			rule: lockfileRule{
				manager:               "Poetry",
				manifest:              pyprojectManifestName,
				manifestMatcherLabel:  pyprojectPoetrySection,
				manifestMatcherNeedle: pyprojectSectionNeedle(pyprojectPoetrySection),
			},
			manifests: []string{pyprojectManifestName},
			lockfiles: []string{poetryLockName},
		},
	}

	t.Run("recoverable", func(t *testing.T) {
		snapshot := lockfileDirSnapshot{repoPath: repo, path: repo, relDir: "."}
		cache := newLockfileManifestCacheWithIO(snapshot, lockfileManifestIO{
			readFileUnderLimit: func(string, string, int64) ([]byte, error) {
				return nil, safeio.ErrFileTooLarge
			},
		})
		result := &lockfileDriftResult{}

		if !result.appendPreparedReplayRule(dir, rule, lockfileGitContext{}, cache) {
			t.Fatal("expected recoverable manifest read error to keep scanning")
		}
		if !errors.Is(result.err, safeio.ErrFileTooLarge) {
			t.Fatalf("expected recoverable read error to be retained, got %v", result.err)
		}
		if len(result.orderedWarnings) != 1 {
			t.Fatalf("expected one recoverable warning, got %#v", result.orderedWarnings)
		}
	})

	t.Run("fatal", func(t *testing.T) {
		snapshot := lockfileDirSnapshot{repoPath: repo, path: repo, relDir: "."}
		cache := newLockfileManifestCacheWithIO(snapshot, lockfileManifestIO{
			readFileUnderLimit: func(string, string, int64) ([]byte, error) {
				return nil, fs.ErrPermission
			},
		})
		result := &lockfileDriftResult{}

		if result.appendPreparedReplayRule(dir, rule, lockfileGitContext{}, cache) {
			t.Fatal("expected fatal manifest read error to stop scanning")
		}
		if !errors.Is(result.err, fs.ErrPermission) {
			t.Fatalf("expected fatal read error to be retained, got %v", result.err)
		}
		if len(result.orderedWarnings) != 0 {
			t.Fatalf("expected no recoverable warnings for fatal error, got %#v", result.orderedWarnings)
		}
	})
}
