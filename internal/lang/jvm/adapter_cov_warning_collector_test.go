package jvm

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestBuildFileWarningCollectorVisitPropagatesWalkError(t *testing.T) {
	repo := t.TempDir()
	walkErr := errors.New("walk failed")
	collector := newBuildFileWarningCollector(repo)
	if err := collector.visit("", nil, walkErr); !errors.Is(err, walkErr) {
		t.Fatalf("expected walk error to propagate, got %v", err)
	}
}

func TestBuildFileWarningCollectorVisitWarnsOnOutsideBuildFile(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	outsideBuild := filepath.Join(outside, buildGradleName)
	testutil.MustWriteFile(t, outsideBuild, `implementation "org.example:demo:1.0.0"`)

	collector := newBuildFileWarningCollector(repo)
	buildEntry := requireDirEntry(t, outside, buildGradleName)
	if err := collector.visit(outsideBuild, buildEntry, nil); err != nil {
		t.Fatalf("unexpected collector visit error for outside build file: %v", err)
	}
	if len(collector.warnings) != 1 || !strings.Contains(collector.warnings[0], "unable to read") {
		t.Fatalf("expected read warning for outside build file, got %#v", collector.warnings)
	}
}

func TestBuildFileWarningCollectorVisitSkipsGradleDir(t *testing.T) {
	repo := t.TempDir()
	collector := newBuildFileWarningCollector(repo)
	skipDir := filepath.Join(repo, ".gradle")
	if err := os.MkdirAll(skipDir, 0o755); err != nil {
		t.Fatalf("mkdir skip dir: %v", err)
	}
	skipEntry := requireDirEntry(t, repo, ".gradle")
	if err := collector.visit(skipDir, skipEntry, nil); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected .gradle visit to skip dir, got %v", err)
	}
}

func TestBuildFileWarningCollectorVisitCollectsRepoBuildFile(t *testing.T) {
	repo := t.TempDir()
	collector := newBuildFileWarningCollector(repo)
	insideBuild := filepath.Join(repo, buildGradleName)
	testutil.MustWriteFile(t, insideBuild, `implementation "org.example:demo:1.0.0"`)
	buildEntry := requireDirEntry(t, repo, buildGradleName)
	if err := collector.visit(insideBuild, buildEntry, nil); err != nil {
		t.Fatalf("unexpected collector visit error for repo build file: %v", err)
	}
	if len(collector.descriptors) != 1 || collector.descriptors[0].Name != "demo" {
		t.Fatalf("expected parsed descriptor to be collected once, got %#v", collector.descriptors)
	}
	if len(collector.warnings) != 1 || collector.warnings[0] != "parse warning" {
		t.Fatalf("expected parser warning append, got %#v", collector.warnings)
	}
}

func TestFormatBuildFileReadWarningIncludesPathAndError(t *testing.T) {
	repo := t.TempDir()
	insideBuild := filepath.Join(repo, buildGradleName)
	warning := formatBuildFileReadWarning(repo, insideBuild, fs.ErrPermission)
	if !strings.Contains(warning, buildGradleName) || !strings.Contains(warning, fs.ErrPermission.Error()) {
		t.Fatalf("unexpected formatted warning: %q", warning)
	}
}

func newBuildFileWarningCollector(repo string) buildFileWarningCollector {
	return buildFileWarningCollector{
		repoPath: repo,
		parser: func(path, content string) ([]dependencyDescriptor, []string) {
			return []dependencyDescriptor{
				{Name: "demo", Group: "org.example", Artifact: "demo"},
				{Name: "demo", Group: "org.example", Artifact: "demo"},
			}, []string{"parse warning"}
		},
		names: []string{buildGradleName},
		seen:  make(map[string]struct{}),
	}
}

func requireDirEntry(t *testing.T, dir, name string) os.DirEntry {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return entry
		}
	}
	t.Fatalf("expected %s entry in %s", name, dir)
	return nil
}

func TestJVMFallbackAndModuleSegmentAdditionalBranches(t *testing.T) {
	if got := fallbackDependency("single"); got != "single" {
		t.Fatalf("fallbackDependency(single) = %q, want %q", got, "single")
	}
	if got := lastModuleSegment(""); got != "" {
		t.Fatalf("lastModuleSegment(empty) = %q, want empty", got)
	}
	if got := sourceLayoutModuleRoot(filepath.FromSlash("module/java/Main.java")); got != "" {
		t.Fatalf("expected non-src path to have no source-layout root, got %q", got)
	}
}
