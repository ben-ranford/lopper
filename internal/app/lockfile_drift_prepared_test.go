//go:build !regressionproof

package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestDotnetLockfilePathIsRelativeToFilesystemRoot(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	path := filepath.Join(root, "projects", dotnetLockfileName)

	relPath, err := dotnetProjectLockfileRelativePath(root, path)
	if err != nil {
		t.Fatalf("relative lockfile path: %v", err)
	}
	if want := "projects/" + dotnetLockfileName; relPath != want {
		t.Fatalf("expected root-relative lockfile path %q, got %q", want, relPath)
	}
}

func TestPrepareLockfileManifestChangeCandidatesBuildsDotnetLockfileIndexOnce(t *testing.T) {
	repo := t.TempDir()
	for _, dir := range []string{"alpha", "beta", "gamma"} {
		writeFile(t, filepath.Join(repo, dir, dotnetCentralManifest), "<Project></Project>\n")
		writeFile(t, filepath.Join(repo, dir, "src", dotnetProjectManifest), "<Project></Project>\n")
		writeFile(t, filepath.Join(repo, dir, "src", dotnetLockfileName), "{}\n")
	}

	original := findDotnetProjectLockfilesFn
	calls := 0
	findDotnetProjectLockfilesFn = func(ctx context.Context, rootDir string) ([]presentLockfile, error) {
		calls++
		return original(ctx, rootDir)
	}
	t.Cleanup(func() { findDotnetProjectLockfilesFn = original })

	_, _, err := prepareLockfileManifestChangeCandidates(context.Background(), repo, []lockfileRule{
		mustLockfileRule(t, ".NET", dotnetCentralManifest),
	})
	if err != nil {
		t.Fatalf("prepare lockfile manifest change candidates: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one distributed .NET lockfile walk, got %d", calls)
	}
}

func TestDotnetProjectLockfileIndexSlicesSortedRepositoryPathsByScope(t *testing.T) {
	index := &dotnetProjectLockfileIndex{
		repoPath: t.TempDir(),
		lockfiles: []string{
			"alpha-other/src/" + dotnetLockfileName,
			"alpha/src/" + dotnetLockfileName,
			"beta/src/" + dotnetLockfileName,
		},
		initialized: true,
	}

	lockfiles, err := index.lockfilesUnder("alpha")
	if err != nil {
		t.Fatalf("index alpha lockfiles: %v", err)
	}
	if len(lockfiles) != 1 || lockfiles[0].name != "src/"+dotnetLockfileName {
		t.Fatalf("expected only alpha lockfile, got %#v", lockfiles)
	}
	if len(index.lockfiles) != 3 {
		t.Fatalf("expected one repository-wide lockfile list, got %#v", index.lockfiles)
	}
	rootLockfiles, err := index.lockfilesUnder(".")
	if err != nil {
		t.Fatalf("index root lockfiles: %v", err)
	}
	if len(rootLockfiles) != 3 || rootLockfiles[1].name != "alpha/src/"+dotnetLockfileName {
		t.Fatalf("expected root scope to retain sorted repository-relative paths, got %#v", rootLockfiles)
	}
}

func TestDotnetProjectLockfileIndexDoesNotMaterializeAncestorCopies(t *testing.T) {
	assertNestedDotnetLockfileAllocationBound(t, ".NET lockfile index", 10, measureDotnetProjectLockfileIndexAllocs)
}

func TestDotnetProjectLockfileIndexDoesNotCacheAncestorDerivedScopes(t *testing.T) {
	index := &dotnetProjectLockfileIndex{
		repoPath: ".",
		scoped:   true,
		scopedLockfilesByScope: map[string][]string{
			".": {
				"nested/one/" + dotnetLockfileName,
				"nested/two/" + dotnetLockfileName,
			},
		},
	}
	for _, scope := range []string{"nested", "nested/one", "nested/two"} {
		lockfiles, err := index.scopedLockfilesUnder(scope)
		if err != nil {
			t.Fatalf("read %s lockfiles: %v", scope, err)
		}
		if len(lockfiles) == 0 {
			t.Fatalf("expected %s lockfiles from ancestor scope", scope)
		}
	}
	if len(index.scopedLockfilesByScope) != 1 {
		t.Fatalf("expected only filesystem-walk scopes to remain cached, got %#v", index.scopedLockfilesByScope)
	}
}

func measureDotnetProjectLockfileIndexAllocs(t *testing.T, repo string, rules []lockfileRule) float64 {
	t.Helper()
	return testing.AllocsPerRun(3, func() {
		index, err := newDotnetProjectLockfileIndex(context.Background(), repo, rules, false)
		if err != nil {
			t.Fatalf("new .NET lockfile index: %v", err)
		}
		if _, err := index.lockfilesUnder("."); err != nil {
			t.Fatalf("read .NET lockfile index: %v", err)
		}
	})
}

func TestPrepareLockfileManifestChangeCandidatesSkipsDotnetIndexWithoutCentralManifest(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", dotnetProjectManifest), "<Project></Project>\\n")
	writeFile(t, filepath.Join(repo, "src", dotnetLockfileName), "{}\\n")

	original := findDotnetProjectLockfilesFn
	calls := 0
	findDotnetProjectLockfilesFn = func(ctx context.Context, rootDir string) ([]presentLockfile, error) {
		calls++
		return original(ctx, rootDir)
	}
	t.Cleanup(func() { findDotnetProjectLockfilesFn = original })

	_, _, err := prepareLockfileManifestChangeCandidates(context.Background(), repo, []lockfileRule{
		mustLockfileRule(t, ".NET", dotnetCentralManifest),
	})
	if err != nil {
		t.Fatalf("prepare lockfile manifest change candidates: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no distributed .NET lockfile walk without a central manifest, got %d", calls)
	}
}

func TestScanLockfileDriftStopOnFirstScopesDotnetLockfileIndexToCentralManifest(t *testing.T) {
	repo := t.TempDir()
	centralDir := filepath.Join(repo, "a-central")
	writeFile(t, filepath.Join(centralDir, dotnetCentralManifest), "<Project></Project>\n")

	original := findDotnetProjectLockfilesFn
	var roots []string
	findDotnetProjectLockfilesFn = func(ctx context.Context, rootDir string) ([]presentLockfile, error) {
		roots = append(roots, rootDir)
		if filepath.Clean(rootDir) == filepath.Clean(repo) {
			return nil, errors.New("unrelated later subtree is unreadable")
		}
		return nil, nil
	}
	t.Cleanup(func() { findDotnetProjectLockfilesFn = original })

	warnings, err := scanLockfileDrift(context.Background(), repo, lockfileGitContext{}, true, []lockfileRule{
		mustLockfileRule(t, ".NET", dotnetCentralManifest),
	})
	if err != nil {
		t.Fatalf("scan lockfile drift: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "a-central") {
		t.Fatalf("expected early central-manifest warning, got %#v", warnings)
	}
	if len(roots) != 1 || filepath.Clean(roots[0]) != filepath.Clean(centralDir) {
		t.Fatalf("expected one central-subtree lockfile walk, got %#v", roots)
	}
}

func TestScanLockfileDriftStopOnFirstReusesAncestorDotnetLockfileIndex(t *testing.T) {
	repo := t.TempDir()
	manifest := "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n"
	for _, dir := range []string{".", "src", "src/App"} {
		writeFile(t, filepath.Join(repo, dir, dotnetCentralManifest), manifest)
	}
	writeFile(t, filepath.Join(repo, "src", "App", "Project", dotnetProjectManifest), "<Project></Project>\n")
	writeFile(t, filepath.Join(repo, "src", "App", "Project", dotnetLockfileName), "{}\n")

	original := findDotnetProjectLockfilesFn
	calls := 0
	findDotnetProjectLockfilesFn = func(ctx context.Context, rootDir string) ([]presentLockfile, error) {
		calls++
		return original(ctx, rootDir)
	}
	t.Cleanup(func() { findDotnetProjectLockfilesFn = original })

	warnings, err := scanLockfileDrift(context.Background(), repo, lockfileGitContext{}, true, []lockfileRule{
		mustLockfileRule(t, ".NET", dotnetCentralManifest),
	})
	if err != nil {
		t.Fatalf("scan lockfile drift: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings with matching project lockfile, got %#v", warnings)
	}
	if calls != 1 {
		t.Fatalf("expected one distributed .NET lockfile walk, got %d", calls)
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
