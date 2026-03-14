package analysis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestServiceAdditionalHelperBranches(t *testing.T) {
	if rootContainsFile("\x00", "/repo/file") {
		t.Fatalf("expected invalid root path to fail root containment")
	}

	metadata := scopeMetadata(ScopeModePackage, "/repo", []string{"/repo", "\x00"})
	if metadata == nil || len(metadata.Packages) != 1 || metadata.Packages[0] != "." {
		t.Fatalf("expected scope metadata to keep repo root and skip invalid roots, got %#v", metadata)
	}

	if _, err := annotateRuntimeTraceIfPresent(t.TempDir(), "js-ts", report.Report{}); err == nil {
		t.Fatalf("expected directory runtime trace path to return error")
	}

	merged := mergeUsageUncertainty(&report.UsageUncertainty{Samples: []report.Location{{File: "a"}, {File: "b"}, {File: "c"}, {File: "d"}}}, &report.UsageUncertainty{Samples: []report.Location{{File: "e"}, {File: "f"}}})
	if len(merged.Samples) != 5 || merged.Samples[4].File != "e" {
		t.Fatalf("expected mergeUsageUncertainty to append only remaining sample slots, got %#v", merged.Samples)
	}

	hasStatic, hasRuntime := runtimeUsageSignals(&report.RuntimeUsage{})
	if !hasStatic || hasRuntime {
		t.Fatalf("expected zero-load static usage to report static-only, got static=%v runtime=%v", hasStatic, hasRuntime)
	}

	left := &report.CodemodReport{Mode: "safe", Suggestions: []report.CodemodSuggestion{{File: "a.js", Line: 1}}}
	got := mergeCodemodReport(left, nil)
	if got == left || got.Mode != left.Mode || len(got.Suggestions) != 1 {
		t.Fatalf("expected mergeCodemodReport to copy left report when right is nil, got %#v", got)
	}
}

func TestServiceAnnotateRuntimeTraceErrorBranchViaAnalyse(t *testing.T) {
	reg := language.NewRegistry()
	if err := reg.Register(&testServiceAdapter{
		id:     "ok",
		detect: language.Detection{Matched: true, Confidence: 90},
		analyse: report.Report{
			Dependencies: []report.DependencyReport{{Name: "dep"}},
		},
	}); err != nil {
		t.Fatalf(registerAdapterFmt, err)
	}

	svc := &Service{Registry: reg}
	traceDir := filepath.Join(t.TempDir(), "trace-dir")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	if _, err := svc.Analyse(context.Background(), Request{
		RepoPath:         ".",
		Language:         "all",
		TopN:             1,
		RuntimeTracePath: traceDir,
	}); err == nil {
		t.Fatalf("expected analyse to surface invalid runtime trace error")
	}
}

func TestServiceAdditionalAnalyseBranches(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write repo file: %v", err)
	}

	svc := &Service{Registry: language.NewRegistry()}
	if _, err := svc.Analyse(context.Background(), Request{
		RepoPath:        repo,
		IncludePatterns: []string{"["},
	}); err == nil {
		t.Fatalf("expected invalid scope pattern to fail analysis")
	}

	traceDir := filepath.Join(t.TempDir(), "trace-dir")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	if _, err := svc.Analyse(context.Background(), Request{
		RepoPath:         repo,
		RuntimeTracePath: traceDir,
	}); err == nil {
		t.Fatalf("expected no-result analysis to surface runtime trace error")
	}
}

func TestMergeUsageUncertaintyRemainingBranch(t *testing.T) {
	merged := mergeUsageUncertainty(&report.UsageUncertainty{Samples: []report.Location{{File: "a"}, {File: "b"}, {File: "c"}, {File: "d"}}}, &report.UsageUncertainty{})
	if len(merged.Samples) != 4 {
		t.Fatalf("expected empty-right merge to preserve left samples, got %#v", merged.Samples)
	}
}
