package scripts

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const gradleInferenceTestName = "TestVSCodeGradleInferenceBounds"

var gradleInferenceOutput []byte
var gradleInferenceAssertion error

// Setup runs before m.Run so regressionproof cannot mistake a missing runner or
// failed TypeScript compilation for a reproduced production defect.
func prepareGradleInferenceProof() (resultErr error) {
	selected, err := regexp.MatchString(strings.Split(flag.Lookup("test.run").Value.String(), "/")[0], gradleInferenceTestName)
	if err != nil || !selected || flag.Lookup("test.list").Value.String() != "" {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("find Gradle proof Node runner: %w", err)
	}
	extension, err := filepath.Abs("../extensions/vscode-lopper")
	if err != nil {
		return err
	}
	compiler, err := gradleProofCompiler(ctx, extension)
	if err != nil {
		return err
	}
	compiled, err := os.MkdirTemp("", "lopper-gradle-proof-")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(compiled)) }()
	cmd := exec.CommandContext(ctx, "node", compiler, "-p", extension, "--outDir", compiled)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("compile checked-out extension: %w\n%s", err, output)
	}
	cmd = exec.CommandContext(ctx, "node", "testdata/vscode-gradle-inference.cjs", filepath.Join(compiled, "languageConfiguration.js"))
	gradleInferenceOutput, gradleInferenceAssertion = cmd.CombinedOutput()
	var exitError *exec.ExitError
	if gradleInferenceAssertion != nil && (!errors.As(gradleInferenceAssertion, &exitError) || exitError.ExitCode() != 1 || !strings.Contains(string(gradleInferenceOutput), "LOPPER_GRADLE_ASSERTION")) {
		return fmt.Errorf("invoke Gradle proof fixture: %w\n%s", gradleInferenceAssertion, gradleInferenceOutput)
	}
	return nil
}

func gradleProofCompiler(ctx context.Context, extension string) (string, error) {
	compiler := filepath.Join(extension, "node_modules", "typescript", "bin", "tsc")
	if _, err := os.Stat(compiler); err == nil {
		return compiler, nil
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return "", fmt.Errorf("provision existing extension tooling: %w", err)
	}
	npm, err = filepath.EvalSymlinks(npm)
	if err != nil {
		return "", fmt.Errorf("resolve npm CLI: %w", err)
	}
	if strings.EqualFold(filepath.Ext(npm), ".cmd") {
		npm = filepath.Join(filepath.Dir(npm), "node_modules", "npm", "bin", "npm-cli.js")
	}
	cmd := exec.CommandContext(ctx, "node", npm, "ci", "--ignore-scripts", "--no-audit", "--no-fund")
	cmd.Dir = extension
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("provision existing extension tooling: %w\n%s", err, output)
	}
	return compiler, nil
}

// The fixture executes the checked-out production TypeScript on base and head.
// Only its explicit behavioral assertions can fail this named regression test.
func TestVSCodeGradleInferenceBounds(t *testing.T) {
	if gradleInferenceAssertion != nil {
		t.Fatalf("production Gradle inference behavior: %v\n%s", gradleInferenceAssertion, gradleInferenceOutput)
	}
	t.Log(string(gradleInferenceOutput))
}
