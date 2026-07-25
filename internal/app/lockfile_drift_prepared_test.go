package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestLockfileManifestIOFromContextUsesDefaults(t *testing.T) {
	called := false
	customRead := func(string, string) ([]byte, error) {
		called = true
		return []byte("ok"), nil
	}

	ctx := context.WithValue(context.Background(), lockfileManifestIOContextKey{}, lockfileManifestIO{
		readFileUnder: customRead,
	})
	fromContext := lockfileManifestIOFromContext(ctx)
	if fromContext.readFileUnder == nil || fromContext.readFileUnderLimit == nil {
		t.Fatalf("expected context readers to be defaulted, got %#v", fromContext)
	}
	if _, err := fromContext.readFileUnder("", ""); err != nil {
		t.Fatalf("custom manifest reader returned error: %v", err)
	}
	if !called {
		t.Fatal("expected context manifest reader to be preserved")
	}
}

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

func TestLockfileManifestChangeCandidatePathsWithReadErrorsSkipsRecoverableManifestErrors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
	writeFile(t, filepath.Join(repo, poetryLockName), "# lock\n")
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, lockfileName), "{}\n")

	snapshot, err := readLockfileDirSnapshot(repo, repo)
	if err != nil {
		t.Fatalf("read lockfile snapshot: %v", err)
	}

	readErrors := &lockfileManifestReadErrors{}
	rules := []lockfileRule{
		mustLockfileRule(t, "Poetry", pyprojectManifestName),
		mustLockfileRule(t, "npm", manifestFileName),
	}
	candidates, err := lockfileManifestChangeCandidatePathsWithReadErrors(snapshot, rules, newLockfileManifestCache(snapshot), readErrors)
	if err != nil {
		t.Fatalf("collect candidate paths: %v", err)
	}

	assertCandidatePaths(t, candidates, []string{lockfileName, manifestFileName})
	if len(readErrors.records) != 1 {
		t.Fatalf("expected one recoverable manifest read error, got %#v", readErrors.records)
	}
	if !errors.Is(readErrors.records[0].err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected recoverable size error, got %v", readErrors.records[0].err)
	}
}

func TestPrepareLockfileRulePropagatesDistributedLockfileDiscoveryError(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, dotnetCentralManifest)
	writeFile(t, manifestPath, "<Project></Project>\n")
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("stat central manifest: %v", err)
	}

	snapshot := lockfileDirSnapshot{
		repoPath: root,
		path:     filepath.Join(root, "missing"),
		relDir:   ".",
		files: map[string]fs.FileInfo{
			dotnetCentralManifest: manifestInfo,
		},
	}

	_, _, err = prepareLockfileRule(snapshot, mustLockfileRule(t, ".NET", dotnetCentralManifest), newLockfileManifestCache(snapshot), &lockfileManifestReadErrors{})
	if err == nil {
		t.Fatal("expected distributed lockfile discovery error")
	}
}

func TestPrepareLockfileRuleSkipsRecoverableReadErrorsWithoutCollector(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
	writeFile(t, filepath.Join(repo, poetryLockName), "# lock\n")

	snapshot, err := readLockfileDirSnapshot(repo, repo)
	if err != nil {
		t.Fatalf("read lockfile snapshot: %v", err)
	}

	prepared, candidates, err := prepareLockfileRule(snapshot, mustLockfileRule(t, "Poetry", pyprojectManifestName), newLockfileManifestCache(snapshot), nil)
	if err != nil {
		t.Fatalf("prepare rule with recoverable read error: %v", err)
	}
	if prepared.manifestReadErr != nil {
		t.Fatalf("expected nil collector to skip recording manifest read error, got %v", prepared.manifestReadErr)
	}
	if prepared.replay != nil || prepared.manifestChange != nil {
		t.Fatalf("expected no prepared replay state on recoverable read error, got %#v", prepared)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected no candidate paths on recoverable read error, got %#v", candidates)
	}
}

func TestPrepareLockfileRuleAndReplayDetectStaleLockfileForNonMatchingManifest(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pyprojectManifestName), "[build-system]\nrequires = [\"setuptools\"]\n")
	writeFile(t, filepath.Join(repo, poetryLockName), "# lock\n")

	snapshot, err := readLockfileDirSnapshot(repo, repo)
	if err != nil {
		t.Fatalf("read lockfile snapshot: %v", err)
	}

	prepared, candidates, err := prepareLockfileRule(snapshot, mustLockfileRule(t, "Poetry", pyprojectManifestName), newLockfileManifestCache(snapshot), &lockfileManifestReadErrors{})
	if err != nil {
		t.Fatalf("prepare stale replay rule: %v", err)
	}
	if prepared.replay == nil {
		t.Fatalf("expected replay state for stale non-matching manifest, got %#v", prepared)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected no candidate paths for stale non-matching manifest, got %#v", candidates)
	}
	if len(prepared.replay.lockfiles) != 1 || prepared.replay.lockfiles[0] != poetryLockName {
		t.Fatalf("expected replay lockfile names to be retained, got %#v", prepared.replay.lockfiles)
	}

	dir := lockfilePreparedDir{repoPath: repo, path: repo, relDir: "."}
	finding, found, err := evaluatePreparedReplayRule(dir, prepared, lockfileGitContext{}, newLockfileManifestCache(snapshot))
	if err != nil {
		t.Fatalf("evaluate prepared replay rule: %v", err)
	}
	if !found {
		t.Fatal("expected stale lockfile finding from prepared replay rule")
	}
	if finding.kind != lockfileDriftStaleLockfile {
		t.Fatalf("expected stale lockfile finding, got %#v", finding)
	}
	if len(finding.lockfiles) != 1 || finding.lockfiles[0].name != poetryLockName {
		t.Fatalf("expected stale finding to retain lockfile names, got %#v", finding.lockfiles)
	}
}

func TestEvaluatePreparedReplayRuleReturnsNoFindingWithoutReplayInputs(t *testing.T) {
	repo := t.TempDir()
	snapshot := lockfileDirSnapshot{repoPath: repo, path: repo, relDir: "."}
	cache := newLockfileManifestCache(snapshot)
	dir := lockfilePreparedDir{repoPath: repo, path: repo, relDir: "."}

	finding, found, err := evaluatePreparedReplayRule(dir, lockfilePreparedRule{}, lockfileGitContext{}, cache)
	if err != nil {
		t.Fatalf("evaluate replay without state: %v", err)
	}
	if found {
		t.Fatalf("expected no finding without replay state, got found=%v finding=%#v", found, finding)
	}
	if finding.kind != 0 || finding.manifest != "" || finding.relDir != "" || len(finding.lockfiles) != 0 {
		t.Fatalf("expected no finding without replay state, got found=%v finding=%#v", found, finding)
	}

	emptyReplay := lockfilePreparedRule{
		replay: &lockfilePreparedRuleReplay{
			rule: lockfileRule{manager: "custom", manifest: "custom.toml"},
		},
	}
	finding, found, err = evaluatePreparedReplayRule(dir, emptyReplay, lockfileGitContext{}, cache)
	if err != nil {
		t.Fatalf("evaluate replay without manifests or lockfiles: %v", err)
	}
	if found {
		t.Fatalf("expected no finding without replay inputs, got found=%v finding=%#v", found, finding)
	}
	if finding.kind != 0 || finding.manifest != "" || finding.relDir != "" || len(finding.lockfiles) != 0 {
		t.Fatalf("expected no finding without replay inputs, got found=%v finding=%#v", found, finding)
	}
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
