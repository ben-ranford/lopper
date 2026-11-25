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
