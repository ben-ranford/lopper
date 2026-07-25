//go:build windows

package safeio

import (
	"strings"
	"testing"
)

func TestRejectUnsupportedWindowsRootAllowsOrdinaryDriveAndUNCPaths(t *testing.T) {
	for _, rawPath := range []string{`C:\cache`, `C:\cache\child`, `\\server\share`, `\\server\share\cache`} {
		if err := rejectUnsupportedWindowsRoot(rawPath); err != nil {
			t.Fatalf("rejectUnsupportedWindowsRoot(%q): %v", rawPath, err)
		}
	}
}

func TestRejectUnsupportedWindowsRootRejectsTrailingDotSpaceAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "drive path trailing dot", path: `C:\cache.`},
		{name: "drive path trailing space", path: `C:\cache `},
		{name: "unc child trailing space", path: `\\server\share\dir `},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectUnsupportedWindowsRoot(tc.path)
			if err == nil || !strings.Contains(err.Error(), "trailing dot or space aliases") {
				t.Fatalf("expected trailing dot/space alias rejection, got %v", err)
			}
		})
	}
}

func TestOpenRootNoFollowRejectsUnsupportedWindowsRoots(t *testing.T) {
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
		{name: "drive trailing dot alias", path: `C:\cache.`, want: "trailing dot or space aliases"},
		{name: "drive trailing space alias", path: `C:\cache `, want: "trailing dot or space aliases"},
		{name: "unc trailing space alias", path: `\\server\share\dir `, want: "trailing dot or space aliases"},
		{name: "drive reserved nul", path: `C:\cache\NUL.txt`, want: "reserved DOS device names"},
		{name: "drive reserved clock", path: `C:\cache\CLOCK$...`, want: "reserved DOS device names"},
		{name: "unc reserved con", path: `\\server\share\dir\CON.log`, want: "reserved DOS device names"},
		{name: "unc reserved lpt9", path: `\\server\share\LPT9 `, want: "reserved DOS device names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := (&osFileSystem{}).OpenRootNoFollow(tc.path)
			if root != nil {
				if closeErr := root.Close(); closeErr != nil {
					t.Fatalf("close unexpected root: %v", closeErr)
				}
				t.Fatal("expected unsupported Windows root to remain nil")
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestOpenCanonicalWriteRootRejectsRawUnsupportedWindowsRootsBeforeAbs(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "drive relative", path: `C:cache`, want: "drive-relative"},
		{name: "rooted without drive", path: `\cache`, want: "include a drive or UNC share"},
		{name: "drive trailing dot alias", path: `C:\cache.`, want: "trailing dot or space aliases"},
		{name: "drive trailing space alias", path: `C:\cache `, want: "trailing dot or space aliases"},
		{name: "drive reserved name", path: `C:\cache\NUL.txt`, want: "reserved DOS device names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertOpenCanonicalWriteRootRejectsRawUnsupportedWindowsRoot(t, tc.path, tc.want)
		})
	}
}

func assertOpenCanonicalWriteRootRejectsRawUnsupportedWindowsRoot(t *testing.T, rawPath, want string) {
	t.Helper()

	absCalls := 0
	openCalls := 0
	withFileSystem(t, &fakeFileSystem{
		abs: func(string) (string, error) {
			absCalls++
			return `C:\normalized`, nil
		},
		openRootNoFollow: func(string) (Root, error) {
			openCalls++
			return &fakeRoot{close: closeWithoutError}, nil
		},
	})

	root, err := OpenCanonicalWriteRoot(rawPath)
	if root != nil {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close unexpected write root: %v", closeErr)
		}
		t.Fatal("expected canonical write root to remain nil")
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected %q error, got %v", want, err)
	}
	if absCalls != 0 {
		t.Fatalf("expected raw validation before Abs, got %d calls", absCalls)
	}
	if openCalls != 0 {
		t.Fatalf("expected raw validation before OpenRootNoFollow, got %d calls", openCalls)
	}
}
