package cpp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestLoadCompileContextEnforcesCompileDatabaseSizeLimit(t *testing.T) {
	repo := t.TempDir()
	compileDB := filepath.Join(repo, compileCommandsFile)
	compileDBContent := `[
  {"directory":".","file":"src/main.cpp","arguments":["c++","-I","include","-c","src/main.cpp"]}
]`

	testutil.MustWritePaddedFile(t, compileDB, compileDBContent, maxCompileDatabaseBytes)
	compileInfo, err := loadCompileContext(repo)
	if err != nil {
		t.Fatalf("loadCompileContext at exact limit: %v", err)
	}
	if !compileInfo.HasCompileDatabase {
		t.Fatal("expected exact-limit compile database to be recorded")
	}
	if len(compileInfo.SourceFiles) != 1 || len(compileInfo.IncludeDirs) != 1 {
		t.Fatalf("expected exact-limit compile database to be parsed, got sources=%#v includeDirs=%#v warnings=%#v", compileInfo.SourceFiles, compileInfo.IncludeDirs, compileInfo.Warnings)
	}

	repo = t.TempDir()
	compileDB = filepath.Join(repo, compileCommandsFile)
	testutil.MustWritePaddedFile(t, compileDB, compileDBContent, maxCompileDatabaseBytes+1)

	compileInfo, err = loadCompileContext(repo)
	if err != nil {
		t.Fatalf("loadCompileContext over limit: %v", err)
	}
	if !compileInfo.HasCompileDatabase {
		t.Fatal("expected oversized compile database presence to be recorded")
	}
	if len(compileInfo.SourceFiles) != 0 || len(compileInfo.IncludeDirs) != 0 {
		t.Fatalf("expected oversized compile database to be skipped, got sources=%#v includeDirs=%#v", compileInfo.SourceFiles, compileInfo.IncludeDirs)
	}
	if !hasWarning(compileInfo.Warnings, compileCommandsFile) || !hasWarning(compileInfo.Warnings, "larger than") || !hasWarning(compileInfo.Warnings, fmt.Sprintf("%d bytes", maxCompileDatabaseBytes)) {
		t.Fatalf("expected oversized compile database warning, got %#v", compileInfo.Warnings)
	}
}

func TestAnalyseEnforcesCPPSourceSizeLimit(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "src", testMainCPPFileName)
	testutil.MustWritePaddedFile(t, sourcePath, fmtCoreIncludeLine, maxScannableCPPFile)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "fmt"})
	if err != nil {
		t.Fatalf("analyse at exact limit: %v", err)
	}
	if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].UsedExportsCount != 1 {
		t.Fatalf("expected exact-limit source to be scanned, got %#v", reportData.Dependencies)
	}

	repo = t.TempDir()
	sourcePath = filepath.Join(repo, "src", "large.cpp")
	testutil.MustWritePaddedFile(t, sourcePath, fmtCoreIncludeLine, maxScannableCPPFile+1)

	reportData, err = NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "fmt"})
	if err != nil {
		t.Fatalf("analyse over limit: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected requested dependency report, got %#v", reportData.Dependencies)
	}
	if reportData.Dependencies[0].UsedExportsCount != 0 || reportData.Dependencies[0].TotalExportsCount != 0 {
		t.Fatalf("expected oversized source to be skipped, got %#v", reportData.Dependencies[0])
	}
	warnings := strings.Join(reportData.Warnings, "\n")
	if !strings.Contains(warnings, "skipped 1 large C/C++ source file") || !strings.Contains(warnings, fmt.Sprintf("%d bytes", maxScannableCPPFile)) {
		t.Fatalf("expected oversized source warning, got %#v", reportData.Warnings)
	}
}
