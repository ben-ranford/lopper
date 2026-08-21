package cpp

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	regressionCPPSourceByteLimit        int64 = 2 * 1024 * 1024
	regressionCPPManifestByteLimit      int64 = 2 * 1024 * 1024
	regressionCPPLargeMetadataByteLimit int64 = 8 * 1024 * 1024
)

func TestLoadCompileContextSkipsOversizedCompileDatabase(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", testMainCPPFileName), fmtCoreIncludeLine)
	testutil.MustWritePaddedFile(t, filepath.Join(repo, compileCommandsFile), `[
  {"directory":".","file":"src/main.cpp","arguments":["c++","-I","include","-c","src/main.cpp"]}
]`, regressionCPPLargeMetadataByteLimit+1)

	compileInfo, err := loadCompileContext(repo)
	if err != nil {
		t.Fatalf("loadCompileContext: %v", err)
	}
	if len(compileInfo.SourceFiles) != 0 || len(compileInfo.IncludeDirs) != 0 {
		t.Fatalf("expected oversized compile database to be skipped, got sources=%#v includeDirs=%#v", compileInfo.SourceFiles, compileInfo.IncludeDirs)
	}
	if !hasWarning(compileInfo.Warnings, "compile_commands.json") || !hasWarning(compileInfo.Warnings, "larger than") || !hasWarning(compileInfo.Warnings, fmt.Sprintf("%d bytes", regressionCPPLargeMetadataByteLimit)) {
		t.Fatalf("expected oversized compile database warning, got %#v", compileInfo.Warnings)
	}
}

func TestAnalyseSkipsOversizedCPPSourceFile(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWritePaddedFile(t, filepath.Join(repo, "src", "large.cpp"), fmtCoreIncludeLine, regressionCPPSourceByteLimit+1)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "fmt"})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected requested dependency report, got %#v", reportData.Dependencies)
	}
	if reportData.Dependencies[0].UsedExportsCount != 0 || reportData.Dependencies[0].TotalExportsCount != 0 {
		t.Fatalf("expected oversized source to be skipped, got %#v", reportData.Dependencies[0])
	}
	if !hasWarning(reportData.Warnings, "skipped 1 large C/C++ source file") || !hasWarning(reportData.Warnings, fmt.Sprintf("%d bytes", regressionCPPSourceByteLimit)) {
		t.Fatalf("expected oversized source warning, got %#v", reportData.Warnings)
	}
}

func TestLoadDependencyManifestSkipsOversizedCPPManifestsAndLocks(t *testing.T) {
	cases := []struct {
		name    string
		content string
		limit   int64
	}{
		{name: vcpkgManifestFile, content: `{"dependencies":["fmt"]}`, limit: regressionCPPManifestByteLimit},
		{name: vcpkgLockFile, content: `{"dependencies":[{"name":"fmt"}]}`, limit: regressionCPPLargeMetadataByteLimit},
		{name: conanManifestFile, content: "[requires]\nfmt/10.2.1\n", limit: regressionCPPManifestByteLimit},
		{name: conanLockFile, content: `{"graph_lock":{"nodes":{"0":{"ref":"fmt/10.2.1"}}}}`, limit: regressionCPPLargeMetadataByteLimit},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, tc.name)
			testutil.MustWritePaddedFile(t, path, tc.content, tc.limit+1)
			catalog := newDependencyCatalog()

			warnings, err := loadDependencyManifest(repo, path, &catalog)
			if err != nil {
				t.Fatalf("loadDependencyManifest: %v", err)
			}
			if got := catalog.list(); len(got) != 0 {
				t.Fatalf("expected oversized %s to add no dependencies, got %#v", tc.name, got)
			}
			if !hasWarning(warnings, tc.name) || !hasWarning(warnings, "larger than") || !hasWarning(warnings, fmt.Sprintf("%d bytes", tc.limit)) {
				t.Fatalf("expected oversized %s warning, got %#v", tc.name, warnings)
			}
		})
	}
}
