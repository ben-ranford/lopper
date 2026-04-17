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

func TestJVMBuildFileWarningCollectorBranches(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	outsideBuild := filepath.Join(outside, buildGradleName)
	testutil.MustWriteFile(t, outsideBuild, `implementation "org.example:demo:1.0.0"`)

	walkErr := errors.New("walk failed")
	collector := buildFileWarningCollector{
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

	if err := collector.visit("", nil, walkErr); !errors.Is(err, walkErr) {
		t.Fatalf("expected walk error to propagate, got %v", err)
	}

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if err := collector.visit(outsideBuild, entries[0], nil); err != nil {
		t.Fatalf("unexpected collector visit error for outside build file: %v", err)
	}
	if len(collector.warnings) != 1 || !strings.Contains(collector.warnings[0], "unable to read") {
		t.Fatalf("expected read warning for outside build file, got %#v", collector.warnings)
	}

	skipDir := filepath.Join(repo, ".gradle")
	if err := os.MkdirAll(skipDir, 0o755); err != nil {
		t.Fatalf("mkdir skip dir: %v", err)
	}
	repoEntries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir for skip branch: %v", err)
	}
	for _, entry := range repoEntries {
		if entry.Name() == ".gradle" {
			if err := collector.visit(skipDir, entry, nil); !errors.Is(err, filepath.SkipDir) {
				t.Fatalf("expected .gradle visit to skip dir, got %v", err)
			}
		}
	}

	insideBuild := filepath.Join(repo, buildGradleName)
	testutil.MustWriteFile(t, insideBuild, `implementation "org.example:demo:1.0.0"`)
	repoEntries, err = os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	var buildEntry os.DirEntry
	for _, entry := range repoEntries {
		if entry.Name() == buildGradleName {
			buildEntry = entry
			break
		}
	}
	if buildEntry == nil {
		t.Fatalf("expected %s entry in repo", buildGradleName)
	}
	if err := collector.visit(insideBuild, buildEntry, nil); err != nil {
		t.Fatalf("unexpected collector visit error for repo build file: %v", err)
	}
	if len(collector.descriptors) != 1 || collector.descriptors[0].Name != "demo" {
		t.Fatalf("expected parsed descriptor to be collected once, got %#v", collector.descriptors)
	}
	if len(collector.warnings) != 2 || collector.warnings[1] != "parse warning" {
		t.Fatalf("expected parser warning append, got %#v", collector.warnings)
	}

	warning := formatBuildFileReadWarning(repo, insideBuild, fs.ErrPermission)
	if !strings.Contains(warning, buildGradleName) || !strings.Contains(warning, fs.ErrPermission.Error()) {
		t.Fatalf("unexpected formatted warning: %q", warning)
	}
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
