package python

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const testMainPy = "main.py"

func TestAdapterDetectWithPythonSource(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testMainPy), "import requests\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected python detection to match")
	}
	if detection.Confidence <= 0 {
		t.Fatalf("expected positive confidence, got %d", detection.Confidence)
	}
}

func TestAdapterAnalyseDependency(t *testing.T) {
	repo := t.TempDir()
	source := "import requests\nfrom numpy import array, mean\narray([1])\nrequests.get('x')\n"
	testutil.MustWriteFile(t, filepath.Join(repo, testMainPy), source)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "numpy",
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}

	dep := reportData.Dependencies[0]
	if dep.Language != "python" {
		t.Fatalf("expected language python, got %q", dep.Language)
	}
	if dep.UsedExportsCount != 1 || dep.TotalExportsCount != 2 {
		t.Fatalf("expected numpy used/total 1/2, got %d/%d", dep.UsedExportsCount, dep.TotalExportsCount)
	}
}

func TestAdapterAnalyseTopN(t *testing.T) {
	repo := t.TempDir()
	source := "import requests\nimport numpy as np\nnp.array([1])\n"
	testutil.MustWriteFile(t, filepath.Join(repo, testMainPy), source)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     2,
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Dependencies) != 2 {
		t.Fatalf("expected two dependencies, got %d", len(reportData.Dependencies))
	}
	names := []string{reportData.Dependencies[0].Name, reportData.Dependencies[1].Name}
	for _, dependency := range []string{"numpy", "requests"} {
		if !slices.Contains(names, dependency) {
			t.Fatalf("expected dependency %q in %#v", dependency, names)
		}
	}
}

func TestAdapterMetadataAndDetect(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "python" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	if len(adapter.Aliases()) == 0 || adapter.Aliases()[0] != "py" {
		t.Fatalf("unexpected adapter aliases: %#v", adapter.Aliases())
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "requirements.txt"), "requests\n")
	ok, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect=true with requirements.txt")
	}
}

func TestNormalizeDependencyID(t *testing.T) {
	if got := normalizeDependencyID(" My_Package "); got != "my-package" {
		t.Fatalf("unexpected normalized dependency ID: %q", got)
	}
}
