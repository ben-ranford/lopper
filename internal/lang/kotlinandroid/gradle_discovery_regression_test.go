package kotlinandroid

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestGradleDiscoveryRejectsIncompleteResults(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "implementation 'org.example:early:1'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, settingsGradleName), `dependencyResolutionManagement { versionCatalogs { create("libs") { from(files("missing.versions.toml")) } } }`)
	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
	if err == nil || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("explicit missing catalog produced partial success: error=%v result=%+v", err, result)
	}
}

func TestStrictGradleMalformedBuildFailsWithoutPartialDeclarations(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "implementation 'org.example:early:1'\nimplementation(\n")
	descriptors, lookups, warnings, err := collectDeclaredDependenciesContext(context.Background(), repo)
	var discoveryErr *shared.GradleDiscoveryError
	if !errors.As(err, &discoveryErr) || len(descriptors) != 0 || !reflect.DeepEqual(lookups, dependencyLookups{}) || len(warnings) != 0 {
		t.Fatalf("malformed strict build returned descriptors=%+v lookups=%+v warnings=%v error=%v", descriptors, lookups, warnings, err)
	}
}

func TestGradleDiscoveryKeepsLateDeclarations(t *testing.T) {
	for _, catalog := range []bool{false, true} {
		t.Run(map[bool]string{false: "build", true: "catalog"}[catalog], func(t *testing.T) {
			repo := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repo, "Main.kt"), "package app\nimport org.example.Widget\nfun run() = Widget()\n")
			build := strings.Repeat(" ", 2<<20) + "implementation 'org.example:late:1'\n"
			if catalog {
				build = "implementation libs.late\n"
				testutil.MustWriteFile(t, filepath.Join(repo, "gradle", "libs.versions.toml"), strings.Repeat(" ", 2<<20)+"[libraries]\nlate = 'org.example:late:1'\n")
			}
			testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), build)
			result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
			if err != nil || len(result.Dependencies) != 1 || result.Dependencies[0].Name != "late" {
				t.Fatalf("late declaration lost: error=%v dependencies=%+v", err, result.Dependencies)
			}
		})
	}
}

func TestGradleDiscoveryLegacyRootSkipCompatibility(t *testing.T) {
	for _, name := range []string{"project", "build", "target"} {
		t.Run(name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), name)
			testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "implementation 'org.example:root:1'\n")
			testutil.MustWriteFile(t, filepath.Join(repo, "out", buildGradleName), "implementation 'org.example:hidden:1'\n")
			items, _, _, _ := collectGradleDeclaredDependencyDescriptors(repo)
			want := 0
			if name == "project" {
				want = 1
			}
			if len(items) != want {
				t.Fatalf("root %s: %v", name, items)
			}
		})
	}
}

func TestStrictGradleDiscoveryCancellationSkipAndConsumerFailure(t *testing.T) {
	t.Run("canceled before walk", func(t *testing.T) {
		repo := t.TempDir()
		root, err := safeio.OpenRootNoFollow(repo)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := root.Close(); err != nil {
				t.Error(err)
			}
		}()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		err = streamGradleFilesContext(ctx, repo, root, shared.NewGradleDiscoveryBudget(repo), func(string, []byte) error {
			called = true
			return nil
		})
		var discoveryErr *shared.GradleDiscoveryError
		if called || !errors.As(err, &discoveryErr) || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled walk called consumer=%t error=%v", called, err)
		}
	})

	t.Run("legacy skipped root", func(t *testing.T) {
		repo := filepath.Join(t.TempDir(), "build")
		testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "implementation 'org.example:hidden:1'\n")
		root, err := safeio.OpenRootNoFollow(repo)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := root.Close(); err != nil {
				t.Error(err)
			}
		}()
		called := false
		if err := streamGradleFilesContext(context.Background(), repo, root, shared.NewGradleDiscoveryBudget(repo), func(string, []byte) error {
			called = true
			return nil
		}); err != nil || called {
			t.Fatalf("legacy root skip called consumer=%t error=%v", called, err)
		}
	})

	t.Run("consumer error is propagated", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "implementation 'org.example:kept:1'\n")
		root, err := safeio.OpenRootNoFollow(repo)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := root.Close(); err != nil {
				t.Error(err)
			}
		}()
		consumerErr := errors.New("parser stopped")
		calls := 0
		err = streamGradleFilesContext(context.Background(), repo, root, shared.NewGradleDiscoveryBudget(repo), func(string, []byte) error {
			calls++
			return consumerErr
		})
		if calls != 1 || !errors.Is(err, consumerErr) {
			t.Fatalf("consumer calls=%d error=%v", calls, err)
		}
	})

	t.Run("read cancellation is typed", func(t *testing.T) {
		assertStrictGradleReadCancellationIsTyped(t)
	})
}

func assertStrictGradleReadCancellationIsTyped(t *testing.T) {
	t.Helper()
	repo := t.TempDir()
	leaf := buildGradleName
	testutil.MustWriteFile(t, filepath.Join(repo, leaf), "implementation 'org.example:kept:1'\n")
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	dir, err := safeio.OpenPinnedDirectory(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	var entry fs.DirEntry
	for _, candidate := range entries {
		if candidate.Name() == leaf {
			entry = candidate
			break
		}
	}
	if entry == nil {
		t.Fatalf("fixture entry %q missing", leaf)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, warning, err := readGradleInputWithinRoot(ctx, repo, shared.RootedWalkFile{Parent: root, Leaf: leaf, Path: filepath.Join(repo, leaf), Entry: entry}, shared.NewGradleDiscoveryBudget(repo))
	var discoveryErr *shared.GradleDiscoveryError
	if warning != "" || !errors.As(err, &discoveryErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("warning=%q error=%v", warning, err)
	}
}

func TestGradleLockfileContextCancellationDiscardsParsedCoordinates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checkedCtx := &cancelOnErrCallContext{
		deadline: ctx.Deadline,
		done:     ctx.Done,
		baseErr:  ctx.Err,
		value:    ctx.Value,
		cancel:   cancel,
		cancelAt: 2,
	}
	items, err := parseGradleLockfileContentContext(checkedCtx, "org.example:early:1=compile\norg.example:late:2=compile")
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(items, []dependencyDescriptor(nil)) {
		t.Fatalf("canceled lock parse returned partial items=%+v error=%v", items, err)
	}
	if checkedCtx.calls != 2 {
		t.Fatalf("expected cancellation at the second line after the first coordinate parsed; ctx.Err called %d times", checkedCtx.calls)
	}
}

// cancelOnErrCallContext cancels at one deterministic context check while
// preserving Context's other behavior. The lockfile parser checks once before
// each line, so cancelAt=2 allows line one to be parsed before line two fails.
type cancelOnErrCallContext struct {
	deadline func() (time.Time, bool)
	done     func() <-chan struct{}
	baseErr  func() error
	value    func(any) any
	cancel   context.CancelFunc
	calls    int
	cancelAt int
}

func (c *cancelOnErrCallContext) Deadline() (time.Time, bool) {
	return c.deadline()
}

func (c *cancelOnErrCallContext) Done() <-chan struct{} {
	return c.done()
}

func (c *cancelOnErrCallContext) Err() error {
	c.calls++
	if c.calls == c.cancelAt {
		c.cancel()
	}
	return c.baseErr()
}

func (c *cancelOnErrCallContext) Value(key any) any {
	return c.value(key)
}

func TestStrictGradleKnownSymlinkInputsWarnAndSkip(t *testing.T) {
	for _, symlinkName := range []string{buildGradleName, buildGradleKTSName, gradleLockfileName} {
		t.Run(symlinkName, func(t *testing.T) {
			assertKnownGradleSymlinkInputWarnsAndSkips(t, symlinkName)
		})
	}
}

func assertKnownGradleSymlinkInputWarnsAndSkips(t *testing.T, symlinkName string) {
	t.Helper()
	repo, target := writeKnownGradleSymlinkFixture(t, symlinkName)
	if err := os.Symlink(target, filepath.Join(repo, symlinkName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	descriptors, lookups, warnings, err := collectDeclaredDependenciesContext(context.Background(), repo)
	if err != nil {
		t.Fatalf("known symlink should be warned and skipped: %v", err)
	}
	if len(descriptors) != 1 || descriptors[0].Name != "kept" {
		t.Fatalf("symlink target or sibling declaration mismatch: %+v", descriptors)
	}
	joinedWarnings := strings.Join(warnings, "\n")
	if strings.Contains(joinedWarnings, "ignored") || !strings.Contains(joinedWarnings, symlinkName) {
		t.Fatalf("expected warning for skipped %s without target content: %v", symlinkName, warnings)
	}
	if symlinkName == gradleLockfileName && lookups.HasLockfile {
		t.Fatal("skipped symlink lockfile was treated as a readable lockfile")
	}
}

func writeKnownGradleSymlinkFixture(t *testing.T, symlinkName string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	targetDir := t.TempDir()
	siblingName, siblingContent := buildGradleName, "implementation 'org.example:kept:1'\n"
	if symlinkName == buildGradleName {
		siblingName, siblingContent = buildGradleKTSName, "implementation(\"org.example:kept:1\")\n"
	}
	testutil.MustWriteFile(t, filepath.Join(repo, siblingName), siblingContent)
	targetContent := "implementation 'org.example:ignored:9'\n"
	if symlinkName == gradleLockfileName {
		targetContent = "org.example:ignored:9=compile\n"
	}
	target := filepath.Join(targetDir, "outside.gradle")
	testutil.MustWriteFile(t, target, targetContent)
	return repo, target
}

func TestStrictGradleRegularInputReplacedBySymlinkIsFatalWithoutPartialResults(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "implementation 'org.example:early:1'\n")
	late := filepath.Join(repo, buildGradleKTSName)
	testutil.MustWriteFile(t, late, "implementation 'org.example:late:1'\n")
	external := filepath.Join(t.TempDir(), "replacement.gradle")
	testutil.MustWriteFile(t, external, "implementation 'org.example:replacement:9'\n")

	baseRoot, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := baseRoot.Close(); err != nil {
			t.Error(err)
		}
	}()
	state := &replaceGradleInputAfterCatalogWalk{path: late, target: external}
	root := &replaceGradleInputRoot{Root: baseRoot, state: state}
	descriptors, lookups, warnings, err := collectGradleDeclarationsWithinRoot(context.Background(), repo, root, shared.NewGradleDiscoveryBudget(repo))
	var discoveryErr *shared.GradleDiscoveryError
	if !state.replaced || state.targetEntryBatches != 2 {
		t.Fatalf("replacement seam did not run at strict build walk: state=%+v", state)
	}
	if !errors.As(err, &discoveryErr) || !errors.Is(err, safeio.ErrTargetPathSymlink) {
		t.Fatalf("regular-observed replacement must stay fatal: %v", err)
	}
	if len(descriptors) != 0 || !reflect.DeepEqual(lookups, dependencyLookups{}) || len(warnings) != 0 {
		t.Fatalf("partial strict result escaped after replacement: descriptors=%+v lookups=%+v warnings=%v", descriptors, lookups, warnings)
	}
}

type replaceGradleInputAfterCatalogWalk struct {
	path               string
	target             string
	targetEntryBatches int
	replaced           bool
}

type replaceGradleInputRoot struct {
	safeio.Root
	state *replaceGradleInputAfterCatalogWalk
}

func (r *replaceGradleInputRoot) Open(name string) (safeio.File, error) {
	file, err := r.Root.Open(name)
	if err != nil || name != "." {
		return file, err
	}
	directory, ok := file.(safeio.ReadDirFile)
	if !ok {
		return file, nil
	}
	return &replaceGradleInputReadDirFile{ReadDirFile: directory, state: r.state}, nil
}

func (r *replaceGradleInputRoot) OpenRoot(name string) (safeio.Root, error) {
	root, err := r.Root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &replaceGradleInputRoot{Root: root, state: r.state}, nil
}

type replaceGradleInputReadDirFile struct {
	safeio.ReadDirFile
	state *replaceGradleInputAfterCatalogWalk
}

func (f *replaceGradleInputReadDirFile) ReadDir(count int) ([]fs.DirEntry, error) {
	entries, err := f.ReadDirFile.ReadDir(count)
	targetEntryBatch := false
	for _, entry := range entries {
		if entry.Name() == filepath.Base(f.state.path) {
			targetEntryBatch = true
			break
		}
	}
	if targetEntryBatch {
		f.state.targetEntryBatches++
		if f.state.targetEntryBatches == 2 {
			if removeErr := os.Remove(f.state.path); removeErr != nil {
				return entries, errors.Join(err, removeErr)
			}
			if linkErr := os.Symlink(f.state.target, f.state.path); linkErr != nil {
				return entries, errors.Join(err, linkErr)
			}
			f.state.replaced = true
		}
	}
	return entries, err
}

func TestReplaceGradleInputAfterRegularObservation(t *testing.T) {
	repo := t.TempDir()
	target := filepath.Join(repo, buildGradleName)
	testutil.MustWriteFile(t, target, "implementation 'org.example:late:1'\n")
	external := filepath.Join(t.TempDir(), "replacement.gradle")
	testutil.MustWriteFile(t, external, "implementation 'org.example:replacement:9'\n")
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()

	// Capture the regular directory-entry metadata before replacing the path.
	dir, err := safeio.OpenPinnedDirectory(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	var observed fs.DirEntry
	for _, entry := range entries {
		if entry.Name() == filepath.Base(target) {
			observed = entry
			break
		}
	}
	if observed == nil || observed.Type()&fs.ModeType != 0 {
		t.Fatalf("fixture did not retain regular entry metadata: %v", observed)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, target); err != nil {
		t.Fatal(err)
	}

	// The rooted visitor must treat saved regular metadata as authoritative and
	// preserve the replacement failure instead of downgrading it to a warning.
	file := shared.RootedWalkFile{Parent: root, Leaf: filepath.Base(target), Path: target, Entry: observed}
	_, _, err = readGradleInputWithinRoot(context.Background(), repo, file, shared.NewGradleDiscoveryBudget(repo))
	var discoveryErr *shared.GradleDiscoveryError
	if !errors.As(err, &discoveryErr) || !errors.Is(err, safeio.ErrTargetPathSymlink) {
		t.Fatalf("regular-observed symlink replacement must remain fatal: %v", err)
	}
}
