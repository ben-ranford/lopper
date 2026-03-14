package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestFindCodemodReport(t *testing.T) {
	if findCodemodReport(nil, "lodash") != nil {
		t.Fatal("expected nil codemod report for nil report input")
	}

	fallback := &report.CodemodReport{Mode: "apply"}
	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "left-pad"},
			{Name: "lodash", Codemod: fallback},
		},
	}
	if got := findCodemodReport(&reportData, "missing"); got != fallback {
		t.Fatalf("expected first available codemod fallback, got %#v", got)
	}
	if got := findCodemodReport(&reportData, "lodash"); got != fallback {
		t.Fatalf("expected dependency-specific codemod report, got %#v", got)
	}
	if findCodemodReport(&report.Report{Dependencies: []report.DependencyReport{{Name: "only"}}}, "missing") != nil {
		t.Fatal("expected nil codemod fallback when no dependency has codemod data")
	}
}

func TestBuildCodemodSkipResults(t *testing.T) {
	if len(buildCodemodSkipResults(nil)) != 0 {
		t.Fatal("expected no skip results for nil input")
	}

	results := buildCodemodSkipResults([]report.CodemodSkip{
		{File: "b.js", ReasonCode: "beta"},
		{File: "a.js", ReasonCode: " alpha "},
		{File: "a.js", ReasonCode: "beta"},
		{File: "a.js", ReasonCode: "beta"},
		{File: "a.js", ReasonCode: ""},
	})
	if len(results) != 2 {
		t.Fatalf("expected grouped skip results, got %#v", results)
	}
	if results[0].File != "a.js" || results[0].PatchCount != 4 {
		t.Fatalf("unexpected grouped skip result: %#v", results[0])
	}
	if results[0].Message != "reason codes: alpha, beta" {
		t.Fatalf("expected deduplicated sorted reason codes, got %q", results[0].Message)
	}
	if results[1].File != "b.js" || results[1].Message != "reason codes: beta" {
		t.Fatalf("unexpected second skip result: %#v", results[1])
	}
}

func TestApplySuggestionsToContent(t *testing.T) {
	suggestions := []report.CodemodSuggestion{
		{File: indexJSFile, Line: 1, Original: importLodashLine, Replacement: importLodashMapLine},
	}
	updated, err := applySuggestionsToContent(importLodashLine+"\r\nmap()\r\n", suggestions)
	if err != nil {
		t.Fatalf("apply suggestions with CRLF: %v", err)
	}
	if !strings.Contains(updated, "\r\n") || !strings.Contains(updated, importLodashMapLine) {
		t.Fatalf("expected CRLF-preserving updated content, got %q", updated)
	}

	_, err = applySuggestionsToContent(importLodashLineWithLF, []report.CodemodSuggestion{{File: indexJSFile, Line: 3, Original: "x", Replacement: "y"}})
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("expected line-range error, got %v", err)
	}
	_, err = applySuggestionsToContent(importLodashLineWithLF, []report.CodemodSuggestion{{File: indexJSFile, Line: 0, Original: "x", Replacement: "y"}})
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("expected zero-line range error, got %v", err)
	}
	_, err = applySuggestionsToContent(importLodashLineWithLF, []report.CodemodSuggestion{{File: indexJSFile, Line: 1, Original: "import { filter } from \"lodash\";", Replacement: "import filter from \"lodash/filter\";"}})
	if err == nil || !strings.Contains(err.Error(), "source line mismatch") {
		t.Fatalf("expected source mismatch error, got %v", err)
	}
}

func TestPrepareCodemodFiles(t *testing.T) {
	repo := t.TempDir()
	writeTextFile(t, filepath.Join(repo, indexJSFile), importLodashLineWithLF, 0o644)

	prepared, failures := prepareCodemodFiles(repo, []report.CodemodSuggestion{
		{File: indexJSFile, Line: 1, Original: importLodashLine, Replacement: importLodashMapLine},
		{File: "../escape.js", Line: 1, Original: "x", Replacement: "y"},
		{File: "missing.js", Line: 1, Original: "x", Replacement: "y"},
	})
	if len(prepared) != 1 {
		t.Fatalf("expected one prepared file, got %#v", prepared)
	}
	if prepared[0].patchCount != 1 || !strings.Contains(prepared[0].updated, lodashMapModule) {
		t.Fatalf("unexpected prepared file payload: %#v", prepared[0])
	}
	if len(failures) != 2 {
		t.Fatalf("expected two preparation failures, got %#v", failures)
	}
}

func TestPrepareCodemodFilesSameLineTieOrdering(t *testing.T) {
	repo := t.TempDir()
	writeTextFile(t, filepath.Join(repo, indexJSFile), importLodashLineWithLF, 0o644)

	prepared, failures := prepareCodemodFiles(repo, []report.CodemodSuggestion{
		{File: indexJSFile, Line: 1, ImportName: "zeta", Original: importLodashLine, Replacement: "import zeta from \"lodash/zeta\";"},
		{File: indexJSFile, Line: 1, ImportName: "alpha", Original: importLodashLine, Replacement: "import alpha from \"lodash/alpha\";"},
	})
	if len(prepared) != 0 {
		t.Fatalf("expected no prepared files when same-line suggestions conflict, got %#v", prepared)
	}
	if len(failures) != 1 || failures[0].PatchCount != 2 {
		t.Fatalf("expected one grouped failure for conflicting same-line suggestions, got %#v", failures)
	}
}

func TestPrepareCodemodFilesStatFailure(t *testing.T) {
	repo := t.TempDir()
	fifoPath := filepath.Join(repo, indexJSFile)
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		t.Fatalf("mkfifo source: %v", err)
	}

	writerDone := make(chan struct{})
	writerErrs := make(chan error, 3)
	go func() {
		defer close(writerDone)
		file, err := os.OpenFile(fifoPath, os.O_WRONLY, 0)
		if err != nil {
			writerErrs <- err
			return
		}
		if _, err := file.WriteString(importLodashLineWithLF); err != nil {
			writerErrs <- err
		}
		if err := os.Remove(fifoPath); err != nil && !os.IsNotExist(err) {
			writerErrs <- err
		}
		if err := file.Close(); err != nil {
			writerErrs <- err
		}
	}()

	prepared, failures := prepareCodemodFiles(repo, []report.CodemodSuggestion{
		{File: indexJSFile, Line: 1, Original: importLodashLine, Replacement: importLodashMapLine},
	})
	<-writerDone
	close(writerErrs)
	for err := range writerErrs {
		t.Fatalf("fifo writer error: %v", err)
	}

	if len(prepared) != 0 {
		t.Fatalf("expected no prepared files when stat fails, got %#v", prepared)
	}
	if len(failures) != 1 || !strings.Contains(strings.ToLower(failures[0].Message), "no such") {
		t.Fatalf("expected stat failure result, got %#v", failures)
	}
}

func TestPrepareCodemodFilesDifferentLineOrdering(t *testing.T) {
	repo := t.TempDir()
	writeTextFile(t, filepath.Join(repo, indexJSFile), importLodashLineWithLF+"map(items)\n", 0o644)

	prepared, failures := prepareCodemodFiles(repo, []report.CodemodSuggestion{
		{File: indexJSFile, Line: 2, ImportName: "mapCall", Original: "map(items)", Replacement: "lodashMap(items)"},
		{File: indexJSFile, Line: 1, ImportName: "mapImport", Original: importLodashLine, Replacement: "import lodashMap from \"lodash/map\";"},
	})
	if len(failures) != 0 {
		t.Fatalf("expected no preparation failures, got %#v", failures)
	}
	if len(prepared) != 1 {
		t.Fatalf("expected one prepared file, got %#v", prepared)
	}
	if prepared[0].patchCount != 2 {
		t.Fatalf("expected both patches to be prepared, got %#v", prepared[0])
	}
	if prepared[0].updated != "import lodashMap from \"lodash/map\";\nlodashMap(items)\n" {
		t.Fatalf("unexpected updated content after ordered apply: %q", prepared[0].updated)
	}
}

func TestApplyPreparedCodemodFiles(t *testing.T) {
	repo := t.TempDir()
	okPath := filepath.Join(repo, indexJSFile)
	writeTextFile(t, okPath, beforeContent, 0o644)

	preparedFiles := []preparedCodemodFile{
		{file: indexJSFile, absPath: okPath, updated: "after\n", patchCount: 1, mode: 0o644},
		{file: "missing.js", absPath: filepath.Join(repo, "missing", "nested.js"), updated: "nope\n", patchCount: 1, mode: 0o644},
	}
	applied, failures := applyPreparedCodemodFiles(repo, preparedFiles, nil)
	if len(applied) != 1 || applied[0].Status != codemodApplyStatusApplied {
		t.Fatalf("expected one applied file result, got %#v", applied)
	}
	if len(failures) != 1 || failures[0].Status != codemodApplyStatusFailed {
		t.Fatalf("expected one failed file result, got %#v", failures)
	}
	if got := readTextFile(t, okPath); got != "after\n" {
		t.Fatalf("expected updated file content, got %q", got)
	}
}

func TestWriteCodemodRollbackArtifact(t *testing.T) {
	repo := t.TempDir()
	rollbackFiles := []preparedCodemodFile{{file: indexJSFile, original: beforeContent, mode: 0o644}}

	backupPath, err := writeCodemodRollbackArtifact(repo, lodashMapModule, rollbackFiles, time.Date(2026, time.March, 13, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("write rollback artifact: %v", err)
	}
	if !strings.Contains(backupPath, "lodash-map") {
		t.Fatalf("expected sanitized dependency name in backup path, got %q", backupPath)
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(backupPath))); err != nil {
		t.Fatalf("expected rollback artifact file to exist: %v", err)
	}

	emptyPath, err := writeCodemodRollbackArtifact(repo, "lodash", nil, time.Now())
	if err != nil || emptyPath != "" {
		t.Fatalf("expected empty rollback artifact result, got path=%q err=%v", emptyPath, err)
	}
}

func TestResolveCodemodFilePath(t *testing.T) {
	repo := t.TempDir()
	if _, err := resolveCodemodFilePath(repo, ""); err == nil {
		t.Fatal("expected empty path resolution error")
	}
	if _, err := resolveCodemodFilePath(repo, filepath.Join(repo, indexJSFile)); err == nil {
		t.Fatal("expected absolute path resolution error")
	}
	if _, err := resolveCodemodFilePath(repo, "../escape.js"); err == nil {
		t.Fatal("expected escaping path resolution error")
	}
	resolved, err := resolveCodemodFilePath(repo, filepath.Join("src", indexJSFile))
	if err != nil || resolved != filepath.Join(repo, "src", indexJSFile) {
		t.Fatalf("expected relative path resolution, got path=%q err=%v", resolved, err)
	}
}

func TestUniqueSortedStrings(t *testing.T) {
	values := uniqueSortedStrings([]string{"beta", "alpha", "beta", "", " alpha "})
	if !reflect.DeepEqual(values, []string{"alpha", "beta"}) {
		t.Fatalf("unexpected unique sorted values: %#v", values)
	}
	if len(uniqueSortedStrings(nil)) != 0 {
		t.Fatal("expected empty unique-sorted result for nil input")
	}
}

func TestSanitizeArtifactName(t *testing.T) {
	if got := sanitizeArtifactName(""); got != "codemod" {
		t.Fatalf("expected blank artifact name fallback, got %q", got)
	}
	if got := sanitizeArtifactName("///"); got != "codemod" {
		t.Fatalf("expected fallback artifact name, got %q", got)
	}
}

func TestSortCodemodApplyResults(t *testing.T) {
	results := []report.CodemodApplyResult{
		{File: "b.js", Status: codemodApplyStatusFailed, PatchCount: 1},
		{File: "a.js", Status: codemodApplyStatusSkipped, PatchCount: 2},
		{File: "a.js", Status: codemodApplyStatusApplied, PatchCount: 1},
		{File: "a.js", Status: codemodApplyStatusApplied, PatchCount: 3},
	}
	sortCodemodApplyResults(results)
	if results[0].Status != codemodApplyStatusApplied || results[1].Status != codemodApplyStatusApplied || results[2].Status != codemodApplyStatusSkipped {
		t.Fatalf("unexpected sort order: %#v", results)
	}
	if results[0].PatchCount != 1 || results[1].PatchCount != 3 {
		t.Fatalf("expected patch-count tie-break ordering, got %#v", results)
	}
}

func TestCodemodApplyError(t *testing.T) {
	err := codemodApplyError([]report.CodemodApplyResult{{File: "bad.js", Status: codemodApplyStatusFailed, Message: "boom"}})
	if !errors.Is(err, ErrCodemodApplyFailed) || !strings.Contains(err.Error(), "bad.js: boom") {
		t.Fatalf("expected wrapped codemod apply error, got %v", err)
	}
	if !errors.Is(codemodApplyError([]report.CodemodApplyResult{{File: "skip.js", Status: codemodApplyStatusSkipped}}), ErrCodemodApplyFailed) {
		t.Fatal("expected bare codemod apply error for skip-only results")
	}
	if !errors.Is(codemodApplyError(nil), ErrCodemodApplyFailed) {
		t.Fatal("expected bare codemod apply error")
	}
}

func TestWriteFileAtomically(t *testing.T) {
	repo := t.TempDir()
	targetPath := filepath.Join(repo, "atomic.txt")
	if err := writeFileAtomically(repo, targetPath, writtenContent, 0o640); err != nil {
		t.Fatalf("write file atomically: %v", err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat atomically written file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected atomically written file mode 0640, got %#o", info.Mode().Perm())
	}
}

func TestWriteFileAtomicallyMissingDirectory(t *testing.T) {
	repo := t.TempDir()
	err := writeFileAtomically(repo, filepath.Join(repo, "missing", "atomic.txt"), writtenContent, 0o640)
	if err == nil {
		t.Fatal("expected atomic write into missing directory to fail")
	}
}

func TestWriteFileAtomicallyRejectsDirectoryTarget(t *testing.T) {
	repo := t.TempDir()
	existingDir := filepath.Join(repo, "existing-dir")
	mustMkdirAll(t, existingDir)

	err := writeFileAtomically(repo, existingDir, writtenContent, 0o640)
	if err == nil {
		t.Fatal("expected atomic rename into existing directory target to fail")
	}
}

func TestWriteFileAtomicallyRejectsSymlinkEscape(t *testing.T) {
	repo := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(repo, "src")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	targetPath := filepath.Join(repo, "src", indexJSFile)
	err := writeFileAtomically(repo, targetPath, writtenContent, 0o640)
	if err == nil {
		t.Fatal("expected atomic write through escaping symlink to fail")
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, indexJSFile)); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside file to remain absent, got err=%v", statErr)
	}
}

func TestWriteCodemodRollbackArtifactBlockedPath(t *testing.T) {
	blockedRepo := t.TempDir()
	blockedPath := filepath.Join(blockedRepo, codemodRollbackDir)
	mustMkdirAll(t, filepath.Dir(blockedPath))
	writeTextFile(t, blockedPath, "blocked\n", 0o600)

	_, err := writeCodemodRollbackArtifact(blockedRepo, "lodash", []preparedCodemodFile{{file: indexJSFile, original: beforeContent, mode: 0o644}}, time.Now())
	if err == nil {
		t.Fatal("expected rollback artifact write to fail when backup path is blocked by a file")
	}
}

func TestWriteCodemodRollbackArtifactReadonlyDir(t *testing.T) {
	readonlyRepo := t.TempDir()
	readonlyDir := filepath.Join(readonlyRepo, codemodRollbackDir)
	mustMkdirAll(t, readonlyDir)
	t.Cleanup(func() {
		if err := os.Chmod(readonlyDir, 0o700); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore readonly rollback dir permissions: %v", err)
		}
	})
	if err := os.Chmod(readonlyDir, 0o500); err != nil {
		t.Fatalf("chmod readonly rollback dir: %v", err)
	}

	_, err := writeCodemodRollbackArtifact(readonlyRepo, "lodash", []preparedCodemodFile{{file: indexJSFile, original: beforeContent, mode: 0o644}}, time.Now())
	if err == nil {
		t.Fatal("expected rollback artifact write to fail when rollback dir is not writable")
	}
}

func TestApplyCodemodIfNeededNoOpWhenApplyDisabled(t *testing.T) {
	repo := t.TempDir()
	reportData := report.Report{Dependencies: []report.DependencyReport{{Name: "lodash"}}}

	updated, err := applyCodemodIfNeeded(context.Background(), reportData, repo, AnalyseRequest{Dependency: "lodash", ApplyCodemod: false, AllowDirty: true}, time.Now())
	if err != nil {
		t.Fatalf("expected applyCodemodIfNeeded no-op when apply mode is disabled, got %v", err)
	}
	if !reflect.DeepEqual(updated, reportData) {
		t.Fatalf("expected report to remain unchanged when apply mode is disabled, got %#v", updated)
	}
}

func TestApplyCodemodIfNeededNoOpWithoutCodemodReport(t *testing.T) {
	reportData := report.Report{Dependencies: []report.DependencyReport{{Name: "lodash"}}}

	updated, err := applyCodemodIfNeeded(context.Background(), reportData, t.TempDir(), AnalyseRequest{Dependency: "lodash", ApplyCodemod: true, AllowDirty: true}, time.Now())
	if err != nil {
		t.Fatalf("expected codemod apply no-op without codemod report, got %v", err)
	}
	if updated.Dependencies[0].Codemod != nil {
		t.Fatalf("expected no codemod apply summary when dependency has no codemod report, got %#v", updated.Dependencies[0].Codemod)
	}
}
