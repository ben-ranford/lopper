package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fuzzCorpusManifest struct {
	Targets []fuzzCorpusTarget `json:"targets"`
}

type fuzzCorpusTarget struct {
	Name         string `json:"name"`
	Source       string `json:"source"`
	Corpus       string `json:"corpus"`
	MinSeedFiles int    `json:"min_seed_files"`
}

func TestParserFuzzCorpusContract(t *testing.T) {
	var manifest fuzzCorpusManifest
	readJSONConfig(t, "testdata/fuzz/manifest.json", &manifest)
	if len(manifest.Targets) == 0 {
		t.Fatal("fuzz corpus manifest must list at least one target")
	}

	for _, target := range manifest.Targets {
		if target.Name == "" || target.Source == "" || target.Corpus == "" {
			t.Fatalf("fuzz corpus manifest contains an incomplete target: %#v", target)
		}
		if target.MinSeedFiles <= 0 {
			t.Fatalf("fuzz corpus manifest target must require at least one seed: %#v", target)
		}

		sourceText := readConfig(t, target.Source)
		wantSignature := "func " + target.Name + "("
		if !strings.Contains(sourceText, wantSignature) {
			t.Fatalf("%s must declare %s", target.Source, wantSignature)
		}

		corpusDir := repoPath(t, target.Corpus)
		entries, err := os.ReadDir(corpusDir)
		if err != nil {
			t.Fatalf("read corpus directory %s: %v", target.Corpus, err)
		}

		seedCount := 0
		for _, entry := range entries {
			if entry.IsDir() {
				t.Fatalf("corpus directory %s must not contain nested directories: %s", target.Corpus, entry.Name())
			}
			seedCount++

			seedPath := filepath.Join(target.Corpus, entry.Name())
			seedText := readConfig(t, seedPath)
			if !strings.HasPrefix(seedText, "go test fuzz v1\n") {
				t.Fatalf("corpus seed %s must use the go fuzz corpus format", seedPath)
			}
		}

		if seedCount < target.MinSeedFiles {
			t.Fatalf("corpus directory %s contains %d seeds, want at least %d", target.Corpus, seedCount, target.MinSeedFiles)
		}
	}
}
