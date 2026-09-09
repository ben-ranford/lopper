package dotnet

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

const streamingTestProgramSource = "Program.cs"

func assertDotNetScanDoesNotRetainSourceDocuments(t *testing.T) {
	t.Helper()
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, streamingTestProgramSource), "using Newtonsoft.Json;\n")

	inputs, err := discoverScanInputs(context.Background(), repo)
	if err != nil {
		t.Fatalf("discover scan inputs: %v", err)
	}
	if len(inputs.SourceFiles) != 0 {
		t.Fatalf("source discovery must not retain source documents, got %#v", inputs.SourceFiles)
	}

	scan, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(scan.Files) != 1 || scan.Files[0].Path != streamingTestProgramSource {
		t.Fatalf("expected source to be parsed during scan, got %#v", scan.Files)
	}
}
