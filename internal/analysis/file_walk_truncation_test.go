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
			for i := 0; i < tc.maxFiles; i++ {
				if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("file-%05d.txt", i)), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, extra := range []bool{false, true} {
				t.Run(fmt.Sprintf("extra-file-%t", extra), func(t *testing.T) {
					if extra {
						if err := os.WriteFile(filepath.Join(repo, "z-extra.txt"), nil, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					result, err := tc.analyse(context.Background(), language.Request{RepoPath: repo})
					if err != nil {
						t.Fatal(err)
					}
					warned := false
					for _, warning := range result.Warnings {
						if strings.Contains(warning, "file limit") && strings.Contains(warning, "partial") {
							warned = true
						}
					}
					if warned != extra {
						t.Fatalf("partial file-limit warning = %t, want %t; warnings: %v", warned, extra, result.Warnings)
					}
				})
			}
		})
	}
}
