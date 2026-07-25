package analysis

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestOpenAuthStoreReturnsErrorWhenUserCacheDirIsEmpty(t *testing.T) {
	original := analysisCacheUserCacheDirFn
	analysisCacheUserCacheDirFn = func() (string, error) { return "   ", nil }
	t.Cleanup(func() {
		analysisCacheUserCacheDirFn = original
	})

	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "cache"), ExplicitPath: true},
		repoRoot:    t.TempDir(),
		storageRoot: filepath.Join(t.TempDir(), "cache"),
	}

	if _, _, err := cache.openAuthStore(); err == nil || !strings.Contains(err.Error(), "empty path") {
		t.Fatalf("expected empty user cache dir error, got %v", err)
	}
}

func TestOpenAuthStoreReturnsErrorWhenAuthParentInspectionFails(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	if err := os.WriteFile(filepath.Join(userCacheDir, "lopper"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write auth parent blocker: %v", err)
	}

	cachePath := filepath.Join(t.TempDir(), "cache")
	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: cachePath, ExplicitPath: true},
		repoRoot:    t.TempDir(),
		storageRoot: cachePath,
	}

	if _, _, err := cache.openAuthStore(); err == nil || !strings.Contains(err.Error(), "inspect cache auth store") {
		t.Fatalf("expected auth store inspection error, got %v", err)
	}
}

func TestOpenAuthStoreReturnsErrorWhenAuthParentSyncFails(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: cachePath, ExplicitPath: true},
		repoRoot:    t.TempDir(),
		storageRoot: cachePath,
	}

	original := analysisCacheAuthMkdirAllDurableFn
	syncErr := errors.New("sync parent failed")
	analysisCacheAuthMkdirAllDurableFn = func(root *safeio.WriteRoot, path string, perm os.FileMode) error {
		if err := root.MkdirAll(path, perm); err != nil {
			return err
		}
		return syncErr
	}
	t.Cleanup(func() {
		analysisCacheAuthMkdirAllDurableFn = original
	})

	_, _, err := cache.openAuthStore()
	if !errors.Is(err, syncErr) || !strings.Contains(err.Error(), "sync cache auth store parent after creation") {
		t.Fatalf("expected auth store parent sync error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(userCacheDir, "lopper", analysisCacheAuthDirName)); statErr != nil {
		t.Fatalf("expected auth store directory creation before sync failure, got %v", statErr)
	}
}

func TestOpenAuthStoreReturnsErrorWhenStorageRootIsMissing(t *testing.T) {
	setTestAnalysisCacheUserCacheDir(t)

	cache := &analysisCache{
		options:  resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "missing-cache"), ExplicitPath: true},
		repoRoot: t.TempDir(),
	}

	if _, _, err := cache.openAuthStore(); err == nil || !strings.Contains(err.Error(), "missing-cache") {
		t.Fatalf("expected missing storage root error, got %v", err)
	}
}

func TestResolveAuthKeyInReadonlyModeWarnsWhenAuthStoreLookupFails(t *testing.T) {
	original := analysisCacheUserCacheDirFn
	analysisCacheUserCacheDirFn = func() (string, error) { return "", errors.New("user-cache lookup failed") }
	t.Cleanup(func() {
		analysisCacheUserCacheDirFn = original
	})

	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "readonly-cache"), ReadOnly: true, ExplicitPath: true},
		repoRoot:    t.TempDir(),
		storageRoot: filepath.Join(t.TempDir(), "readonly-cache"),
	}

	key, err := cache.resolveAuthKey()
	if err != nil {
		t.Fatalf("resolveAuthKey(readonly lookup failure): %v", err)
	}
	if len(key) != 0 {
		t.Fatalf("expected cold-cache auth key, got %x", key)
	}
	warnings := strings.Join(cache.takeWarnings(), "\n")
	if !strings.Contains(warnings, "auth store unavailable") {
		t.Fatalf("expected readonly auth-store warning, got %q", warnings)
	}
}

func TestResolveAuthKeyInReadonlyModeWarnsOnUnreadableKeyTarget(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	storageRoot := filepath.Join(t.TempDir(), "readonly-cache")
	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, storageRoot)
	if err := os.MkdirAll(keyPath, 0o750); err != nil {
		t.Fatalf("mkdir key-path directory: %v", err)
	}

	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: storageRoot, ReadOnly: true, ExplicitPath: true},
		repoRoot:    t.TempDir(),
		storageRoot: storageRoot,
	}

	key, err := cache.resolveAuthKey()
	if err != nil {
		t.Fatalf("resolveAuthKey(readonly unreadable target): %v", err)
	}
	if len(key) != 0 {
		t.Fatalf("expected cold-cache auth key, got %x", key)
	}
}

func TestSignPointerReturnsErrorWhenReadonlyCacheHasNoAuthKey(t *testing.T) {
	setTestAnalysisCacheUserCacheDir(t)
	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "missing-cache"), ReadOnly: true, ExplicitPath: true},
		repoRoot:    t.TempDir(),
		storageRoot: filepath.Join(t.TempDir(), "missing-cache"),
	}

	_, err := cache.signPointer(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, "object")
	if err == nil || !strings.Contains(err.Error(), "cache auth key unavailable") {
		t.Fatalf("expected readonly missing-auth error, got %v", err)
	}
}

func TestCanonicalUserCacheDirReturnsErrorWhenPathInspectionFails(t *testing.T) {
	if _, err := canonicalUserCacheDir("\x00", false); err == nil || !strings.Contains(err.Error(), "inspect user cache dir") {
		t.Fatalf("expected user cache inspection error, got %v", err)
	}
}

func TestCanonicalUserCacheDirRejectsRepoControlledAuthStoreOnCreation(t *testing.T) {
	parent := t.TempDir()
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatalf("resolve canonical parent: %v", err)
	}
	missing := filepath.Join(parent, "missing-cache")

	if _, err := canonicalUserCacheDir(missing, false, canonicalParent); err == nil || !strings.Contains(err.Error(), "repository-controlled storage") {
		t.Fatalf("expected repo-controlled auth-store rejection, got %v", err)
	}
}

func TestPublishMissingAuthKeyReturnsErrorWhenWinnerPathCannotBeCreated(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	authRoot, _ := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)

	if err := publishMissingAuthKey(authRoot, filepath.Join("missing", "winner.key")); err == nil || !strings.Contains(err.Error(), "publish cache auth key winner") {
		t.Fatalf("expected winner publish path error, got %v", err)
	}
}

func TestPublishMissingAuthKeyReturnsErrorWhenDirectorySyncFails(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	authRoot, _ := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)

	original := analysisCacheAuthSyncDirFn
	syncErr := errors.New("sync key dir failed")
	analysisCacheAuthSyncDirFn = func(root *safeio.WriteRoot) error {
		return syncErr
	}
	t.Cleanup(func() {
		analysisCacheAuthSyncDirFn = original
	})

	if err := publishMissingAuthKey(authRoot, "winner.key"); !errors.Is(err, syncErr) {
		t.Fatalf("expected key directory sync error, got %v", err)
	}
}

func TestPublishMissingAuthKeyReturnsErrorWhenCandidateCreationFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based temp file failures are not portable on windows")
	}

	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	authRoot, _ := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	if err := os.Chmod(authDir, 0o500); err != nil {
		t.Fatalf("chmod auth dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(authDir, 0o750); err != nil {
			t.Fatalf("restore auth dir perms: %v", err)
		}
	})

	if err := publishMissingAuthKey(authRoot, "winner.key"); err == nil || !strings.Contains(err.Error(), "create cache auth key candidate") {
		t.Fatalf("expected candidate-creation failure, got %v", err)
	}
}

func TestWriteAuthKeyCandidateReturnsErrorWhenTempFileCannotBeCreated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based temp file failures are not portable on windows")
	}

	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	authRoot, _ := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	if err := os.Chmod(authDir, 0o500); err != nil {
		t.Fatalf("chmod auth dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(authDir, 0o750); err != nil {
			t.Fatalf("restore auth dir perms: %v", err)
		}
	})

	if _, err := writeAuthKeyCandidate(authRoot, []byte(strings.Repeat("ab", analysisCacheAuthKeyLength))); err == nil || !strings.Contains(err.Error(), "create cache auth key candidate") {
		t.Fatalf("expected temp-file creation failure, got %v", err)
	}
}

func TestRemoveAuthFileIfPresentReturnsErrorForNonEmptyDirectory(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(filepath.Join(authDir, "blocked"), 0o750); err != nil {
		t.Fatalf("mkdir blocked dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "blocked", "child.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocked child: %v", err)
	}
	authRoot, _ := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)

	if err := removeAuthFileIfPresent(authRoot, "blocked"); err == nil {
		t.Fatal("expected non-empty directory removal to fail")
	}
}

func TestRotateInvalidAuthKeyReturnsErrorWhenRotationTargetCannotBeRead(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)

	keyPath := filepath.Join(authDir, keyName)
	if err := os.WriteFile(keyPath, []byte("invalid"), 0o600); err != nil {
		t.Fatalf("write invalid key: %v", err)
	}
	generation, err := invalidAuthKeyGeneration(authRoot, keyName)
	if err != nil {
		t.Fatalf("invalidAuthKeyGeneration: %v", err)
	}
	rotationPath := filepath.Join(authDir, keyName+analysisCacheAuthRotateTag+generation)
	if err := os.MkdirAll(rotationPath, 0o750); err != nil {
		t.Fatalf("mkdir rotation path blocker: %v", err)
	}

	err = rotateInvalidAuthKey(authRoot, keyName)
	if err == nil || !strings.Contains(err.Error(), "read cache auth key rotation candidate") {
		t.Fatalf("expected rotation-candidate read error, got %v", err)
	}
}

func TestRotateInvalidAuthKeyReturnsGenerationErrorWhenKeyTargetIsDirectory(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	if err := os.Mkdir(filepath.Join(authDir, keyName), 0o750); err != nil {
		t.Fatalf("mkdir key target: %v", err)
	}

	if err := rotateInvalidAuthKey(authRoot, keyName); err == nil || !strings.Contains(err.Error(), "read invalid cache auth key") {
		t.Fatalf("expected invalid-generation error, got %v", err)
	}
}

func TestCreateOrRotateAuthKeyReturnsRotationFailureWhenReplacingInvalidKey(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	if err := os.WriteFile(filepath.Join(authDir, keyName), []byte("invalid"), 0o600); err != nil {
		t.Fatalf("write invalid key: %v", err)
	}
	generation, err := invalidAuthKeyGeneration(authRoot, keyName)
	if err != nil {
		t.Fatalf("invalidAuthKeyGeneration: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(authDir, keyName+analysisCacheAuthRotateTag+generation), 0o750); err != nil {
		t.Fatalf("mkdir rotation blocker: %v", err)
	}

	if _, err := (&analysisCache{}).createOrRotateAuthKey(authRoot, keyName, true); err == nil || !strings.Contains(err.Error(), "read cache auth key rotation candidate") {
		t.Fatalf("expected rotation failure during replacement, got %v", err)
	}
}

func TestInvalidAuthKeyGenerationReturnsErrorForDirectoryTarget(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)

	if err := os.Mkdir(filepath.Join(authDir, keyName), 0o750); err != nil {
		t.Fatalf("mkdir key target: %v", err)
	}

	if _, err := invalidAuthKeyGeneration(authRoot, keyName); err == nil || !strings.Contains(err.Error(), "read invalid cache auth key") {
		t.Fatalf("expected invalid-key generation read error, got %v", err)
	}
}

func TestLookupReturnsErrorWhenReadonlyStorageRootCannotBeStated(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write storage blocker: %v", err)
	}

	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: filepath.Join(blocker, "cache"), ReadOnly: true, ExplicitPath: true},
		cacheable:   true,
		storageRoot: filepath.Join(blocker, "cache"),
	}

	_, _, err := cache.lookup(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"})
	if err == nil {
		t.Fatal("expected readonly storage stat error")
	}
}

func TestLookupFailsClosedWhenPointerPathIsDirectory(t *testing.T) {
	cacheDir := t.TempDir()
	mustMkdirCacheLayout(t, cacheDir)
	entry := cacheEntryDescriptor{KeyLabel: "cache-key", KeyDigest: "key", InputDigest: "input"}
	if err := os.Mkdir(filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json"), 0o750); err != nil {
		t.Fatalf("mkdir pointer path: %v", err)
	}

	rootInfo, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	cache := &analysisCache{
		options:         resolvedCacheOptions{Enabled: true, Path: cacheDir, ExplicitPath: true},
		cacheable:       true,
		storageRoot:     cacheDir,
		storageRootInfo: rootInfo,
	}

	got, hit, err := cache.lookup(entry)
	if err != nil {
		t.Fatalf("lookup(pointer read error): %v", err)
	}
	if hit {
		t.Fatalf("expected miss for unreadable pointer path, got %#v", got)
	}
	if cache.metadata.Misses != 1 {
		t.Fatalf("expected miss count 1, got %#v", cache.metadata)
	}
	if len(cache.metadata.Invalidations) != 1 || cache.metadata.Invalidations[0].Reason != "pointer-read-error" {
		t.Fatalf("expected pointer-read-error invalidation, got %#v", cache.metadata.Invalidations)
	}
}

func TestLookupReportsObjectReadErrorWhenObjectPathIsDirectory(t *testing.T) {
	cacheDir := t.TempDir()
	mustMkdirCacheLayout(t, cacheDir)
	entry := cacheEntryDescriptor{KeyLabel: "cache-key", KeyDigest: "key", InputDigest: "input"}
	objectDigest := "object-digest"
	if err := os.Mkdir(filepath.Join(cacheDir, cacheObjectsDirName, objectDigest+".json"), 0o750); err != nil {
		t.Fatalf("mkdir object path: %v", err)
	}

	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: cacheDir, ExplicitPath: true},
		cacheable:   true,
		storageRoot: cacheDir,
		authKey:     []byte(strings.Repeat("a", analysisCacheAuthKeyLength)),
	}
	var err error
	cache.storageRootInfo, err = os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	signature, err := pointerSignature(cache.authKey, entry, objectDigest)
	if err != nil {
		t.Fatalf("pointerSignature: %v", err)
	}
	mustWritePointer(t, filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json"), cachePointer{
		InputDigest:  entry.InputDigest,
		ObjectDigest: objectDigest,
		Signature:    signature,
	})

	got, hit, err := cache.lookup(entry)
	if err != nil {
		t.Fatalf("lookup(object read error): %v", err)
	}
	if hit {
		t.Fatalf("expected miss for unreadable object path, got %#v", got)
	}
	if cache.metadata.Misses != 1 {
		t.Fatalf("expected miss count 1, got %#v", cache.metadata)
	}
	if len(cache.metadata.Invalidations) != 1 || cache.metadata.Invalidations[0].Reason != "object-read-error" {
		t.Fatalf("expected object-read-error invalidation, got %#v", cache.metadata.Invalidations)
	}
}

func TestLookupFailsClosedWhenPointerPathBecomesSymlink(t *testing.T) {
	cacheDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve cache dir: %v", err)
	}
	mustMkdirCacheLayout(t, cacheDir)
	entry := cacheEntryDescriptor{KeyLabel: "cache-key", KeyDigest: "key", InputDigest: "input"}
	outsideFile := filepath.Join(t.TempDir(), "outside-pointer.json")
	mustWriteFile(t, outsideFile, []byte(`{"inputDigest":"input","objectDigest":"forged","signature":"forged"}`))

	rootInfo, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	cache := &analysisCache{
		options:         resolvedCacheOptions{Enabled: true, Path: cacheDir, ExplicitPath: true},
		cacheable:       true,
		storageRoot:     cacheDir,
		storageRootInfo: rootInfo,
	}
	if err := os.Symlink(outsideFile, filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	got, hit, err := cache.lookup(entry)
	if err != nil {
		t.Fatalf("lookup(pointer symlink): %v", err)
	}
	if hit {
		t.Fatalf("expected miss for symlinked pointer path, got %#v", got)
	}
	if len(cache.metadata.Invalidations) != 1 || cache.metadata.Invalidations[0].Reason != "pointer-read-error" {
		t.Fatalf("expected pointer-read-error invalidation, got %#v", cache.metadata.Invalidations)
	}
}

func TestLookupFailsClosedWhenObjectPathBecomesSymlink(t *testing.T) {
	cacheDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve cache dir: %v", err)
	}
	mustMkdirCacheLayout(t, cacheDir)
	entry := cacheEntryDescriptor{KeyLabel: "cache-key", KeyDigest: "key", InputDigest: "input"}
	objectDigest := mustWriteCachedObject(t, cacheDir, report.Report{RepoPath: "repo"})
	outsideFile := filepath.Join(t.TempDir(), "outside-object.json")
	mustWriteFile(t, outsideFile, []byte(`{"report":{"repoPath":"outside"}}`))

	authKey := []byte(strings.Repeat("a", analysisCacheAuthKeyLength))
	signature, err := pointerSignature(authKey, entry, objectDigest)
	if err != nil {
		t.Fatalf("pointerSignature: %v", err)
	}
	mustWritePointer(t, filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json"), cachePointer{
		InputDigest:  entry.InputDigest,
		ObjectDigest: objectDigest,
		Signature:    signature,
	})
	objectPath := filepath.Join(cacheDir, cacheObjectsDirName, objectDigest+".json")
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("remove object path: %v", err)
	}
	if err := os.Symlink(outsideFile, objectPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	rootInfo, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	cache := &analysisCache{
		options:         resolvedCacheOptions{Enabled: true, Path: cacheDir, ExplicitPath: true},
		cacheable:       true,
		storageRoot:     cacheDir,
		storageRootInfo: rootInfo,
		authKey:         authKey,
	}

	got, hit, err := cache.lookup(entry)
	if err != nil {
		t.Fatalf("lookup(object symlink): %v", err)
	}
	if hit {
		t.Fatalf("expected miss for symlinked object path, got %#v", got)
	}
	if len(cache.metadata.Invalidations) != 1 || cache.metadata.Invalidations[0].Reason != "object-read-error" {
		t.Fatalf("expected object-read-error invalidation, got %#v", cache.metadata.Invalidations)
	}
}

func TestLookupFailsClosedWhenStorageRootChangesAfterInit(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	cacheDir := filepath.Join(parent, "cache")
	mustMkdirCacheLayout(t, cacheDir)
	entry := cacheEntryDescriptor{KeyLabel: "cache-key", KeyDigest: "key", InputDigest: "input"}
	mustWriteFile(t, filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json"), []byte(`{"inputDigest":"input"}`))

	rootInfo, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	cache := &analysisCache{
		options:         resolvedCacheOptions{Enabled: true, Path: cacheDir, ExplicitPath: true},
		cacheable:       true,
		storageRoot:     cacheDir,
		storageRootInfo: rootInfo,
	}

	renamed := filepath.Join(parent, "cache-old")
	if err := os.Rename(cacheDir, renamed); err != nil {
		t.Fatalf("rename cache dir: %v", err)
	}
	if err := os.Mkdir(cacheDir, 0o750); err != nil {
		t.Fatalf("recreate cache dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(cacheDir, cacheKeysDirName), 0o750); err != nil {
		t.Fatalf("mkdir replacement keys dir: %v", err)
	}
	mustWriteFile(t, filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json"), []byte(`{"inputDigest":"input"}`))

	got, hit, err := cache.lookup(entry)
	if err != nil {
		t.Fatalf("lookup(changed root): %v", err)
	}
	if hit {
		t.Fatalf("expected miss for replaced cache root, got %#v", got)
	}
	if len(cache.metadata.Invalidations) != 1 || cache.metadata.Invalidations[0].Reason != "pointer-read-error" {
		t.Fatalf("expected pointer-read-error invalidation, got %#v", cache.metadata.Invalidations)
	}
}

func TestPointerTrustedReturnsFalseWhenAuthKeyIsUnavailable(t *testing.T) {
	setTestAnalysisCacheUserCacheDir(t)
	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "missing-cache"), ReadOnly: true, ExplicitPath: true},
		repoRoot:    t.TempDir(),
		storageRoot: filepath.Join(t.TempDir(), "missing-cache"),
	}

	trusted, err := cache.pointerTrusted(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, cachePointer{
		ObjectDigest: "object",
		Signature:    "present",
	})
	if err != nil {
		t.Fatalf("pointerTrusted(unavailable key): %v", err)
	}
	if trusted {
		t.Fatal("expected missing auth key to leave pointer untrusted")
	}
}

func TestPointerTrustedReturnsErrorWhenAuthStoreLookupFails(t *testing.T) {
	original := analysisCacheUserCacheDirFn
	analysisCacheUserCacheDirFn = func() (string, error) { return "", errors.New("user-cache lookup failed") }
	t.Cleanup(func() {
		analysisCacheUserCacheDirFn = original
	})

	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "cache"), ExplicitPath: true},
		repoRoot:    t.TempDir(),
		storageRoot: filepath.Join(t.TempDir(), "cache"),
	}

	_, err := cache.pointerTrusted(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, cachePointer{
		ObjectDigest: "object",
		Signature:    "present",
	})
	if err == nil || !strings.Contains(err.Error(), "user-cache lookup failed") {
		t.Fatalf("expected pointerTrusted auth-store lookup error, got %v", err)
	}
}

func TestStoreReturnsErrorWhenCanonicalStorageRootIsMissing(t *testing.T) {
	setTestAnalysisCacheUserCacheDir(t)
	cache := &analysisCache{
		options:   resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "missing-cache"), ExplicitPath: true},
		cacheable: true,
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: "repo"})
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing canonical storage root error, got %v", err)
	}
}

func TestStoreReturnsErrorWhenStorageRootIsAFile(t *testing.T) {
	setTestAnalysisCacheUserCacheDir(t)
	storageRoot := filepath.Join(t.TempDir(), "cache-file")
	if err := os.WriteFile(storageRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("write storage root blocker: %v", err)
	}
	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: storageRoot, ExplicitPath: true},
		cacheable:   true,
		storageRoot: storageRoot,
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: "repo"})
	if err == nil {
		t.Fatal("expected write-root open failure for file-backed storage root")
	}
}

func TestInitializeStorageReturnsErrorWhenKeysDirectoryCannotBeCreated(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(cachePath, 0o750); err != nil {
		t.Fatalf("mkdir cache path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cachePath, cacheKeysDirName), []byte("x"), 0o600); err != nil {
		t.Fatalf("write keys blocker: %v", err)
	}

	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cachePath, ExplicitPath: true}}
	if err := cache.initializeStorage(repo); err == nil {
		t.Fatal("expected keys-directory creation failure")
	}
}

func TestResolveCacheStorageRootReturnsErrorWhenExplicitPathCannotBeCreated(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	repo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}

	options := resolvedCacheOptions{
		Path:         filepath.Join(blocker, "cache"),
		ExplicitPath: true,
	}
	if _, err := resolveCacheStorageRoot(options, repo, canonicalRepo); err == nil {
		t.Fatal("expected explicit cache-root creation failure")
	}
}

func TestResolveCacheStorageRootReturnsErrorWhenRepoRelativePathCannotBeCreated(t *testing.T) {
	repo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}
	blocker := filepath.Join(repo, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	options := resolvedCacheOptions{Path: filepath.Join(repo, "blocked", "cache")}
	if _, err := resolveCacheStorageRoot(options, repo, canonicalRepo); err == nil {
		t.Fatal("expected repo-relative cache-root creation failure")
	}
}

func TestResolveCacheStorageRootReturnsMissingExplicitPathInReadonlyMode(t *testing.T) {
	repo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}

	missingPath := filepath.Join(t.TempDir(), "missing-cache")
	options := resolvedCacheOptions{
		Path:         missingPath,
		ExplicitPath: true,
		ReadOnly:     true,
	}

	resolved, err := resolveCacheStorageRoot(options, repo, canonicalRepo)
	if err != nil {
		t.Fatalf("resolveCacheStorageRoot(readonly missing explicit path): %v", err)
	}
	if resolved != missingPath {
		t.Fatalf("expected missing explicit path to be returned unchanged, got %q", resolved)
	}
}

func TestCachePathEscapesRepoFailsClosedWhenRelativePathErrors(t *testing.T) {
	cachePath := t.TempDir()
	if !cachePathEscapesRepo(cachePath, "\x00") {
		t.Fatal("expected invalid repo path to fail closed")
	}
}

func TestCanonicalUserCacheDirReturnsErrorWhenAncestorInspectionFails(t *testing.T) {
	parent := filepath.Join("\x00", "missing")
	if _, err := canonicalUserCacheDir(filepath.Join(parent, "child"), false); err == nil || !strings.Contains(err.Error(), "inspect user cache dir") {
		t.Fatalf("expected ancestor inspection error, got %v", err)
	}
}

func TestCanonicalUserCacheDirReturnsErrorWhenParentSyncFails(t *testing.T) {
	original := analysisCacheAuthMkdirAllDurableFn
	analysisCacheAuthMkdirAllDurableFn = func(root *safeio.WriteRoot, path string, perm os.FileMode) error {
		if err := root.MkdirAll(path, perm); err != nil {
			return err
		}
		return errors.New("sync user-cache parent failed")
	}
	t.Cleanup(func() {
		analysisCacheAuthMkdirAllDurableFn = original
	})

	missing := filepath.Join(t.TempDir(), "nested", "cache")
	if _, err := canonicalUserCacheDir(missing, false); err == nil || !strings.Contains(err.Error(), "sync user cache parent after creation") {
		t.Fatalf("expected user-cache parent sync failure, got %v", err)
	}
}

func TestPathAtOrBelowReturnsTrueWhenRelativeComputationFailsForInvalidPath(t *testing.T) {
	if !pathAtOrBelow("\x00", t.TempDir()) {
		t.Fatal("expected invalid path to fail closed as under the protected root")
	}
}

func TestWriteFileDigestOrMissingReturnsWriterErrorForMissingSentinel(t *testing.T) {
	dir := t.TempDir()
	err := writeFileDigestOrMissing(io.Discard, filepath.Join(dir, "missing.txt"))
	if err != nil {
		t.Fatalf("expected io.Discard to accept missing sentinel, got %v", err)
	}
}

func TestReadAnalysisCacheAuthKeyReturnsReadErrorForDirectoryTarget(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}

	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	if err := os.Mkdir(filepath.Join(authDir, keyName), 0o750); err != nil {
		t.Fatalf("mkdir key path: %v", err)
	}

	if _, err := readAnalysisCacheAuthKey(authRoot, keyName, false); err == nil || !strings.Contains(err.Error(), "read cache auth key") {
		t.Fatalf("expected auth key read error for directory target, got %v", err)
	}
}

func TestStoreReturnsErrorWhenAuthStoreLookupFails(t *testing.T) {
	original := analysisCacheUserCacheDirFn
	analysisCacheUserCacheDirFn = func() (string, error) { return "", errors.New("user-cache lookup failed") }
	t.Cleanup(func() {
		analysisCacheUserCacheDirFn = original
	})

	cacheDir := t.TempDir()
	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: cacheDir, ExplicitPath: true},
		cacheable:   true,
		storageRoot: cacheDir,
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: "repo"})
	if err == nil || !strings.Contains(err.Error(), "user-cache lookup failed") {
		t.Fatalf("expected auth store lookup failure, got %v", err)
	}
}

func TestStoreReturnsErrorWhenCanonicalStorageRootDisappearsAfterSigning(t *testing.T) {
	setTestAnalysisCacheUserCacheDir(t)
	storageRoot := filepath.Join(t.TempDir(), "missing-cache")
	cache := &analysisCache{
		options:     resolvedCacheOptions{Enabled: true, Path: storageRoot, ExplicitPath: true},
		cacheable:   true,
		storageRoot: storageRoot,
		authKey:     bytes.Repeat([]byte{0x33}, analysisCacheAuthKeyLength),
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: "repo"})
	if err == nil || (!errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "open canonical root")) {
		t.Fatalf("expected canonical storage root failure after signing, got %v", err)
	}
}

func TestStoreReturnsErrorWhenStorageRootChangesAfterInit(t *testing.T) {
	setTestAnalysisCacheUserCacheDir(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	cacheDir := filepath.Join(parent, "cache")
	mustMkdirCacheLayout(t, cacheDir)
	rootInfo, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	cache := &analysisCache{
		options:         resolvedCacheOptions{Enabled: true, Path: cacheDir, ExplicitPath: true},
		cacheable:       true,
		storageRoot:     cacheDir,
		storageRootInfo: rootInfo,
		authKey:         bytes.Repeat([]byte{0x44}, analysisCacheAuthKeyLength),
	}

	renamed := filepath.Join(parent, "cache-old")
	if err := os.Rename(cacheDir, renamed); err != nil {
		t.Fatalf("rename cache dir: %v", err)
	}
	if err := os.Mkdir(cacheDir, 0o750); err != nil {
		t.Fatalf("recreate cache dir: %v", err)
	}
	mustMkdirCacheLayout(t, cacheDir)

	entry := cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}
	err = cache.store(entry, report.Report{RepoPath: "repo"})
	if !errors.Is(err, safeio.ErrFileChanged) {
		t.Fatalf("expected changed-root write failure, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(cacheDir, cacheObjectsDirName)); statErr != nil {
		t.Fatalf("stat replacement objects dir: %v", statErr)
	}
	entries, err := os.ReadDir(filepath.Join(cacheDir, cacheObjectsDirName))
	if err != nil {
		t.Fatalf("read replacement objects dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected replacement objects dir to stay empty, got %#v", entries)
	}
	entries, err = os.ReadDir(filepath.Join(renamed, cacheObjectsDirName))
	if err != nil {
		t.Fatalf("read original objects dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected original objects dir to stay empty, got %#v", entries)
	}
}

func TestRotateInvalidAuthKeyReturnsReadErrorWhenRotationCandidateIsInvalid(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}

	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	keyPath := filepath.Join(authDir, keyName)
	if err := os.WriteFile(keyPath, []byte("invalid-current-key"), 0o600); err != nil {
		t.Fatalf("write invalid current key: %v", err)
	}

	generation, err := invalidAuthKeyGeneration(authRoot, keyName)
	if err != nil {
		t.Fatalf("invalidAuthKeyGeneration: %v", err)
	}
	rotationPath := filepath.Join(authDir, keyName+analysisCacheAuthRotateTag+generation)
	if err := os.WriteFile(rotationPath, []byte("invalid-rotation-key"), 0o600); err != nil {
		t.Fatalf("write invalid rotation key: %v", err)
	}

	err = rotateInvalidAuthKey(authRoot, keyName)
	if err == nil || !strings.Contains(err.Error(), "read cache auth key rotation candidate") {
		t.Fatalf("expected invalid rotation candidate read error, got %v", err)
	}
}
