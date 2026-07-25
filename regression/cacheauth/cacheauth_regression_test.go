package cacheauth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestDefaultRepoLocalCacheForgedEntryMisses(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "index.js"), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")

	cachePath := filepath.Join(repo, ".lopper-cache")
	testutil.MustWriteFile(t, filepath.Join(cachePath, "objects", "forged-object.json"), marshalCachedPayload(t, report.Report{
		Dependencies: []report.DependencyReport{{
			Name:              "forged-dep",
			UsedExportsCount:  99,
			TotalExportsCount: 100,
			UsedPercent:       99,
		}},
	}))
	testutil.MustWriteFile(t, filepath.Join(cachePath, "keys", cacheKeyDigest(t, repo)+".json"), marshalPointer(t, cachePointer{
		InputDigest:  cacheInputDigest(t, repo),
		ObjectDigest: "forged-object",
	}))

	svc := analysis.NewService()
	got, err := svc.Analyse(context.Background(), analysis.Request{
		RepoPath: repo,
		Language: "js",
		TopN:     1,
	})
	if err != nil {
		t.Fatalf("analyse repository with forged cache entry: %v", err)
	}
	if len(got.Dependencies) == 1 && got.Dependencies[0].Name == "forged-dep" {
		t.Fatalf("expected forged cache hit to be rejected, got %#v", got.Dependencies)
	}
	if got.Cache == nil || got.Cache.Hits != 0 || got.Cache.Misses != 1 || got.Cache.Writes != 1 {
		t.Fatalf("expected cache miss and rewrite after rejecting forged entry, got %#v", got.Cache)
	}
	if len(got.Cache.Invalidations) == 0 || got.Cache.Invalidations[0].Reason != "pointer-untrusted" {
		t.Fatalf("expected pointer-untrusted invalidation, got %#v", got.Cache.Invalidations)
	}
}

type cachePointer struct {
	InputDigest  string `json:"inputDigest"`
	ObjectDigest string `json:"objectDigest"`
}

func cacheKeyDigest(t *testing.T, repo string) string {
	t.Helper()
	key, err := hashJSON(map[string]any{
		"adapter":                   "js-ts",
		"configPath":                "",
		"dependency":                "",
		"includeRegistryProvenance": false,
		"language":                  "js",
		"root":                      filepath.Clean(repo),
		"runtimeProfile":            "",
		"schema":                    "v1",
		"suggestOnly":               false,
		"topN":                      1,
	})
	if err != nil {
		t.Fatalf("hash cache key: %v", err)
	}
	return key
}

func cacheInputDigest(t *testing.T, repo string) string {
	t.Helper()
	records := []string{
		digestRecord(t, "index.js", filepath.Join(repo, "index.js")),
		digestRecord(t, "package.json", filepath.Join(repo, "package.json")),
	}
	sort.Strings(records)
	sum := sha256.Sum256([]byte(records[0] + records[1]))
	return hex.EncodeToString(sum[:])
}

func digestRecord(t *testing.T, relativePath, absolutePath string) string {
	t.Helper()
	content, err := os.ReadFile(absolutePath)
	if err != nil {
		t.Fatalf("read %s: %v", absolutePath, err)
	}
	digest := sha256.Sum256(content)
	return relativePath + "\x00" + hex.EncodeToString(digest[:]) + "\n"
}

func marshalCachedPayload(t *testing.T, payload report.Report) string {
	t.Helper()
	data, err := json.Marshal(struct {
		Report report.Report `json:"report"`
	}{Report: payload})
	if err != nil {
		t.Fatalf("marshal cached payload: %v", err)
	}
	return string(data)
}

func marshalPointer(t *testing.T, pointer cachePointer) string {
	t.Helper()
	data, err := json.Marshal(pointer)
	if err != nil {
		t.Fatalf("marshal cache pointer: %v", err)
	}
	return string(data)
}

func hashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
