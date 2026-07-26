package shared

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestLoadGradleCatalogResolverWithinRootTreatsBlankRepoPathAsNoop(t *testing.T) {
	resolver, warnings, err := LoadGradleCatalogResolverWithinRoot(context.Background(), " \t ", nil)
	if err != nil {
		t.Fatalf("load blank rooted Gradle catalog resolver: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for blank repo path, got %#v", warnings)
	}
	if resolver.knownCatalogs == nil || len(resolver.knownCatalogs) != 0 {
		t.Fatalf("expected initialized empty catalog set, got %#v", resolver.knownCatalogs)
	}
}

func TestLoadGradleCatalogResolverWithinRootReturnsPartialResultAtTraversalLimit(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat rooted Gradle repository: %v", err)
	}
	directory := &sharedWalkTestDirectory{
		sharedWalkTestFile: sharedWalkTestFile{
			stat: func() (fs.FileInfo, error) {
				return info, nil
			},
		},
		fillEntry:     &sharedWalkTestDirEntry{name: "ignored.txt"},
		overflowEntry: &sharedWalkTestDirEntry{name: "overflow.txt"},
		repeatEntries: defaultGradleCatalogMaxTraversalEntries - 1,
	}
	root := &sharedWalkTestRoot{
		info: info,
		open: func(name string) (safeio.File, error) {
			if name != "." {
				t.Fatalf("unexpected rooted Gradle directory open %q", name)
			}
			return directory, nil
		},
	}

	resolver, warnings, err := LoadGradleCatalogResolverWithinRoot(context.Background(), repo, root)
	if err != nil {
		t.Fatalf("expected pure traversal limit to return a partial resolver, got %v", err)
	}
	const wantWarning = "Gradle version catalog scan reached the rooted walk limit of 4096 traversal entries; results may be partial"
	if len(warnings) != 1 || warnings[0] != wantWarning {
		t.Fatalf("expected exact partial-result warning %q, got %#v", wantWarning, warnings)
	}
	if resolver.knownCatalogs == nil || len(resolver.knownCatalogs) != 0 {
		t.Fatalf("expected partial resolver with no discovered catalogs, got %#v", resolver.knownCatalogs)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected limited rooted directory to close once, got %d", directory.closeCalls)
	}
	if !directory.overflowServed {
		t.Fatal("expected rooted walk to probe past the configured traversal limit")
	}
}

func TestGradleCatalogRegistryParseSourcesWithinRootSortsSourceKeysBeforeFatalFailure(t *testing.T) {
	repo := t.TempDir()
	expected := errors.New("fatal alpha source failure")
	sourceByKey := map[string]gradleCatalogSource{
		buildGradleCatalogScopeKey(filepath.Join(repo, "zeta"), "libs"):  {root: filepath.Join(repo, "zeta"), name: "libs", path: filepath.Join(repo, "zeta.versions.toml"), rootedReadErr: errors.New("fatal zeta source failure"), rootedLoaded: true},
		buildGradleCatalogScopeKey(filepath.Join(repo, "alpha"), "libs"): {root: filepath.Join(repo, "alpha"), name: "libs", path: filepath.Join(repo, "alpha.versions.toml"), rootedReadErr: expected, rootedLoaded: true},
		buildGradleCatalogScopeKey(filepath.Join(repo, "beta"), "libs"):  {root: filepath.Join(repo, "beta"), name: "libs", path: filepath.Join(repo, "beta.versions.toml"), rootedReadErr: errors.New("fatal beta source failure"), rootedLoaded: true},
	}
	sortedKeys := make([]string, 0, len(sourceByKey))
	for key := range sourceByKey {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)
	if got := sourceByKey[sortedKeys[0]].path; filepath.Base(got) != "alpha.versions.toml" {
		t.Fatalf("expected alpha source to sort first, got %q", got)
	}

	for iteration := 0; iteration < 16; iteration++ {
		registry := newGradleCatalogRegistry(repo)
		keys := append([]string(nil), sortedKeys...)
		if iteration%2 == 1 {
			for left, right := 0, len(keys)-1; left < right; left, right = left+1, right-1 {
				keys[left], keys[right] = keys[right], keys[left]
			}
		}
		offset := iteration % len(keys)
		keys = append(keys[offset:], keys[:offset]...)
		for _, key := range keys {
			registry.sources[key] = sourceByKey[key]
		}

		err := registry.parseSourcesWithinRoot()
		if !errors.Is(err, expected) {
			t.Fatalf("iteration %d: expected deterministic alpha failure, got %v", iteration, err)
		}
	}
}

func TestGradleCatalogRegistryLoadCapturedSourceWithinRootClassifiesUncapturedPaths(t *testing.T) {
	repo := t.TempDir()
	insidePath := filepath.Join(repo, "gradle", "missing.versions.toml")
	registry := newGradleCatalogRegistry(repo)
	err := registry.loadCapturedSourceWithinRoot(gradleCatalogSource{
		root: repo,
		name: "libs",
		path: insidePath,
	})
	if err != nil {
		t.Fatalf("classify uncaptured in-root catalog: %v", err)
	}
	if len(registry.warnings) != 1 || !strings.Contains(registry.warnings[0], "gradle/missing.versions.toml") || !strings.Contains(registry.warnings[0], fs.ErrNotExist.Error()) {
		t.Fatalf("expected missing in-root catalog warning, got %#v", registry.warnings)
	}

	outsidePath := filepath.Join(filepath.Dir(repo), "outside.versions.toml")
	registry = newGradleCatalogRegistry(repo)
	err = registry.loadCapturedSourceWithinRoot(gradleCatalogSource{
		root: repo,
		name: "libs",
		path: outsidePath,
	})
	if err != nil {
		t.Fatalf("classify uncaptured escaping catalog: %v", err)
	}
	if len(registry.warnings) != 1 || !strings.Contains(registry.warnings[0], safeio.ErrPathEscapesRoot.Error()) {
		t.Fatalf("expected escaping catalog warning, got %#v", registry.warnings)
	}
}

func TestGradleCatalogCollectSourcesWithinRootPropagatesSecondPassOpenError(t *testing.T) {
	repo := t.TempDir()
	catalogPath := filepath.Join(repo, "gradle", defaultGradleCatalogFileName)
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0o755); err != nil {
		t.Fatalf("create catalog directory: %v", err)
	}
	if err := os.WriteFile(catalogPath, []byte("[libraries]\nentry = \"example:artifact:1\"\n"), 0o600); err != nil {
		t.Fatalf("write catalog fixture: %v", err)
	}
	realRoot := openSharedTestRoot(t, repo)
	captureErr := errors.New("open catalog directory during capture")
	gradleOpenCalls := 0
	root := &sharedWalkSwapRoot{
		Root: realRoot,
		openRoot: func(name string) (safeio.Root, error) {
			if name == "gradle" {
				gradleOpenCalls++
				if gradleOpenCalls == 2 {
					return nil, captureErr
				}
			}
			return realRoot.OpenRoot(name)
		},
	}
	registry := newGradleCatalogRegistry(repo)

	err := registry.collectSourcesWithinRoot(context.Background(), root)
	if !errors.Is(err, captureErr) {
		t.Fatalf("expected second-pass catalog traversal failure, got %v", err)
	}
	if len(registry.sources) != 1 || gradleOpenCalls != 2 {
		t.Fatalf("expected discovery before second-pass failure, sources=%#v opens=%d", registry.sources, gradleOpenCalls)
	}
}
