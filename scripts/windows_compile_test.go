package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAdvisoryTestsCompileForWindows(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "advisory.test.exe")
	cmd := exec.Command("go", "test", "-c", "-o", outputPath, "./internal/advisory")
	cmd.Dir = repoPath(t, "")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64")

	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-compile advisory tests for Windows: %v\n%s", err, output)
	}
}
