package jvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestCollectDeclaredDependenciesWithinRootHonorsCancellation(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), `dependencies { implementation("org.junit.jupiter:junit-jupiter-api:5.10.0") }`)
	root := openJVMTestRoot(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, _, err := collectDeclaredDependenciesWithinRoot(ctx, repo, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled dependency collection, got %v", err)
	}
}

func TestParseBuildFilesWithWarningsWithinRootHonorsCancellation(t *testing.T) {
	repo := t.TempDir()
	root := openJVMTestRoot(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	descriptors, warnings, err := parseBuildFilesWithWarningsWithinRoot(ctx, repo, root, func(string, string) ([]dependencyDescriptor, []string) { return nil, nil }, pomXMLName, buildGradleName, buildGradleKTSName)
	if len(descriptors) != 0 || len(warnings) != 0 {
		t.Fatalf("expected canceled rooted parse to return nil results, got descriptors=%#v warnings=%#v", descriptors, warnings)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled rooted build parse, got %v", err)
	}
}

func TestCollectDeclaredDependenciesWithinRootPropagatesDirectoryCloseError(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	closeErr := errors.New("close dependency directory")
	root := &jvmRootedTestRoot{
		info: info,
		open: func(string) (safeio.File, error) {
			return &jvmRootedTestDirectory{
				jvmRootedTestFile: jvmRootedTestFile{
					close: func() error { return closeErr },
				},
			}, nil
		},
	}

	_, _, _, _, err = collectDeclaredDependenciesWithinRoot(context.Background(), repo, root)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected dependency directory close error, got %v", err)
	}
}

func TestParseBuildFilesWithWarningsWithinRootPropagatesTraversalLimitCloseError(t *testing.T) {
	repo := t.TempDir()
	closeErr := errors.New("close overflowing build directory")
	root := newJVMRootedTraversalLimitRoot(t, repo, closeErr)

	descriptors, warnings, err := parseBuildFilesWithWarningsWithinRoot(context.Background(), repo, root, func(string, string) ([]dependencyDescriptor, []string) { return nil, nil }, pomXMLName, buildGradleName, buildGradleKTSName)
	if !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "rooted walk traversal limit exceeded") {
		t.Fatalf("expected joined build traversal-limit and close error, got %v", err)
	}
	if len(descriptors) != 0 || len(warnings) != 0 {
		t.Fatalf("expected operational failure not to be downgraded, got descriptors=%#v warnings=%#v", descriptors, warnings)
	}
}

func TestCollectDeclaredDependenciesWithinRootReadsBuildFilesWithinRoot(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), `dependencies { implementation("org.junit.jupiter:junit-jupiter-api:5.10.0") }`)
	root := openJVMTestRoot(t, repo)

	descriptors, prefixes, aliases, warnings, err := collectDeclaredDependenciesWithinRoot(context.Background(), repo, root)
	if err != nil {
		t.Fatalf("collect rooted dependencies: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no rooted dependency warnings, got %#v", warnings)
	}
	if len(descriptors) != 1 || descriptors[0].Name != "junit-jupiter-api" {
		t.Fatalf("expected rooted build descriptor, got %#v", descriptors)
	}
	if prefixes["org.junit.jupiter.junit.jupiter.api"] == "" || aliases["junit.jupiter.api"] == "" {
		t.Fatalf("expected rooted dependency lookups, got prefixes=%#v aliases=%#v", prefixes, aliases)
	}
}

func TestScanRepoWithinRootPropagatesDirectoryCloseError(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	closeErr := errors.New("close source directory")
	root := &jvmRootedTestRoot{
		info: info,
		open: func(string) (safeio.File, error) {
			return &jvmRootedTestDirectory{
				jvmRootedTestFile: jvmRootedTestFile{
					close: func() error { return closeErr },
				},
			}, nil
		},
	}

	_, err = scanRepoWithinRoot(context.Background(), repo, root, map[string]string{}, map[string]string{})
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected source directory close error, got %v", err)
	}
}

func TestScanRepoWithinRootPropagatesTraversalLimitCloseError(t *testing.T) {
	repo := t.TempDir()
	closeErr := errors.New("close overflowing source directory")
	root := newJVMRootedTraversalLimitRoot(t, repo, closeErr)

	result, err := scanRepoWithinRoot(context.Background(), repo, root, map[string]string{}, map[string]string{})
	if !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "rooted walk traversal limit exceeded") {
		t.Fatalf("expected joined source traversal-limit and close error, got %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("expected operational failure not to be downgraded to warnings, got %#v", result.Warnings)
	}
}

func TestScanRepoWithinRootDowngradesPureTraversalLimitToWarning(t *testing.T) {
	repo := t.TempDir()
	root := newJVMRootedTraversalLimitRoot(t, repo, nil)

	result, err := scanRepoWithinRoot(context.Background(), repo, root, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatalf("expected pure rooted source traversal limit to downgrade to warnings, got %v", err)
	}
	joinedWarnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(joinedWarnings, "JVM source scan reached the rooted walk limit") {
		t.Fatalf("expected rooted source traversal warning, got %#v", result.Warnings)
	}
}

func TestScanRepoWithinRootPropagatesFileLimitCloseError(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "Main.java")
	testutil.MustWriteFile(t, sourcePath, "class Main {}\n")
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	closeErr := errors.New("close oversized source")
	closeCalls := 0
	statCalls := 0
	root := newJVMRootedReadTestRoot(t, repo, filepath.Base(sourcePath), &jvmRootedTestFile{
		close: func() error {
			closeCalls++
			return closeErr
		},
		stat: func() (fs.FileInfo, error) {
			statCalls++
			if statCalls == 1 {
				return info, nil
			}
			return &jvmRootedSizedFileInfo{FileInfo: info, size: maxScannableJVMSourceFile + 1}, nil
		},
	})

	result, err := scanRepoWithinRoot(context.Background(), repo, root, map[string]string{}, map[string]string{})
	if !errors.Is(err, safeio.ErrFileTooLarge) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined source file-limit and close error, got %v", err)
	}
	if result.SkippedLargeFiles != 0 || len(result.Warnings) != 0 {
		t.Fatalf("expected impure source read failure not to be downgraded, got %#v", result)
	}
	if closeCalls != 1 {
		t.Fatalf("expected oversized source to close once, got %d", closeCalls)
	}
}

func TestScanRepoWithSourceReaderPropagatesImpureSymlinkError(t *testing.T) {
	repo := t.TempDir()
	sourceLink := createJVMSourceSymlink(t, repo, "Linked.java")
	closeErr := errors.New("close source symlink")
	readSource := func(string, string, int64) ([]byte, error) {
		return nil, errors.Join(safeio.ErrTargetPathSymlink, closeErr)
	}
	result, err := scanRepoWithSourceReader(context.Background(), repo, map[string]string{}, map[string]string{}, readSource)

	if !errors.Is(err, safeio.ErrTargetPathSymlink) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined source symlink and close error for %s, got %v", sourceLink, err)
	}
	if result.SkippedSymlinks != 0 || len(result.Warnings) != 0 {
		t.Fatalf("expected impure source symlink failure not to be downgraded, got %#v", result)
	}
}

func TestScanRepoWithSourceReaderPropagatesTextOnlySymlinkError(t *testing.T) {
	repo := t.TempDir()
	createJVMSourceSymlink(t, repo, "Linked.java")
	readErr := errors.New("too many symlinks while opening source")
	readSource := func(string, string, int64) ([]byte, error) {
		return nil, readErr
	}

	result, err := scanRepoWithSourceReader(context.Background(), repo, map[string]string{}, map[string]string{}, readSource)
	if !errors.Is(err, readErr) {
		t.Fatalf("expected non-sentinel symlink error to propagate, got %v", err)
	}
	if result.SkippedSymlinks != 0 || len(result.Warnings) != 0 {
		t.Fatalf("expected non-sentinel symlink error not to be downgraded, got %#v", result)
	}
}

func TestScanJVMSourceFileWithReaderPropagatesImpureMissingPath(t *testing.T) {
	repo := t.TempDir()
	sourceDir := filepath.Join(repo, "src")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	closeErr := errors.New("close source ancestor")
	root := newJVMNestedMissingReadRoot(t, repo, "src", closeErr)
	sourcePath := filepath.Join(repo, "src", "Main.java")
	result := &scanResult{}
	readSource := func(repoPath, targetPath string, maxBytes int64) ([]byte, error) {
		relativePath, err := filepath.Rel(repoPath, targetPath)
		if err != nil {
			return nil, err
		}
		return safeio.ReadFileWithinRootLimit(root, relativePath, maxBytes)
	}

	err := scanJVMSourceFileWithReader(repo, sourcePath, nil, nil, nil, result, readSource)
	if err == nil {
		t.Fatal("expected impure missing source read to propagate")
	}
	if !errors.Is(err, os.ErrNotExist) || !errors.Is(err, closeErr) {
		t.Fatalf("expected missing source and close identities to survive, got %v", err)
	}
	if len(result.Warnings) != 0 || result.SkippedSymlinks != 0 || result.SkippedLargeFiles != 0 {
		t.Fatalf("expected impure missing source read not to downgrade, got %#v", result)
	}

	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("expected source path error, got %#v", err)
	}
	if pathErr.Path != filepath.Join("src", "Main.java") {
		t.Fatalf("expected rooted source path to propagate, got %q", pathErr.Path)
	}
}

func TestBuildFileWarningCollectorVisitWithinRootPropagatesImpureMissingPath(t *testing.T) {
	repo := t.TempDir()
	buildDir := filepath.Join(repo, "app")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatalf("mkdir build dir: %v", err)
	}
	closeErr := errors.New("close build ancestor")
	root := newJVMNestedMissingReadRoot(t, repo, "app", closeErr)
	buildPath := filepath.Join(repo, "app", buildGradleName)
	collector := buildFileWarningCollector{
		repoPath: repo,
		parser: func(string, string) ([]dependencyDescriptor, []string) {
			return nil, nil
		},
		names: []string{buildGradleName},
		seen:  map[string]struct{}{},
	}

	err := collector.visitWithinRoot(root, filepath.Join("app", buildGradleName), buildPath, &jvmRootedTestDirEntry{name: buildGradleName})
	if err == nil {
		t.Fatal("expected impure missing build read to propagate")
	}
	if !errors.Is(err, os.ErrNotExist) || !errors.Is(err, closeErr) {
		t.Fatalf("expected missing build and close identities to survive, got %v", err)
	}
	if len(collector.warnings) != 0 {
		t.Fatalf("expected impure missing build read not to downgrade, got %#v", collector.warnings)
	}

	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("expected build path error, got %#v", err)
	}
	if pathErr.Path != filepath.Join("app", buildGradleName) {
		t.Fatalf("expected rooted build path to propagate, got %q", pathErr.Path)
	}
}

func TestScanRepoWithinRootSuccessAndEmptyRepoGuards(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", "com", "example", "Main.java"), "package com.example;\nimport org.junit.jupiter.api.Test;\nclass Main {}\n")
	root := openJVMTestRoot(t, repo)

	result, err := scanRepoWithinRoot(context.Background(), repo, root, map[string]string{"org.junit.jupiter": "junit-jupiter-api"}, map[string]string{})
	if err != nil {
		t.Fatalf("scan rooted repo: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].Imports[0].Dependency != "junit-jupiter-api" {
		t.Fatalf("expected rooted scan result, got %#v", result)
	}

	if _, err := scanRepoWithinRoot(context.Background(), "", root, map[string]string{}, map[string]string{}); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected invalid empty repo path, got %v", err)
	}
}

func TestJVMAnalyseIgnoresUnrelatedFileFloodForCandidateBudgets(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), `dependencies { implementation("org.junit.jupiter:junit-jupiter-api:5.10.0") }`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", "com", "example", "Main.java"), "package com.example;\nimport org.junit.jupiter.api.Test;\nclass Main {}\n")
	for index := 0; index < maxJVMBuildFiles+32; index++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "docs", fmt.Sprintf("note-%04d.txt", index)), "ignored\n")
	}

	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 1})
	if err != nil {
		t.Fatalf("analyse repo with unrelated file flood: %v", err)
	}
	if len(result.Dependencies) == 0 || result.Dependencies[0].Name != "junit-jupiter-api" {
		t.Fatalf("expected declared/scanned JVM dependency to survive unrelated file flood, got %#v", result.Dependencies)
	}
	warnings := strings.Join(result.Warnings, "\n")
	if strings.Contains(warnings, "rooted walk limit") || strings.Contains(warnings, "no JVM dependencies discovered") {
		t.Fatalf("expected unrelated flood not to trip candidate-budget warnings, got %#v", result.Warnings)
	}
}

func TestScanRepoWithinRootIgnoresSourceSymlinkDecoyFloodForCandidateBudget(t *testing.T) {
	repo := t.TempDir()
	outsideSource := filepath.Join(t.TempDir(), "Outside.java")
	testutil.MustWriteFile(t, outsideSource, "class Outside {}\n")
	for index := 0; index < maxJVMSourceFiles+32; index++ {
		linkPath := filepath.Join(repo, "decoys", fmt.Sprintf("Link%04d.java", index))
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
			t.Fatalf("mkdir source decoy dir: %v", err)
		}
		if err := os.Symlink(outsideSource, linkPath); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", "com", "example", "Main.java"), "package com.example;\nclass Main {}\n")
	root := openJVMTestRoot(t, repo)

	result, err := scanRepoWithinRoot(context.Background(), repo, root, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatalf("scan rooted repo with source symlink flood: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != filepath.Join("src", "main", "java", "com", "example", "Main.java") {
		t.Fatalf("expected legitimate later source file to be scanned, got %#v", result.Files)
	}
	joinedWarnings := strings.Join(result.Warnings, "\n")
	if strings.Contains(joinedWarnings, "rooted walk limit") {
		t.Fatalf("expected source symlink decoys not to consume rooted-walk candidate budget, got %#v", result.Warnings)
	}
}

func TestCollectDeclaredDependenciesWithinRootIgnoresBuildSymlinkDecoyFloodForCandidateBudget(t *testing.T) {
	repo := t.TempDir()
	outsideBuild := filepath.Join(t.TempDir(), buildGradleName)
	testutil.MustWriteFile(t, outsideBuild, `dependencies { implementation("org.fake:fake:1.0.0") }`)
	for index := 0; index <= maxJVMBuildFiles/3+8; index++ {
		decoyDir := filepath.Join(repo, "decoys", fmt.Sprintf("module-%04d", index))
		if err := os.MkdirAll(decoyDir, 0o755); err != nil {
			t.Fatalf("mkdir build decoy dir: %v", err)
		}
		for _, name := range []string{pomXMLName, buildGradleName, buildGradleKTSName} {
			if err := os.Symlink(outsideBuild, filepath.Join(decoyDir, name)); err != nil {
				t.Skipf("symlink not supported: %v", err)
			}
		}
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "app", buildGradleName), `dependencies { implementation("org.junit.jupiter:junit-jupiter-api:5.10.0") }`)
	root := openJVMTestRoot(t, repo)

	descriptors, _, _, warnings, err := collectDeclaredDependenciesWithinRoot(context.Background(), repo, root)
	if err != nil {
		t.Fatalf("collect rooted dependencies with build symlink flood: %v", err)
	}
	if len(descriptors) != 1 || descriptors[0].Name != "junit-jupiter-api" {
		t.Fatalf("expected legitimate later build file to be parsed, got %#v", descriptors)
	}
	joinedWarnings := strings.Join(warnings, "\n")
	if strings.Contains(joinedWarnings, "rooted walk limit") {
		t.Fatalf("expected build symlink decoys not to consume rooted-walk candidate budget, got %#v", warnings)
	}
}

func TestCollectDeclaredDependenciesWithinRootDowngradesTraversalLimitToWarning(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "app", buildGradleName), `dependencies { implementation("org.junit.jupiter:junit-jupiter-api:5.10.0") }`)
	for index := 0; index < maxJVMBuildTraversalEntries+16; index++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "flood", fmt.Sprintf("entry-%05d.txt", index)), "ignored\n")
	}
	root := openJVMTestRoot(t, repo)

	descriptors, _, _, warnings, err := collectDeclaredDependenciesWithinRoot(context.Background(), repo, root)
	if err != nil {
		t.Fatalf("collect rooted dependencies under traversal cap: %v", err)
	}
	if len(descriptors) != 1 || descriptors[0].Name != "junit-jupiter-api" {
		t.Fatalf("expected rooted dependency descriptors before traversal cap, got %#v", descriptors)
	}
	joinedWarnings := strings.Join(warnings, "\n")
	if !strings.Contains(joinedWarnings, "JVM build file scan reached the rooted walk limit") {
		t.Fatalf("expected partial rooted-walk warning, got %#v", warnings)
	}
}

func TestScanRepoWithinRootReportsSkippedAndEmptyWarnings(t *testing.T) {
	repo := t.TempDir()
	largeSource := filepath.Join(repo, "src", "main", "java", "com", "example", "Large.java")
	testutil.MustWriteFile(t, largeSource, strings.Repeat("a", int(maxScannableJVMSourceFile)+1))
	outsideSource := filepath.Join(t.TempDir(), "Outside.java")
	testutil.MustWriteFile(t, outsideSource, "class Outside {}\n")
	linkPath := filepath.Join(repo, "src", "main", "java", "com", "example", "Linked.java")
	if err := os.Symlink(outsideSource, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	root := openJVMTestRoot(t, repo)

	result, err := scanRepoWithinRoot(context.Background(), repo, root, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatalf("scan rooted repo warnings: %v", err)
	}
	if result.SkippedLargeFiles != 1 || result.SkippedSymlinks != 1 || len(result.Files) != 0 {
		t.Fatalf("expected one large skip, one symlink skip, and no scanned files, got %#v", result)
	}
	if len(result.Warnings) != 4 {
		t.Fatalf("expected aggregate skipped and empty warnings, got %#v", result.Warnings)
	}
}

func TestCollectDeclaredDependenciesWithinRootReportsWarnings(t *testing.T) {
	repo := t.TempDir()
	settingsPath := filepath.Join(repo, buildGradleKTSName)
	testutil.MustWriteFile(t, settingsPath, `dependencies { implementation(libs.okhttp) }`)
	root := openJVMTestRoot(t, repo)

	collector := buildFileWarningCollector{
		repoPath: repo,
		parser: func(path, content string) ([]dependencyDescriptor, []string) {
			return []dependencyDescriptor{{Name: "okhttp", Group: "com.squareup.okhttp3", Artifact: "okhttp"}}, []string{"parse warning"}
		},
		names: []string{buildGradleKTSName},
		seen:  make(map[string]struct{}),
	}
	if err := collector.visitWithinRoot(root, filepath.Join("..", buildGradleKTSName), filepath.Join(repo, "..", buildGradleKTSName), mustReadJVMSourceDirEntry(t, repo, filepath.Base(settingsPath))); err != nil {
		t.Fatalf("visit escaping rooted build file: %v", err)
	}
	if len(collector.warnings) != 1 || !strings.Contains(collector.warnings[0], "unable to read ../build.gradle.kts") {
		t.Fatalf("expected rooted build warning for escaping path, got %#v", collector.warnings)
	}

	collector = buildFileWarningCollector{
		repoPath: repo,
		parser: func(path, content string) ([]dependencyDescriptor, []string) {
			return []dependencyDescriptor{{Name: "okhttp", Group: "com.squareup.okhttp3", Artifact: "okhttp"}}, []string{"parse warning"}
		},
		names: []string{buildGradleKTSName},
		seen:  make(map[string]struct{}),
	}
	if err := collector.visitWithinRoot(root, filepath.Base(settingsPath), settingsPath, mustReadJVMSourceDirEntry(t, repo, filepath.Base(settingsPath))); err != nil {
		t.Fatalf("visit rooted build file: %v", err)
	}
	if len(collector.descriptors) != 1 || len(collector.warnings) != 1 || collector.warnings[0] != "parse warning" {
		t.Fatalf("expected rooted build parse warning and descriptor, got descriptors=%#v warnings=%#v", collector.descriptors, collector.warnings)
	}
}

func TestBuildFileWarningCollectorVisitWithinRootSkipsDuplicateDescriptor(t *testing.T) {
	repo := t.TempDir()
	settingsPath := filepath.Join(repo, buildGradleKTSName)
	testutil.MustWriteFile(t, settingsPath, `dependencies { implementation("com.squareup.okhttp3:okhttp:4.12.0") }`)
	root := openJVMTestRoot(t, repo)
	collector := buildFileWarningCollector{
		repoPath: repo,
		parser: func(string, string) ([]dependencyDescriptor, []string) {
			return []dependencyDescriptor{{Name: "okhttp", Group: "com.squareup.okhttp3", Artifact: "okhttp"}}, nil
		},
		names: []string{buildGradleKTSName},
		seen:  map[string]struct{}{"com.squareup.okhttp3:okhttp": {}},
	}

	if err := collector.visitWithinRoot(root, filepath.Base(settingsPath), settingsPath, mustReadJVMSourceDirEntry(t, repo, filepath.Base(settingsPath))); err != nil {
		t.Fatalf("visit duplicate rooted build file: %v", err)
	}

	if len(collector.descriptors) != 0 {
		t.Fatalf("expected duplicate rooted descriptor to be skipped, got %#v", collector.descriptors)
	}
}

func TestParseBuildFilesWithinRootPropagatesImpureReadErrors(t *testing.T) {
	for _, test := range []jvmImpureBuildReadTest{
		{
			name:       "file limit with close error",
			newFile:    newOversizedJVMBuildTestFile,
			wantRead:   safeio.ErrFileTooLarge,
			closeLabel: "oversized build file",
		},
		{
			name:       "symlink sentinel with close error",
			newFile:    newSymlinkJVMBuildTestFile,
			wantRead:   safeio.ErrTargetPathSymlink,
			closeLabel: "build file after stat failure",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertImpureJVMBuildReadError(t, test)
		})
	}
}

type jvmImpureBuildReadTest struct {
	name       string
	newFile    func(fs.FileInfo, error, *int) safeio.File
	wantRead   error
	closeLabel string
}

func newOversizedJVMBuildTestFile(info fs.FileInfo, closeErr error, closeCalls *int) safeio.File {
	statCalls := 0
	return &jvmRootedTestFile{
		close: func() error {
			(*closeCalls)++
			return closeErr
		},
		stat: func() (fs.FileInfo, error) {
			statCalls++
			if statCalls == 1 {
				return info, nil
			}
			return &jvmRootedSizedFileInfo{FileInfo: info, size: maxScannableJVMBuildFile + 1}, nil
		},
	}
}

func newSymlinkJVMBuildTestFile(_ fs.FileInfo, closeErr error, closeCalls *int) safeio.File {
	return &jvmRootedTestFile{
		close: func() error {
			(*closeCalls)++
			return closeErr
		},
		stat: func() (fs.FileInfo, error) {
			return nil, safeio.ErrTargetPathSymlink
		},
	}
}

func assertImpureJVMBuildReadError(t *testing.T, test jvmImpureBuildReadTest) {
	t.Helper()
	repo := t.TempDir()
	buildPath := filepath.Join(repo, buildGradleName)
	testutil.MustWriteFile(t, buildPath, `dependencies { implementation("org.example:example:1.0.0") }`)
	info, err := os.Stat(buildPath)
	if err != nil {
		t.Fatalf("stat build file: %v", err)
	}
	closeErr := errors.New("close " + test.closeLabel)
	closeCalls := 0
	root := newJVMRootedReadTestRoot(t, repo, filepath.Base(buildPath), test.newFile(info, closeErr, &closeCalls))

	parser := func(string, string) ([]dependencyDescriptor, []string) {
		t.Fatal("parser must not run after rooted read failure")
		return nil, nil
	}
	descriptors, warnings, err := parseBuildFilesWithWarningsWithinRoot(context.Background(), repo, root, parser, buildGradleName)
	if !errors.Is(err, test.wantRead) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined rooted build read and close error, got %v", err)
	}
	if len(descriptors) != 0 || len(warnings) != 0 {
		t.Fatalf("expected impure build read failure not to be downgraded, got descriptors=%#v warnings=%#v", descriptors, warnings)
	}
	if closeCalls != 1 {
		t.Fatalf("expected failed build file to close once, got %d", closeCalls)
	}
}

func TestRootedReadersRejectEscapingPaths(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), `dependencies { implementation("org.junit.jupiter:junit-jupiter-api:5.10.0") }`)
	root := openJVMTestRoot(t, repo)

	if content, err := readJVMBuildFileWithinRoot(root, repo, filepath.Join(repo, buildGradleName)); err != nil || !strings.Contains(string(content), "junit-jupiter-api") {
		t.Fatalf("expected rooted build reader success, got content=%q err=%v", string(content), err)
	}
	if _, err := readJVMBuildFileWithinRoot(root, repo, filepath.Join(repo, "..", buildGradleName)); err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected rooted build reader to reject escaping path, got %v", err)
	}

	if _, _, err := shared.LoadGradleCatalogResolverWithinRoot(context.Background(), "", root); err != nil {
		t.Fatalf("expected empty rooted Gradle resolver to succeed, got %v", err)
	}
	if _, err := readJVMBuildFileWithinRoot(root, "relative-repo", filepath.Join(repo, buildGradleName)); err == nil {
		t.Fatal("expected rooted build reader relative-path rel error")
	}
}

func TestCollectDeclaredDependenciesWithinRootResolvesCatalogReferences(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "settings.gradle.kts"), `
dependencyResolutionManagement {
  versionCatalogs {
    create("testLibs") {
      from(files("gradle/test-libs.versions.toml"))
    }
  }
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "gradle", "test-libs.versions.toml"), `
[libraries]
junit-jupiter = { group = "org.junit.jupiter", name = "junit-jupiter-api", version = "5.10.0" }
`)
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleKTSName), `
dependencies {
  implementation(testLibs.junit.jupiter)
}
`)
	root := openJVMTestRoot(t, repo)

	descriptors, prefixes, aliases, warnings, err := collectDeclaredDependenciesWithinRoot(context.Background(), repo, root)
	if err != nil {
		t.Fatalf("collect rooted catalog dependencies: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected rooted catalog dependency resolution without warnings, got %#v", warnings)
	}
	if len(descriptors) != 1 || descriptors[0].Name != "junit-jupiter-api" {
		t.Fatalf("expected rooted catalog descriptor, got %#v", descriptors)
	}
	if prefixes["org.junit.jupiter.junit.jupiter.api"] == "" || aliases["junit.jupiter.api"] == "" {
		t.Fatalf("expected rooted catalog descriptor lookups, got prefixes=%#v aliases=%#v", prefixes, aliases)
	}
}

func openJVMTestRoot(t *testing.T, repo string) safeio.Root {
	t.Helper()
	root, err := safeio.OpenRoot(repo)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatalf("close root: %v", closeErr)
		}
	})
	return root
}

type jvmRootedTestRoot struct {
	safeio.Root
	info     fs.FileInfo
	lstat    func(string) (fs.FileInfo, error)
	open     func(string) (safeio.File, error)
	openRoot func(string) (safeio.Root, error)
	close    func() error
}

func (r *jvmRootedTestRoot) Open(name string) (safeio.File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return nil, errors.New("unexpected open: " + name)
}

func (*jvmRootedTestRoot) OpenFile(string, int, os.FileMode) (safeio.File, error) {
	return nil, errors.New("unexpected open file")
}

func (r *jvmRootedTestRoot) OpenRoot(name string) (safeio.Root, error) {
	if r.openRoot != nil {
		return r.openRoot(name)
	}
	if r.Root != nil {
		return r.Root.OpenRoot(name)
	}
	return nil, errors.New("unexpected open root")
}

func (r *jvmRootedTestRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.lstat != nil {
		return r.lstat(name)
	}
	if name == "." && r.info != nil {
		return r.info, nil
	}
	return nil, errors.New("unexpected lstat: " + name)
}

func (*jvmRootedTestRoot) Mkdir(string, os.FileMode) error { return errors.New("unexpected mkdir") }
func (*jvmRootedTestRoot) Chmod(string, os.FileMode) error { return errors.New("unexpected chmod") }
func (*jvmRootedTestRoot) MkdirAll(string, os.FileMode) error {
	return errors.New("unexpected mkdir all")
}
func (*jvmRootedTestRoot) Rename(string, string) error { return errors.New("unexpected rename") }
func (*jvmRootedTestRoot) Remove(string) error         { return errors.New("unexpected remove") }
func (r *jvmRootedTestRoot) Close() error {
	if r.close != nil {
		return r.close()
	}
	if r.Root != nil {
		return r.Root.Close()
	}
	return nil
}

type jvmRootedTestFile struct {
	close func() error
	stat  func() (fs.FileInfo, error)
	sync  func() error
}

func (*jvmRootedTestFile) Read([]byte) (int, error)    { return 0, io.EOF }
func (*jvmRootedTestFile) Write(p []byte) (int, error) { return len(p), nil }
func (f *jvmRootedTestFile) Close() error {
	if f.close != nil {
		return f.close()
	}
	return nil
}
func (f *jvmRootedTestFile) Stat() (fs.FileInfo, error) {
	if f.stat != nil {
		return f.stat()
	}
	return nil, errors.New("unexpected stat")
}
func (*jvmRootedTestFile) Chmod(os.FileMode) error { return nil }
func (f *jvmRootedTestFile) Sync() error {
	if f.sync != nil {
		return f.sync()
	}
	return nil
}

type jvmRootedSizedFileInfo struct {
	fs.FileInfo
	size int64
}

func (i *jvmRootedSizedFileInfo) Size() int64 {
	return i.size
}

type jvmRootedTestDirectory struct {
	jvmRootedTestFile
	fillEntry    fs.DirEntry
	extraEntries int
	readErr      error
	noProgress   bool
}

func (d *jvmRootedTestDirectory) ReadDir(count int) ([]fs.DirEntry, error) {
	if d.readErr != nil {
		return nil, d.readErr
	}
	if d.noProgress {
		return nil, nil
	}
	if d.fillEntry != nil {
		entries := make([]fs.DirEntry, count+d.extraEntries)
		for index := range entries {
			entries[index] = d.fillEntry
		}
		d.fillEntry = nil
		return entries, nil
	}
	return nil, io.EOF
}

type jvmRootedTestDirEntry struct {
	name string
}

func (e *jvmRootedTestDirEntry) Name() string    { return e.name }
func (*jvmRootedTestDirEntry) IsDir() bool       { return false }
func (*jvmRootedTestDirEntry) Type() fs.FileMode { return 0 }
func (e *jvmRootedTestDirEntry) Info() (fs.FileInfo, error) {
	return &jvmRootedTestFileInfo{name: e.name}, nil
}

type jvmRootedTestFileInfo struct {
	name string
}

func (i *jvmRootedTestFileInfo) Name() string     { return i.name }
func (*jvmRootedTestFileInfo) Size() int64        { return 0 }
func (*jvmRootedTestFileInfo) Mode() fs.FileMode  { return 0 }
func (*jvmRootedTestFileInfo) ModTime() time.Time { return time.Time{} }
func (*jvmRootedTestFileInfo) IsDir() bool        { return false }
func (*jvmRootedTestFileInfo) Sys() any           { return nil }

func newJVMNestedMissingReadRoot(t *testing.T, repo, dirName string, closeErr error) safeio.Root {
	t.Helper()
	repoRoot := openJVMTestRoot(t, repo)
	dirRoot := openJVMTestRoot(t, filepath.Join(repo, dirName))
	return &jvmRootedTestRoot{
		Root:  repoRoot,
		lstat: repoRoot.Lstat,
		open:  repoRoot.Open,
		openRoot: func(name string) (safeio.Root, error) {
			if name != dirName {
				return repoRoot.OpenRoot(name)
			}
			return &jvmRootedTestRoot{
				Root:  dirRoot,
				lstat: dirRoot.Lstat,
				open:  dirRoot.Open,
				close: func() error {
					if err := dirRoot.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
						return err
					}
					return closeErr
				},
			}, nil
		},
	}
}

func newJVMRootedReadTestRoot(t *testing.T, repo, targetName string, targetFile safeio.File) safeio.Root {
	t.Helper()
	realRoot := openJVMTestRoot(t, repo)
	return &jvmRootedTestRoot{
		Root:  realRoot,
		lstat: realRoot.Lstat,
		open: func(name string) (safeio.File, error) {
			if name == targetName {
				return targetFile, nil
			}
			return realRoot.Open(name)
		},
	}
}

func newJVMRootedTraversalLimitRoot(t *testing.T, repo string, closeErr error) safeio.Root {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(repo, "ignored.txt"), "ignored\n")
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read traversal-limit fixture directory: %v", err)
	}
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat traversal-limit fixture directory: %v", err)
	}
	directory := &jvmRootedTestDirectory{
		jvmRootedTestFile: jvmRootedTestFile{
			close: func() error { return closeErr },
			stat:  func() (fs.FileInfo, error) { return info, nil },
		},
		fillEntry:    entries[0],
		extraEntries: 1,
	}
	return &jvmRootedTestRoot{
		info: info,
		open: func(string) (safeio.File, error) {
			return directory, nil
		},
	}
}
