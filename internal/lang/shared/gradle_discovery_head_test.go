package shared

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
	sitter "github.com/smacker/go-tree-sitter"
)

func TestGradleLegacyParserKeepsPartialResultsWhileContextParserRejectsSyntaxErrors(t *testing.T) {
	content := `
dependencies {
  implementation("androidx.core:core-ktx:1.13.1")
  implementation(group = "com.squareup.okhttp3", name = "okhttp", version = "4.12.0")
  implementation name: 'core-ktx', group: 'androidx.core', version: '1.13.1'
  api group: 'com.google.guava', name: 'guava', version: '33.2.0-jre'
  api name: 'activity-ktx', group: 'androidx.activity'
}
`

	var legacy []GradleDependencyCoordinate
	for _, path := range []string{"build.gradle", "build.gradle.kts"} {
		legacy = append(legacy, ParseGradleDependencyCoordinatesForFile(path, content)...)
		strict, err := ParseGradleDependencyCoordinatesForFileContext(context.Background(), path, []byte(content))
		if err == nil || !reflect.DeepEqual(strict, []GradleDependencyCoordinate(nil)) {
			t.Fatalf("strict parser for %s returned partial results %v, error %v", path, strict, err)
		}
	}

	seen := make(map[string]struct{}, len(legacy))
	for _, coordinate := range legacy {
		seen[coordinate.Group+":"+coordinate.Artifact] = struct{}{}
	}
	for _, want := range []string{
		"androidx.core:core-ktx",
		"com.squareup.okhttp3:okhttp",
		"com.google.guava:guava",
		"androidx.activity:activity-ktx",
	} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("legacy parser lost %q from mixed-syntax input: %#v", want, legacy)
		}
	}
	if references := parseGradleCatalogReferencesForFile("build.gradle", "dependencies { implementation(libs.widget)"); !reflect.DeepEqual(references, []gradleCatalogReference(nil)) {
		t.Fatalf("legacy catalog-reference parser published results from malformed syntax: %+v", references)
	}
}

func TestGradleContextParserReturnsCoordinatesForValidBuildFiles(t *testing.T) {
	for _, tc := range []struct{ path, named string }{
		{path: "build.gradle", named: `testImplementation group: "org.example", name: "test-fixture", version: "2"`},
		{path: "build.gradle.kts", named: `testImplementation(group = "org.example", name = "test-fixture", version = "2")`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			content := `
repositories { mavenCentral() }
dependencies {
  implementation("org.example:widget:1")
  ` + tc.named + `
  implementation(project(":feature"))
  implementation(libs.unresolved)
}
tasks.register("unrelated") { doLast { println("not a dependency") } }
`
			want := []GradleDependencyCoordinate{
				{Group: "org.example", Artifact: "widget", Version: "1"},
				{Group: "org.example", Artifact: "test-fixture", Version: "2"},
			}
			got, err := ParseGradleDependencyCoordinatesForFileContext(context.Background(), tc.path, []byte(content))
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("strict parser coordinates=%+v error=%v, want %+v", got, err, want)
			}
		})
	}
}

func TestGradleDiscoveryByteBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		perFile, total      int64
		limited             bool
	}{
		{"exact file", "1234", "", 4, 8, false},
		{"file plus one", "12345", "", 4, 8, true},
		{"exact aggregate", "123", "45", 4, 5, false},
		{"aggregate plus one", "123", "456", 4, 5, true},
		{"empty at zero", "1234", "", 4, 4, false},
		{"nonempty at zero", "1234", "x", 4, 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeGradleCatalogTestFile(t, filepath.Join(repo, "one"), tc.first)
			writeGradleCatalogTestFile(t, filepath.Join(repo, "two"), tc.second)
			root := openSharedTestRoot(t, repo)
			budget := NewGradleDiscoveryBudget(repo)
			budget.perFile, budget.total = tc.perFile, tc.total
			_, err := budget.ReadWithinRoot(context.Background(), root, "one", filepath.Join(repo, "one"))
			if err == nil {
				_, err = budget.ReadWithinRoot(context.Background(), root, "two", filepath.Join(repo, "two"))
			}
			if errors.Is(err, ErrGradleDiscoveryLimit) != tc.limited {
				t.Fatalf("limit=%t error=%v", tc.limited, err)
			}
			if tc.limited {
				assertGradleLimit(t, err)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestStrictGradleRegularOpenDoesNotBlockOnFIFO runs the potentially blocking
// production open in a child so a regression cannot wedge the test process.
func TestStrictGradleRegularOpenDoesNotBlockOnFIFO(t *testing.T) {
	if operation := os.Getenv("LOPPER_STRICT_GRADLE_FIFO_CHILD"); operation != "" {
		strictGradleFIFOChild(t, operation)
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX FIFO control")
	}
	for _, operation := range []string{"initial", "replacement", "catalog", "zero"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStrictGradleRegularOpenDoesNotBlockOnFIFO$")
			cmd.Env = append(os.Environ(), "LOPPER_STRICT_GRADLE_FIFO_CHILD="+operation)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("strict Gradle read blocked opening FIFO; child stopped by bounded test sentinel: %s", output)
			}
			if err != nil {
				t.Fatalf("strict Gradle FIFO child failed: %v\n%s", err, output)
			}
		})
	}
}

func strictGradleFIFOChild(t *testing.T, operation string) {
	t.Helper()
	repo, leaf, path := makeStrictGradleFIFOFixture(t, operation)
	root := openSharedTestRoot(t, repo)
	wrapped := &strictGradleFIFOReplaceRoot{Root: root, target: leaf, path: path, replace: operation == "replacement" || operation == "catalog" || operation == "zero"}
	budget := NewGradleDiscoveryBudget(repo)
	if operation == "zero" {
		budget.used = budget.total
	}
	err := readStrictGradleFIFOInput(t, operation, repo, wrapped, budget, leaf, path)
	assertStrictGradleFIFORejected(t, operation, err, wrapped)
}

func makeStrictGradleFIFOFixture(t *testing.T, operation string) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	leaf := "input"
	if operation == "catalog" {
		writeGradleCatalogTestFile(t, filepath.Join(repo, "settings.gradle"), `dependencyResolutionManagement { versionCatalogs { create("libs") { from(files("catalog.versions.toml")) } } }`)
		leaf = "catalog.versions.toml"
	}
	path := filepath.Join(repo, leaf)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("regular"), 0o600); err != nil {
		t.Fatal(err)
	}
	if operation == "initial" {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command("mkfifo", path).Run(); err != nil {
			t.Fatalf("create no-writer FIFO: %v", err)
		}
	}
	return repo, leaf, path
}

func readStrictGradleFIFOInput(t *testing.T, operation, repo string, root safeio.Root, budget *GradleDiscoveryBudget, leaf, path string) error {
	t.Helper()
	if operation == "catalog" {
		resolver, _, err := LoadGradleCatalogResolverStrict(context.Background(), repo, root, budget)
		if err != nil && (resolver.knownCatalogs != nil || len(resolver.scopes) != 0) {
			t.Fatalf("fatal catalog replacement returned a partial resolver: %#v", resolver)
		}
		return err
	}
	data, err := budget.ReadWithinRoot(context.Background(), root, leaf, path)
	if err != nil && !reflect.DeepEqual(data, []byte(nil)) {
		t.Fatalf("fatal regular-file replacement returned partial bytes: %q", data)
	}
	return err
}

func assertStrictGradleFIFORejected(t *testing.T, operation string, err error, wrapped *strictGradleFIFOReplaceRoot) {
	t.Helper()
	if err == nil {
		t.Fatal("expected strict read to reject nonregular/replaced FIFO")
	}
	if operation == "replacement" || operation == "catalog" || operation == "zero" {
		var failure *GradleDiscoveryError
		if !errors.As(err, &failure) {
			t.Fatalf("regular-observed FIFO replacement was not fatal: %v", err)
		}
		if !wrapped.replaced {
			t.Fatal("rooted open seam did not replace the observed regular leaf")
		}
	}
	if operation == "zero" && errors.Is(err, ErrGradleDiscoveryLimit) {
		t.Fatalf("regular-observed replacement was downgraded to a byte limit: %v", err)
	}
}

type strictGradleFIFOReplaceRoot struct {
	safeio.Root
	target, path      string
	replace, replaced bool
}

func (r *strictGradleFIFOReplaceRoot) swap(name string) error {
	if !r.replace || r.replaced || filepath.Clean(name) != filepath.Clean(r.target) {
		return nil
	}
	r.replaced = true
	if err := os.Remove(r.path); err != nil {
		return err
	}
	return exec.Command("mkfifo", r.path).Run()
}

func (r *strictGradleFIFOReplaceRoot) Open(name string) (safeio.File, error) {
	if err := r.swap(name); err != nil {
		return nil, err
	}
	return r.Root.Open(name)
}

func (r *strictGradleFIFOReplaceRoot) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	if err := r.swap(name); err != nil {
		return nil, err
	}
	return r.Root.OpenFile(name, flag, perm)
}

func assertGradleLimit(t *testing.T, err error) {
	t.Helper()
	var failure *GradleDiscoveryError
	if !errors.As(err, &failure) || !errors.Is(err, safeio.ErrFileTooLarge) || failure.ObservedAtLeast != failure.Limit+1 {
		t.Fatalf("missing byte limit identity: %v", err)
	}
}

func TestGradleDiscoveryUniqueChargesAndPolicy(t *testing.T) {
	repo := t.TempDir()
	writeGradleCatalogTestFile(t, filepath.Join(repo, "input"), "123")
	root := openSharedTestRoot(t, repo)
	budget := NewGradleDiscoveryBudget(repo)
	if budget.perFile != 32<<20 || budget.total != 256<<20 {
		t.Fatal("production limits changed")
	}
	budget.total = 3
	for range 2 {
		if _, err := budget.ReadWithinRoot(context.Background(), root, "input", filepath.Join(repo, "input")); err != nil {
			t.Fatal(err)
		}
	}
	if budget.used != 3 || len(budget.charged) != 1 {
		t.Fatalf("duplicate charge: %+v", budget)
	}
	for _, limit := range []int64{0, -1} {
		budget.perFile = limit
		if _, err := budget.ReadWithinRoot(context.Background(), root, "input", filepath.Join(repo, "input")); err == nil {
			t.Fatal("nonpositive limit accepted")
		}
	}
}

func TestGradleDiscoveryBudgetRejectsEscapesAndCancellationBeforeOpen(t *testing.T) {
	repo := t.TempDir()
	root := openSharedTestRoot(t, repo)
	budget := NewGradleDiscoveryBudget(repo)
	outside := filepath.Join(filepath.Dir(repo), "outside.gradle")
	_, err := budget.ReadWithinRoot(context.Background(), root, "outside.gradle", outside)
	if !errors.Is(err, safeio.ErrPathEscapesRoot) {
		t.Fatalf("outside input error=%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	openCalls := 0
	observed := &strictGradleOpenFileRoot{Root: root, openFile: func(string, int, os.FileMode) (safeio.File, error) {
		openCalls++
		return nil, errors.New("unexpected open")
	}}
	_, err = NewGradleDiscoveryBudget(repo).ReadWithinRoot(ctx, observed, "input", filepath.Join(repo, "input"))
	var discoveryErr *GradleDiscoveryError
	if openCalls != 0 || !errors.As(err, &discoveryErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read opened=%d error=%v", openCalls, err)
	}
	var nilBudget *GradleDiscoveryBudget
	_, err = nilBudget.ReadWithinRoot(context.Background(), root, "input", filepath.Join(repo, "input"))
	if err == nil || !strings.Contains(err.Error(), "invalid Gradle discovery byte policy") {
		t.Fatalf("nil budget error=%v", err)
	}
}

func TestGradleDiscoveryWalkFailureMapsEveryBound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sentinel error
		resource string
		limit    int
	}{
		{name: "traversal", sentinel: errRootedWalkTraversalLimit, resource: "traversal entries", limit: 12},
		{name: "candidate files", sentinel: errRootedWalkFileLimit, resource: "candidate files", limit: 7},
		{name: "candidate work", sentinel: errRootedWalkWorkLimit, resource: "candidate work items", limit: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget := RootedWalkBudget{MaxTraversalEntries: 12, MaxFiles: 7, MaxWorkItems: 5}
			err := GradleDiscoveryWalkFailure("repo", budget, tc.sentinel)
			var typed *GradleDiscoveryError
			if !errors.As(err, &typed) || !errors.Is(err, ErrGradleDiscoveryLimit) || typed.Resource != tc.resource || typed.Limit != int64(tc.limit) || typed.ObservedAtLeast != int64(tc.limit+1) {
				t.Fatalf("walk error=%+v", err)
			}
		})
	}
	if err := GradleDiscoveryWalkFailure("repo", RootedWalkBudget{}, errors.New("ordinary walk error")); err == nil || errors.Is(err, ErrGradleDiscoveryLimit) {
		t.Fatalf("ordinary walk error was misclassified: %v", err)
	}
}

func TestGradleDiscoveryErrorFormatsBoundedAndOrdinaryFailures(t *testing.T) {
	limit := &GradleDiscoveryError{Path: "gradle/libs.versions.toml", Resource: "file bytes", Limit: 32, ObservedAtLeast: 33, Err: ErrGradleDiscoveryLimit}
	if !strings.Contains(limit.Error(), "observed at least 33") || !errors.Is(limit, ErrGradleDiscoveryLimit) {
		t.Fatalf("limit error=%v", limit)
	}
	ordinary := &GradleDiscoveryError{Path: "settings.gradle", Operation: "parse", Err: context.Canceled}
	if !strings.Contains(ordinary.Error(), "during parse") || !errors.Is(ordinary, context.Canceled) {
		t.Fatalf("ordinary error=%v", ordinary)
	}
}

func TestStrictGradleCatalogRequiresActiveContextAndByteBudget(t *testing.T) {
	repo := t.TempDir()
	root := openSharedTestRoot(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := LoadGradleCatalogResolverStrict(ctx, repo, root, NewGradleDiscoveryBudget(repo)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled catalog load error=%v", err)
	}
	if _, _, err := LoadGradleCatalogResolverStrict(context.Background(), repo, root, nil); err == nil || !strings.Contains(err.Error(), "missing Gradle byte budget") {
		t.Fatalf("catalog load without budget error=%v", err)
	}
	if got := ParseGradleDependencyCoordinates("implementation 'org.example:legacy:1'", nil); !reflect.DeepEqual(got, []GradleDependencyCoordinate(nil)) {
		t.Fatalf("legacy parser with no language returned %v", got)
	}
	var nilResolver *GradleCatalogResolver
	items, warnings, err := nilResolver.ParseDependencyReferencesContext(context.Background(), "build.gradle", []byte("implementation(libs.example)"))
	if err != nil || !reflect.DeepEqual(items, []GradleCatalogLibrary(nil)) || !reflect.DeepEqual(warnings, []string(nil)) {
		t.Fatalf("nil resolver result items=%v warnings=%v error=%v", items, warnings, err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	resolver := &GradleCatalogResolver{}
	items, warnings, err = resolver.ParseDependencyReferencesContext(canceled, "build.gradle", []byte("implementation(libs.example)"))
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(items, []GradleCatalogLibrary(nil)) || !reflect.DeepEqual(warnings, []string(nil)) {
		t.Fatalf("canceled reference parse items=%v warnings=%v error=%v", items, warnings, err)
	}
}

func TestGradleCatalogSkipsKnownNonRegularInputsButNotPriorRegularObservations(t *testing.T) {
	repo := t.TempDir()
	gradleDir := filepath.Join(repo, "gradle")
	if err := os.Mkdir(gradleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	leaf := "libs.versions.toml"
	path := filepath.Join(gradleDir, leaf)
	if err := os.WriteFile(filepath.Join(repo, "catalog-target"), []byte("[libraries]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "catalog-target"), path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	entries, err := os.ReadDir(gradleDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("catalog entries=%v error=%v", entries, err)
	}
	file := RootedWalkFile{Leaf: leaf, Path: path, Entry: entries[0]}
	registry := newGradleCatalogRegistry(repo)
	registry.strict = NewGradleDiscoveryBudget(repo)
	skipped := registry.skipKnownNonRegularGradleFile(file)
	if !skipped || len(registry.warnings) != 1 || !strings.Contains(registry.warnings[0], "symlink") {
		t.Fatalf("unobserved symlink skipped=%t warnings=%v", skipped, registry.warnings)
	}
	registry = newGradleCatalogRegistry(repo)
	registry.strict = NewGradleDiscoveryBudget(repo, path)
	if registry.skipKnownNonRegularGradleFile(file) || len(registry.warnings) != 0 {
		t.Fatalf("prior regular observation was downgraded: warnings=%v", registry.warnings)
	}
}

func TestGradleCatalogDefaultRegistrationRequiresGradleDirectory(t *testing.T) {
	repo := t.TempDir()
	registry := newGradleCatalogRegistry(repo)
	registry.registerDiscoveredDefaultCatalog(RootedWalkFile{Path: filepath.Join(repo, "config", "libs.versions.toml")})
	if len(registry.sources) != 0 {
		t.Fatalf("non-Gradle TOML was registered as default catalog: %+v", registry.sources)
	}
}

type cancelAfterContextErr struct {
	deadline func() (time.Time, bool)
	done     <-chan struct{}
	value    func(any) any
	err      func() error
	cancel   context.CancelFunc
	errCalls int
	cancelAt int
}

func (c *cancelAfterContextErr) Deadline() (time.Time, bool) {
	return c.deadline()
}

func (c *cancelAfterContextErr) Done() <-chan struct{} {
	return c.done
}

func (c *cancelAfterContextErr) Err() error {
	c.errCalls++
	if c.errCalls == c.cancelAt {
		c.cancel()
	}
	return c.err()
}

func (c *cancelAfterContextErr) Value(key any) any {
	return c.value(key)
}

func TestGradleCatalogCancellationAfterParsingDoesNotMergePartialCatalog(t *testing.T) {
	repo := t.TempDir()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelAfterContextErr{
		deadline: base.Deadline,
		done:     base.Done(),
		value:    base.Value,
		err:      base.Err,
		cancel:   cancel,
		cancelAt: 2,
	}
	registry := newGradleCatalogRegistry(repo)
	registry.strict = NewGradleDiscoveryBudget(repo)
	source := gradleCatalogSource{root: repo, name: "libs", path: filepath.Join(repo, "gradle", "libs.versions.toml")}
	err := registry.loadSourceResult(ctx, source, []byte("[libraries]\nwidget = 'org.example:widget:1'\n"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("catalog load after cancellation error=%v", err)
	}
	if len(registry.scopesByRoot) != 0 {
		t.Fatalf("partially parsed catalog was merged: %+v", registry.scopesByRoot)
	}
}

type gradleObservedFile struct {
	safeio.File
	reader            io.Reader
	readBytes, closes *int
	cancel            context.CancelFunc
	closeErr          error
}

func (f *gradleObservedFile) Read(p []byte) (int, error) {
	n, err := f.reader.Read(p)
	*f.readBytes += n
	if f.cancel != nil {
		f.cancel()
	}
	return n, err
}
func (f *gradleObservedFile) Close() error {
	*f.closes++
	return errors.Join(f.File.Close(), f.closeErr)
}

func TestGradleDiscoveryProbeJoinsReadContextAndClose(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "input")
	writeGradleCatalogTestFile(t, path, "1234")
	root := openSharedTestRoot(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readBytes, closes := 0, 0
	closeErr := errors.New("close refused")
	wrapped := &strictGradleOpenFileRoot{Root: root, openFile: func(name string, flag int, perm os.FileMode) (safeio.File, error) {
		file, err := root.OpenFile(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &gradleObservedFile{File: file, reader: strings.NewReader(strings.Repeat("x", 1024)), readBytes: &readBytes, closes: &closes, cancel: cancel, closeErr: closeErr}, nil
	}}
	budget := NewGradleDiscoveryBudget(repo)
	budget.perFile = 4
	data, err := budget.ReadWithinRoot(ctx, wrapped, "input", path)
	if !reflect.DeepEqual(data, []byte(nil)) || readBytes != 5 || closes != 1 {
		t.Fatalf("data=%v read=%d closes=%d", data, readBytes, closes)
	}
	assertGradleLimit(t, err)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, closeErr) {
		t.Fatalf("lost joined causes: %v", err)
	}
}

func TestGradleStrictCatalogDeduplicatesConfiguredSources(t *testing.T) {
	repo := t.TempDir()
	settings := `dependencyResolutionManagement { versionCatalogs { create("one") { from(files("build/custom.toml")) }; create("two") { from(files("build/custom.toml")) } } }`
	catalog := "[libraries]\nwidget = 'org.example:widget:1'\n"
	writeGradleCatalogTestFile(t, filepath.Join(repo, "settings.gradle"), settings)
	writeGradleCatalogTestFile(t, filepath.Join(repo, "build/custom.toml"), catalog)
	budget := NewGradleDiscoveryBudget(repo)
	budget.total = int64(len(settings) + len(catalog))
	resolver, _, err := LoadGradleCatalogResolverStrict(context.Background(), repo, openSharedTestRoot(t, repo), budget)
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := resolver.ParseDependencyReferencesContext(context.Background(), filepath.Join(repo, "build.gradle"), []byte("implementation one.widget\nimplementation two.widget\n"))
	if err != nil || len(items) != 1 || budget.used != budget.total || len(budget.charged) != 2 {
		t.Fatalf("items=%v budget=%+v error=%v", items, budget, err)
	}
}

func TestGradleStrictCatalogMultipleConfiguredFilesKeepsFirstSource(t *testing.T) {
	repo := t.TempDir()
	settings := `dependencyResolutionManagement { versionCatalogs { create("tools") { from(files("first.toml", "second.toml")) } } }`
	first := "[libraries]\nwidget = 'org.example:first:1'\n"
	second := "[libraries]\nwidget = 'org.example:second:2'\n"
	writeGradleCatalogTestFile(t, filepath.Join(repo, "settings.gradle"), settings)
	writeGradleCatalogTestFile(t, filepath.Join(repo, "first.toml"), first)
	writeGradleCatalogTestFile(t, filepath.Join(repo, "second.toml"), second)

	budget := NewGradleDiscoveryBudget(repo)
	resolver, warnings, err := LoadGradleCatalogResolverStrict(context.Background(), repo, openSharedTestRoot(t, repo), budget)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "multiple Gradle version catalog files declared for tools") || !strings.Contains(warnings[0], "using first.toml") {
		t.Fatalf("multiple-file compatibility warning=%v", warnings)
	}
	items, referenceWarnings, err := resolver.ParseDependencyReferencesContext(context.Background(), filepath.Join(repo, "build.gradle"), []byte("implementation tools.widget"))
	if err != nil || len(referenceWarnings) != 0 || !reflect.DeepEqual(items, []GradleCatalogLibrary{{Alias: "widget", Catalog: "tools", Group: "org.example", Artifact: "first", Version: "1"}}) {
		t.Fatalf("resolved items=%+v warnings=%v error=%v", items, referenceWarnings, err)
	}
	if budget.used != int64(len(settings)+len(first)) || len(budget.charged) != 2 {
		t.Fatalf("strict discovery read unselected catalog source: budget=%+v", budget)
	}
}

func TestGradleStrictCatalogAbsenceAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, settings, catalog string
		want                    error
	}{
		{name: "optional absent"},
		{name: "explicit missing", settings: `dependencyResolutionManagement { versionCatalogs { create("libs") { from(files("missing.toml")) } } }`, want: fs.ErrNotExist},
		{name: "invalid catalog", catalog: "[libraries\ninvalid", want: errors.New("parse")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if tc.settings != "" {
				writeGradleCatalogTestFile(t, filepath.Join(repo, "settings.gradle"), tc.settings)
			}
			if tc.catalog != "" {
				writeGradleCatalogTestFile(t, filepath.Join(repo, "gradle/libs.versions.toml"), tc.catalog)
			}
			_, _, err := LoadGradleCatalogResolverStrict(context.Background(), repo, openSharedTestRoot(t, repo), NewGradleDiscoveryBudget(repo))
			assertGradleCatalogFailure(t, err, tc.want)
		})
	}
}

func assertGradleCatalogFailure(t *testing.T, err, want error) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var typed *GradleDiscoveryError
	if !errors.As(err, &typed) {
		t.Fatalf("expected fatal catalog: %v", err)
	}
	if errors.Is(want, fs.ErrNotExist) && !errors.Is(err, want) {
		t.Fatalf("missing cause: %v", err)
	}
}

func TestGradleContextParserCancelsDuringAST(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	visited := 0
	err := parseGradleTree(ctx, "build.gradle", []byte("implementation 'org.example:a:1'\nimplementation 'org.example:b:2'"), gradleLanguageForPath("build.gradle"), func(_ *sitter.Node) { visited++; cancel() })
	if !errors.Is(err, context.Canceled) || visited != 1 {
		t.Fatalf("visited=%d error=%v", visited, err)
	}
	items, err := ParseGradleDependencyCoordinatesForFileContext(ctx, "build.gradle", nil)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(items, []GradleDependencyCoordinate(nil)) {
		t.Fatalf("canceled empty parse: %v %v", items, err)
	}
}

func TestGradleStrictCatalogJoinsCancellationAndReadFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closeErr := errors.New("catalog close failed")
	registry := newGradleCatalogRegistry(t.TempDir())
	registry.strict = NewGradleDiscoveryBudget(registry.repoPath)
	for _, err := range []error{
		registry.loadSettingsFileResult(ctx, "settings.gradle", nil, closeErr),
		registry.loadSourceResult(ctx, gradleCatalogSource{path: "libs.versions.toml"}, nil, closeErr),
	} {
		if !errors.Is(err, context.Canceled) || !errors.Is(err, closeErr) {
			t.Fatalf("lost read/cancel cause: %v", err)
		}
	}
}

func TestGradleDiscoveryZeroAllowanceUsesOneProbe(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "input")
	writeGradleCatalogTestFile(t, path, "")
	root := openSharedTestRoot(t, repo)
	readBytes, closes := 0, 0
	wrapped := &strictGradleOpenFileRoot{Root: root, openFile: func(name string, flag int, perm os.FileMode) (safeio.File, error) {
		file, err := root.OpenFile(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &gradleObservedFile{File: file, reader: strings.NewReader("growth"), readBytes: &readBytes, closes: &closes}, nil
	}}
	budget := NewGradleDiscoveryBudget(repo)
	budget.used = budget.total
	_, err := budget.ReadWithinRoot(context.Background(), wrapped, "input", path)
	if !errors.Is(err, ErrGradleDiscoveryLimit) || readBytes != 1 || closes != 1 {
		t.Fatalf("zero remaining read=%d closes=%d error=%v", readBytes, closes, err)
	}
	missingBudget := NewGradleDiscoveryBudget(repo)
	missingBudget.used = missingBudget.total
	_, err = missingBudget.ReadWithinRoot(context.Background(), root, "missing", filepath.Join(repo, "missing"))
	if !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrGradleDiscoveryLimit) {
		t.Fatalf("missing input at zero allowance error=%v", err)
	}
}

type strictGradleOpenFileRoot struct {
	safeio.Root
	openFile func(string, int, os.FileMode) (safeio.File, error)
}

func (r *strictGradleOpenFileRoot) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	if r.openFile != nil {
		return r.openFile(name, flag, perm)
	}
	return r.Root.OpenFile(name, flag, perm)
}

func TestStrictCatalogRegularEntryReplacementRemainsFatal(t *testing.T) {
	for _, name := range []string{"settings.gradle", "settings.gradle.kts"} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, name)
			writeGradleCatalogTestFile(t, path, "")
			root := openSharedTestRoot(t, repo)
			wrapped := &gradleCatalogReplacementRoot{Root: root, target: name, replace: func() error {
				if err := os.Remove(path); err != nil {
					return err
				}
				return os.Symlink("absent", path)
			}}
			_, _, err := LoadGradleCatalogResolverStrict(t.Context(), repo, wrapped, NewGradleDiscoveryBudget(repo))
			if !wrapped.replaced || !errors.Is(err, safeio.ErrTargetPathSymlink) {
				t.Fatalf("observed regular settings replacement downgraded: replaced=%v err=%v", wrapped.replaced, err)
			}
			var failure *GradleDiscoveryError
			if !errors.As(err, &failure) {
				t.Fatalf("untyped replacement: %v", err)
			}
		})
	}
}

type gradleCatalogReplacementRoot struct {
	safeio.Root
	target   string
	replace  func() error
	replaced bool
}

func (r *gradleCatalogReplacementRoot) Lstat(name string) (fs.FileInfo, error) {
	if name == r.target && !r.replaced {
		r.replaced = true
		if err := r.replace(); err != nil {
			return nil, err
		}
	}
	return r.Root.Lstat(name)
}

func TestStrictCatalogExplicitSymlinkRemainsRequired(t *testing.T) {
	for _, name := range []string{"catalog.toml", "gradle/libs.versions.toml"} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			writeGradleCatalogTestFile(t, filepath.Join(repo, "settings.gradle"), `dependencyResolutionManagement { versionCatalogs { create("libs") { from(files("`+name+`")) } } }`)
			path := filepath.Join(repo, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("absent", path); err != nil {
				t.Fatal(err)
			}
			_, _, err := LoadGradleCatalogResolverStrict(t.Context(), repo, openSharedTestRoot(t, repo), NewGradleDiscoveryBudget(repo))
			if !errors.Is(err, safeio.ErrTargetPathSymlink) {
				t.Fatalf("configured required catalog downgraded: %v", err)
			}
		})
	}
}

func TestGradleDiscoveryRegularObservationOwnership(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "settings.gradle")
	paths := []string{path}
	budget := NewGradleDiscoveryBudget(repo, paths...)
	paths[0] = filepath.Join(repo, "other")
	if _, ok := budget.observedRegular[path]; !ok {
		t.Fatal("caller mutation changed regular-file observation")
	}
	if len(budget.observedRegular) != 1 {
		t.Fatal("unexpected retained observations")
	}
	if len(NewGradleDiscoveryBudget(repo).observedRegular) != 0 {
		t.Fatal("standalone budget gained an observation")
	}
}
