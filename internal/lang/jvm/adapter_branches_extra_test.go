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
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestJVMDetectWithConfidenceEmptyRepoPathAndErrors(t *testing.T) {
	adapter := NewAdapter()

	t.Run("detect from empty repo path", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "class Main {}")
		testutil.Chdir(t, repo)

		detection, err := adapter.DetectWithConfidence(context.Background(), "")
		if err != nil {
			t.Fatalf("detect with confidence: %v", err)
		}
		if !detection.Matched || detection.Confidence != 35 || len(detection.Roots) == 0 {
			t.Fatalf("unexpected detection result for empty repo path: %#v", detection)
		}
	})

	t.Run("non-directory repo path errors", func(t *testing.T) {
		repoFile := filepath.Join(t.TempDir(), "repo-file")
		testutil.MustWriteFile(t, repoFile, "x")
		if _, err := adapter.DetectWithConfidence(context.Background(), repoFile); err == nil {
			t.Fatalf("expected detect-with-confidence error for non-directory repo path")
		}
		if matched, err := adapter.Detect(context.Background(), repoFile); err == nil || matched {
			t.Fatalf("expected detect error to propagate, matched=%v err=%v", matched, err)
		}
	})
}

func TestJVMRootSignalAndScanErrorBranches(t *testing.T) {
	detection := &language.Detection{}
	roots := map[string]struct{}{}

	repoFile := filepath.Join(t.TempDir(), "repo-file")
	testutil.MustWriteFile(t, repoFile, "x")
	if err := applyJVMRootSignals(repoFile, detection, roots); err == nil {
		t.Fatalf("expected root signal stat error for non-directory repo path")
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
	testutil.MustWriteFile(t, entryPath, `<dependency><groupId>org.junit</groupId><artifactId>junit</artifactId></dependency>`)
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

	// Trigger parseImports branches: fallback dependency, wildcard symbol, empty symbol.
	content := []byte("import com.example.lib.;\nimport com.foo.bar.*;\nimport custom.module.Type;\n")
	imports := parseImports(content, "A.java", "", map[string]string{}, map[string]string{})
	if len(imports) == 0 {
		t.Fatalf("expected parsed imports from mixed content")
	}

	// scanJVMSourceFile rel-path fallback branch using empty repoPath.
	javaPath := filepath.Join(repo, "Main.java")
	testutil.MustWriteFile(t, javaPath, "import custom.dep.Type;\n")
	result := &scanResult{}
	if err := scanJVMSourceFile("", javaPath, nil, nil, result); err != nil {
		t.Fatalf("scanJVMSourceFile with empty repoPath: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected scanned file")
	}

	// WalkDir error propagation branch.
	_, err := scanRepo(context.Background(), filepath.Join(t.TempDir(), "missing"), map[string]string{}, map[string]string{})
	if err == nil {
		t.Fatalf("expected scanRepo error for missing path")
	}
}

func TestJVMAnalyseWarningAndErrorBranches(t *testing.T) {
	repo := t.TempDir()
	javaPath := filepath.Join(repo, "Main.java")
	testutil.MustWriteFile(t, javaPath, "import custom.dep.Type;\n")
	rep, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 1})
	if err != nil {
		t.Fatalf("analyse repo without manifests: %v", err)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), "no JVM dependencies discovered") {
		t.Fatalf("expected missing-manifest warning, got %#v", rep.Warnings)
	}

	ctx := testutil.CanceledContext()
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
