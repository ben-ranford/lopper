package testutil

import (
	"path/filepath"
	"testing"
)

func TestLoadByteFuzzCorpus(t *testing.T) {
	dir := t.TempDir()
	MustWriteFile(t, filepath.Join(dir, "b"), "go test fuzz v1\n[]byte(`second\\nseed`)\n")
	MustWriteFile(t, filepath.Join(dir, "a"), "go test fuzz v1\n[]byte(\"first seed\")\n")

	seeds := LoadByteFuzzCorpus(t, dir)
	if len(seeds) != 2 {
		t.Fatalf("expected two seeds, got %#v", seeds)
	}
	if seeds[0].Name != "a" || string(seeds[0].Data) != "first seed" {
		t.Fatalf("unexpected first seed %#v", seeds[0])
	}
	if seeds[1].Name != "b" || string(seeds[1].Data) != "second\\nseed" {
		t.Fatalf("unexpected second seed %#v", seeds[1])
	}
}
