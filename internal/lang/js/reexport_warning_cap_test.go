package js

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestReExportCycleWarningsAreNotRepeatedPerDependency(t *testing.T) {
	const dependencies = 20
	repo := t.TempDir()
	var imports []string
	imports = append(imports, `import { x } from "./a"`, `console.log(x)`)
	for i := 0; i < dependencies; i++ {
		name := fmt.Sprintf("dep%d", i)
		imports = append(imports, fmt.Sprintf("import %q", name))
		depDir := filepath.Join(repo, "node_modules", name)
		mustWriteFile(t, filepath.Join(depDir, "package.json"), testPackageJSONMain)
		mustWriteFile(t, filepath.Join(depDir, "index.js"), "export function value() {}\n")
	}
	mustWriteFile(t, filepath.Join(repo, "src", "index.ts"), strings.Join(imports, "\n")+"\n")
	mustWriteFile(t, filepath.Join(repo, "src", "a.ts"), `export { x } from "./b"`+"\n")
	mustWriteFile(t, filepath.Join(repo, "src", "b.ts"), `export { x } from "./a"`+"\n")

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     1,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	joined := strings.Join(reportData.Warnings, "\n")
	if got := strings.Count(joined, "re-export attribution cycle"); got != 1 {
		t.Fatalf("expected one cycle warning, got %d in %#v", got, reportData.Warnings)
	}
}

func TestAppendUniqueDependencyWarningsCapsAndSkips(t *testing.T) {
	seen := map[string]struct{}{"already": {}}
	dst := appendUniqueDependencyWarnings([]string{"already"}, seen, []string{"", "already", "fresh"})
	if len(dst) != 2 || dst[1] != "fresh" {
		t.Fatalf("expected empty and duplicate warnings to be skipped, got %#v", dst)
	}

	full := make([]string, maxAggregatedDependencyWarnings)
	for i := range full {
		full[i] = fmt.Sprintf("w%d", i)
	}
	capped := appendUniqueDependencyWarnings(full, map[string]struct{}{}, []string{"overflow", "another"})
	if len(capped) != maxAggregatedDependencyWarnings+1 || capped[len(capped)-1] != aggregatedWarningCapNotice {
		t.Fatalf("expected one cap notice, got %#v", capped[len(capped)-1:])
	}
	again := appendUniqueDependencyWarnings(capped, map[string]struct{}{aggregatedWarningCapNotice: {}}, []string{"still-more"})
	if len(again) != len(capped) {
		t.Fatalf("expected a second overflow to keep the existing cap notice, got %d warnings", len(again))
	}
}
