package scripts

import (
	"os/exec"
	"strings"
	"testing"
)

func TestGostyleRegressionRunnerOffline(t *testing.T) {
	t.Parallel()
	command := exec.Command("python3", "-B", repoPath(t, "scripts/check_gostyle_regression_test.py"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("offline gostyle runner contracts failed: %v\n%s", err, output)
	}
}

func TestGostyleMakeTargetIncludesRegressionGate(t *testing.T) {
	t.Parallel()
	command := exec.Command("make", "-n", "gostyle")
	command.Dir = repoPath(t, ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect gostyle recipe: %v\n%s", err, output)
	}
	for _, argument := range []string{"scripts/check_gostyle_regression.py", "--go", "--toolchain", "--version", "--config .gostyle.yml", "--root ."} {
		if !strings.Contains(string(output), argument) {
			t.Fatalf("gostyle target omitted required regression gate argument %q: %s", argument, output)
		}
	}
}
