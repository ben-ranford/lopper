package analysis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/cpp"
	"github.com/ben-ranford/lopper/internal/lang/elixir"
	"github.com/ben-ranford/lopper/internal/lang/powershell"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestAdaptersWarnWhenFileWalkIsTruncated(t *testing.T) {
	for _, tc := range []struct {
		name     string
		maxFiles int
		analyse  func(context.Context, language.Request) (report.Result, error)
	}{
		{name: "cpp", maxFiles: 4096, analyse: cpp.NewAdapter().Analyse},
		{name: "elixir", maxFiles: 2400, analyse: elixir.NewAdapter().Analyse},
		{name: "powershell", maxFiles: 8192, analyse: powershell.NewAdapter().Analyse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFileWalkTruncationFixture(t, repo, tc.maxFiles)
			assertFileWalkTruncationWarning(t, tc.analyse, repo, false)
			if err := os.WriteFile(filepath.Join(repo, "z-extra.txt"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			assertFileWalkTruncationWarning(t, tc.analyse, repo, true)
		})
	}
}

func writeFileWalkTruncationFixture(t *testing.T, repo string, maxFiles int) {
	t.Helper()
	for i := 0; i < maxFiles; i++ {
		if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("file-%05d.txt", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertFileWalkTruncationWarning(t *testing.T, analyse func(context.Context, language.Request) (report.Result, error), repo string, want bool) {
	t.Helper()
	result, err := analyse(context.Background(), language.Request{RepoPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	if got := hasFileWalkTruncationWarning(result.Warnings); got != want {
		t.Fatalf("partial file-limit warning = %t, want %t; warnings: %v", got, want, result.Warnings)
	}
}

func hasFileWalkTruncationWarning(warnings []string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, "file limit") && strings.Contains(warning, "partial") {
			return true
		}
	}
	return false
}
