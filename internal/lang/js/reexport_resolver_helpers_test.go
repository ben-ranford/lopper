package js

import "testing"

const (
	testImporterPath = "src/index.ts"
	testFileAPath    = "src/a.ts"
	testBarrelPath   = "src/barrel.ts"
	testUtilsPath    = "src/utils/index.ts"
	testMainPath     = "src/main.ts"
	testMissingPath  = "./missing"
)

func TestSelectReExportCandidatesPrefersExact(t *testing.T) {
	bindings := []ReExportBinding{
		{ExportName: "*", SourceModule: "./all"},
		{ExportName: "mapAlias", SourceModule: "./map"},
	}
	candidates := selectReExportCandidates(bindings, "mapAlias")
	if len(candidates) != 1 || candidates[0].SourceModule != "./map" {
		t.Fatalf("expected exact candidate first, got %#v", candidates)
	}
}

func TestSelectReExportCandidatesFallsBackToWildcard(t *testing.T) {
	bindings := []ReExportBinding{
		{ExportName: "*", SourceModule: "./all"},
		{ExportName: "named", SourceModule: "./named"},
	}
	candidates := selectReExportCandidates(bindings, "missing")
	if len(candidates) != 1 || candidates[0].ExportName != "*" {
		t.Fatalf("expected wildcard fallback candidate, got %#v", candidates)
	}
}

func TestNormalizeRequestedExport(t *testing.T) {
	if got := normalizeRequestedExport("*", "map"); got != "map" {
		t.Fatalf("expected wildcard source to map to requested export, got %q", got)
	}
	if got := normalizeRequestedExport("filter", "map"); got != "filter" {
		t.Fatalf("expected explicit source export to be preserved, got %q", got)
	}
}

func TestAppendTrailClonesInput(t *testing.T) {
	orig := []string{"a"}
	next := appendTrail(orig, "b")
	if len(orig) != 1 || orig[0] != "a" {
		t.Fatalf("expected original trail unchanged, got %#v", orig)
	}
	if len(next) != 2 || next[1] != "b" {
		t.Fatalf("expected appended trail, got %#v", next)
	}
}

func TestReExportResolverCoverageBranches(t *testing.T) {
	resolver := &reExportResolver{
		filesByPath:  map[string]FileScan{},
		resolveCache: map[string]string{},
		warningSet:   map[string]struct{}{},
	}

	req := resolveExportRequest{
		importerPath:    testImporterPath,
		currentFilePath: testFileAPath,
		requestedExport: "x",
		visited:         map[string]struct{}{},
		localTrail:      []string{},
	}

	if _, _, ok := resolver.prepareExportResolution(req); ok {
		t.Fatalf("expected missing file path to fail preparation")
	}

	req.visited = map[string]struct{}{testFileAPath + "|x": {}}
	if !resolver.hasResolutionCycle(req) {
		t.Fatalf("expected cycle to be detected")
	}
	if len(resolver.warningSet) == 0 {
		t.Fatalf("expected cycle warning to be recorded")
	}

	req.localTrail = []string{testImporterPath}
	origin := resolver.dependencyExportOrigin(req, "lodash", "map")
	if origin.dependencyModule != "lodash" || origin.dependencyExport != "map" {
		t.Fatalf("unexpected dependency export origin: %#v", origin)
	}
	if len(origin.localTrail) != 2 || origin.localTrail[1] != testFileAPath {
		t.Fatalf("expected local trail append, got %#v", origin.localTrail)
	}
}

func TestReExportResolverResolveLocalModule(t *testing.T) {
	resolver := &reExportResolver{
		filesByPath: map[string]FileScan{
			testBarrelPath: {Path: testBarrelPath},
			testUtilsPath:  {Path: testUtilsPath},
		},
		resolveCache: map[string]string{},
		warningSet:   map[string]struct{}{},
	}

	assertResolvedModule(t, resolver, testMainPath, "./barrel", testBarrelPath)
	assertResolvedModule(t, resolver, testMainPath, "./utils", testUtilsPath)
	assertUnresolvedModule(t, resolver, testMainPath, testMissingPath)
	// Hit negative cache path.
	assertUnresolvedModule(t, resolver, testMainPath, testMissingPath)
}

func assertResolvedModule(t *testing.T, resolver *reExportResolver, importer, module, expected string) {
	t.Helper()
	path, ok := resolver.resolveLocalModule(importer, module)
	if !ok || path != expected {
		t.Fatalf("expected resolved module %q, got path=%q ok=%v", expected, path, ok)
	}
}

func assertUnresolvedModule(t *testing.T, resolver *reExportResolver, importer, module string) {
	t.Helper()
	path, ok := resolver.resolveLocalModule(importer, module)
	if ok || path != "" {
		t.Fatalf("expected unresolved module, got path=%q ok=%v", path, ok)
	}
}

func TestReExportResolverResolveImportAttributionSkips(t *testing.T) {
	resolver := &reExportResolver{
		filesByPath:  map[string]FileScan{},
		resolveCache: map[string]string{},
		warningSet:   map[string]struct{}{},
	}

	if _, ok := resolver.resolveImportAttribution(testMainPath, ImportBinding{
		Module:     "lodash",
		ExportName: "map",
		LocalName:  "map",
		Kind:       ImportNamed,
	}, "lodash"); ok {
		t.Fatalf("expected non-local import to skip resolver")
	}
	if _, ok := resolver.resolveImportAttribution(testMainPath, ImportBinding{
		Module:     "./barrel",
		ExportName: "*",
		LocalName:  "ns",
		Kind:       ImportNamespace,
	}, "lodash"); ok {
		t.Fatalf("expected namespace local import to skip resolver")
	}
}

func TestReExportResolverResolveExportCandidateBranches(t *testing.T) {
	resolver := &reExportResolver{
		filesByPath: map[string]FileScan{
			testFileAPath: {
				Path: testFileAPath,
				ReExports: []ReExportBinding{
					{SourceModule: "lodash", SourceExportName: "map", ExportName: "mapAlias"},
					{SourceModule: "./b", SourceExportName: "filter", ExportName: "filterAlias"},
				},
			},
			"src/b.ts": {
				Path: "src/b.ts",
				ReExports: []ReExportBinding{
					{SourceModule: "lodash", SourceExportName: "filter", ExportName: "filter"},
				},
			},
		},
		resolveCache: map[string]string{},
		warningSet:   map[string]struct{}{},
	}

	req := resolveExportRequest{
		importerPath:    testImporterPath,
		currentFilePath: testFileAPath,
		requestedExport: "mapAlias",
		dependency:      "lodash",
		visited:         map[string]struct{}{},
		localTrail:      []string{},
	}

	origin, ok := resolver.resolveExportCandidate(req, ReExportBinding{
		SourceModule:     "lodash",
		SourceExportName: "map",
		ExportName:       "mapAlias",
	}, map[string]struct{}{})
	if !ok || origin.dependencyModule != "lodash" || origin.dependencyExport != "map" {
		t.Fatalf("expected direct dependency origin, got origin=%#v ok=%v", origin, ok)
	}

	origin, ok = resolver.resolveExportCandidate(req, ReExportBinding{
		SourceModule:     "./b",
		SourceExportName: "filter",
		ExportName:       "filterAlias",
	}, map[string]struct{}{})
	if !ok || origin.dependencyModule != "lodash" || origin.dependencyExport != "filter" {
		t.Fatalf("expected local chain resolution to dependency origin, got origin=%#v ok=%v", origin, ok)
	}

	if _, ok = resolver.resolveExportCandidate(req, ReExportBinding{
		SourceModule:     "react",
		SourceExportName: "default",
		ExportName:       "reactAlias",
	}, map[string]struct{}{}); ok {
		t.Fatalf("expected non-local non-target dependency source to be skipped")
	}

	if _, ok = resolver.resolveExportCandidate(req, ReExportBinding{
		SourceModule:     testMissingPath,
		SourceExportName: "x",
		ExportName:       "x",
	}, map[string]struct{}{}); ok {
		t.Fatalf("expected unresolved local source to be skipped")
	}
}
