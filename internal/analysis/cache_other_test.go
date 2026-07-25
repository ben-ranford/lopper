//go:build !windows

package analysis

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCacheStorageRootAcceptsUnixExplicitPathsThatLookWindowsLike(t *testing.T) {
	repo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(cwd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	})

	workDir := t.TempDir()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir workdir: %v", err)
	}

	for _, rawPath := range []string{
		`C:cache`,
		`\cache`,
		`\\server`,
		`C:\cache `,
		`C:\cache.`,
		`\\server\share\dir `,
		`\\server\share\dir.`,
		`cache `,
		`cache.\child`,
		`CON`,
		`sub\NUL.txt`,
	} {
		options := resolvedCacheOptions{
			Path:         rawPath,
			ExplicitPath: true,
			ReadOnly:     true,
		}
		resolved, err := resolveCacheStorageRoot(options, repo, canonicalRepo)
		if err != nil {
			t.Fatalf("resolveCacheStorageRoot(%q): %v", rawPath, err)
		}
		want, err := filepath.Abs(rawPath)
		if err != nil {
			t.Fatalf("Abs(%q): %v", rawPath, err)
		}
		if resolved != want {
			t.Fatalf("resolveCacheStorageRoot(%q) = %q, want %q", rawPath, resolved, want)
		}
	}
}

func TestCanonicalUserCacheDirAcceptsUnixNamesUnsafeOnWindows(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{`cache `, `cache.\child`, `CON`, `sub\NUL.txt`} {
		cacheDir := filepath.Join(parent, name)
		if err := os.Mkdir(cacheDir, 0o750); err != nil {
			t.Fatalf("mkdir user cache dir %q: %v", name, err)
		}
		got, err := canonicalUserCacheDir(cacheDir, false)
		if err != nil {
			t.Fatalf("canonicalUserCacheDir(%q): %v", name, err)
		}
		want, err := filepath.EvalSymlinks(cacheDir)
		if err != nil {
			t.Fatalf("resolve user cache dir %q: %v", name, err)
		}
		if got != want {
			t.Fatalf("canonicalUserCacheDir(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAnalysisCacheAuthKeyNameUsesSameStorageIdentityForCaseAliases(t *testing.T) {
	parent := t.TempDir()
	storageRoot := filepath.Join(parent, "StorageRoot")
	if err := os.Mkdir(storageRoot, 0o750); err != nil {
		t.Fatalf("mkdir storage root: %v", err)
	}
	caseAlias := filepath.Join(parent, "storageroot")
	storageInfo, err := os.Stat(storageRoot)
	if err != nil {
		t.Fatalf("stat storage root: %v", err)
	}
	aliasInfo, err := os.Stat(caseAlias)
	if os.IsNotExist(err) {
		t.Skip("filesystem is case-sensitive")
	}
	if err != nil {
		t.Fatalf("stat case alias: %v", err)
	}
	if !os.SameFile(storageInfo, aliasInfo) {
		t.Skip("case spellings resolve to different storage directories")
	}

	storageKey, err := analysisCacheAuthKeyName(storageRoot, storageInfo)
	if err != nil {
		t.Fatalf("derive storage auth-key identity: %v", err)
	}
	aliasKey, err := analysisCacheAuthKeyName(caseAlias, aliasInfo)
	if err != nil {
		t.Fatalf("derive alias auth-key identity: %v", err)
	}
	if storageKey != aliasKey {
		t.Fatalf("expected one auth-key identity for the same storage directory, got %q and %q", storageKey, aliasKey)
	}
}

func TestFormatStorageDirectoryIdentityPreservesNativeSignedAndUnsignedValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{
			name: "positive values retain existing hexadecimal encoding",
			got:  formatStorageDirectoryIdentity(int32(0x2a), uint64(0xff)),
			want: "device:2a;inode:ff",
		},
		{
			name: "negative signed values retain sign without overflow",
			got:  formatStorageDirectoryIdentity(int32(-0x2a), int64(-0xff)),
			want: "device:-2a;inode:-ff",
		},
		{
			name: "maximum unsigned values remain lossless",
			got:  formatStorageDirectoryIdentity(^uint32(0), ^uint64(0)),
			want: "device:ffffffff;inode:ffffffffffffffff",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("formatStorageDirectoryIdentity() = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestFormatStorageDirectoryIdentityDistinguishesEveryTupleComponent(t *testing.T) {
	identities := []string{
		formatStorageDirectoryIdentity(int32(1), int64(23)),
		formatStorageDirectoryIdentity(int32(12), int64(3)),
		formatStorageDirectoryIdentity(int32(-1), int64(23)),
		formatStorageDirectoryIdentity(uint32(1), uint64(24)),
	}
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if _, exists := seen[identity]; exists {
			t.Fatalf("duplicate storage identity encoding %q from distinct device/inode tuples", identity)
		}
		seen[identity] = struct{}{}
	}
}

func TestAnalysisCacheAuthKeyNameUsesSameStorageIdentityForDotAlias(t *testing.T) {
	storageRoot := t.TempDir()
	alias := storageRoot + string(os.PathSeparator) + "."
	storageInfo, err := os.Stat(storageRoot)
	if err != nil {
		t.Fatalf("stat storage root: %v", err)
	}
	aliasInfo, err := os.Stat(alias)
	if err != nil {
		t.Fatalf("stat storage root alias: %v", err)
	}
	if !os.SameFile(storageInfo, aliasInfo) {
		t.Fatal("dot alias did not resolve to the storage directory")
	}

	storageKey, err := analysisCacheAuthKeyName(storageRoot, storageInfo)
	if err != nil {
		t.Fatalf("derive storage auth-key identity: %v", err)
	}
	aliasKey, err := analysisCacheAuthKeyName(alias, aliasInfo)
	if err != nil {
		t.Fatalf("derive alias auth-key identity: %v", err)
	}
	if storageKey != aliasKey {
		t.Fatalf("expected one auth-key identity for dot aliases, got %q and %q", storageKey, aliasKey)
	}
}

func TestAnalysisCacheAuthKeyNameDistinguishesStorageDirectories(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	infoA, err := os.Stat(rootA)
	if err != nil {
		t.Fatalf("stat first storage root: %v", err)
	}
	infoB, err := os.Stat(rootB)
	if err != nil {
		t.Fatalf("stat second storage root: %v", err)
	}
	if os.SameFile(infoA, infoB) {
		t.Fatal("test storage directories unexpectedly share one filesystem identity")
	}

	keyA, err := analysisCacheAuthKeyName(rootA, infoA)
	if err != nil {
		t.Fatalf("derive first storage auth-key identity: %v", err)
	}
	keyB, err := analysisCacheAuthKeyName(rootB, infoB)
	if err != nil {
		t.Fatalf("derive second storage auth-key identity: %v", err)
	}
	if keyA == keyB {
		t.Fatalf("expected distinct auth-key identities, got %q", keyA)
	}
}
