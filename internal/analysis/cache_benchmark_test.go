package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestAnalysisCacheComputeInputDigestMatchesLegacyEncoding(t *testing.T) {
	repo := t.TempDir()
	mustWriteBenchmarkFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"cache-bench\"\n}\n")
	mustWriteBenchmarkFile(t, filepath.Join(repo, "src", "b.ts"), "export const beta = 2;\n")
	mustWriteBenchmarkFile(t, filepath.Join(repo, "src", "a.ts"), "export const alpha = 1;\n")
	mustWriteBenchmarkFile(t, filepath.Join(repo, "src", "nested", "main.go"), "package nested\n\nconst Main = \"ok\"\n")
	mustWriteBenchmarkFile(t, filepath.Join(repo, ".git", "config"), "[core]\n\trepositoryformatversion = 0\n")
	configPath := filepath.Join(repo, ".lopper.yml")
	mustWriteBenchmarkFile(t, configPath, "thresholds: {}\n")

	cache := &analysisCache{}
	got, err := cache.computeInputDigest(repo, configPath)
	if err != nil {
		t.Fatalf("compute input digest: %v", err)
	}

	files, err := cache.collectRelevantFiles(repo)
	if err != nil {
		t.Fatalf("collect relevant files: %v", err)
	}
	want := legacyInputDigest(t, files, configPath)
	if got != want {
		t.Fatalf("expected digest %q, got %q", want, got)
	}
}

func BenchmarkCacheComputeInputDigest(b *testing.B) {
	repo := b.TempDir()
	configPath := filepath.Join(repo, ".lopper.yml")
	mustWriteBenchmarkFile(b, configPath, "thresholds:\n  low-confidence-warning-percent: 30\n")
	mustWriteBenchmarkFile(b, filepath.Join(repo, "go.mod"), "module example.com/cachebench\n")
	for i := 0; i < 96; i++ {
		dir := filepath.Join(repo, "src", "pkg"+strconv.Itoa(i%12))
		fileName := filepath.Join(dir, "file"+strconv.Itoa(i)+".ts")
		mustWriteBenchmarkFile(b, fileName, benchmarkDigestSource(i))
	}

	cache := &analysisCache{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := cache.computeInputDigest(repo, configPath); err != nil {
			b.Fatalf("compute input digest: %v", err)
		}
	}
}

func legacyInputDigest(t testing.TB, files []cacheRelevantFile, configPath string) string {
	t.Helper()

	records := make([]string, 0, len(files)+1)
	for _, file := range files {
		digest, err := hashFile(file.absolutePath)
		if err != nil {
			t.Fatalf("hash file %s: %v", file.absolutePath, err)
		}
		records = append(records, file.relativePath+"\x00"+digest)
	}
	if strings.TrimSpace(configPath) != "" {
		digest, err := hashFileOrMissing(strings.TrimSpace(configPath))
		if err != nil {
			t.Fatalf("hash config %s: %v", configPath, err)
		}
		records = append(records, "config\x00"+filepath.Clean(strings.TrimSpace(configPath))+"\x00"+digest)
	}

	sort.Strings(records)
	hasher := sha256.New()
	for _, record := range records {
		if _, err := io.WriteString(hasher, record); err != nil {
			t.Fatalf("write record: %v", err)
		}
		if _, err := io.WriteString(hasher, "\n"); err != nil {
			t.Fatalf("write record delimiter: %v", err)
		}
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func benchmarkDigestSource(index int) string {
	return strings.Repeat("import { thing"+strconv.Itoa(index%7)+" } from \"dep\"\n", 6) +
		"export function file" + strconv.Itoa(index) + "() {\n" +
		"  return thing" + strconv.Itoa(index%7) + ";\n" +
		"}\n"
}

func mustWriteBenchmarkFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
