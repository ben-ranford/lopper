package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

var windowsTestCompilePackages = []string{
	"./internal/advisory",
	"./internal/baseline",
	"./internal/csvsanitize",
	"./internal/dashboard",
	"./internal/featureflags",
	"./internal/gitexec",
	"./internal/githubaction",
	"./internal/language",
	"./internal/notify",
	"./internal/prmetadata",
	"./internal/report",
	"./internal/report/model",
	"./internal/runtime",
	"./internal/safeio",
	"./internal/terminal",
	"./internal/testsupport",
	"./internal/testutil",
	"./internal/thresholds",
	"./internal/version",
	"./internal/workspace",
	"./tools/benchdelta",
	"./tools/coveragegate",
	"./tools/featureflag",
	"./tools/prcheck",
	"./tools/regressionproof",
}

// Packages that transitively depend on cgo/tree-sitter parsers are deliberately
// outside this CGO-disabled contract: cmd/lopper, internal/analysis,
// internal/app, internal/cli, internal/mcp, internal/ui, internal/lang/*, and
// scripts (whose JVM regression test imports internal/lang/jvm).
func TestWindowsCompatibleTestsCompile(t *testing.T) {
	for index, packagePath := range windowsTestCompilePackages {
		t.Run(packagePath, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "windows-"+strconv.Itoa(index)+".test.exe")
			cmd := exec.Command("go", "test", "-c", "-o", outputPath, packagePath)
			cmd.Dir = repoPath(t, "")
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64")

			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("cross-compile Windows tests: %v\n%s", err, output)
			}
		})
	}
}
