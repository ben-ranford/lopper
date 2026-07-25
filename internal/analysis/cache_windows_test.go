//go:build windows

package analysis

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withTestAnalysisCachePathFuncs(t *testing.T, absFn func(string) (string, error), mkdirAllFn func(string, os.FileMode) error, evalSymlinksFn func(string) (string, error)) {
	t.Helper()
	prevAbs := analysisCacheAbsFn
	prevMkdirAll := analysisCacheMkdirAllFn
	prevEvalSymlinks := analysisCacheEvalSymlinksFn
	analysisCacheAbsFn = absFn
	analysisCacheMkdirAllFn = mkdirAllFn
	analysisCacheEvalSymlinksFn = evalSymlinksFn
	t.Cleanup(func() {
		analysisCacheAbsFn = prevAbs
		analysisCacheMkdirAllFn = prevMkdirAll
		analysisCacheEvalSymlinksFn = prevEvalSymlinks
	})
}

func TestValidateExplicitCachePathRejectsTrailingDotSpaceAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "drive trailing space", path: `C:\cache `},
		{name: "drive trailing dot", path: `C:\cache.`},
		{name: "unc child trailing space", path: `\\server\share\dir `},
		{name: "unc child trailing dot", path: `\\server\share\dir.`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExplicitCachePath(tc.path)
			if err == nil || !strings.Contains(err.Error(), "trailing dot or space aliases") {
				t.Fatalf("expected trailing dot/space alias rejection, got %v", err)
			}
		})
	}
}

func TestResolveCacheStorageRootRejectsUnsupportedWindowsExplicitPaths(t *testing.T) {
	repo := t.TempDir()
	canonicalRepo := repo

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "drive relative", path: `C:cache`, want: "drive-relative"},
		{name: "rooted without drive", path: `\cache`, want: "include a drive or UNC share"},
		{name: "verbatim drive", path: `\\?\C:\cache`, want: "device or namespace forms"},
		{name: "local device", path: `\\.\C:\cache`, want: "device or namespace forms"},
		{name: "object manager", path: `\??\C:\cache`, want: "device or namespace forms"},
		{name: "globalroot", path: `\\?\GLOBALROOT\Device\HarddiskVolume1\cache`, want: "device or namespace forms"},
		{name: "incomplete unc", path: `\\server`, want: "UNC host and share"},
		{name: "drive trailing space alias", path: `C:\cache `, want: "trailing dot or space aliases"},
		{name: "drive trailing dot alias", path: `C:\cache.`, want: "trailing dot or space aliases"},
		{name: "unc trailing space alias", path: `\\server\share\dir `, want: "trailing dot or space aliases"},
		{name: "unc trailing dot alias", path: `\\server\share\dir.`, want: "trailing dot or space aliases"},
		{name: "drive reserved nul", path: `C:\cache\NUL.txt`, want: "reserved DOS device names"},
		{name: "drive reserved con", path: `C:\cache\sub\con `, want: "reserved DOS device names"},
		{name: "unc reserved aux", path: `\\server\share\AUX.cfg`, want: "reserved DOS device names"},
		{name: "unc reserved lpt9", path: `\\server\share\dir\LPT9...`, want: "reserved DOS device names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := resolvedCacheOptions{
				Path:         tc.path,
				ExplicitPath: true,
				ReadOnly:     true,
			}
			absCalls := 0
			mkdirAllCalls := 0
			evalCalls := 0
			withTestAnalysisCachePathFuncs(t,
				func(path string) (string, error) {
					absCalls++
					return filepath.Abs(path)
				},
				func(path string, perm os.FileMode) error {
					mkdirAllCalls++
					return os.MkdirAll(path, perm)
				},
				func(path string) (string, error) {
					evalCalls++
					return filepath.EvalSymlinks(path)
				},
			)
			if _, err := resolveCacheStorageRoot(options, repo, canonicalRepo); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q rejection for %q, got %v", tc.want, tc.path, err)
			}
			if absCalls != 0 {
				t.Fatalf("expected raw alias rejection before Abs, got %d calls", absCalls)
			}
			if mkdirAllCalls != 0 {
				t.Fatalf("expected raw alias rejection before MkdirAll, got %d calls", mkdirAllCalls)
			}
			if evalCalls != 0 {
				t.Fatalf("expected raw alias rejection before EvalSymlinks, got %d calls", evalCalls)
			}
		})
	}
}

func TestNewAnalysisCacheRejectsUnsupportedWindowsExplicitPath(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "namespace", path: `\\?\GLOBALROOT\Device\HarddiskVolume1\cache`, want: "device or namespace forms"},
		{name: "drive trailing space alias", path: `C:\cache `, want: "trailing dot or space aliases"},
		{name: "drive trailing dot alias", path: `C:\cache.`, want: "trailing dot or space aliases"},
		{name: "unc trailing space alias", path: `\\server\share\dir `, want: "trailing dot or space aliases"},
		{name: "unc trailing dot alias", path: `\\server\share\dir.`, want: "trailing dot or space aliases"},
		{name: "drive reserved nul", path: `C:\cache\NUL.txt`, want: "reserved DOS device names"},
		{name: "unc reserved con", path: `\\server\share\dir\CON.log`, want: "reserved DOS device names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			absCalls := 0
			mkdirAllCalls := 0
			evalCalls := 0
			withTestAnalysisCachePathFuncs(t,
				func(path string) (string, error) {
					absCalls++
					return filepath.Abs(path)
				},
				func(path string, perm os.FileMode) error {
					mkdirAllCalls++
					return os.MkdirAll(path, perm)
				},
				func(path string) (string, error) {
					evalCalls++
					return "", errors.New("unexpected EvalSymlinks call")
				},
			)
			cache := newAnalysisCache(Request{Cache: &CacheOptions{
				Enabled: true,
				Path:    tc.path,
			}}, repo)
			if cache.cacheable {
				t.Fatal("expected cache to fail closed for unsupported Windows explicit path")
			}
			warnings := cache.takeWarnings()
			if len(warnings) != 1 || !strings.Contains(warnings[0], tc.want) {
				t.Fatalf("expected %q warning, got %#v", tc.want, warnings)
			}
			if absCalls != 0 {
				t.Fatalf("expected raw alias rejection before Abs, got %d calls", absCalls)
			}
			if mkdirAllCalls != 0 {
				t.Fatalf("expected raw alias rejection before MkdirAll, got %d calls", mkdirAllCalls)
			}
			if evalCalls != 0 {
				t.Fatalf("expected raw alias rejection before EvalSymlinks, got %d calls", evalCalls)
			}
		})
	}
}
