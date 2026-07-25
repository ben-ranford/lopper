package jvm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	mainJavaFile  = "Main.java"
	gradleDirName = ".gradle"
	readDirErrFmt = "readdir repo: %v"
)

func TestJVMDetectWithConfidenceEmptyRepoPathAndErrors(t *testing.T) {
	adapter := NewAdapter()

	t.Run("detect from empty repo path", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, mainJavaFile), "class Main {}")
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
	if applyJVMRootSignals(repoFile, detection, roots) == nil {
		t.Fatalf("expected root signal stat error for non-directory repo path")
	}

	repo := t.TempDir()
	for _, dirName := range []string{pomXMLName, buildGradleName, buildGradleKTSName} {
		if err := os.Mkdir(filepath.Join(repo, dirName), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dirName, err)
		}
	}

	detection = &language.Detection{}
	roots = map[string]struct{}{}
	if err := applyJVMRootSignals(repo, detection, roots); err != nil {
		t.Fatalf("apply root signals with manifest-named directories: %v", err)
	}
	if detection.Matched || detection.Confidence != 0 || len(roots) != 0 {
		t.Fatalf("expected no root signals from manifest-named directories, got detection=%#v roots=%#v", detection, roots)
	}
}

func TestJVMSourceAndBuildFileBranches(t *testing.T) {
	repo := t.TempDir()
	t.Run("source file branches", func(t *testing.T) { assertJVMSourceFileBranches(t, repo) })
	t.Run("missing build root", func(t *testing.T) { assertMissingBuildRootBranch(t, repo) })
	t.Run("build file entry branches", func(t *testing.T) { assertBuildFileEntryBranches(t, repo) })
	t.Run("gradle dir skip branch", func(t *testing.T) { assertGradleDirSkipBranch(t, repo) })
}

func assertJVMSourceFileBranches(t *testing.T, repo string) {
	t.Helper()
	result := &scanResult{}
	if err := scanJVMSourceFile(repo, filepath.Join(repo, "README.md"), nil, nil, result); err != nil {
		t.Fatalf("scan non-source file should be no-op: %v", err)
	}
	if scanJVMSourceFile(repo, filepath.Join(repo, "Missing.java"), nil, nil, result) == nil {
		t.Fatalf("expected read error for missing source file")
	}
}

func assertMissingBuildRootBranch(t *testing.T, repo string) {
	t.Helper()
	descriptors := parseBuildFiles(filepath.Join(repo, "missing-root"), pomXMLName, func(string) []dependencyDescriptor {
		return []dependencyDescriptor{{Name: "x"}}
	})
	if len(descriptors) != 0 {
		t.Fatalf("expected no descriptors when walking missing root, got %#v", descriptors)
	}
}

func assertBuildFileEntryBranches(t *testing.T, repo string) {
	t.Helper()
	entryPath := filepath.Join(repo, pomXMLName)
	testutil.MustWriteFile(t, entryPath, `<dependency><groupId>org.junit</groupId><artifactId>junit</artifactId></dependency>`)
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf(readDirErrFmt, err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected file entries")
	}
	seen := map[string]struct{}{}
	var collected []dependencyDescriptor
	descriptorParser := func(string) []dependencyDescriptor {
		return []dependencyDescriptor{{Name: "junit", Group: "org.junit", Artifact: "junit"}, {Name: "junit", Group: "org.junit", Artifact: "junit"}}
	}
	for _, entry := range entries {
		err := parseBuildFileEntry(repo, filepath.Join(repo, entry.Name()), entry, []string{pomXMLName}, descriptorParser, seen, &collected)
		if err != nil {
			t.Fatalf("parseBuildFileEntry: %v", err)
		}
	}
	if len(collected) != 1 {
		t.Fatalf("expected descriptor dedupe in parseBuildFileEntry, got %#v", collected)
	}
	err = parseBuildFileEntry(repo, filepath.Join(repo, "missing-pom.xml"), entries[0], []string{pomXMLName}, func(string) []dependencyDescriptor { return nil }, map[string]struct{}{}, &collected)
	if err != nil {
		t.Fatalf("expected nil error when parseBuildFileEntry read fails, got %v", err)
	}
}

func assertGradleDirSkipBranch(t *testing.T, repo string) {
	t.Helper()
	gradleDir := filepath.Join(repo, gradleDirName)
	if err := os.MkdirAll(gradleDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", gradleDirName, err)
	}
	dirEntries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf(readDirErrFmt, err)
	}
	for _, entry := range dirEntries {
		if !entry.IsDir() || entry.Name() != gradleDirName {
			continue
		}
		var collected []dependencyDescriptor
		err := parseBuildFileEntry(repo, filepath.Join(repo, gradleDirName), entry, []string{pomXMLName}, func(string) []dependencyDescriptor { return nil }, map[string]struct{}{}, &collected)
		if !errors.Is(err, filepath.SkipDir) {
			t.Fatalf("expected filepath.SkipDir for %s dir, got %v", gradleDirName, err)
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
	javaPath := filepath.Join(repo, mainJavaFile)
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

func TestJVMSourceScanSkipsExpectedSymlinkReadErrors(t *testing.T) {
	tests := []struct {
		name        string
		readErr     error
		wantWarning string
	}{
		{
			name:        "missing target",
			readErr:     &fs.PathError{Op: "openat", Path: "Missing.java", Err: fs.ErrNotExist},
			wantWarning: "target missing",
		},
		{
			name:        "unreadable target",
			readErr:     &fs.PathError{Op: "openat", Path: "Unreadable.java", Err: fs.ErrPermission},
			wantWarning: "target unreadable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := t.TempDir()
			sourceLink := createJVMSourceSymlink(t, repo, strings.ReplaceAll(test.name, " ", "-")+".java")
			readSource := func(rootDir, targetPath string, maxBytes int64) ([]byte, error) {
				if rootDir != repo || targetPath != sourceLink || maxBytes != maxScannableJVMSourceFile {
					t.Fatalf("unexpected source read: root=%q path=%q limit=%d", rootDir, targetPath, maxBytes)
				}
				return nil, test.readErr
			}
			result, err := scanRepoWithSourceReader(context.Background(), repo, nil, nil, readSource)
			if err != nil {
				t.Fatalf("expected symlink read error to be skipped: %v", err)
			}
			if result.SkippedSymlinks != 1 {
				t.Fatalf("expected one skipped symlink, got %#v", result)
			}
			warnings := strings.Join(result.Warnings, "\n")
			if !strings.Contains(warnings, "skipped JVM source symlink") ||
				!strings.Contains(warnings, test.wantWarning) ||
				!strings.Contains(warnings, "skipped 1 unreadable or untrusted JVM source symlink(s)") {
				t.Fatalf("expected symlink warnings for %s, got %#v", test.name, result.Warnings)
			}
		})
	}
}

func TestJVMSourceScanPropagatesUnexpectedSymlinkReadFailure(t *testing.T) {
	repo := t.TempDir()
	sourceLink := createJVMSourceSymlink(t, repo, "Unexpected.java")
	unexpectedErr := errors.New("injected unexpected JVM source read failure")

	readSource := func(rootDir, targetPath string, maxBytes int64) ([]byte, error) {
		if rootDir != repo || targetPath != sourceLink || maxBytes != maxScannableJVMSourceFile {
			t.Fatalf("unexpected source read: root=%q path=%q limit=%d", rootDir, targetPath, maxBytes)
		}
		return nil, &fs.PathError{Op: "read", Path: targetPath, Err: unexpectedErr}
	}
	result, err := scanRepoWithSourceReader(context.Background(), repo, nil, nil, readSource)
	if !errors.Is(err, unexpectedErr) {
		t.Fatalf("expected unexpected symlink read failure to propagate, got %v", err)
	}
	if result.SkippedSymlinks != 0 || len(result.Warnings) != 0 {
		t.Fatalf("expected no downgraded warning for unexpected read failure, got %#v", result)
	}
}

func TestJVMClassifySkippableSourceReadErrorRequiresSymlink(t *testing.T) {
	repo := t.TempDir()
	regularSource := filepath.Join(repo, "Regular.java")
	testutil.MustWriteFile(t, regularSource, "class Regular {}\n")

	warning, skip := classifySkippableJVMSourceReadError(repo, regularSource, &fs.PathError{
		Op:   "openat",
		Path: regularSource,
		Err:  fs.ErrPermission,
	})
	if skip || warning != "" {
		t.Fatalf("expected regular files not to be downgraded as symlink skips, warning=%q skip=%v", warning, skip)
	}
}

func TestJVMClassifySkippableSourceReadErrorClassifiesRepoEscapeSentinel(t *testing.T) {
	repo := t.TempDir()
	sourceLink := createJVMSourceSymlink(t, repo, "EscapesRoot.java")

	warning, skip := classifySkippableJVMSourceReadError(repo, sourceLink, errors.New("path escapes root: /private/tmp/outside.java"))
	if !skip {
		t.Fatalf("expected repo-escape sentinel error to be downgraded for source symlink")
	}
	if !strings.Contains(warning, "skipped JVM source symlink EscapesRoot.java: target escapes repo root") {
		t.Fatalf("expected repo-escape warning, got %q", warning)
	}
}

func TestJVMClassifySkippableSourceReadErrorClassifiesParentEscapePathError(t *testing.T) {
	repo := t.TempDir()
	sourceLink := createJVMSourceSymlink(t, repo, "EscapesParent.java")

	warning, skip := classifySkippableJVMSourceReadError(repo, sourceLink, &fs.PathError{
		Op:   "openat",
		Path: sourceLink,
		Err:  errors.New("path escapes from parent"),
	})
	if !skip {
		t.Fatalf("expected parent-escape path error to be downgraded for source symlink")
	}
	if !strings.Contains(warning, "skipped JVM source symlink EscapesParent.java: target escapes repo root") {
		t.Fatalf("expected parent-escape warning, got %q", warning)
	}
}

func TestJVMClassifySkippableSourceReadErrorIgnoresCanceledContext(t *testing.T) {
	repo := t.TempDir()
	sourceLink := createJVMSourceSymlink(t, repo, "Canceled.java")

	warning, skip := classifySkippableJVMSourceReadError(repo, sourceLink, context.Canceled)
	if skip || warning != "" {
		t.Fatalf("expected canceled reads not to be downgraded, warning=%q skip=%v", warning, skip)
	}
}

func TestJVMRelativeSourceScanPathReturnsOriginalWhenRelFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Outside.java")

	relativePath := relativeSourceScanPath("relative-repo-root", path)
	if relativePath != path {
		t.Fatalf("expected rel fallback to preserve original path, got %q want %q", relativePath, path)
	}
}

func createJVMSourceSymlink(t *testing.T, repo, name string) string {
	t.Helper()
	target := filepath.Join(repo, "source-target.txt")
	testutil.MustWriteFile(t, target, "not scanned\n")
	sourceLink := filepath.Join(repo, name)
	if err := os.Symlink(filepath.Base(target), sourceLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	return sourceLink
}

func TestJVMAnalyseWarningAndErrorBranches(t *testing.T) {
	repo := t.TempDir()
	javaPath := filepath.Join(repo, mainJavaFile)
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
