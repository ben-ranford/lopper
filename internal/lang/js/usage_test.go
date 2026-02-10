package js

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNamespaceUsageComputedProperty(t *testing.T) {
	repo := t.TempDir()
	source := "import * as util from \"lodash\"\nutil['map']([1], (x) => x)\n"
	path := filepath.Join(repo, "index.js")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	usage := result.Files[0].NamespaceUsage
	props, ok := usage["util"]
	if !ok {
		t.Fatalf("expected namespace usage for util")
	}
	if props["map"] == 0 {
		t.Fatalf("expected computed property map usage")
	}
}
