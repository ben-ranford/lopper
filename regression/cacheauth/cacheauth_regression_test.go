package cacheauth_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

type countingJSAdapter struct {
	calls int
}

func (*countingJSAdapter) ID() string        { return "js-ts" }
func (*countingJSAdapter) Aliases() []string { return []string{"js"} }
func (*countingJSAdapter) Detect(context.Context, string) (bool, error) {
	return true, nil
}
func (a *countingJSAdapter) Analyse(context.Context, language.Request) (report.Report, error) {
	a.calls++
	return report.Report{
		Dependencies: []report.DependencyReport{{
			Name:              "live-dep",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			UsedPercent:       50,
		}},
	}, nil
}

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

func TestDefaultRepoLocalCacheTrustedEntryHitsOnPOSIXPaths(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "index.js"), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")

	svc := analysis.NewService()
	first, err := svc.Analyse(context.Background(), analysis.Request{
		RepoPath: repo,
		Language: "js",
		TopN:     1,
	})
	if err != nil {
		t.Fatalf("first analyse repository with default cache: %v", err)
	}
	if first.Cache == nil || first.Cache.Hits != 0 || first.Cache.Misses != 1 || first.Cache.Writes != 1 {
		t.Fatalf("expected first run to populate cache, got %#v", first.Cache)
	}

	second, err := svc.Analyse(context.Background(), analysis.Request{
		RepoPath: repo,
		Language: "js",
		TopN:     1,
	})
	if err != nil {
		t.Fatalf("second analyse repository with default cache: %v", err)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 || second.Cache.Writes != 0 {
		t.Fatalf("expected trusted default cache hit on POSIX paths, got %#v", second.Cache)
	}
}

func TestOversizedPermissiveAuthKeyRotatesToPrivateValidKey(t *testing.T) {
	userCacheDir, _, svc, req := newIsolatedAnalysisCacheEnv(t)
	if _, err := svc.Analyse(context.Background(), req); err != nil {
		t.Fatalf("seed cache auth key: %v", err)
	}

	authDir := filepath.Join(userCacheDir, "lopper", "analysis-cache-auth")
	keyPath := singleAuthKeyPath(t, authDir)
	oversizedKey := []byte(strings.Repeat("f", 256))
	if err := os.WriteFile(keyPath, oversizedKey, 0o644); err != nil {
		t.Fatalf("seed oversized auth key: %v", err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("set oversized auth key mode: %v", err)
	}

	if _, err := svc.Analyse(context.Background(), req); err != nil {
		t.Fatalf("analyse repository with oversized auth key: %v", err)
	}

	encodedKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read rotated auth key: %v", err)
	}
	if len(encodedKey) != sha256.Size*2 {
		t.Fatalf("expected %d-byte encoded auth key, got %d bytes", sha256.Size*2, len(encodedKey))
	}
	decodedKey, err := hex.DecodeString(string(encodedKey))
	if err != nil {
		t.Fatalf("decode rotated auth key: %v", err)
	}
	if len(decodedKey) != sha256.Size {
		t.Fatalf("expected %d-byte auth key, got %d bytes", sha256.Size, len(decodedKey))
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat rotated auth key: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected rotated auth key mode 0600, got %#o", info.Mode().Perm())
	}
}

func TestPermissiveValidAuthKeyRotatesAndRejectsOldSignatures(t *testing.T) {
	userCacheDir, repo, _, req := newIsolatedAnalysisCacheEnv(t)
	adapter := &countingJSAdapter{}
	registry := language.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register counting adapter: %v", err)
	}
	svc := &analysis.Service{Registry: registry}
	first, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("seed cache auth key: %v", err)
	}
	assertSeededLiveRun(t, first, adapter.calls)

	authDir := filepath.Join(userCacheDir, "lopper", "analysis-cache-auth")
	keyPath := singleAuthKeyPath(t, authDir)
	originalEncodedKey, originalKey := readEncodedAuthKey(t, keyPath)
	forgeSignedCachePointer(t, repo, originalKey)
	setPermissiveKeyMode(t, keyPath)

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse repository with permissive auth key: %v", err)
	}
	assertForgedPointerRejection(t, second, adapter.calls)
	assertRotatedKeyReplaced(t, keyPath, originalEncodedKey)
}

func newIsolatedAnalysisCacheEnv(t *testing.T) (string, string, *analysis.Service, analysis.Request) {
	t.Helper()
	cacheEnvRoot := t.TempDir()
	t.Setenv("HOME", cacheEnvRoot)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(cacheEnvRoot, "xdg-cache"))
	t.Setenv("LocalAppData", filepath.Join(cacheEnvRoot, "local-app-data"))

	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("resolve isolated user cache dir: %v", err)
	}
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "index.js"), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	return userCacheDir, repo, analysis.NewService(), analysis.Request{
		RepoPath: repo,
		Language: "js",
		TopN:     1,
	}
}

func assertSeededLiveRun(t *testing.T, got report.Report, adapterCalls int) {
	t.Helper()
	if got.Cache == nil || got.Cache.Writes != 1 {
		t.Fatalf("expected first run to populate cache, got %#v", got.Cache)
	}
	if adapterCalls != 1 {
		t.Fatalf("expected seed run to execute adapter once, got %d calls", adapterCalls)
	}
}

func readEncodedAuthKey(t *testing.T, keyPath string) ([]byte, []byte) {
	t.Helper()
	encoded, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read initial auth key: %v", err)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatalf("decode initial auth key: %v", err)
	}
	return encoded, decoded
}

func forgeSignedCachePointer(t *testing.T, repo string, originalKey []byte) {
	t.Helper()
	cachePath := filepath.Join(repo, ".lopper-cache")
	forgedPayload := marshalCachedPayload(t, report.Report{
		Dependencies: []report.DependencyReport{{
			Name:              "forged-dep",
			UsedExportsCount:  99,
			TotalExportsCount: 100,
			UsedPercent:       99,
		}},
	})
	objectSum := sha256.Sum256([]byte(forgedPayload))
	objectDigest := hex.EncodeToString(objectSum[:])
	testutil.MustWriteFile(t, filepath.Join(cachePath, "objects", objectDigest+".json"), forgedPayload)
	keyDigest := cacheKeyDigest(t, repo)
	inputDigest := cacheInputDigest(t, repo)
	signature := cachePointerSignature(t, originalKey, keyDigest, inputDigest, objectDigest)
	testutil.MustWriteFile(t, filepath.Join(cachePath, "keys", keyDigest+".json"), marshalPointer(t, cachePointer{
		InputDigest:  inputDigest,
		ObjectDigest: objectDigest,
		Signature:    signature,
	}))
}

func setPermissiveKeyMode(t *testing.T, keyPath string) {
	t.Helper()
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("set permissive auth key mode: %v", err)
	}
}

func assertForgedPointerRejection(t *testing.T, got report.Report, adapterCalls int) {
	t.Helper()
	if len(got.Dependencies) == 1 && got.Dependencies[0].Name == "forged-dep" {
		t.Fatalf("expected permissive-key rotation to reject old signed pointer, got %#v", got.Dependencies)
	}
	if got.Cache == nil || got.Cache.Hits != 0 || got.Cache.Misses != 1 || got.Cache.Writes != 1 {
		t.Fatalf("expected pointer rejection to execute the adapter and rewrite the cache, got %#v", got.Cache)
	}
	if len(got.Cache.Invalidations) == 0 || got.Cache.Invalidations[0].Reason != "pointer-untrusted" {
		t.Fatalf("expected pointer-untrusted invalidation, got %#v", got.Cache.Invalidations)
	}
	if adapterCalls != 2 {
		t.Fatalf("expected pointer rejection to execute adapter again, got %d calls", adapterCalls)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0].Name != "live-dep" {
		t.Fatalf("expected live adapter result after rejecting old signature, got %#v", got.Dependencies)
	}
}

func assertRotatedKeyReplaced(t *testing.T, keyPath string, originalEncodedKey []byte) {
	t.Helper()
	rotatedEncodedKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read rotated auth key: %v", err)
	}
	if strings.TrimSpace(string(rotatedEncodedKey)) == strings.TrimSpace(string(originalEncodedKey)) {
		t.Fatal("expected permissive key rotation to replace the compromised key")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat rotated auth key: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected rotated auth key mode 0600, got %#o", info.Mode().Perm())
	}
}

func singleAuthKeyPath(t *testing.T, authDir string) string {
	t.Helper()
	authEntries, err := os.ReadDir(authDir)
	if err != nil {
		t.Fatalf("read auth store: %v", err)
	}
	var keyPath string
	for _, entry := range authEntries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".key") {
			if keyPath != "" {
				t.Fatalf("expected one cache auth key, found at least %q and %q", filepath.Base(keyPath), entry.Name())
			}
			keyPath = filepath.Join(authDir, entry.Name())
		}
	}
	if keyPath == "" {
		t.Fatal("expected cache warmup to create an auth key")
	}
	return keyPath
}

type cachePointer struct {
	InputDigest  string `json:"inputDigest"`
	ObjectDigest string `json:"objectDigest"`
	Signature    string `json:"signature,omitempty"`
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

func cachePointerSignature(t *testing.T, key []byte, keyDigest, inputDigest, objectDigest string) string {
	t.Helper()
	mac := hmac.New(sha256.New, key)
	for _, part := range []string{"v1", keyDigest, inputDigest, objectDigest} {
		if _, err := mac.Write([]byte(part)); err != nil {
			t.Fatalf("write pointer signature part: %v", err)
		}
		if _, err := mac.Write([]byte{0}); err != nil {
			t.Fatalf("write pointer signature separator: %v", err)
		}
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func hashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
