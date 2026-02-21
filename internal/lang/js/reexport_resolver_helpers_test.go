package js

import "testing"

func TestReExportResolverHelperFunctions(t *testing.T) {
	t.Run("selectReExportCandidates prefers exact", func(t *testing.T) {
		bindings := []ReExportBinding{
			{ExportName: "*", SourceModule: "./all"},
			{ExportName: "mapAlias", SourceModule: "./map"},
		}
		candidates := selectReExportCandidates(bindings, "mapAlias")
		if len(candidates) != 1 || candidates[0].SourceModule != "./map" {
			t.Fatalf("expected exact candidate first, got %#v", candidates)
		}
	})

	t.Run("selectReExportCandidates falls back to wildcard", func(t *testing.T) {
		bindings := []ReExportBinding{
			{ExportName: "*", SourceModule: "./all"},
			{ExportName: "named", SourceModule: "./named"},
		}
		candidates := selectReExportCandidates(bindings, "missing")
		if len(candidates) != 1 || candidates[0].ExportName != "*" {
			t.Fatalf("expected wildcard fallback candidate, got %#v", candidates)
		}
	})

	t.Run("normalizeRequestedExport", func(t *testing.T) {
		if got := normalizeRequestedExport("*", "map"); got != "map" {
			t.Fatalf("expected wildcard source to map to requested export, got %q", got)
		}
		if got := normalizeRequestedExport("filter", "map"); got != "filter" {
			t.Fatalf("expected explicit source export to be preserved, got %q", got)
		}
	})

	t.Run("appendTrail clones input", func(t *testing.T) {
		orig := []string{"a"}
		next := appendTrail(orig, "b")
		if len(orig) != 1 || orig[0] != "a" {
			t.Fatalf("expected original trail unchanged, got %#v", orig)
		}
		if len(next) != 2 || next[1] != "b" {
			t.Fatalf("expected appended trail, got %#v", next)
		}
	})
}

func TestReExportResolverCoverageBranches(t *testing.T) {
	resolver := &reExportResolver{
		filesByPath:  map[string]FileScan{},
		resolveCache: map[string]string{},
		warningSet:   map[string]struct{}{},
	}

	req := resolveExportRequest{
		importerPath:    "src/index.ts",
		currentFilePath: "src/a.ts",
		requestedExport: "x",
		visited:         map[string]struct{}{},
		localTrail:      []string{},
	}

	if _, _, ok := resolver.prepareExportResolution(req); ok {
		t.Fatalf("expected missing file path to fail preparation")
	}

	req.visited = map[string]struct{}{"src/a.ts|x": {}}
	if !resolver.hasResolutionCycle(req) {
		t.Fatalf("expected cycle to be detected")
	}
	if len(resolver.warningSet) == 0 {
		t.Fatalf("expected cycle warning to be recorded")
	}

	req.localTrail = []string{"src/index.ts"}
	origin := resolver.dependencyExportOrigin(req, "lodash", "map")
	if origin.dependencyModule != "lodash" || origin.dependencyExport != "map" {
		t.Fatalf("unexpected dependency export origin: %#v", origin)
	}
	if len(origin.localTrail) != 2 || origin.localTrail[1] != "src/a.ts" {
		t.Fatalf("expected local trail append, got %#v", origin.localTrail)
	}
}

func TestReExportResolverResolveLocalAndAttributionBranches(t *testing.T) {
	resolver := &reExportResolver{
		filesByPath: map[string]FileScan{
			"src/barrel.ts":      {Path: "src/barrel.ts"},
			"src/utils/index.ts": {Path: "src/utils/index.ts"},
		},
		resolveCache: map[string]string{},
		warningSet:   map[string]struct{}{},
	}

	if path, ok := resolver.resolveLocalModule("src/main.ts", "./barrel"); !ok || path != "src/barrel.ts" {
		t.Fatalf("expected direct extension resolution, got path=%q ok=%v", path, ok)
	}
	if path, ok := resolver.resolveLocalModule("src/main.ts", "./utils"); !ok || path != "src/utils/index.ts" {
		t.Fatalf("expected index file resolution, got path=%q ok=%v", path, ok)
	}
	if path, ok := resolver.resolveLocalModule("src/main.ts", "./missing"); ok || path != "" {
		t.Fatalf("expected unresolved local module, got path=%q ok=%v", path, ok)
	}
	// hit negative cache path
	if path, ok := resolver.resolveLocalModule("src/main.ts", "./missing"); ok || path != "" {
		t.Fatalf("expected unresolved local module from cache, got path=%q ok=%v", path, ok)
	}

	if _, ok := resolver.resolveImportAttribution("src/main.ts", ImportBinding{
		Module:     "lodash",
		ExportName: "map",
		LocalName:  "map",
		Kind:       ImportNamed,
	}, "lodash"); ok {
		t.Fatalf("expected non-local import to skip resolver")
	}
	if _, ok := resolver.resolveImportAttribution("src/main.ts", ImportBinding{
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
			"src/a.ts": {
				Path: "src/a.ts",
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
		importerPath:    "src/index.ts",
		currentFilePath: "src/a.ts",
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
		SourceModule:     "./missing",
		SourceExportName: "x",
		ExportName:       "x",
	}, map[string]struct{}{}); ok {
		t.Fatalf("expected unresolved local source to be skipped")
	}
}
