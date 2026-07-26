package analysis

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestReadAnalysisCacheAuthKeyAcceptsOwnerOnlyWindowsDACL(t *testing.T) {
	root, err := safeio.OpenWriteRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open auth-key root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close auth-key root: %v", closeErr)
		}
	})
	tempName, tempFile, err := root.CreatePrivateTempFile()
	if err != nil {
		t.Fatalf("create owner-only auth key: %v", err)
	}
	encodedKey := bytes.Repeat([]byte("ab"), analysisCacheAuthKeyLength)
	if _, err := tempFile.Write(encodedKey); err != nil {
		t.Fatalf("write owner-only auth key: %v", err)
	}
	if err := tempFile.Sync(); err != nil {
		t.Fatalf("sync owner-only auth key: %v", err)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatalf("close owner-only auth key: %v", err)
	}
	if err := root.Rename(tempName, "cache.key"); err != nil {
		t.Fatalf("publish owner-only auth key: %v", err)
	}

	key, err := readAnalysisCacheAuthKey(root, "cache.key", false)
	if err != nil {
		t.Fatalf("read owner-only auth key: %v", err)
	}
	if !bytes.Equal(key, bytes.Repeat([]byte{0xab}, analysisCacheAuthKeyLength)) {
		t.Fatalf("decoded owner-only auth key = %x", key)
	}
}

func TestCanonicalUserCacheDirRejectsRawUnsupportedWindowsPathsBeforeResolution(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "drive relative", path: `C:cache`, want: "drive-relative"},
		{name: "rooted without drive", path: `\cache`, want: "include a drive or UNC share"},
		{name: "rooted without drive forward slash", path: `/cache`, want: "include a drive or UNC share"},
		{name: "namespace", path: `\\?\C:\cache`, want: "device or namespace forms"},
		{name: "incomplete unc", path: `\\server`, want: "UNC host and share"},
		{name: "trailing space leaf", path: `cache `, want: "trailing dot or space aliases"},
		{name: "trailing dot nested", path: `cache.\child`, want: "trailing dot or space aliases"},
		{name: "trailing space nested", path: `sub\cache \child`, want: "trailing dot or space aliases"},
		{name: "reserved con leaf", path: `CON`, want: "reserved DOS device names"},
		{name: "reserved nul nested", path: `sub\NUL.txt`, want: "reserved DOS device names"},
		{name: "reserved superscript device", path: `sub\COM¹.txt`, want: "reserved DOS device names"},
		{name: "reserved conin device", path: `sub\CONIN$.txt`, want: "reserved DOS device names"},
		{name: "reserved nul stream", path: `sub\NUL:stream`, want: "reserved DOS device names"},
		{name: "reserved superscript stream", path: `sub\COM¹:stream`, want: "reserved DOS device names"},
		{name: "reserved conin stream", path: `sub\CONIN$:stream`, want: "reserved DOS device names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousAbs := analysisCacheAuthAbsFn
			previousMkdirAllDurable := analysisCacheAuthMkdirAllDurableFn
			previousOpenRoot := analysisCacheAuthOpenRootFn
			absCalls := 0
			mkdirAllDurableCalls := 0
			openRootCalls := 0
			analysisCacheAuthAbsFn = func(string) (string, error) {
				absCalls++
				return `C:\normalized`, nil
			}
			analysisCacheAuthMkdirAllDurableFn = func(*safeio.WriteRoot, string, os.FileMode) error {
				mkdirAllDurableCalls++
				return errors.New("unexpected durable mkdir")
			}
			analysisCacheAuthOpenRootFn = func(string) (*safeio.WriteRoot, error) {
				openRootCalls++
				return nil, errors.New("unexpected canonical root open")
			}
			t.Cleanup(func() {
				analysisCacheAuthAbsFn = previousAbs
				analysisCacheAuthMkdirAllDurableFn = previousMkdirAllDurable
				analysisCacheAuthOpenRootFn = previousOpenRoot
			})

			_, err := canonicalUserCacheDir(tc.path, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q rejection, got %v", tc.want, err)
			}
			if absCalls != 0 {
				t.Fatalf("expected rejection before Abs, got %d calls", absCalls)
			}
			if mkdirAllDurableCalls != 0 {
				t.Fatalf("expected rejection before MkdirAllDurable, got %d calls", mkdirAllDurableCalls)
			}
			if openRootCalls != 0 {
				t.Fatalf("expected rejection before OpenCanonicalWriteRoot, got %d calls", openRootCalls)
			}
		})
	}
}

func TestValidateRawUserCacheDirAllowsValidRelativeWindowsPaths(t *testing.T) {
	for _, rawPath := range []string{`.`, `..\cache`, `cache`, `cache.dir\child`, `cache dir\child`, `sub\COM10.txt`} {
		if err := validateRawUserCacheDir(rawPath); err != nil {
			t.Fatalf("validateRawUserCacheDir(%q): %v", rawPath, err)
		}
	}
}

func TestValidateRawUserCacheDirRejectsUnsupportedWindowsKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "drive relative", path: `C:cache`, want: "drive-relative"},
		{name: "rooted without drive", path: `\cache`, want: "include a drive or UNC share"},
		{name: "rooted without drive forward slash", path: `/cache`, want: "include a drive or UNC share"},
		{name: "namespace", path: `\\?\C:\cache`, want: "device or namespace forms"},
		{name: "incomplete unc", path: `\\server`, want: "UNC host and share"},
		{name: "superscript reserved", path: `sub\LPT².log`, want: "reserved DOS device names"},
		{name: "conout reserved", path: `sub\CONOUT$.log`, want: "reserved DOS device names"},
		{name: "nul stream reserved", path: `sub\NUL:stream`, want: "reserved DOS device names"},
		{name: "superscript stream reserved", path: `sub\COM¹:stream`, want: "reserved DOS device names"},
		{name: "conin stream reserved", path: `sub\CONIN$:stream`, want: "reserved DOS device names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRawUserCacheDir(tc.path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestPathAtOrBelowWindowsHardeningCases(t *testing.T) {
	testCases := []struct {
		name string
		path string
		root string
		want bool
	}{
		{
			name: "drive relative child fails closed",
			path: `C:root\child`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "drive relative traversal fails closed",
			path: `C:..\root`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "rooted without drive fails closed",
			path: `\root\child`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "mixed separators stay contained",
			path: `C:/root\child/grandchild`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "same drive exact root",
			path: `C:\root`,
			root: `c:\root`,
			want: true,
		},
		{
			name: "same drive descendant",
			path: `C:\root\child`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "cross drive rejected",
			path: `D:\root\child`,
			root: `C:\root`,
			want: false,
		},
		{
			name: "unc exact share path",
			path: `\\server\share\root`,
			root: `\\SERVER\SHARE\root`,
			want: true,
		},
		{
			name: "unc descendant",
			path: `\\server\share\root\child`,
			root: `\\server\share\root`,
			want: true,
		},
		{
			name: "unc outside sibling",
			path: `\\server\share\other`,
			root: `\\server\share\root`,
			want: false,
		},
		{
			name: "unc traversal outside root rejected",
			path: `\\server\share\root\..\other`,
			root: `\\server\share\root`,
			want: false,
		},
		{
			name: "unc different share rejected",
			path: `\\server\other\root`,
			root: `\\server\share\root`,
			want: false,
		},
		{
			name: "verbatim drive exact root fails closed",
			path: `\\?\C:\root`,
			root: `c:\root`,
			want: true,
		},
		{
			name: "verbatim drive mixed separators descendant fails closed",
			path: `\\?\C:/root\child/grandchild`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "verbatim drive equivalent repo path fails closed",
			path: `\\?\C:\repo\cache`,
			root: `C:\repo`,
			want: true,
		},
		{
			name: "verbatim drive different drive still fails closed",
			path: `\\?\D:\root\child`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "verbatim unc exact share path fails closed",
			path: `\\?\UNC\server\share\root`,
			root: `\\SERVER\SHARE\root`,
			want: true,
		},
		{
			name: "verbatim unc mixed case descendant fails closed",
			path: `\\?\UNC\Server\Share\root/child`,
			root: `\\server\share\root`,
			want: true,
		},
		{
			name: "verbatim unc different share still fails closed",
			path: `\\?\UNC\server\other\root`,
			root: `\\server\share\root`,
			want: true,
		},
		{
			name: "device namespace path fails closed",
			path: `\\.\C:\root\child`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "nt object manager path fails closed",
			path: `\??\C:\root\child`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "globalroot device path fails closed",
			path: `\\?\GLOBALROOT\Device\HarddiskVolume1\root\child`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "globalroot forward slashes fail closed",
			path: `//?/GLOBALROOT/Device/HarddiskVolume1/root/child`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "reserved device name fails closed",
			path: `\\.\NUL`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "drive path reserved component fails closed",
			path: `C:\root\cache\NUL.txt`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "unc path reserved component fails closed",
			path: `\\server\share\root\AUX.cfg`,
			root: `\\server\share\root`,
			want: true,
		},
		{
			name: "benign windows names remain comparable",
			path: `C:\root\console\cache`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "incomplete verbatim prefix fails closed",
			path: `\\?\`,
			root: `C:\root`,
			want: true,
		},
		{
			name: "incomplete verbatim unc prefix fails closed",
			path: `\\?\UNC\server`,
			root: `\\server\share\root`,
			want: true,
		},
		{
			name: "ambiguous root fails closed",
			path: `C:\root\child`,
			root: `\\.\C:\root`,
			want: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathAtOrBelow(tc.path, tc.root); got != tc.want {
				t.Fatalf("pathAtOrBelow(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.want)
			}
		})
	}
}

func TestResolveAuthKeyRejectsWindowsAtoBtoAStorageSwapBeforeSelectingCrossCacheKey(t *testing.T) {
	setTestAnalysisCacheUserCacheDir(t)
	fixture := newWindowsStorageIdentitySwapFixture(t)

	keyNameA, err := analysisCacheAuthKeyName(fixture.pathA, fixture.infoA)
	if err != nil {
		t.Fatalf("derive cache A auth key name: %v", err)
	}
	keyNameB, err := analysisCacheAuthKeyName(fixture.pathB, fixture.infoB)
	if err != nil {
		t.Fatalf("derive cache B auth key name: %v", err)
	}
	if keyNameA == keyNameB {
		t.Fatalf("expected distinct auth key names for cache A and B, got %q", keyNameA)
	}

	previousOpen := analysisCacheOpenStorageIdentityFileFn
	fixture.open = previousOpen
	analysisCacheOpenStorageIdentityFileFn = fixture.openBThenRestoreA
	previousRead := analysisCacheReadAuthKeyFn
	bKey := bytes.Repeat([]byte{0xb2}, analysisCacheAuthKeyLength)
	var requestedKeyNames []string
	analysisCacheReadAuthKeyFn = func(_ *safeio.WriteRoot, keyName string, _ bool) ([]byte, error) {
		requestedKeyNames = append(requestedKeyNames, keyName)
		if keyName == keyNameB {
			return append([]byte(nil), bKey...), nil
		}
		return nil, fmt.Errorf("unexpected auth key lookup: %s", keyName)
	}
	t.Cleanup(func() {
		analysisCacheOpenStorageIdentityFileFn = previousOpen
		analysisCacheReadAuthKeyFn = previousRead
	})

	cache := &analysisCache{
		options: resolvedCacheOptions{
			Enabled:      true,
			Path:         fixture.pathA,
			ExplicitPath: true,
		},
		repoRoot:        t.TempDir(),
		storageRoot:     fixture.pathA,
		storageRootInfo: fixture.infoA,
	}
	key, err := cache.resolveAuthKey()
	if !errors.Is(err, safeio.ErrFileChanged) {
		t.Fatalf("expected storage identity change error, got key %x and error %v", key, err)
	}
	if !strings.Contains(err.Error(), fixture.pathA) ||
		!strings.Contains(err.Error(), "changed while resolving auth identity") {
		t.Fatalf("expected storage path and identity context, got %v", err)
	}
	if len(key) != 0 || len(cache.authKey) != 0 {
		t.Fatalf("expected no live auth key after identity mismatch, got result %x and cache %x", key, cache.authKey)
	}
	if len(requestedKeyNames) != 0 {
		t.Fatalf("expected identity mismatch before any auth key lookup, requested %v", requestedKeyNames)
	}
	if got, want := strings.Join(fixture.events, ","), "A->away,B->A,open:B-at-A,B-at-A->B,away->A"; got != want {
		t.Fatalf("unexpected swap events: got %q, want %q", got, want)
	}
	fixture.assertRestored(t)
}

type windowsStorageIdentitySwapFixture struct {
	pathA      string
	pathB      string
	displacedA string
	infoA      fs.FileInfo
	infoB      fs.FileInfo
	events     []string
	open       func(string) (*os.File, error)
}

func newWindowsStorageIdentitySwapFixture(t *testing.T) *windowsStorageIdentitySwapFixture {
	t.Helper()
	parent := t.TempDir()
	fixture := &windowsStorageIdentitySwapFixture{
		pathA:      filepath.Join(parent, "cache-a"),
		pathB:      filepath.Join(parent, "cache-b"),
		displacedA: filepath.Join(parent, "cache-a-displaced"),
	}
	if err := os.Mkdir(fixture.pathA, 0o750); err != nil {
		t.Fatalf("create cache A: %v", err)
	}
	if err := os.Mkdir(fixture.pathB, 0o750); err != nil {
		t.Fatalf("create cache B: %v", err)
	}
	var err error
	fixture.infoA, err = os.Stat(fixture.pathA)
	if err != nil {
		t.Fatalf("stat cache A: %v", err)
	}
	fixture.infoB, err = os.Stat(fixture.pathB)
	if err != nil {
		t.Fatalf("stat cache B: %v", err)
	}
	return fixture
}

func (f *windowsStorageIdentitySwapFixture) openBThenRestoreA(storageRoot string) (*os.File, error) {
	if !strings.EqualFold(filepath.Clean(storageRoot), filepath.Clean(f.pathA)) {
		return nil, fmt.Errorf("unexpected storage identity path: %s", storageRoot)
	}
	if err := f.swapAForB(); err != nil {
		return nil, err
	}
	opened, openErr := f.openBAtA(storageRoot)
	restoreErr := f.restoreA()
	if openErr == nil && restoreErr == nil {
		return opened, nil
	}
	var closeErr error
	if opened != nil {
		closeErr = opened.Close()
	}
	return nil, errors.Join(openErr, restoreErr, closeErr)
}

func (f *windowsStorageIdentitySwapFixture) swapAForB() error {
	if err := os.Rename(f.pathA, f.displacedA); err != nil {
		return fmt.Errorf("move cache A away: %w", err)
	}
	f.events = append(f.events, "A->away")
	if err := os.Rename(f.pathB, f.pathA); err != nil {
		rollbackErr := os.Rename(f.displacedA, f.pathA)
		return errors.Join(
			fmt.Errorf("move cache B to cache A pathname: %w", err),
			wrapWindowsStorageSwapError("restore cache A after failed swap", rollbackErr),
		)
	}
	f.events = append(f.events, "B->A")
	return nil
}

func (f *windowsStorageIdentitySwapFixture) openBAtA(storageRoot string) (*os.File, error) {
	opened, err := f.open(storageRoot)
	if err != nil {
		return nil, fmt.Errorf("open cache B through cache A pathname: %w", err)
	}
	openedInfo, err := opened.Stat()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("stat cache B through cache A pathname: %w", err),
			opened.Close(),
		)
	}
	if !os.SameFile(f.infoB, openedInfo) {
		return nil, errors.Join(
			fmt.Errorf("cache A pathname did not resolve to cache B"),
			opened.Close(),
		)
	}
	f.events = append(f.events, "open:B-at-A")
	return opened, nil
}

func (f *windowsStorageIdentitySwapFixture) restoreA() error {
	if err := os.Rename(f.pathA, f.pathB); err != nil {
		return fmt.Errorf("restore cache B pathname: %w", err)
	}
	f.events = append(f.events, "B-at-A->B")
	if err := os.Rename(f.displacedA, f.pathA); err != nil {
		rollbackErr := os.Rename(f.pathB, f.pathA)
		return errors.Join(
			fmt.Errorf("restore cache A pathname: %w", err),
			wrapWindowsStorageSwapError("restore cache B to cache A pathname after failed restore", rollbackErr),
		)
	}
	f.events = append(f.events, "away->A")
	return nil
}

func (f *windowsStorageIdentitySwapFixture) assertRestored(t *testing.T) {
	t.Helper()
	infoA, err := os.Stat(f.pathA)
	if err != nil {
		t.Fatalf("stat restored cache A: %v", err)
	}
	if !os.SameFile(f.infoA, infoA) {
		t.Fatal("cache A pathname did not return to cache A")
	}
	infoB, err := os.Stat(f.pathB)
	if err != nil {
		t.Fatalf("stat restored cache B: %v", err)
	}
	if !os.SameFile(f.infoB, infoB) {
		t.Fatal("cache B pathname did not return to cache B")
	}
}

func wrapWindowsStorageSwapError(context string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", context, err)
}
