package analysis

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/windowspath"
)

func writePrivateCacheAuthKey(t *testing.T, root *safeio.WriteRoot, keyName string, encoded []byte) fs.FileInfo {
	t.Helper()
	if err := root.WritePrivateFileReplacingAtomically(keyName, encoded); err != nil {
		t.Fatalf("write private cache auth key: %v", err)
	}
	got, info, err := root.ReadRegularFileUnderLimit(keyName, analysisCacheAuthKeyMaxBytes)
	if err != nil {
		t.Fatalf("read private cache auth key: %v", err)
	}
	if !bytes.Equal(got, encoded) {
		t.Fatalf("private cache auth key = %q, want %q", got, encoded)
	}
	return info
}

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

func TestPathAtOrBelowFailsClosedWhenRelativeCheckErrors(t *testing.T) {
	prevRel := analysisCachePathRelFn
	analysisCachePathRelFn = func(string, string) (string, error) {
		return "", errors.New("rel failed")
	}
	t.Cleanup(func() {
		analysisCachePathRelFn = prevRel
	})

	if !pathAtOrBelow("/cache", "/repo") {
		t.Fatal("expected relative ancestry error to fail closed")
	}
}

func TestPathAtOrBelowFailsClosedWhenIdentityLookupErrors(t *testing.T) {
	prevRel := analysisCachePathRelFn
	prevLstat := analysisCachePathLstatFn
	analysisCachePathRelFn = func(string, string) (string, error) {
		return filepath.Join("..", "cache"), nil
	}
	analysisCachePathLstatFn = func(string) (fs.FileInfo, error) {
		return nil, errors.New("identity lookup failed")
	}
	t.Cleanup(func() {
		analysisCachePathRelFn = prevRel
		analysisCachePathLstatFn = prevLstat
	})

	if !pathAtOrBelow("/cache", "/repo") {
		t.Fatal("expected identity lookup error to fail closed")
	}
}

func TestPathAtOrBelowByExistingIdentityFindsExistingDescendantAncestor(t *testing.T) {
	root := t.TempDir()
	descendant := filepath.Join(root, "existing", "descendant")
	if err := os.MkdirAll(descendant, 0o750); err != nil {
		t.Fatalf("create existing descendant: %v", err)
	}

	got, ok, err := pathAtOrBelowByExistingIdentity(descendant, root)
	if err != nil {
		t.Fatalf("identity-check existing descendant: %v", err)
	}
	if !ok || !got {
		t.Fatalf("expected protected root inode in descendant ancestry, got=%v ok=%v", got, ok)
	}
}

func TestPathAtOrBelowByExistingIdentityRejectsDeeperRoot(t *testing.T) {
	path := t.TempDir()
	root := filepath.Join(path, "existing-root")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatalf("create deeper protected root: %v", err)
	}

	got, ok, err := pathAtOrBelowByExistingIdentity(path, root)
	if err != nil {
		t.Fatalf("identity-check deeper root: %v", err)
	}
	if !ok || got {
		t.Fatalf("expected deeper root to reject ancestor check, got=%v ok=%v", got, ok)
	}
}

func TestPathAtOrBelowByExistingIdentityRejectsDifferentExistingBranch(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "path")
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatalf("create candidate path: %v", err)
	}
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatalf("create protected root: %v", err)
	}

	got, ok, err := pathAtOrBelowByExistingIdentity(path, root)
	if err != nil {
		t.Fatalf("identity-check different branches: %v", err)
	}
	if !ok || got {
		t.Fatalf("expected different branches to reject descendant check, got=%v ok=%v", got, ok)
	}
}

func TestPathAtOrBelowByExistingIdentityComparesMissingComponents(t *testing.T) {
	parent := t.TempDir()
	tests := []struct {
		name string
		path string
		root string
		want bool
	}{
		{
			name: "case-only different missing root",
			path: filepath.Join(parent, "MissingRoot", "child"),
			root: filepath.Join(parent, "missingroot"),
			want: runtime.GOOS == "windows",
		},
		{
			name: "different missing root",
			path: filepath.Join(parent, "candidate", "child"),
			root: filepath.Join(parent, "protected"),
			want: false,
		},
		{
			name: "protected suffix deeper than candidate",
			path: filepath.Join(parent, "missingroot"),
			root: filepath.Join(parent, "MissingRoot", "child"),
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok, err := pathAtOrBelowByExistingIdentity(test.path, test.root)
			if err != nil {
				t.Fatalf("identity-check missing components: %v", err)
			}
			if !ok || got != test.want {
				t.Fatalf("identity result = %v, applicable=%v, want %v/true", got, ok, test.want)
			}
		})
	}
}

func TestMissingAncestryAtOrBelowCaseSensitivity(t *testing.T) {
	rootDir := t.TempDir()
	rootInfo, err := os.Lstat(rootDir)
	if err != nil {
		t.Fatalf("lstat root dir: %v", err)
	}

	pathAncestry := existingPathAncestry{
		ancestors: []existingPathAncestor{{path: rootDir, info: rootInfo}},
		remainder: []string{"MissingRoot", "child"},
	}
	rootAncestry := existingPathAncestry{
		ancestors: []existingPathAncestor{{path: rootDir, info: rootInfo}},
		remainder: []string{"missingroot"},
	}

	prevCaseInsensitive := analysisCacheMissingAncestryCaseInsensitiveFn
	analysisCacheMissingAncestryCaseInsensitiveFn = func() bool { return false }
	t.Cleanup(func() {
		analysisCacheMissingAncestryCaseInsensitiveFn = prevCaseInsensitive
	})

	if missingAncestryAtOrBelow(pathAncestry, rootAncestry, rootInfo) {
		t.Fatal("expected case-sensitive missing ancestry comparison to keep differently cased descendants distinct")
	}

	analysisCacheMissingAncestryCaseInsensitiveFn = func() bool { return true }
	if !missingAncestryAtOrBelow(pathAncestry, rootAncestry, rootInfo) {
		t.Fatal("expected known case-insensitive missing ancestry comparison to preserve Windows-style aliases")
	}
}

func TestPathAtOrBelowByExistingIdentityFailsWhenAncestorIdentityChanges(t *testing.T) {
	pathRoot := t.TempDir()
	unstableAncestor := filepath.Join(pathRoot, "ancestor")
	path := filepath.Join(unstableAncestor, "descendant")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("create candidate ancestry: %v", err)
	}
	root := t.TempDir()
	originalInfo, err := os.Lstat(unstableAncestor)
	if err != nil {
		t.Fatalf("inspect original ancestor identity: %v", err)
	}
	replacementInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatalf("inspect replacement ancestor identity: %v", err)
	}

	prevLstat := analysisCachePathLstatFn
	ancestorLookups := 0
	analysisCachePathLstatFn = func(name string) (fs.FileInfo, error) {
		if name == unstableAncestor {
			ancestorLookups++
			if ancestorLookups == 1 {
				return originalInfo, nil
			}
			return replacementInfo, nil
		}
		return os.Lstat(name)
	}
	t.Cleanup(func() {
		analysisCachePathLstatFn = prevLstat
	})

	if _, _, err := pathAtOrBelowByExistingIdentity(path, root); !errors.Is(err, safeio.ErrFileChanged) {
		t.Fatalf("expected unstable ancestor identity to fail closed, got %v", err)
	}
}

func TestPathAtOrBelowByExistingIdentityFailsWhenAncestryRevalidationErrors(t *testing.T) {
	tests := []struct {
		name       string
		watchedDir func(path, root string) string
	}{
		{
			name:       "candidate ancestor",
			watchedDir: func(path, _ string) string { return path },
		},
		{
			name:       "protected-root ancestor",
			watchedDir: func(_, root string) string { return root },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir()
			root := t.TempDir()
			watchedDir := test.watchedDir(path, root)
			prevLstat := analysisCachePathLstatFn
			lookups := 0
			revalidationErr := errors.New("ancestor revalidation failed")
			analysisCachePathLstatFn = func(name string) (fs.FileInfo, error) {
				if name == watchedDir {
					lookups++
					if lookups > 1 {
						return nil, revalidationErr
					}
				}
				return os.Lstat(name)
			}
			t.Cleanup(func() {
				analysisCachePathLstatFn = prevLstat
			})

			if _, _, err := pathAtOrBelowByExistingIdentity(path, root); !errors.Is(err, revalidationErr) {
				t.Fatalf("expected ancestry revalidation error to fail closed, got %v", err)
			}
		})
	}
}

func TestPathAtOrBelowByExistingIdentityReturnsProtectedRootLookupError(t *testing.T) {
	path := t.TempDir()
	root := filepath.Join("\x00", "protected")
	if _, _, err := pathAtOrBelowByExistingIdentity(path, root); err == nil ||
		!strings.Contains(err.Error(), "inspect path ancestry") {
		t.Fatalf("expected protected-root lookup failure, got %v", err)
	}
}

func TestInspectExistingPathAncestryReturnsLookupError(t *testing.T) {
	if _, err := inspectExistingPathAncestry("\x00"); err == nil || !strings.Contains(err.Error(), "inspect path ancestry") {
		t.Fatalf("expected invalid path lookup error, got %v", err)
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

func TestPathAtOrBelowWindowsRejectsDifferentDriveVolume(t *testing.T) {
	got, ok := pathAtOrBelowWindows(`D:\repo\cache`, `C:\repo`)
	if !ok {
		t.Fatal("expected Windows path comparison to apply")
	}
	if got {
		t.Fatal("expected different drive volumes not to compare as descendants")
	}
}

func TestPathAtOrBelowWindowsPreflight(t *testing.T) {
	absoluteDrive := windowspath.Classify(`C:\repo\cache`)
	driveRoot := windowspath.Classify(`C:\repo`)

	for _, tc := range []struct {
		name     string
		path     string
		root     string
		pathInfo windowspath.Classification
		rootInfo windowspath.Classification
		want     bool
		handled  bool
	}{
		{
			name:     "nul path fails closed",
			path:     "cache\x00",
			root:     `C:\repo`,
			pathInfo: absoluteDrive,
			rootInfo: driveRoot,
			want:     true,
			handled:  true,
		},
		{
			name:     "relative path under windows root fails closed",
			path:     `cache`,
			root:     `C:\repo`,
			pathInfo: windowspath.Classify(`cache`),
			rootInfo: driveRoot,
			want:     true,
			handled:  true,
		},
		{
			name:     "clean absolute path continues",
			path:     `C:\repo\cache`,
			root:     `C:\repo`,
			pathInfo: absoluteDrive,
			rootInfo: driveRoot,
			want:     false,
			handled:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, handled := pathAtOrBelowWindowsPreflight(tc.path, tc.root, tc.pathInfo, tc.rootInfo)
			if handled != tc.handled || got != tc.want {
				t.Fatalf("pathAtOrBelowWindowsPreflight(%q, %q) = (%v, %v), want (%v, %v)", tc.path, tc.root, got, handled, tc.want, tc.handled)
			}
		})
	}
}

func TestPathAtOrBelowWindowsUnsafe(t *testing.T) {
	if !pathAtOrBelowWindowsUnsafe(`\\?\C:\repo`, `C:\repo`, windowspath.Classify(`\\?\C:\repo`), windowspath.Classify(`C:\repo`)) {
		t.Fatal("expected ambiguous namespace path to fail closed")
	}
	if !pathAtOrBelowWindowsUnsafe(`cache `, `C:\repo`, windowspath.Classify(`cache `), windowspath.Classify(`C:\repo`)) {
		t.Fatal("expected trimmed alias path to fail closed")
	}
	if !pathAtOrBelowWindowsUnsafe(`NUL.txt`, `C:\repo`, windowspath.Classify(`NUL.txt`), windowspath.Classify(`C:\repo`)) {
		t.Fatal("expected reserved DOS name path to fail closed")
	}
	if pathAtOrBelowWindowsUnsafe(`C:\repo\cache`, `C:\repo`, windowspath.Classify(`C:\repo\cache`), windowspath.Classify(`C:\repo`)) {
		t.Fatal("expected ordinary absolute path not to be marked unsafe")
	}
}

func TestReadAnalysisCacheAuthKeyRejectsPermissiveModeWhenWritable(t *testing.T) {
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
	if !errors.Is(err, errAnalysisCacheAuthKeyInvalid) {
		t.Fatalf("expected permissive key to be treated as invalid, got key=%x err=%v", key, err)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat permissive key: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("expected permissive key mode to remain unchanged until rotation, got %o", info.Mode().Perm())
	}
}

func TestFinishReadOnlyResolvedAuthKeyWarnsWhenUnavailable(t *testing.T) {
	cache := &analysisCache{}
	key, err := cache.finishReadOnlyResolvedAuthKey(errors.New("backend unavailable"))
	if err != nil {
		t.Fatalf("finishReadOnlyResolvedAuthKey(unavailable): %v", err)
	}
	if len(key) != 0 {
		t.Fatalf("expected cold cache on unavailable auth key, got %x", key)
	}
	warnings := strings.Join(cache.takeWarnings(), "\n")
	if !strings.Contains(warnings, "auth key unavailable") {
		t.Fatalf("expected unavailable warning, got %q", warnings)
	}
}

func TestEnsureAuthStorePathSkipsReadonly(t *testing.T) {
	cache := &analysisCache{options: resolvedCacheOptions{ReadOnly: true}}
	prevMkdirAll := analysisCacheAuthMkdirAllDurableFn
	analysisCacheAuthMkdirAllDurableFn = func(*safeio.WriteRoot, string, os.FileMode) error {
		t.Fatal("readonly auth-store path check must not create directories")
		return nil
	}
	t.Cleanup(func() {
		analysisCacheAuthMkdirAllDurableFn = prevMkdirAll
	})

	if err := cache.ensureAuthStorePath(t.TempDir(), filepath.Join("lopper", analysisCacheAuthDirName), filepath.Join(t.TempDir(), "lopper", analysisCacheAuthDirName)); err != nil {
		t.Fatalf("ensureAuthStorePath(readonly): %v", err)
	}
}

func TestAuthStoreMissingReturnsFalseForExistingPath(t *testing.T) {
	authRootPath := filepath.Join(t.TempDir(), "analysis-cache-auth")
	if err := os.Mkdir(authRootPath, 0o750); err != nil {
		t.Fatalf("mkdir auth store: %v", err)
	}
	missing, err := authStoreMissing(authRootPath)
	if err != nil {
		t.Fatalf("authStoreMissing(existing): %v", err)
	}
	if missing {
		t.Fatal("expected existing auth store not to be reported missing")
	}
}

func TestCreateAuthStorePathReturnsCreateErrorWhenExistingStoreSyncFails(t *testing.T) {
	prevMkdirAll := analysisCacheAuthMkdirAllDurableFn
	mkdirErr := errors.New("mkdir failed")
	analysisCacheAuthMkdirAllDurableFn = func(*safeio.WriteRoot, string, os.FileMode) error {
		return mkdirErr
	}
	t.Cleanup(func() {
		analysisCacheAuthMkdirAllDurableFn = prevMkdirAll
	})

	canonicalUserCacheDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(user cache dir): %v", err)
	}
	if err := createAuthStorePath(canonicalUserCacheDir, filepath.Join("lopper", analysisCacheAuthDirName), false); err == nil || !strings.Contains(err.Error(), "create cache auth store") {
		t.Fatalf("expected existing-store mkdir error, got %v", err)
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

func TestReadAnalysisCacheAuthKeyFailsClosedOnPrivacyErrors(t *testing.T) {
	target := newAnalysisCacheAuthTestTarget(t)
	encoded := []byte(strings.Repeat("ab", analysisCacheAuthKeyLength))
	writePrivateCacheAuthKey(t, target.root, target.keyName, encoded)
	tests := []struct {
		name        string
		privacyErr  error
		wantChanged bool
	}{
		{
			name:        "target changed",
			privacyErr:  safeio.ErrFileChanged,
			wantChanged: true,
		},
		{
			name:       "privacy lookup failed",
			privacyErr: errors.New("privacy lookup failed"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prevPrivacy := analysisCacheAuthKeyPrivateToOwnerFn
			analysisCacheAuthKeyPrivateToOwnerFn = func(*safeio.WriteRoot, string, fs.FileInfo) (bool, error) {
				return false, test.privacyErr
			}
			t.Cleanup(func() {
				analysisCacheAuthKeyPrivateToOwnerFn = prevPrivacy
			})

			_, err := readAnalysisCacheAuthKey(target.root, target.keyName, true)
			if test.wantChanged {
				if !errors.Is(err, errAnalysisCacheAuthKeyChanged) {
					t.Fatalf("expected changed-key identity, got %v", err)
				}
				return
			}
			if !errors.Is(err, test.privacyErr) {
				t.Fatalf("expected privacy error identity %v, got %v", test.privacyErr, err)
			}
		})
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

func TestCreateOrRotateAuthKeyPropagatesCompromiseObservationErrors(t *testing.T) {
	encoded := []byte(strings.Repeat("ab", analysisCacheAuthKeyLength))
	compromisedErr := newCompromisedAuthKeyError(encoded, &stubFileInfo{mode: 0o644})
	if !errors.Is(compromisedErr, errAnalysisCacheAuthKeyInvalid) ||
		!strings.Contains(compromisedErr.Error(), "permissions are not owner-only") {
		t.Fatalf("compromised error lost invalid-key identity or reason: %v", compromisedErr)
	}

	t.Run("initial observation hook", func(t *testing.T) {
		hookErr := errors.New("initial compromise hook failed")
		prevHook := analysisCacheAuthAfterCompromisedReadFn
		analysisCacheAuthAfterCompromisedReadFn = func() error { return hookErr }
		t.Cleanup(func() {
			analysisCacheAuthAfterCompromisedReadFn = prevHook
		})

		_, err := (&analysisCache{}).createOrRotateAuthKeyFromError(nil, "cache.key", true, compromisedErr)
		if !errors.Is(err, hookErr) {
			t.Fatalf("expected initial compromise hook error, got %v", err)
		}
	})

	t.Run("retry observation hook", func(t *testing.T) {
		hookErr := errors.New("retry compromise hook failed")
		prevRead := analysisCacheReadAuthKeyFn
		prevHook := analysisCacheAuthAfterCompromisedReadFn
		analysisCacheReadAuthKeyFn = func(*safeio.WriteRoot, string, bool) ([]byte, error) {
			return nil, compromisedErr
		}
		analysisCacheAuthAfterCompromisedReadFn = func() error { return hookErr }
		t.Cleanup(func() {
			analysisCacheReadAuthKeyFn = prevRead
			analysisCacheAuthAfterCompromisedReadFn = prevHook
		})

		_, err := (&analysisCache{}).createOrRotateAuthKeyFromError(nil, "cache.key", true, nil)
		if !errors.Is(err, hookErr) {
			t.Fatalf("expected retry compromise hook error, got %v", err)
		}
	})
}

func TestCreateOrRotateAuthKeyRetainsCompromisedDigestAcrossRetry(t *testing.T) {
	encoded := []byte(strings.Repeat("ab", analysisCacheAuthKeyLength))

	t.Run("readonly rotation policy rejects reused bytes", func(t *testing.T) {
		target := newAnalysisCacheAuthTestTarget(t)
		info := writePrivateCacheAuthKey(t, target.root, target.keyName, encoded)
		initialErr := newCompromisedAuthKeyError(encoded, info)

		_, err := (&analysisCache{}).createOrRotateAuthKeyFromError(target.root, target.keyName, false, initialErr)
		if !errors.Is(err, errAnalysisCacheAuthKeyInvalid) {
			t.Fatalf("expected retained compromised digest to remain invalid, got %v", err)
		}
	})

	t.Run("rotation failure preserves its identity", func(t *testing.T) {
		target := newAnalysisCacheAuthTestTarget(t)
		info := writePrivateCacheAuthKey(t, target.root, target.keyName, encoded)
		initialErr := newCompromisedAuthKeyError(encoded, info)
		rotateErr := errors.New("rotate retained compromise")
		prevRotate := analysisCacheRotateCompromisedAuthKeyFn
		analysisCacheRotateCompromisedAuthKeyFn = func(*safeio.WriteRoot, string, compromisedAuthKeyState) error {
			return rotateErr
		}
		t.Cleanup(func() {
			analysisCacheRotateCompromisedAuthKeyFn = prevRotate
		})

		_, err := (&analysisCache{}).createOrRotateAuthKeyFromError(target.root, target.keyName, true, initialErr)
		if !errors.Is(err, rotateErr) {
			t.Fatalf("expected retained-compromise rotation error, got %v", err)
		}
	})
}

func TestCreateOrRotateAuthKeyHandlesCompromisedPrivacyRecheckErrors(t *testing.T) {
	encoded := []byte(strings.Repeat("ab", analysisCacheAuthKeyLength))
	key := bytes.Repeat([]byte{0xab}, analysisCacheAuthKeyLength)

	t.Run("changed target retries before returning later read error", func(t *testing.T) {
		target := newAnalysisCacheAuthTestTarget(t)
		info := writePrivateCacheAuthKey(t, target.root, target.keyName, encoded)
		initialErr := newCompromisedAuthKeyError(encoded, info)
		laterReadErr := errors.New("read after changed privacy target")
		readCalls := 0
		prevRead := analysisCacheReadAuthKeyFn
		prevPrivacy := analysisCacheAuthKeyPrivateToOwnerFn
		prevSleep := analysisCacheSleepFn
		analysisCacheReadAuthKeyFn = func(*safeio.WriteRoot, string, bool) ([]byte, error) {
			readCalls++
			if readCalls == 1 {
				return key, nil
			}
			return nil, laterReadErr
		}
		analysisCacheAuthKeyPrivateToOwnerFn = func(*safeio.WriteRoot, string, fs.FileInfo) (bool, error) {
			return false, safeio.ErrFileChanged
		}
		analysisCacheSleepFn = func(time.Duration) {}
		t.Cleanup(func() {
			analysisCacheReadAuthKeyFn = prevRead
			analysisCacheAuthKeyPrivateToOwnerFn = prevPrivacy
			analysisCacheSleepFn = prevSleep
		})

		_, err := (&analysisCache{}).createOrRotateAuthKeyFromError(target.root, target.keyName, true, initialErr)
		if !errors.Is(err, laterReadErr) {
			t.Fatalf("expected retry after changed privacy target, got %v", err)
		}
		if readCalls != 2 {
			t.Fatalf("auth key reads = %d, want one retry after identity change", readCalls)
		}
	})

	t.Run("privacy lookup error fails closed", func(t *testing.T) {
		target := newAnalysisCacheAuthTestTarget(t)
		info := writePrivateCacheAuthKey(t, target.root, target.keyName, encoded)
		initialErr := newCompromisedAuthKeyError(encoded, info)
		privacyErr := errors.New("privacy recheck failed")
		prevRead := analysisCacheReadAuthKeyFn
		prevPrivacy := analysisCacheAuthKeyPrivateToOwnerFn
		analysisCacheReadAuthKeyFn = func(*safeio.WriteRoot, string, bool) ([]byte, error) {
			return key, nil
		}
		analysisCacheAuthKeyPrivateToOwnerFn = func(*safeio.WriteRoot, string, fs.FileInfo) (bool, error) {
			return false, privacyErr
		}
		t.Cleanup(func() {
			analysisCacheReadAuthKeyFn = prevRead
			analysisCacheAuthKeyPrivateToOwnerFn = prevPrivacy
		})

		_, err := (&analysisCache{}).createOrRotateAuthKeyFromError(target.root, target.keyName, true, initialErr)
		if !errors.Is(err, privacyErr) {
			t.Fatalf("expected privacy recheck error identity, got %v", err)
		}
	})
}

func TestCurrentCompromisedAuthKeyStateRejectsChangedContent(t *testing.T) {
	encoded := []byte(strings.Repeat("ab", analysisCacheAuthKeyLength))
	key := bytes.Repeat([]byte{0xab}, analysisCacheAuthKeyLength)

	t.Run("key disappeared", func(t *testing.T) {
		target := newAnalysisCacheAuthTestTarget(t)
		info := writePrivateCacheAuthKey(t, target.root, target.keyName, encoded)
		state := compromisedAuthKeyStateForData(encoded, info)
		if err := target.root.Remove(target.keyName); err != nil {
			t.Fatalf("remove observed compromised key: %v", err)
		}

		_, compromised, err := currentCompromisedAuthKeyState(target.root, target.keyName, key, map[string]compromisedAuthKeyState{state.contentDigest: state})
		if !errors.Is(err, errAnalysisCacheAuthKeyChanged) || compromised {
			t.Fatalf("expected disappeared key to report changed, compromised=%v err=%v", compromised, err)
		}
	})

	t.Run("key became malformed", func(t *testing.T) {
		target := newAnalysisCacheAuthTestTarget(t)
		info := writePrivateCacheAuthKey(t, target.root, target.keyName, encoded)
		state := compromisedAuthKeyStateForData(encoded, info)
		writePrivateCacheAuthKey(t, target.root, target.keyName, []byte("malformed"))

		_, compromised, err := currentCompromisedAuthKeyState(target.root, target.keyName, key, map[string]compromisedAuthKeyState{state.contentDigest: state})
		if !errors.Is(err, errAnalysisCacheAuthKeyChanged) || compromised {
			t.Fatalf("expected malformed replacement to report changed, compromised=%v err=%v", compromised, err)
		}
	})

	t.Run("key became non-regular", func(t *testing.T) {
		target := newAnalysisCacheAuthTestTarget(t)
		info := writePrivateCacheAuthKey(t, target.root, target.keyName, encoded)
		state := compromisedAuthKeyStateForData(encoded, info)
		if err := target.root.Remove(target.keyName); err != nil {
			t.Fatalf("remove observed compromised key: %v", err)
		}
		if err := os.Mkdir(target.keyPath, 0o700); err != nil {
			t.Fatalf("replace compromised key with directory: %v", err)
		}

		_, compromised, err := currentCompromisedAuthKeyState(target.root, target.keyName, key, map[string]compromisedAuthKeyState{state.contentDigest: state})
		if err == nil || !strings.Contains(err.Error(), "recheck cache auth key after compromise") || compromised {
			t.Fatalf("expected non-regular recheck failure, compromised=%v err=%v", compromised, err)
		}
	})
}

func TestCurrentCompromisedAuthKeyStateFailsClosedOnPrivacyErrors(t *testing.T) {
	encoded := []byte(strings.Repeat("ab", analysisCacheAuthKeyLength))
	key := bytes.Repeat([]byte{0xab}, analysisCacheAuthKeyLength)
	target := newAnalysisCacheAuthTestTarget(t)
	info := writePrivateCacheAuthKey(t, target.root, target.keyName, encoded)
	state := compromisedAuthKeyStateForData(encoded, info)
	states := map[string]compromisedAuthKeyState{state.contentDigest: state}

	tests := []struct {
		name        string
		privacyErr  error
		wantChanged bool
	}{
		{
			name:        "target changed",
			privacyErr:  safeio.ErrFileChanged,
			wantChanged: true,
		},
		{
			name:       "privacy lookup failed",
			privacyErr: errors.New("privacy lookup failed"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prevPrivacy := analysisCacheAuthKeyPrivateToOwnerFn
			analysisCacheAuthKeyPrivateToOwnerFn = func(*safeio.WriteRoot, string, fs.FileInfo) (bool, error) {
				return false, test.privacyErr
			}
			t.Cleanup(func() {
				analysisCacheAuthKeyPrivateToOwnerFn = prevPrivacy
			})

			_, compromised, err := currentCompromisedAuthKeyState(target.root, target.keyName, key, states)
			if compromised {
				t.Fatal("privacy recheck error reported target as safely classified")
			}
			if test.wantChanged {
				if !errors.Is(err, errAnalysisCacheAuthKeyChanged) {
					t.Fatalf("expected changed-key identity, got %v", err)
				}
				return
			}
			if !errors.Is(err, test.privacyErr) {
				t.Fatalf("expected privacy error identity %v, got %v", test.privacyErr, err)
			}
		})
	}
}

func TestCurrentCompromisedAuthKeyStateRemembersNewPermissiveDigest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode-bit interleaving is covered by native Windows DACL tests")
	}
	target := newAnalysisCacheAuthTestTarget(t)
	oldEncoded := []byte(strings.Repeat("ab", analysisCacheAuthKeyLength))
	oldInfo := writePrivateCacheAuthKey(t, target.root, target.keyName, oldEncoded)
	oldState := compromisedAuthKeyStateForData(oldEncoded, oldInfo)
	states := map[string]compromisedAuthKeyState{oldState.contentDigest: oldState}

	currentEncoded := []byte(strings.Repeat("cd", analysisCacheAuthKeyLength))
	writePrivateCacheAuthKey(t, target.root, target.keyName, currentEncoded)
	if err := os.Chmod(target.keyPath, 0o644); err != nil {
		t.Fatalf("expose replacement key: %v", err)
	}
	currentKey := bytes.Repeat([]byte{0xcd}, analysisCacheAuthKeyLength)
	hookErr := errors.New("new compromised digest observed")
	hookCalls := 0
	prevHook := analysisCacheAuthAfterCompromisedReadFn
	analysisCacheAuthAfterCompromisedReadFn = func() error {
		hookCalls++
		return hookErr
	}
	t.Cleanup(func() {
		analysisCacheAuthAfterCompromisedReadFn = prevHook
	})

	_, compromised, err := currentCompromisedAuthKeyState(target.root, target.keyName, currentKey, states)
	if !errors.Is(err, hookErr) || compromised {
		t.Fatalf("expected new compromised digest hook error, compromised=%v err=%v", compromised, err)
	}
	currentDigest := authKeyContentDigest(currentEncoded)
	if hookCalls != 1 {
		t.Fatalf("new compromised digest hook calls = %d, want 1", hookCalls)
	}
	if _, remembered := states[currentDigest]; !remembered {
		t.Fatal("new permissive key digest was not retained")
	}

	analysisCacheAuthAfterCompromisedReadFn = func() error {
		hookCalls++
		return nil
	}
	gotState, compromised, err := currentCompromisedAuthKeyState(target.root, target.keyName, currentKey, states)
	if err != nil || !compromised {
		t.Fatalf("expected remembered permissive digest to remain compromised, compromised=%v err=%v", compromised, err)
	}
	if gotState.contentDigest != currentDigest {
		t.Fatalf("compromised digest = %q, want %q", gotState.contentDigest, currentDigest)
	}
	if hookCalls != 1 {
		t.Fatalf("remembered digest retriggered observation hook, calls=%d", hookCalls)
	}
}

func TestRotateCompromisedAuthKeyRejectsChangedOrUnreadableTarget(t *testing.T) {
	t.Run("target disappeared", func(t *testing.T) {
		target, state := newCompromisedAuthKeyRotationFixture(t)
		if err := target.root.Remove(target.keyName); err != nil {
			t.Fatalf("remove compromised target: %v", err)
		}
		assertRotateCompromisedAuthKeyResult(t, rotateCompromisedAuthKey(target.root, target.keyName, state), func(err error) bool {
			return errors.Is(err, errAnalysisCacheAuthKeyChanged)
		})
	})

	t.Run("different strict winner", func(t *testing.T) {
		target, state := newCompromisedAuthKeyRotationFixture(t)
		want := []byte(strings.Repeat("ef", analysisCacheAuthKeyLength))
		writePrivateCacheAuthKey(t, target.root, target.keyName, want)
		assertRotateCompromisedAuthKeyResult(t, rotateCompromisedAuthKey(target.root, target.keyName, state), func(err error) bool {
			return errors.Is(err, errAnalysisCacheAuthKeyChanged)
		})
		assertPreservedStrictWinner(t, target, want)
	})

	t.Run("non-regular target", func(t *testing.T) {
		target, state := newCompromisedAuthKeyRotationFixture(t)
		if err := target.root.Remove(target.keyName); err != nil {
			t.Fatalf("remove compromised target: %v", err)
		}
		if err := os.Mkdir(target.keyPath, 0o700); err != nil {
			t.Fatalf("replace compromised target with directory: %v", err)
		}
		assertRotateCompromisedAuthKeyResult(t, rotateCompromisedAuthKey(target.root, target.keyName, state), func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "recheck compromised cache auth key")
		})
	})
}

func newCompromisedAuthKeyRotationFixture(t *testing.T) (analysisCacheAuthTestTarget, compromisedAuthKeyState) {
	t.Helper()
	target := newAnalysisCacheAuthTestTarget(t)
	encoded := []byte(strings.Repeat("ab", analysisCacheAuthKeyLength))
	info := writePrivateCacheAuthKey(t, target.root, target.keyName, encoded)
	state := compromisedAuthKeyStateForData(encoded, info)
	rotationName := target.keyName + analysisCacheAuthRotateTag + state.generation
	writePrivateCacheAuthKey(t, target.root, rotationName, []byte(strings.Repeat("cd", analysisCacheAuthKeyLength)))
	return target, state
}

func assertRotateCompromisedAuthKeyResult(t *testing.T, err error, ok func(error) bool) {
	t.Helper()
	if !ok(err) {
		t.Fatalf("unexpected rotate result: %v", err)
	}
}

func assertPreservedStrictWinner(t *testing.T, target analysisCacheAuthTestTarget, want []byte) {
	t.Helper()
	got, _, err := target.root.ReadRegularFileUnderLimit(target.keyName, analysisCacheAuthKeyMaxBytes)
	if err != nil {
		t.Fatalf("read preserved strict winner: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("strict winner changed to %q, want %q", got, want)
	}
}

func TestAuthKeyContentDigestUsesRawBytesForMalformedKey(t *testing.T) {
	data := []byte("malformed-auth-key")
	if got, want := authKeyContentDigest(data), sha256Hex(data); got != want {
		t.Fatalf("malformed key digest = %q, want raw-content digest %q", got, want)
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

func TestPublishMissingAuthKeyFallsBackWhenHardLinkUnsupported(t *testing.T) {
	for _, linkErr := range []error{syscall.ENOTSUP, syscall.EPERM} {
		t.Run(linkErr.Error(), func(t *testing.T) {
			prevLink := analysisCacheAuthLinkFn
			analysisCacheAuthLinkFn = func(*safeio.WriteRoot, string, string) error {
				return &os.LinkError{Op: "link", Old: "candidate", New: "cache.key", Err: linkErr}
			}
			t.Cleanup(func() {
				analysisCacheAuthLinkFn = prevLink
			})

			authRoot, rootDir := openTempAuthRoot(t)
			if err := publishMissingAuthKey(authRoot, "cache.key"); err != nil {
				t.Fatalf("publishMissingAuthKey(fallback): %v", err)
			}

			encodedKey, err := os.ReadFile(filepath.Join(rootDir, "cache.key"))
			if err != nil {
				t.Fatalf("read fallback-published key: %v", err)
			}
			if _, err := decodeAuthKey(strings.TrimSpace(string(encodedKey))); err != nil {
				t.Fatalf("decode fallback-published key: %v", err)
			}
		})
	}
}

func TestPublishMissingAuthKeyFallbackConcurrentCreatorsPublishSingleWinner(t *testing.T) {
	prevLink := analysisCacheAuthLinkFn
	analysisCacheAuthLinkFn = func(*safeio.WriteRoot, string, string) error {
		return &os.LinkError{Op: "link", Old: "candidate", New: "cache.key", Err: syscall.ENOTSUP}
	}
	t.Cleanup(func() {
		analysisCacheAuthLinkFn = prevLink
	})

	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(temp auth dir): %v", err)
	}

	const workers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			root, err := safeio.OpenCanonicalWriteRoot(rootDir)
			if err != nil {
				errs <- err
				return
			}
			defer func() {
				if closeErr := root.Close(); closeErr != nil {
					errs <- closeErr
				}
			}()
			errs <- publishMissingAuthKey(root, "cache.key")
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent fallback publish: %v", err)
		}
	}

	encodedKey, err := os.ReadFile(filepath.Join(rootDir, "cache.key"))
	if err != nil {
		t.Fatalf("read persisted fallback key: %v", err)
	}
	if _, err := decodeAuthKey(strings.TrimSpace(string(encodedKey))); err != nil {
		t.Fatalf("decode persisted fallback key: %v", err)
	}
}

func TestPublishMissingAuthKeyFallbackPreservesLateWinnerIdentityAndCleansUp(t *testing.T) {
	requireAuthFallbackPlatform(t)
	prevLink := analysisCacheAuthLinkFn
	prevHook := analysisCacheAuthBeforeFallbackInstallFn
	analysisCacheAuthLinkFn = func(*safeio.WriteRoot, string, string) error {
		return &os.LinkError{Op: "link", Old: "candidate", New: "cache.key", Err: syscall.ENOTSUP}
	}
	authRoot, rootDir := openTempAuthRoot(t)
	lateWinner := strings.Repeat("cd", analysisCacheAuthKeyLength)
	var winnerInfo fs.FileInfo
	analysisCacheAuthBeforeFallbackInstallFn = func() error {
		winnerPath := filepath.Join(rootDir, "cache.key")
		if err := os.WriteFile(winnerPath, []byte(lateWinner), 0o600); err != nil {
			return err
		}
		var err error
		winnerInfo, err = os.Lstat(winnerPath)
		return err
	}
	t.Cleanup(func() {
		analysisCacheAuthLinkFn = prevLink
		analysisCacheAuthBeforeFallbackInstallFn = prevHook
	})

	if err := publishMissingAuthKey(authRoot, "cache.key"); err != nil {
		t.Fatalf("publishMissingAuthKey(late winner): %v", err)
	}

	encodedKey, err := os.ReadFile(filepath.Join(rootDir, "cache.key"))
	if err != nil {
		t.Fatalf("read late winner key: %v", err)
	}
	if string(encodedKey) != lateWinner {
		t.Fatalf("expected fallback publish not to overwrite late winner, got %q", string(encodedKey))
	}
	persistedInfo, err := os.Lstat(filepath.Join(rootDir, "cache.key"))
	if err != nil {
		t.Fatalf("stat late winner key: %v", err)
	}
	if winnerInfo == nil || !os.SameFile(winnerInfo, persistedInfo) {
		t.Fatal("expected no-replace publication to preserve the late winner identity")
	}
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		t.Fatalf("read auth root after late-winner race: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "cache.key" {
		t.Fatalf("expected candidate and lock cleanup after late-winner race, got entries %v", authDirectoryEntryNames(entries))
	}
}

func TestPublishMissingAuthKeyFallbackReturnsHookError(t *testing.T) {
	prevLink := analysisCacheAuthLinkFn
	prevHook := analysisCacheAuthBeforeFallbackInstallFn
	analysisCacheAuthLinkFn = func(*safeio.WriteRoot, string, string) error {
		return &os.LinkError{Op: "link", Old: "candidate", New: "cache.key", Err: syscall.ENOTSUP}
	}
	analysisCacheAuthBeforeFallbackInstallFn = func() error { return errors.New("hook failed") }
	t.Cleanup(func() {
		analysisCacheAuthLinkFn = prevLink
		analysisCacheAuthBeforeFallbackInstallFn = prevHook
	})

	authRoot, _ := openTempAuthRoot(t)
	if err := publishMissingAuthKey(authRoot, "cache.key"); err == nil || !strings.Contains(err.Error(), "hook failed") {
		t.Fatalf("expected fallback hook failure, got %v", err)
	}
}

func TestPublishMissingAuthKeyFallbackReturnsDirectorySyncError(t *testing.T) {
	prevLink := analysisCacheAuthLinkFn
	prevSync := analysisCacheAuthSyncDirFn
	analysisCacheAuthLinkFn = func(*safeio.WriteRoot, string, string) error {
		return &os.LinkError{Op: "link", Old: "candidate", New: "cache.key", Err: syscall.ENOTSUP}
	}
	analysisCacheAuthSyncDirFn = func(*safeio.WriteRoot) error { return errors.New("sync failed") }
	t.Cleanup(func() {
		analysisCacheAuthLinkFn = prevLink
		analysisCacheAuthSyncDirFn = prevSync
	})

	authRoot, _ := openTempAuthRoot(t)
	if err := publishMissingAuthKey(authRoot, "cache.key"); err == nil || !strings.Contains(err.Error(), "sync cache auth key directory after fallback publish") {
		t.Fatalf("expected fallback sync failure, got %v", err)
	}
}

func TestPublishMissingAuthKeyReturnsObservedWinnerSyncError(t *testing.T) {
	prevSync := analysisCacheAuthSyncDirFn
	syncErr := errors.New("sync failed")
	analysisCacheAuthSyncDirFn = func(*safeio.WriteRoot) error { return syncErr }
	t.Cleanup(func() {
		analysisCacheAuthSyncDirFn = prevSync
	})

	authRoot, rootDir := openTempAuthRoot(t)
	if err := os.WriteFile(filepath.Join(rootDir, "cache.key"), []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)), 0o600); err != nil {
		t.Fatalf("write existing auth key winner: %v", err)
	}
	err := publishMissingAuthKey(authRoot, "cache.key")
	if !errors.Is(err, syncErr) || !strings.Contains(err.Error(), "after observing winner") {
		t.Fatalf("expected observed-winner durability error, got %v", err)
	}
}

func TestPublishMissingAuthKeyFallbackReturnsLockAcquireError(t *testing.T) {
	prevLink := analysisCacheAuthLinkFn
	prevLock := analysisCacheAuthLockDirectoryFn
	analysisCacheAuthLinkFn = func(*safeio.WriteRoot, string, string) error {
		return &os.LinkError{Op: "link", Old: "candidate", New: "cache.key", Err: syscall.ENOTSUP}
	}
	analysisCacheAuthLockDirectoryFn = func(*safeio.WriteRoot) (io.Closer, error) {
		return nil, errors.New("lock failed")
	}
	t.Cleanup(func() {
		analysisCacheAuthLinkFn = prevLink
		analysisCacheAuthLockDirectoryFn = prevLock
	})

	authRoot, _ := openTempAuthRoot(t)
	if err := publishMissingAuthKey(authRoot, "cache.key"); err == nil || !strings.Contains(err.Error(), "acquire cache auth key publish lock") {
		t.Fatalf("expected fallback lock acquisition failure, got %v", err)
	}
}

func TestPublishMissingAuthKeyFallbackReturnsNoReplaceErrorAndCleansCandidate(t *testing.T) {
	prevLink := analysisCacheAuthLinkFn
	prevRename := analysisCacheAuthRenameNoReplaceFn
	analysisCacheAuthLinkFn = func(*safeio.WriteRoot, string, string) error {
		return &os.LinkError{Op: "link", Old: "candidate", New: "cache.key", Err: syscall.ENOTSUP}
	}
	analysisCacheAuthRenameNoReplaceFn = func(*safeio.WriteRoot, string, string) error {
		return errors.New("rename failed")
	}
	t.Cleanup(func() {
		analysisCacheAuthLinkFn = prevLink
		analysisCacheAuthRenameNoReplaceFn = prevRename
	})

	authRoot, rootDir := openTempAuthRoot(t)
	if err := publishMissingAuthKey(authRoot, "cache.key"); err == nil || !strings.Contains(err.Error(), "publish cache auth key winner without hard link") {
		t.Fatalf("expected fallback rename failure, got %v", err)
	}
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		t.Fatalf("read auth root after rename failure: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected rename failure to clean candidate, got entries %v", authDirectoryEntryNames(entries))
	}
}

func TestPublishMissingAuthKeyFallbackReturnsWinnerStatError(t *testing.T) {
	prevLink := analysisCacheAuthLinkFn
	analysisCacheAuthLinkFn = func(*safeio.WriteRoot, string, string) error {
		return &os.LinkError{Op: "link", Old: "candidate", New: "cache.key", Err: syscall.ENOTSUP}
	}
	t.Cleanup(func() {
		analysisCacheAuthLinkFn = prevLink
	})

	authRoot, _ := openTempAuthRoot(t)
	if err := publishMissingAuthKey(authRoot, "../cache.key"); err == nil || !strings.Contains(err.Error(), "inspect cache auth key winner before fallback publish") {
		t.Fatalf("expected fallback winner-stat failure, got %v", err)
	}
}

func TestPublishMissingAuthKeyFallbackRecoversAfterLockHolderCrashes(t *testing.T) {
	requireAuthFallbackPlatform(t)
	prevLink := analysisCacheAuthLinkFn
	analysisCacheAuthLinkFn = func(*safeio.WriteRoot, string, string) error {
		return &os.LinkError{Op: "link", Old: "candidate", New: "cache.key", Err: syscall.ENOTSUP}
	}
	t.Cleanup(func() {
		analysisCacheAuthLinkFn = prevLink
	})

	authRoot, rootDir := openTempAuthRoot(t)
	runAuthDirectoryLockHelperToCrash(t, rootDir)
	if err := publishMissingAuthKey(authRoot, "cache.key"); err != nil {
		t.Fatalf("publish after crashed lock holder: %v", err)
	}
	encodedKey, err := os.ReadFile(filepath.Join(rootDir, "cache.key"))
	if err != nil {
		t.Fatalf("read key published after crashed lock holder: %v", err)
	}
	if _, err := decodeAuthKey(strings.TrimSpace(string(encodedKey))); err != nil {
		t.Fatalf("decode key published after crashed lock holder: %v", err)
	}
}

func TestPublishMissingAuthKeyFallbackWaitsForLiveLockHolder(t *testing.T) {
	requireAuthFallbackPlatform(t)
	prevLink := analysisCacheAuthLinkFn
	prevHook := analysisCacheAuthBeforeFallbackInstallFn
	prevLock := analysisCacheAuthLockDirectoryFn
	analysisCacheAuthLinkFn = func(*safeio.WriteRoot, string, string) error {
		return &os.LinkError{Op: "link", Old: "candidate", New: "cache.key", Err: syscall.ENOTSUP}
	}
	enteredInstall := make(chan struct{})
	attemptedLock := make(chan struct{})
	analysisCacheAuthLockDirectoryFn = func(root *safeio.WriteRoot) (io.Closer, error) {
		close(attemptedLock)
		return root.LockDirectory()
	}
	analysisCacheAuthBeforeFallbackInstallFn = func() error {
		close(enteredInstall)
		return nil
	}
	t.Cleanup(func() {
		analysisCacheAuthLinkFn = prevLink
		analysisCacheAuthBeforeFallbackInstallFn = prevHook
		analysisCacheAuthLockDirectoryFn = prevLock
	})

	authRoot, rootDir := openTempAuthRoot(t)
	helper := startAuthDirectoryLockHelper(t, rootDir)
	publishErr := make(chan error, 1)
	go func() {
		publishErr <- publishMissingAuthKey(authRoot, "cache.key")
	}()
	select {
	case <-attemptedLock:
	case <-time.After(5 * time.Second):
		t.Fatal("fallback publisher did not attempt directory lock acquisition")
	}

	lockWasStolen := false
	select {
	case <-enteredInstall:
		lockWasStolen = true
	case <-time.After(150 * time.Millisecond):
	}
	helper.release(t)
	helper.wait(t)

	select {
	case err := <-publishErr:
		if err != nil {
			t.Fatalf("publish after live lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fallback publisher did not resume after live lock release")
	}
	if lockWasStolen {
		t.Fatal("fallback publisher entered installation while another process held the directory lock")
	}
}

func TestAnalysisCacheAuthDirectoryLockHelper(t *testing.T) {
	rootDir := os.Getenv("LOPPER_TEST_AUTH_LOCK_ROOT")
	if rootDir == "" {
		return
	}
	root, err := safeio.OpenCanonicalWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open helper auth root: %v", err)
	}
	lock, err := root.LockDirectory()
	if err != nil {
		t.Fatalf("lock helper auth root: %v", err)
	}
	if err := os.WriteFile(os.Getenv("LOPPER_TEST_AUTH_LOCK_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatalf("signal helper lock readiness: %v", err)
	}
	if os.Getenv("LOPPER_TEST_AUTH_LOCK_CRASH") == "1" {
		os.Exit(0)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Fatalf("close helper auth root: %v", err)
		}
	}()
	defer func() {
		if err := lock.Close(); err != nil {
			t.Fatalf("unlock helper auth root: %v", err)
		}
	}()
	releasePath := os.Getenv("LOPPER_TEST_AUTH_LOCK_RELEASE")
	for {
		if _, err := os.Lstat(releasePath); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect helper release signal: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type authDirectoryLockHelper struct {
	cmd         *exec.Cmd
	releasePath string
	output      bytes.Buffer
	waited      bool
}

func startAuthDirectoryLockHelper(t *testing.T, rootDir string) *authDirectoryLockHelper {
	t.Helper()
	signalDir := t.TempDir()
	helper := &authDirectoryLockHelper{
		releasePath: filepath.Join(signalDir, "release"),
	}
	helper.cmd = exec.Command(os.Args[0], "-test.run=^TestAnalysisCacheAuthDirectoryLockHelper$")
	helper.cmd.Env = append(os.Environ(), "LOPPER_TEST_AUTH_LOCK_ROOT="+rootDir, "LOPPER_TEST_AUTH_LOCK_READY="+filepath.Join(signalDir, "ready"), "LOPPER_TEST_AUTH_LOCK_RELEASE="+helper.releasePath)
	helper.cmd.Stdout = &helper.output
	helper.cmd.Stderr = &helper.output
	if err := helper.cmd.Start(); err != nil {
		t.Fatalf("start auth directory lock helper: %v", err)
	}
	t.Cleanup(func() {
		if helper.waited {
			return
		}
		killErr := helper.cmd.Process.Kill()
		waitErr := helper.cmd.Wait()
		var exitErr *exec.ExitError
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			t.Errorf("kill auth directory lock helper: %v", killErr)
		}
		if waitErr != nil && !errors.As(waitErr, &exitErr) {
			t.Errorf("wait for killed auth directory lock helper: %v", waitErr)
		}
	})
	waitForAuthLockHelperReady(t, filepath.Join(signalDir, "ready"), helper)
	return helper
}

func runAuthDirectoryLockHelperToCrash(t *testing.T, rootDir string) {
	t.Helper()
	signalDir := t.TempDir()
	var output bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestAnalysisCacheAuthDirectoryLockHelper$")
	cmd.Env = append(os.Environ(), "LOPPER_TEST_AUTH_LOCK_ROOT="+rootDir, "LOPPER_TEST_AUTH_LOCK_READY="+filepath.Join(signalDir, "ready"), "LOPPER_TEST_AUTH_LOCK_RELEASE="+filepath.Join(signalDir, "release"), "LOPPER_TEST_AUTH_LOCK_CRASH=1")
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("run crashing auth directory lock helper: %v\n%s", err, output.String())
	}
	if _, err := os.Lstat(filepath.Join(signalDir, "ready")); err != nil {
		t.Fatalf("crashing helper did not acquire the directory lock: %v\n%s", err, output.String())
	}
}

func waitForAuthLockHelperReady(t *testing.T, readyPath string, helper *authDirectoryLockHelper) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(readyPath); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect auth lock helper readiness: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("auth lock helper did not become ready\n%s", helper.output.String())
}

func (h *authDirectoryLockHelper) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(h.releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release auth directory lock helper: %v", err)
	}
}

func (h *authDirectoryLockHelper) wait(t *testing.T) {
	t.Helper()
	if h.waited {
		return
	}
	h.waited = true
	if err := h.cmd.Wait(); err != nil {
		t.Fatalf("wait for auth directory lock helper: %v\n%s", err, h.output.String())
	}
}

func requireAuthFallbackPlatform(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("kernel-backed cache auth fallback is not supported on this platform")
	}
}

func authDirectoryEntryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
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
