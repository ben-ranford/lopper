package jvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestJVMDetectWithConfidenceEmptyRepoPathAndErrors(t *testing.T) {
	adapter := NewAdapter()

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Main.java"), "class Main {}")
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	detection, err := adapter.DetectWithConfidence(context.Background(), "")
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched || detection.Confidence != 35 {
		t.Fatalf("expected matched detection with floor confidence 35, got %#v", detection)
	}
	if len(detection.Roots) == 0 {
		t.Fatalf("expected detection roots")
	}

	repoFile := filepath.Join(t.TempDir(), "repo-file")
	writeFile(t, repoFile, "x")
	if _, err := adapter.DetectWithConfidence(context.Background(), repoFile); err == nil {
		t.Fatalf("expected detect error for non-directory repo path")
	}
	if _, err := adapter.Detect(context.Background(), repoFile); err == nil {
		t.Fatalf("expected detect error to propagate")
	}
}

func TestJVMRootSignalAndScanErrorBranches(t *testing.T) {
	detection := &language.Detection{}
	roots := map[string]struct{}{}

	repoFile := filepath.Join(t.TempDir(), "repo-file")
	writeFile(t, repoFile, "x")
	if err := applyJVMRootSignals(repoFile, detection, roots); err == nil {
		t.Fatalf("expected root signal stat error for non-directory repo path")
	}

	repo := t.TempDir()
	ctx := canceledContext()
	if _, err := scanRepo(ctx, repo, map[string]string{}, map[string]string{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
}

func TestJVMSourceAndBuildFileBranches(t *testing.T) {
	repo := t.TempDir()
	result := &scanResult{}
	if err := scanJVMSourceFile(repo, filepath.Join(repo, "README.md"), nil, nil, result); err != nil {
		t.Fatalf("scan non-source file should be no-op: %v", err)
	}
	if err := scanJVMSourceFile(repo, filepath.Join(repo, "Missing.java"), nil, nil, result); err == nil {
		t.Fatalf("expected read error for missing source file")
	}

	descriptors := parseBuildFiles(filepath.Join(repo, "missing-root"), pomXMLName, func(string) []dependencyDescriptor {
		return []dependencyDescriptor{{Name: "x"}}
	})
	if len(descriptors) != 0 {
		t.Fatalf("expected no descriptors when walking missing root, got %#v", descriptors)
	}

	entryPath := filepath.Join(repo, "pom.xml")
	writeFile(t, entryPath, `<dependency><groupId>org.junit</groupId><artifactId>junit</artifactId></dependency>`)
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected file entries")
	}
	seen := map[string]struct{}{}
	collected := []dependencyDescriptor{}
	for _, entry := range entries {
		err := parseBuildFileEntry(
			filepath.Join(repo, entry.Name()),
			entry,
			[]string{pomXMLName},
			func(string) []dependencyDescriptor {
				return []dependencyDescriptor{
					{Name: "junit", Group: "org.junit", Artifact: "junit"},
					{Name: "junit", Group: "org.junit", Artifact: "junit"},
				}
			},
			seen,
			&collected,
		)
		if err != nil {
			t.Fatalf("parseBuildFileEntry: %v", err)
		}
	}
	if len(collected) != 1 {
		t.Fatalf("expected descriptor dedupe in parseBuildFileEntry, got %#v", collected)
	}

	// Parse entry using a file entry but incorrect path to exercise read failure branch.
	err = parseBuildFileEntry(
		filepath.Join(repo, "missing-pom.xml"),
		entries[0],
		[]string{pomXMLName},
		func(string) []dependencyDescriptor { return nil },
		map[string]struct{}{},
		&collected,
	)
	if err != nil {
		t.Fatalf("expected nil error when parseBuildFileEntry read fails, got %v", err)
	}

	// Directory skip branch.
	gradleDir := filepath.Join(repo, ".gradle")
	if err := os.MkdirAll(gradleDir, 0o755); err != nil {
		t.Fatalf("mkdir .gradle: %v", err)
	}
	dirEntries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo: %v", err)
	}
	for _, entry := range dirEntries {
		if entry.IsDir() && entry.Name() == ".gradle" {
			err := parseBuildFileEntry(
				filepath.Join(repo, ".gradle"),
				entry,
				[]string{pomXMLName},
				func(string) []dependencyDescriptor { return nil },
				map[string]struct{}{},
				&collected,
			)
			if !errors.Is(err, filepath.SkipDir) {
				t.Fatalf("expected filepath.SkipDir for .gradle dir, got %v", err)
			}
		}
	}
}

func TestJVMRecommendationAndLookupBranches(t *testing.T) {
	dep, warnings := buildDependencyReport("missing", scanResult{})
	if dep.Name != "missing" || len(warnings) == 0 {
		t.Fatalf("expected warning for missing dependency imports, dep=%#v warnings=%#v", dep, warnings)
	}

	recs := buildRecommendations(report.DependencyReport{
		UsedImports:   nil,
		UnusedImports: []report.ImportUse{{Name: "*", Module: "x"}},
	})
	if len(recs) < 2 {
		t.Fatalf("expected removal and wildcard recommendations, got %#v", recs)
	}

	prefixes := map[string]string{}
	aliases := map[string]string{}
	addGroupLookups(prefixes, aliases, "name", "")
	addArtifactLookups(prefixes, aliases, "name", "org.example", "")
	if len(prefixes) != 0 || len(aliases) != 0 {
		t.Fatalf("expected no lookups for empty group/artifact, got prefixes=%#v aliases=%#v", prefixes, aliases)
	}

	if got := fallbackDependency(""); got != "" {
		t.Fatalf("expected empty fallback for empty module, got %q", got)
	}
	if got := lastModuleSegment("a.b."); strings.TrimSpace(got) != "" {
		t.Fatalf("expected empty last module segment for trailing dot module, got %q", got)
	}

	// Wildcard imports should emit risk cue path in buildDependencyReport.
	scan := scanResult{
		Files: []fileScan{
			{
				Path: "A.java",
				Imports: []importBinding{
					{
						Dependency: "dep",
						Module:     "x.dep",
						Name:       "*",
						Local:      "*",
						Wildcard:   true,
					},
				},
				Usage: map[string]int{"*": 1},
			},
		},
	}
	depReport, _ := buildDependencyReport("dep", scan)
	if len(depReport.RiskCues) == 0 {
		t.Fatalf("expected wildcard risk cue in dependency report")
	}
}

func TestJVMScanCallbackAndParseBranches(t *testing.T) {
	repo := t.TempDir()
	ctx := canceledContext()
	if _, err := scanRepo(ctx, repo, map[string]string{}, map[string]string{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}

	// Trigger parseImports branches: fallback dependency, wildcard symbol, empty symbol.
	content := []byte("import com.example.lib.;\nimport com.foo.bar.*;\nimport custom.module.Type;\n")
	imports := parseImports(content, "A.java", "", map[string]string{}, map[string]string{})
	if len(imports) == 0 {
		t.Fatalf("expected parsed imports from mixed content")
	}

	// scanJVMSourceFile rel-path fallback branch using empty repoPath.
	javaPath := filepath.Join(repo, "Main.java")
	writeFile(t, javaPath, "import custom.dep.Type;\n")
	result := &scanResult{}
	if err := scanJVMSourceFile("", javaPath, nil, nil, result); err != nil {
		t.Fatalf("scanJVMSourceFile with empty repoPath: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected scanned file")
	}

	// walkJVMDetectionEntry skip-dir branch.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatalf("mkdir build dir: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir root: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() == "build" {
			visited := 0
			detection := &language.Detection{}
			err := walkJVMDetectionEntry(filepath.Join(root, "build"), entry, map[string]struct{}{}, detection, &visited, 10)
			if !errors.Is(err, filepath.SkipDir) {
				t.Fatalf("expected filepath.SkipDir for build dir, got %v", err)
			}
		}
	}

	// DetectWithConfidence should ignore fs.SkipAll as non-error.
	manyFilesRepo := t.TempDir()
	writeNumberedTextFiles(t, manyFilesRepo, 1050)
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), manyFilesRepo)
	if err != nil {
		t.Fatalf("detect with many files: %v", err)
	}
	if detection.Matched {
		t.Fatalf("did not expect matched detection from non-source files")
	}

	// WalkDir error propagation branch.
	_, err = scanRepo(context.Background(), filepath.Join(t.TempDir(), "missing"), map[string]string{}, map[string]string{})
	if err == nil {
		t.Fatalf("expected scanRepo error for missing path")
	}
}

func TestJVMAnalyseWarningAndErrorBranches(t *testing.T) {
	repo := t.TempDir()
	javaPath := filepath.Join(repo, "Main.java")
	writeFile(t, javaPath, "import custom.dep.Type;\n")
	rep, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 1})
	if err != nil {
		t.Fatalf("analyse repo without manifests: %v", err)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), "no JVM dependencies discovered") {
		t.Fatalf("expected missing-manifest warning, got %#v", rep.Warnings)
	}

	ctx := canceledContext()
	if _, err := NewAdapter().Analyse(ctx, language.Request{RepoPath: repo, TopN: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error from analyse scan, got %v", err)
	}
}

func TestJVMMiscHelperBranches(t *testing.T) {
	if !shouldIgnoreImport("", "") {
		t.Fatalf("expected blank import to be ignored")
	}

	descriptors := dedupeAndSortDescriptors([]dependencyDescriptor{
		{Name: "same", Group: "b.group", Artifact: "same"},
		{Name: "same", Group: "a.group", Artifact: "same"},
		{Name: "solo", Group: "", Artifact: ""},
	})
	if len(descriptors) < 2 {
		t.Fatalf("expected deduped descriptors, got %#v", descriptors)
	}
	if descriptors[0].Name != "same" || descriptors[0].Group != "a.group" {
		t.Fatalf("expected tie-break sort by group for equal names, got %#v", descriptors)
	}
}
