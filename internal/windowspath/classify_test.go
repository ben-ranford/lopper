package windowspath

import "testing"

func TestClassifyReturnsExpectedKindsAndParts(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want Classification
	}{
		{name: "empty", path: "", want: Classification{}},
		{name: "drive absolute", path: `C:\cache\child`, want: Classification{Kind: KindDriveAbsolute, Volume: "C:", Path: "cache\\child"}},
		{name: "drive relative", path: `C:cache\child`, want: Classification{Kind: KindDriveRelative, Volume: "C:", Path: `cache\child`}},
		{name: "rooted without drive", path: `\cache\child`, want: Classification{Kind: KindRootedWithoutDrive, Path: `cache\child`}},
		{name: "namespace", path: `\\.\pipe\cache`, want: Classification{Kind: KindAmbiguous}},
		{name: "unc absolute mixed separators", path: `\\server/share\cache/child`, want: Classification{Kind: KindUNCAbsolute, Volume: `\\server\share`, Path: `cache\child`}},
		{name: "unc incomplete", path: `\\server`, want: Classification{Kind: KindUNCIncomplete}},
		{name: "unix style ignored", path: `/tmp/cache`, want: Classification{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.path); got != tc.want {
				t.Fatalf("Classify(%q) = %#v, want %#v", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassificationIsAbsolute(t *testing.T) {
	for _, tc := range []struct {
		name string
		info Classification
		want bool
	}{
		{name: "drive absolute", info: Classification{Kind: KindDriveAbsolute}, want: true},
		{name: "unc absolute", info: Classification{Kind: KindUNCAbsolute}, want: true},
		{name: "drive relative", info: Classification{Kind: KindDriveRelative}, want: false},
		{name: "ambiguous", info: Classification{Kind: KindAmbiguous}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.IsAbsolute(); got != tc.want {
				t.Fatalf("Classification(%#v).IsAbsolute() = %v, want %v", tc.info, got, tc.want)
			}
		})
	}
}

func TestCleanNormalizesMixedSeparators(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "relative path", path: `cache\reports/../final.txt`, want: "/cache/final.txt"},
		{name: "empty path", path: "", want: "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Clean(tc.path); got != tc.want {
				t.Fatalf("Clean(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsReservedDOSName(t *testing.T) {
	for _, tc := range []struct {
		name      string
		component string
		want      bool
	}{
		{name: "nul", component: "NUL", want: true},
		{name: "con with extension", component: "con.txt", want: true},
		{name: "aux with trailing dots", component: "AUX...", want: true},
		{name: "clock with trailing space", component: "clock$ ", want: true},
		{name: "com9 with extension and spaces", component: "com9 .log", want: true},
		{name: "lpt1 lowercase", component: "lpt1", want: true},
		{name: "trimmed empty dots", component: "...", want: false},
		{name: "trimmed empty spaces", component: "   ", want: false},
		{name: "console benign", component: "console", want: false},
		{name: "cache benign", component: "cache", want: false},
		{name: "com10 benign", component: "com10", want: false},
		{name: "clock benign suffix", component: "clock$work", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsReservedDOSName(tc.component); got != tc.want {
				t.Fatalf("IsReservedDOSName(%q) = %v, want %v", tc.component, got, tc.want)
			}
		})
	}
}

func TestHasTrimmedComponentAlias(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{name: "drive path cache trailing dot", path: `C:\cache.\dir`, want: true},
		{name: "drive path cache trailing space", path: `C:\cache \dir`, want: true},
		{name: "unc path child trailing space", path: `\\server\share\dir `, want: true},
		{name: "drive relative child trailing dot", path: `C:cache.\file.txt`, want: true},
		{name: "rooted without drive child trailing space", path: `\cache \file.txt`, want: true},
		{name: "drive path benign", path: `C:\cache\dir`, want: false},
		{name: "unc path benign", path: `\\server\share\dir`, want: false},
		{name: "namespace path ignored here", path: `\\.\cache `, want: false},
		{name: "unix path ignored", path: `/tmp/cache `, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasTrimmedComponentAlias(tc.path); got != tc.want {
				t.Fatalf("HasTrimmedComponentAlias(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestHasReservedDOSNameComponent(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{name: "drive path nul", path: `C:\cache\NUL`, want: true},
		{name: "drive path con extension", path: `C:/cache/con.txt`, want: true},
		{name: "drive path clock trailing dot", path: `C:\cache\CLOCK$...`, want: true},
		{name: "drive path benign console", path: `C:\cache\console`, want: false},
		{name: "drive relative aux", path: `C:aux.txt`, want: true},
		{name: "rooted without drive lpt9", path: `\cache\LPT9 `, want: true},
		{name: "unc path prn", path: `\\server\share\PRN.log`, want: true},
		{name: "unc mixed separators com1", path: `\\server\share/cache\COM1.cfg`, want: true},
		{name: "unc benign cache", path: `\\server\share\cache`, want: false},
		{name: "namespace path ignored here", path: `\\.\NUL`, want: false},
		{name: "unix path ignored", path: `/tmp/NUL`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasReservedDOSNameComponent(tc.path); got != tc.want {
				t.Fatalf("HasReservedDOSNameComponent(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
