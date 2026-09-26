package lang_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/dotnet"
	"github.com/ben-ranford/lopper/internal/lang/golang"
	"github.com/ben-ranford/lopper/internal/lang/js"
	"github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/lang/kotlinandroid"
	"github.com/ben-ranford/lopper/internal/lang/python"
	"github.com/ben-ranford/lopper/internal/language"
)

type detectorTraversalCase struct {
	name   string
	detect func(context.Context, string) (language.Detection, error)
	source string
	skip   string
	cap    int
}

func TestDetectorTraversalContracts(t *testing.T) {
	cases := []detectorTraversalCase{
		{"go", golang.NewAdapter().DetectWithConfidence, "source.go", "vendor", 1024},
		{"python", python.NewAdapter().DetectWithConfidence, "source.py", ".venv", 512},
		{"dotnet", dotnet.NewAdapter().DetectWithConfidence, "source.cs", "obj", 1024},
		{"js", js.NewAdapter().DetectWithConfidence, "source.js", ".next", 256},
		{"jvm", jvm.NewAdapter().DetectWithConfidence, "source.java", "target", 0},
		{"android", kotlinandroid.NewAdapter().DetectWithConfidence, "AndroidManifest.xml", ".gradle", 1200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("normal", func(t *testing.T) { assertDetectorSource(t, tc, t.TempDir()) })
			t.Run("skipped directory", func(t *testing.T) { assertDetectorSkippedDirectory(t, tc) })
			t.Run("cancellation", func(t *testing.T) { assertDetectorCancellation(t, tc) })
			if tc.cap == 0 {
				return // JVM has separate bounded, confined-candidate tests.
			}
			t.Run("root named like skipped directory", func(t *testing.T) {
				assertDetectorSource(t, tc, filepath.Join(t.TempDir(), tc.skip))
			})
			t.Run("file cap", func(t *testing.T) { assertDetectorFileCap(t, tc) })
		})
	}
}

func assertDetectorSource(t *testing.T, tc detectorTraversalCase, repo string) {
	t.Helper()
	writeDetectionFile(t, filepath.Join(repo, tc.source))
	detection, err := tc.detect(context.Background(), repo)
	if err != nil || !detection.Matched || detection.Confidence != 35 || len(detection.Roots) != 1 || detection.Roots[0] != repo {
		t.Fatalf("detection = %#v, err = %v", detection, err)
	}
}

func assertDetectorSkippedDirectory(t *testing.T, tc detectorTraversalCase) {
	t.Helper()
	repo := t.TempDir()
	writeDetectionFile(t, filepath.Join(repo, tc.skip, tc.source))
	detection, err := tc.detect(context.Background(), repo)
	if err != nil || detection.Matched {
		t.Fatalf("detection = %#v, err = %v", detection, err)
	}
}

func assertDetectorCancellation(t *testing.T, tc detectorTraversalCase) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tc.detect(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func assertDetectorFileCap(t *testing.T, tc detectorTraversalCase) {
	t.Helper()
	repo := t.TempDir()
	for i := 0; i < tc.cap-1; i++ {
		writeDetectionFile(t, filepath.Join(repo, fmt.Sprintf("a%04d.txt", i)))
	}
	writeDetectionFile(t, filepath.Join(repo, "z", tc.source))
	detection, err := tc.detect(context.Background(), repo)
	if err != nil || !detection.Matched {
		t.Fatalf("at cap: %#v, %v", detection, err)
	}
	writeDetectionFile(t, filepath.Join(repo, "b.txt"))
	detection, err = tc.detect(context.Background(), repo)
	if err != nil || detection.Matched {
		t.Fatalf("beyond cap: %#v, %v", detection, err)
	}
}

func writeDetectionFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
}
