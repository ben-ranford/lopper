package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

const (
	aliasConflictReason    = "alias-conflict"
	beforeContent          = "before\n"
	codemodSuggestOnlyMode = "suggest-only"
	importLodashLine       = "import { map } from \"lodash\";"
	importLodashLineWithLF = importLodashLine + "\n"
	importLodashMapLine    = "import map from \"lodash/map\";"
	indexJSFile            = "index.js"
	lodashMapModule        = "lodash/map"
	srcAJSFile             = "src/a.js"
	writtenContent         = "written\n"
)

type codemodApplyCounts struct {
	AppliedFiles   int
	AppliedPatches int
	SkippedFiles   int
	SkippedPatches int
	FailedFiles    int
	FailedPatches  int
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeTextFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readTextFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	if got := readTextFile(t, path); !strings.Contains(got, want) {
		t.Fatalf("expected %s to contain %q, got %q", path, want, got)
	}
}

func requireCodemodApplyReport(t *testing.T, reportData report.Report) *report.CodemodApplyReport {
	t.Helper()
	if len(reportData.Dependencies) == 0 || reportData.Dependencies[0].Codemod == nil || reportData.Dependencies[0].Codemod.Apply == nil {
		t.Fatalf("expected codemod apply summary, got %#v", reportData)
	}
	return reportData.Dependencies[0].Codemod.Apply
}

func assertCodemodApplyCounts(t *testing.T, applyReport *report.CodemodApplyReport, want codemodApplyCounts) {
	t.Helper()
	if applyReport.AppliedFiles != want.AppliedFiles || applyReport.AppliedPatches != want.AppliedPatches {
		t.Fatalf("unexpected applied summary: %#v", applyReport)
	}
	if applyReport.SkippedFiles != want.SkippedFiles || applyReport.SkippedPatches != want.SkippedPatches {
		t.Fatalf("unexpected skipped summary: %#v", applyReport)
	}
	if applyReport.FailedFiles != want.FailedFiles || applyReport.FailedPatches != want.FailedPatches {
		t.Fatalf("unexpected failed summary: %#v", applyReport)
	}
}

func assertRollbackArtifact(t *testing.T, repo, backupPath, dependency, original string) {
	t.Helper()
	if backupPath == "" {
		t.Fatal("expected backup path in apply summary")
	}

	backupData, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(backupPath)))
	if err != nil {
		t.Fatalf("read backup artifact: %v", err)
	}
	var artifact struct {
		Dependency string `json:"dependency"`
		Files      []struct {
			File    string `json:"file"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if err := json.Unmarshal(backupData, &artifact); err != nil {
		t.Fatalf("decode backup artifact: %v", err)
	}
	if artifact.Dependency != dependency || len(artifact.Files) != 1 {
		t.Fatalf("unexpected backup artifact payload: %#v", artifact)
	}
	if artifact.Files[0].Content != original {
		t.Fatalf("expected original file content in rollback artifact, got %q", artifact.Files[0].Content)
	}
}
