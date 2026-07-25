package scripts

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/jvm"
)

func TestJVMGradleSymlinkedInputIsNotConsumed(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "build.gradle")

	writeFile(t, outside, "dependencies { implementation 'org.junit.jupiter:junit-jupiter-api:5.10.0' }\n")
	if err := os.Symlink(outside, filepath.Join(repo, "build.gradle")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	detection, err := jvm.NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected escaping build.gradle symlink to be ignored, got %#v", detection)
	}
}
