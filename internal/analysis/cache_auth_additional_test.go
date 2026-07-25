package analysis

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestPathAtOrBelowWindowsReturnsNotApplicableForNonWindowsRoot(t *testing.T) {
	got, ok := pathAtOrBelowWindows(`C:\repo\cache`, "/tmp/repo")
	if ok {
		t.Fatal("expected non-Windows root to skip Windows-specific comparison")
	}
	if got {
		t.Fatal("expected non-applicable Windows comparison to return false")
	}
}

func TestPathAtOrBelowWindowsReturnsNotApplicableForPOSIXAbsolutePaths(t *testing.T) {
	got, ok := pathAtOrBelowWindows("/private/var/cache/child", "/private/var/cache")
	if ok {
		t.Fatal("expected POSIX absolute paths to fall through to host ancestry checks")
	}
	if got {
		t.Fatal("expected non-applicable POSIX comparison to return false")
	}
	if !pathAtOrBelow("/private/var/cache/child", "/private/var/cache") {
		t.Fatal("expected POSIX ancestry to be evaluated by filepath.Rel")
	}
}

func TestPathAtOrBelowWindowsFailsClosedForAmbiguousPath(t *testing.T) {
	got, ok := pathAtOrBelowWindows(`\\?\GLOBALROOT\Device\HarddiskVolume1\repo`, `C:\repo`)
	if !ok {
		t.Fatal("expected ambiguous Windows path to be classified")
	}
	if !got {
		t.Fatal("expected ambiguous Windows path to fail closed as protected")
	}
}

func TestPathAtOrBelowWindowsFailsClosedForReservedDOSName(t *testing.T) {
	got, ok := pathAtOrBelowWindows(`C:\repo\NUL.txt`, `C:\repo`)
	if !ok {
		t.Fatal("expected reserved DOS name path to be classified")
	}
	if !got {
		t.Fatal("expected reserved DOS name path to fail closed as protected")
	}
}

func TestPathAtOrBelowWindowsFailsClosedForTrimmedComponentAliases(t *testing.T) {
	tests := []struct {
		name string
		path string
		root string
		want bool
	}{
		{name: "candidate trailing dot leaf", path: `C:\outside.`, root: `C:\repo`, want: true},
		{name: "candidate trailing space leaf", path: `C:\outside `, root: `C:\repo`, want: true},
		{name: "candidate trailing dot nested", path: `C:\outside\nested.\cache`, root: `C:\repo`, want: true},
		{name: "candidate trailing space nested", path: `C:\outside\nested \cache`, root: `C:\repo`, want: true},
		{name: "root trailing dot leaf", path: `C:\outside`, root: `C:\repo.`, want: true},
		{name: "root trailing space leaf", path: `C:\outside`, root: `C:\repo `, want: true},
		{name: "root trailing dot nested", path: `C:\outside`, root: `C:\repo\nested.`, want: true},
		{name: "root trailing space nested", path: `C:\outside`, root: `C:\repo\nested `, want: true},
		{name: "canonical dotted candidate", path: `C:\outside.dir\cache`, root: `C:\repo`, want: false},
		{name: "canonical spaced candidate", path: `C:\outside dir\cache`, root: `C:\repo`, want: false},
		{name: "canonical dotted root", path: `C:\outside`, root: `C:\repo.dir`, want: false},
		{name: "canonical spaced root", path: `C:\outside`, root: `C:\repo dir\nested`, want: false},
		{name: "canonical descendant", path: `C:\repo.dir\nested space\cache`, root: `C:\repo.dir\nested space`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pathAtOrBelowWindows(tt.path, tt.root)
			if !ok {
				t.Fatal("expected Windows path comparison to apply")
			}
			if got != tt.want {
				t.Fatalf("pathAtOrBelowWindows(%q, %q) = %v, want %v", tt.path, tt.root, got, tt.want)
			}
		})
	}
}

func TestPathAtOrBelowWindowsReturnsNotApplicableForDriveRelativeRoot(t *testing.T) {
	got, ok := pathAtOrBelowWindows(`C:\repo\cache`, `C:repo`)
	if ok {
		t.Fatal("expected drive-relative root to skip Windows-specific comparison")
	}
	if got {
		t.Fatal("expected non-applicable drive-relative comparison to return false")
	}
}

func TestPathAtOrBelowWindowsRejectsKindMismatch(t *testing.T) {
	got, ok := pathAtOrBelowWindows(`\\server\share\repo\cache`, `C:\repo`)
	if !ok {
		t.Fatal("expected Windows path comparison to apply")
	}
	if got {
		t.Fatal("expected UNC path not to compare as below drive-root path")
	}
}

func TestPathAtOrBelowWindowsTreatsDriveRootAsAncestor(t *testing.T) {
	got, ok := pathAtOrBelowWindows(`C:\repo\cache`, `C:\`)
	if !ok {
		t.Fatal("expected Windows path comparison to apply")
	}
	if !got {
		t.Fatal("expected drive root to contain descendant path")
	}
}

func TestReadAnalysisCacheAuthKeyRepairsPermissiveModeWhenRequested(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)), 0o644); err != nil {
		t.Fatalf("write permissive key: %v", err)
	}

	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	key, err := readAnalysisCacheAuthKey(authRoot, keyName, true)
	if err != nil {
		t.Fatalf("readAnalysisCacheAuthKey(repair perms): %v", err)
	}
	if len(key) != analysisCacheAuthKeyLength {
		t.Fatalf("expected repaired auth key length %d, got %d", analysisCacheAuthKeyLength, len(key))
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat repaired key: %v", err)
	}
	if info.Mode().Perm() != analysisCacheAuthKeyPerm {
		t.Fatalf("expected repaired key perms %o, got %o", analysisCacheAuthKeyPerm, info.Mode().Perm())
	}
}

func TestCanonicalUserCacheDirReturnsCanonicalExistingDirectory(t *testing.T) {
	cacheDir := t.TempDir()
	resolved, err := canonicalUserCacheDir(cacheDir, false)
	if err != nil {
		t.Fatalf("canonicalUserCacheDir(existing): %v", err)
	}

	want, err := filepath.EvalSymlinks(cacheDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(existing cache dir): %v", err)
	}
	if resolved != want {
		t.Fatalf("canonicalUserCacheDir(existing) = %q, want %q", resolved, want)
	}
}

func TestCanonicalUserCacheDirCreatesPathBelowSymlinkedAncestor(t *testing.T) {
	targetParent := t.TempDir()
	aliasParent := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(targetParent, aliasParent); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	requested := filepath.Join(aliasParent, "child", "grandchild")
	resolved, err := canonicalUserCacheDir(requested, false)
	if err != nil {
		t.Fatalf("canonicalUserCacheDir(symlinked ancestor): %v", err)
	}

	want, err := filepath.EvalSymlinks(filepath.Join(targetParent, "child", "grandchild"))
	if err != nil {
		t.Fatalf("EvalSymlinks(created canonical path): %v", err)
	}
	if resolved != want {
		t.Fatalf("canonicalUserCacheDir(symlinked ancestor) = %q, want %q", resolved, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("stat created canonical path: %v", err)
	}
}

func TestCanonicalUserCacheDirReturnsErrorForSymlinkLoopAncestor(t *testing.T) {
	parent := t.TempDir()
	loop := filepath.Join(parent, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := canonicalUserCacheDir(filepath.Join(loop, "child"), false)
	if err == nil || !strings.Contains(err.Error(), "inspect user cache dir") {
		t.Fatalf("expected symlink-loop ancestor error, got %v", err)
	}
}

func TestCanonicalUserCacheDirReturnsMkdirAllErrorForBlockedDescendant(t *testing.T) {
	parent := t.TempDir()
	existing := filepath.Join(parent, "existing")
	if err := os.MkdirAll(existing, 0o750); err != nil {
		t.Fatalf("mkdir existing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "blocked"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	_, err := canonicalUserCacheDir(filepath.Join(existing, "blocked", "child"), false)
	if err == nil {
		t.Fatal("expected blocked descendant mkdir error")
	}
}

func TestInvalidAuthKeyGenerationReturnsStableHashForOversizedRegularFile(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("a", analysisCacheAuthKeyMaxBytes+1)), 0o600); err != nil {
		t.Fatalf("write oversized key: %v", err)
	}

	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	generation, err := invalidAuthKeyGeneration(authRoot, keyName)
	if err != nil {
		t.Fatalf("invalidAuthKeyGeneration(oversized): %v", err)
	}
	if generation == "" {
		t.Fatal("expected oversized invalid auth key to produce a generation hash")
	}
}

func TestInvalidAuthKeyGenerationReturnsStableHashForInvalidRegularFile(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("invalid-auth-key"), 0o600); err != nil {
		t.Fatalf("write invalid key: %v", err)
	}

	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	generation, err := invalidAuthKeyGeneration(authRoot, keyName)
	if err != nil {
		t.Fatalf("invalidAuthKeyGeneration(invalid): %v", err)
	}
	if generation == "" {
		t.Fatal("expected invalid auth key to produce a generation hash")
	}
}

func TestResolveAuthKeyReturnsColdCacheWhenReadonlyKeyChanges(t *testing.T) {
	prevReadAuthKey := analysisCacheReadAuthKeyFn
	analysisCacheReadAuthKeyFn = func(*safeio.WriteRoot, string, bool) ([]byte, error) {
		return nil, errAnalysisCacheAuthKeyChanged
	}
	t.Cleanup(func() {
		analysisCacheReadAuthKeyFn = prevReadAuthKey
	})

	setTestAnalysisCacheUserCacheDir(t)
	storageRoot := t.TempDir()
	cache := &analysisCache{
		options:     resolvedCacheOptions{ReadOnly: true},
		repoRoot:    t.TempDir(),
		storageRoot: storageRoot,
	}

	key, err := cache.resolveAuthKey()
	if err != nil {
		t.Fatalf("resolveAuthKey(readonly changed): %v", err)
	}
	if len(key) != 0 {
		t.Fatalf("expected readonly changed key to return cold cache, got %x", key)
	}
}

func TestResolveAuthKeyRechecksWritableKeyWhenInitialReadReportsChange(t *testing.T) {
	prevReadAuthKey := analysisCacheReadAuthKeyFn
	prevSleep := analysisCacheSleepFn
	calls := 0
	analysisCacheReadAuthKeyFn = func(*safeio.WriteRoot, string, bool) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errAnalysisCacheAuthKeyChanged
		}
		return []byte(strings.Repeat("a", analysisCacheAuthKeyLength)), nil
	}
	analysisCacheSleepFn = func(time.Duration) {}
	t.Cleanup(func() {
		analysisCacheReadAuthKeyFn = prevReadAuthKey
		analysisCacheSleepFn = prevSleep
	})

	setTestAnalysisCacheUserCacheDir(t)
	storageRoot := t.TempDir()
	cache := &analysisCache{
		options:     resolvedCacheOptions{},
		repoRoot:    t.TempDir(),
		storageRoot: storageRoot,
	}

	key, err := cache.resolveAuthKey()
	if err != nil {
		t.Fatalf("resolveAuthKey(writable changed): %v", err)
	}
	if len(key) != analysisCacheAuthKeyLength {
		t.Fatalf("expected writable changed key length %d, got %d", analysisCacheAuthKeyLength, len(key))
	}
	if calls < 2 {
		t.Fatalf("expected changed-key path to retry, got %d read calls", calls)
	}
}

func TestCreateOrRotateAuthKeyTimesOutWhenKeyKeepsChanging(t *testing.T) {
	prevReadAuthKey := analysisCacheReadAuthKeyFn
	prevSleep := analysisCacheSleepFn
	analysisCacheReadAuthKeyFn = func(*safeio.WriteRoot, string, bool) ([]byte, error) {
		return nil, errAnalysisCacheAuthKeyChanged
	}
	analysisCacheSleepFn = func(time.Duration) {}
	t.Cleanup(func() {
		analysisCacheReadAuthKeyFn = prevReadAuthKey
		analysisCacheSleepFn = prevSleep
	})

	authRoot, _ := openTempAuthRoot(t)
	if _, err := (&analysisCache{}).createOrRotateAuthKey(authRoot, "cache.key", true); err == nil || !strings.Contains(err.Error(), "timed out waiting for persisted winner") {
		t.Fatalf("expected changed-key retry timeout, got %v", err)
	}
}

func TestPublishMissingAuthKeyReturnsRandomReadError(t *testing.T) {
	prevRandRead := analysisCacheRandReadFn
	analysisCacheRandReadFn = func([]byte) (int, error) {
		return 0, errors.New("entropy unavailable")
	}
	t.Cleanup(func() {
		analysisCacheRandReadFn = prevRandRead
	})

	authRoot, _ := openTempAuthRoot(t)
	if err := publishMissingAuthKey(authRoot, "cache.key"); err == nil || !strings.Contains(err.Error(), "generate cache auth key") || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("expected random read failure, got %v", err)
	}
}

func TestRotateInvalidAuthKeyReturnsPublishErrorForMissingRotationCandidate(t *testing.T) {
	prevReadAuthKey := analysisCacheReadAuthKeyFn
	prevPublish := analysisCachePublishMissingAuthKeyFn
	prevGeneration := analysisCacheInvalidKeyGenerationFn
	analysisCacheReadAuthKeyFn = func(_ *safeio.WriteRoot, keyName string, _ bool) ([]byte, error) {
		if strings.Contains(keyName, analysisCacheAuthRotateTag) {
			return nil, errAnalysisCacheAuthKeyMissing
		}
		return nil, errAnalysisCacheAuthKeyInvalid
	}
	analysisCachePublishMissingAuthKeyFn = func(*safeio.WriteRoot, string) error {
		return errors.New("publish failed")
	}
	analysisCacheInvalidKeyGenerationFn = func(*safeio.WriteRoot, string) (string, error) {
		return "generation", nil
	}
	t.Cleanup(func() {
		analysisCacheReadAuthKeyFn = prevReadAuthKey
		analysisCachePublishMissingAuthKeyFn = prevPublish
		analysisCacheInvalidKeyGenerationFn = prevGeneration
	})

	authRoot, _ := openTempAuthRoot(t)
	if err := rotateInvalidAuthKey(authRoot, "cache.key"); err == nil || !strings.Contains(err.Error(), "publish failed") {
		t.Fatalf("expected rotation publish failure, got %v", err)
	}
}

func TestRotateInvalidAuthKeyReturnsChangedWhenGenerationChangesMidRotation(t *testing.T) {
	prevReadAuthKey := analysisCacheReadAuthKeyFn
	prevGeneration := analysisCacheInvalidKeyGenerationFn
	generationCalls := 0
	analysisCacheReadAuthKeyFn = func(_ *safeio.WriteRoot, keyName string, _ bool) ([]byte, error) {
		if strings.Contains(keyName, analysisCacheAuthRotateTag) {
			return []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)), nil
		}
		return nil, errAnalysisCacheAuthKeyInvalid
	}
	analysisCacheInvalidKeyGenerationFn = func(*safeio.WriteRoot, string) (string, error) {
		generationCalls++
		if generationCalls == 1 {
			return "generation-one", nil
		}
		return "generation-two", nil
	}
	t.Cleanup(func() {
		analysisCacheReadAuthKeyFn = prevReadAuthKey
		analysisCacheInvalidKeyGenerationFn = prevGeneration
	})

	authRoot, rootDir := openTempAuthRoot(t)
	if err := os.WriteFile(filepath.Join(rootDir, "cache.key"+analysisCacheAuthRotateTag+"generation-one"), []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)), 0o600); err != nil {
		t.Fatalf("write rotation candidate: %v", err)
	}
	if err := rotateInvalidAuthKey(authRoot, "cache.key"); !errors.Is(err, errAnalysisCacheAuthKeyChanged) {
		t.Fatalf("expected rotation generation mismatch to report changed key, got %v", err)
	}
}

func TestRotateInvalidAuthKeyReturnsReadErrorWhenRotationCandidateCannotBeRead(t *testing.T) {
	prevReadAuthKey := analysisCacheReadAuthKeyFn
	prevGeneration := analysisCacheInvalidKeyGenerationFn
	analysisCacheReadAuthKeyFn = func(*safeio.WriteRoot, string, bool) ([]byte, error) {
		return []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)), nil
	}
	analysisCacheInvalidKeyGenerationFn = func(*safeio.WriteRoot, string) (string, error) {
		return "generation", nil
	}
	t.Cleanup(func() {
		analysisCacheReadAuthKeyFn = prevReadAuthKey
		analysisCacheInvalidKeyGenerationFn = prevGeneration
	})

	authRoot, _ := openTempAuthRoot(t)
	if err := rotateInvalidAuthKey(authRoot, "cache.key"); err == nil || !strings.Contains(err.Error(), "read cache auth key rotation candidate") {
		t.Fatalf("expected rotation candidate read error, got %v", err)
	}
}

func TestRotateInvalidAuthKeyReturnsValidationErrorForInvalidRotationCandidate(t *testing.T) {
	prevReadAuthKey := analysisCacheReadAuthKeyFn
	prevGeneration := analysisCacheInvalidKeyGenerationFn
	analysisCacheReadAuthKeyFn = func(*safeio.WriteRoot, string, bool) ([]byte, error) {
		return []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)), nil
	}
	analysisCacheInvalidKeyGenerationFn = func(*safeio.WriteRoot, string) (string, error) {
		return "generation", nil
	}
	t.Cleanup(func() {
		analysisCacheReadAuthKeyFn = prevReadAuthKey
		analysisCacheInvalidKeyGenerationFn = prevGeneration
	})

	authRoot, rootDir := openTempAuthRoot(t)
	if err := os.WriteFile(filepath.Join(rootDir, "cache.key"+analysisCacheAuthRotateTag+"generation"), []byte("invalid"), 0o600); err != nil {
		t.Fatalf("write invalid rotation candidate: %v", err)
	}
	if err := rotateInvalidAuthKey(authRoot, "cache.key"); err == nil || !strings.Contains(err.Error(), "validate cache auth key rotation candidate") {
		t.Fatalf("expected invalid rotation candidate error, got %v", err)
	}
}

func TestRotateInvalidAuthKeyReturnsGenerationRecheckError(t *testing.T) {
	prevReadAuthKey := analysisCacheReadAuthKeyFn
	prevGeneration := analysisCacheInvalidKeyGenerationFn
	generationCalls := 0
	analysisCacheReadAuthKeyFn = func(*safeio.WriteRoot, string, bool) ([]byte, error) {
		return []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)), nil
	}
	analysisCacheInvalidKeyGenerationFn = func(*safeio.WriteRoot, string) (string, error) {
		generationCalls++
		if generationCalls == 1 {
			return "generation", nil
		}
		return "", errors.New("generation recheck failed")
	}
	t.Cleanup(func() {
		analysisCacheReadAuthKeyFn = prevReadAuthKey
		analysisCacheInvalidKeyGenerationFn = prevGeneration
	})

	authRoot, rootDir := openTempAuthRoot(t)
	if err := os.WriteFile(filepath.Join(rootDir, "cache.key"+analysisCacheAuthRotateTag+"generation"), []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)), 0o600); err != nil {
		t.Fatalf("write rotation candidate: %v", err)
	}
	if err := rotateInvalidAuthKey(authRoot, "cache.key"); err == nil || !strings.Contains(err.Error(), "generation recheck failed") {
		t.Fatalf("expected generation recheck failure, got %v", err)
	}
}

func TestRotateInvalidAuthKeyReturnsRenameErrorForRootTargetInstall(t *testing.T) {
	prevReadAuthKey := analysisCacheReadAuthKeyFn
	prevGeneration := analysisCacheInvalidKeyGenerationFn
	analysisCacheReadAuthKeyFn = func(*safeio.WriteRoot, string, bool) ([]byte, error) {
		return []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)), nil
	}
	analysisCacheInvalidKeyGenerationFn = func(*safeio.WriteRoot, string) (string, error) {
		return "generation", nil
	}
	t.Cleanup(func() {
		analysisCacheReadAuthKeyFn = prevReadAuthKey
		analysisCacheInvalidKeyGenerationFn = prevGeneration
	})

	authRoot, rootDir := openTempAuthRoot(t)
	if err := os.WriteFile(filepath.Join(rootDir, "."+analysisCacheAuthRotateTag+"generation"), []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)), 0o600); err != nil {
		t.Fatalf("write root-target rotation candidate: %v", err)
	}
	if err := rotateInvalidAuthKey(authRoot, "."); err == nil || !strings.Contains(err.Error(), "install rotated cache auth key") {
		t.Fatalf("expected root-target rename failure, got %v", err)
	}
}

func openTempAuthRoot(t *testing.T) (*safeio.WriteRoot, string) {
	t.Helper()
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(temp auth root): %v", err)
	}
	root, err := safeio.OpenCanonicalWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenCanonicalWriteRoot(%s): %v", rootDir, err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Fatalf("close temp auth root: %v", err)
		}
	})
	return root, rootDir
}
