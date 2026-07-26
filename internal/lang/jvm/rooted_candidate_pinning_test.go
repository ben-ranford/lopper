package jvm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestRootedAnalysisCandidateReadsHaveBoundedOperationCounts(t *testing.T) {
	const (
		depth = 6
		width = 6
	)

	t.Run("source", func(t *testing.T) {
		repo := t.TempDir()
		candidates := createJVMDeepWideCandidateTree(t, repo, depth, width, func(dir string, level, branch int) {
			name := fmt.Sprintf("Source_%02d_%02d.java", level, branch)
			writeJVMRootedCandidate(t, filepath.Join(dir, name), "package bounded;\nclass Source {}\n")
		})
		counts, root := openCountingJVMRoot(t, repo)

		result, err := scanRepoWithinRoot(context.Background(), repo, root, nil, nil)
		if err != nil {
			t.Fatalf("scan deep-wide rooted sources: %v", err)
		}
		assertJVMRootedOperationCounts(t, repo, counts, candidates, 1)
		if len(result.Files) != candidates {
			t.Fatalf("expected %d source candidates, got %d", candidates, len(result.Files))
		}
	})

	t.Run("build", func(t *testing.T) {
		repo := t.TempDir()
		candidates := createJVMDeepWideCandidateTree(t, repo, depth, width, func(dir string, level, branch int) {
			content := fmt.Sprintf("dependencies { implementation(\"example:artifact-%02d-%02d:1\") }\n", level, branch)
			writeJVMRootedCandidate(t, filepath.Join(dir, buildGradleName), content)
		})
		counts, root := openCountingJVMRoot(t, repo)
		parserCalls := 0
		parser := func(string, string) ([]dependencyDescriptor, []string) {
			parserCalls++
			return nil, nil
		}

		_, warnings, err := parseBuildFilesWithWarningsWithinRoot(context.Background(), repo, root, parser, buildGradleName)
		if err != nil {
			t.Fatalf("scan deep-wide rooted builds: %v", err)
		}
		if len(warnings) != 0 {
			t.Fatalf("expected no rooted build warnings, got %#v", warnings)
		}
		assertJVMRootedOperationCounts(t, repo, counts, candidates, 1)
		if parserCalls != candidates {
			t.Fatalf("expected %d build candidates, got %d parser calls", candidates, parserCalls)
		}
	})

	t.Run("catalog", func(t *testing.T) {
		repo := t.TempDir()
		candidates := createJVMDeepWideCandidateTree(t, repo, depth, width, func(dir string, level, branch int) {
			catalogPath := filepath.Join(dir, "gradle", "libs.versions.toml")
			content := fmt.Sprintf("[libraries]\nentry_%02d_%02d = \"example:artifact-%02d-%02d:1\"\n", level, branch, level, branch)
			writeJVMRootedCandidate(t, catalogPath, content)
		})
		counts, root := openCountingJVMRoot(t, repo)

		_, warnings, err := shared.LoadGradleCatalogResolverWithinRoot(context.Background(), repo, root)
		if err != nil {
			t.Fatalf("scan deep-wide rooted catalogs: %v", err)
		}
		if len(warnings) != 0 {
			t.Fatalf("expected no rooted catalog warnings, got %#v", warnings)
		}
		assertJVMRootedOperationCounts(t, repo, counts, candidates, 2)
	})
}

func TestRootedAnalysisCandidateReadsSurviveEnumerationNamespaceSwaps(t *testing.T) {
	t.Run("source", testRootedSourceCandidateReadSurvivesSwap)
	t.Run("build", testRootedBuildCandidateReadSurvivesSwap)
	t.Run("catalog", testRootedCatalogCandidateReadSurvivesSwap)
}

func testRootedSourceCandidateReadSurvivesSwap(t *testing.T) {
	repo := t.TempDir()
	parentRel := filepath.Join("deep", "src")
	leaf := "Main.java"
	replacement := "package replacement;\nclass Main {}\n"
	writeJVMRootedCandidate(t, filepath.Join(repo, parentRel, leaf), "package original;\nclass Main {}\n")
	swap, root := openSwappingJVMRoot(t, repo, parentRel, leaf, replacement, 1)

	result, err := scanRepoWithinRoot(context.Background(), repo, root, nil, nil)
	if err != nil {
		t.Fatalf("scan source through swapped namespace: %v", err)
	}
	if !swap.swapped || len(result.Files) != 1 || result.Files[0].Package != "original" {
		t.Fatalf("expected pinned original source after swap, swapped=%v files=%#v", swap.swapped, result.Files)
	}
	assertJVMRootedNamespaceContains(t, repo, parentRel, leaf, replacement)
}

func testRootedBuildCandidateReadSurvivesSwap(t *testing.T) {
	repo := t.TempDir()
	parentRel := filepath.Join("deep", "app")
	leaf := buildGradleName
	original := `dependencies { implementation("original:artifact:1") }`
	replacement := `dependencies { implementation("replacement:artifact:1") }`
	writeJVMRootedCandidate(t, filepath.Join(repo, parentRel, leaf), original)
	swap, root := openSwappingJVMRoot(t, repo, parentRel, leaf, replacement, 1)
	var parsedContent string
	parser := func(_, content string) ([]dependencyDescriptor, []string) {
		parsedContent = content
		return nil, nil
	}

	_, _, err := parseBuildFilesWithWarningsWithinRoot(context.Background(), repo, root, parser, buildGradleName)
	if err != nil {
		t.Fatalf("scan build through swapped namespace: %v", err)
	}
	if !swap.swapped || parsedContent != original {
		t.Fatalf("expected pinned original build after swap, swapped=%v content=%q", swap.swapped, parsedContent)
	}
	assertJVMRootedNamespaceContains(t, repo, parentRel, leaf, replacement)
}

func testRootedCatalogCandidateReadSurvivesSwap(t *testing.T) {
	repo := t.TempDir()
	parentRel := filepath.Join("deep", "app", "gradle")
	leaf := "libs.versions.toml"
	original := "[libraries]\noriginal = \"original:artifact:1\"\n"
	replacement := "[libraries]\nreplacement = \"replacement:artifact:1\"\n"
	writeJVMRootedCandidate(t, filepath.Join(repo, parentRel, leaf), original)
	swap, root := openSwappingJVMRoot(t, repo, parentRel, leaf, replacement, 2)

	resolver, warnings, err := shared.LoadGradleCatalogResolverWithinRoot(context.Background(), repo, root)
	if err != nil {
		t.Fatalf("scan catalog through swapped namespace: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no rooted catalog warnings, got %#v", warnings)
	}
	buildPath := filepath.Join(repo, "deep", "app", buildGradleName)
	libraries, parseWarnings := resolver.ParseDependencyReferences(buildPath, "dependencies { implementation(libs.original) }")
	if !swap.swapped || len(parseWarnings) != 0 || len(libraries) != 1 || libraries[0].Group != "original" {
		t.Fatalf("expected pinned original catalog after swap, swapped=%v libraries=%#v warnings=%#v", swap.swapped, libraries, parseWarnings)
	}
	assertJVMRootedNamespaceContains(t, repo, parentRel, leaf, replacement)
}

type jvmRootOperationCounts struct {
	openRootCalls int
	fileOpenCalls int
}

type countingJVMRoot struct {
	safeio.Root
	counts *jvmRootOperationCounts
}

func openCountingJVMRoot(t *testing.T, repo string) (*jvmRootOperationCounts, safeio.Root) {
	t.Helper()
	root := openJVMTestRoot(t, repo)
	counts := &jvmRootOperationCounts{}
	return counts, &countingJVMRoot{Root: root, counts: counts}
}

func (r *countingJVMRoot) Open(name string) (safeio.File, error) {
	if name != "." {
		r.counts.fileOpenCalls++
	}
	return r.Root.Open(name)
}

func (r *countingJVMRoot) OpenRoot(name string) (safeio.Root, error) {
	r.counts.openRootCalls++
	child, err := r.Root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &countingJVMRoot{Root: child, counts: r.counts}, nil
}

func assertJVMRootedOperationCounts(t *testing.T, repo string, counts *jvmRootOperationCounts, candidates, traversalPasses int) {
	t.Helper()
	directories := countJVMRootedDirectories(t, repo)
	expectedRootOpens := traversalPasses * (directories - 1)
	if counts.openRootCalls != expectedRootOpens {
		t.Fatalf("expected %d pinned child-root opens for %d pass(es), got %d", expectedRootOpens, traversalPasses, counts.openRootCalls)
	}
	if counts.fileOpenCalls != candidates {
		t.Fatalf("expected %d callback-scoped candidate opens, got %d", candidates, counts.fileOpenCalls)
	}
}

func createJVMDeepWideCandidateTree(t *testing.T, repo string, depth, width int, writeCandidate func(dir string, level, branch int)) int {
	t.Helper()
	current := repo
	candidates := 0
	for level := 0; level < depth; level++ {
		current = filepath.Join(current, fmt.Sprintf("level-%02d", level))
		if err := os.MkdirAll(current, 0o755); err != nil {
			t.Fatalf("create deep directory: %v", err)
		}
		for branch := 0; branch < width; branch++ {
			branchDir := filepath.Join(current, fmt.Sprintf("branch-%02d", branch))
			if err := os.MkdirAll(branchDir, 0o755); err != nil {
				t.Fatalf("create wide directory: %v", err)
			}
			writeCandidate(branchDir, level, branch)
			candidates++
		}
	}
	return candidates
}

func writeJVMRootedCandidate(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create candidate parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write rooted candidate: %v", err)
	}
}

func countJVMRootedDirectories(t *testing.T, repo string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(repo, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("count rooted directories: %v", err)
	}
	return count
}

type jvmEnumerationSwap struct {
	t                  *testing.T
	repo               string
	parentRel          string
	leaf               string
	replacementContent string
	triggerEnumeration int
	enumerations       int
	swapped            bool
}

type swappingJVMRoot struct {
	safeio.Root
	relativePath string
	swap         *jvmEnumerationSwap
}

type swappingJVMDirectory struct {
	safeio.ReadDirFile
	swap *jvmEnumerationSwap
}

func openSwappingJVMRoot(t *testing.T, repo, parentRel, leaf, replacementContent string, triggerEnumeration int) (*jvmEnumerationSwap, safeio.Root) {
	t.Helper()
	root := openJVMTestRoot(t, repo)
	swap := &jvmEnumerationSwap{
		t:                  t,
		repo:               repo,
		parentRel:          filepath.Clean(parentRel),
		leaf:               leaf,
		replacementContent: replacementContent,
		triggerEnumeration: triggerEnumeration,
	}
	return swap, &swappingJVMRoot{Root: root, relativePath: ".", swap: swap}
}

func (r *swappingJVMRoot) Open(name string) (safeio.File, error) {
	file, err := r.Root.Open(name)
	if err != nil || name != "." || filepath.Clean(r.relativePath) != r.swap.parentRel {
		return file, err
	}
	directory, ok := file.(safeio.ReadDirFile)
	if !ok {
		return nil, errors.Join(fs.ErrInvalid, file.Close())
	}
	return &swappingJVMDirectory{ReadDirFile: directory, swap: r.swap}, nil
}

func (r *swappingJVMRoot) OpenRoot(name string) (safeio.Root, error) {
	child, err := r.Root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &swappingJVMRoot{
		Root:         child,
		relativePath: filepath.Join(r.relativePath, name),
		swap:         r.swap,
	}, nil
}

func (d *swappingJVMDirectory) ReadDir(count int) ([]fs.DirEntry, error) {
	entries, err := d.ReadDirFile.ReadDir(count)
	if containsJVMRootedEntry(entries, d.swap.leaf) {
		d.swap.observeCandidateEnumeration()
	}
	return entries, err
}

func containsJVMRootedEntry(entries []fs.DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}

func (s *jvmEnumerationSwap) observeCandidateEnumeration() {
	s.enumerations++
	if s.swapped || s.enumerations != s.triggerEnumeration {
		return
	}
	parentPath := filepath.Join(s.repo, s.parentRel)
	holdingPath := parentPath + "-pinned-original"
	if err := os.Rename(parentPath, holdingPath); err != nil {
		s.t.Fatalf("rename enumerated candidate parent: %v", err)
	}
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		s.t.Fatalf("create replacement candidate parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentPath, s.leaf), []byte(s.replacementContent), 0o600); err != nil {
		s.t.Fatalf("write replacement candidate: %v", err)
	}
	s.swapped = true
}

func assertJVMRootedNamespaceContains(t *testing.T, repo, parentRel, leaf, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, parentRel, leaf))
	if err != nil {
		t.Fatalf("read replacement namespace candidate: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(want) {
		t.Fatalf("expected replacement namespace content %q, got %q", want, data)
	}
}
