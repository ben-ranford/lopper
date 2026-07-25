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
		{name: "rooted without drive forward slashes", path: `/repo/child`, want: Classification{Kind: KindRootedWithoutDrive, Path: `repo/child`}},
		{name: "namespace", path: `\\.\pipe\cache`, want: Classification{Kind: KindAmbiguous}},
		{name: "namespace forward slashes", path: `//./pipe/cache`, want: Classification{Kind: KindAmbiguous}},
		{name: "globalroot forward slashes", path: `//?/GLOBALROOT/Device/HarddiskVolume1/cache`, want: Classification{Kind: KindAmbiguous}},
		{name: "device namespace forward slashes", path: `/Device/HarddiskVolume1/cache`, want: Classification{Kind: KindAmbiguous}},
		{name: "unc absolute mixed separators", path: `\\server/share\cache/child`, want: Classification{Kind: KindUNCAbsolute, Volume: `\\server\share`, Path: `cache\child`}},
		{name: "unc incomplete", path: `\\server`, want: Classification{Kind: KindUNCIncomplete}},
		{name: "single rooted path from slash", path: `/cache`, want: Classification{Kind: KindRootedWithoutDrive, Path: `cache`}},
		{name: "unqualified relative backslashes", path: `cache\child`, want: Classification{}},
		{name: "unqualified relative forward slashes", path: `cache/child`, want: Classification{}},
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
		{name: "nul with stream", component: "NUL:stream", want: true},
		{name: "aux with trailing dots", component: "AUX...", want: true},
		{name: "clock with trailing space", component: "clock$ ", want: true},
		{name: "conin", component: "CONIN$", want: true},
		{name: "conout with extension", component: "conout$.log", want: true},
		{name: "conin with stream", component: "CONIN$:stream", want: true},
		{name: "com9 with extension and spaces", component: "com9 .log", want: true},
		{name: "com superscript one", component: "COM¹", want: true},
		{name: "com superscript three extension", component: "COM³.log", want: true},
		{name: "com superscript one stream", component: "COM¹:stream", want: true},
		{name: "lpt1 lowercase", component: "lpt1", want: true},
		{name: "lpt superscript two", component: "LPT²", want: true},
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
		{name: "relative leaf trailing dot", path: `cache.`, want: true},
		{name: "relative leaf trailing space", path: `cache `, want: true},
		{name: "relative nested trailing dot", path: `cache.\child`, want: true},
		{name: "relative nested trailing space", path: `sub\cache \child`, want: true},
		{name: "drive path benign", path: `C:\cache\dir`, want: false},
		{name: "unc path benign", path: `\\server\share\dir`, want: false},
		{name: "relative dotted component benign", path: `cache.dir\child`, want: false},
		{name: "relative spaced component benign", path: `cache dir\child`, want: false},
		{name: "relative forward slash benign", path: `cache/child`, want: false},
		{name: "relative current directory component benign", path: `.`, want: false},
		{name: "relative parent directory component benign", path: `..\cache`, want: false},
		{name: "nested parent directory component benign", path: `cache\..\child`, want: false},
		{name: "namespace path ignored here", path: `\\.\cache `, want: false},
		{name: "forward-slash rooted path trailing space alias", path: `/tmp/cache `, want: true},
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
		{name: "drive path conin", path: `C:\cache\CONIN$`, want: true},
		{name: "drive path nul stream", path: `C:\cache\NUL:stream`, want: true},
		{name: "drive path com superscript stream", path: `C:\cache\COM¹:stream`, want: true},
		{name: "unc path conout", path: `\\server\share\CONOUT$.log`, want: true},
		{name: "unc path conin stream", path: `\\server\share\CONIN$:stream`, want: true},
		{name: "relative nested com superscript", path: `sub\COM¹.txt`, want: true},
		{name: "relative nested nul stream", path: `sub\NUL:stream`, want: true},
		{name: "relative nested lpt superscript", path: `sub/LPT².log`, want: true},
		{name: "relative leaf con", path: `CON`, want: true},
		{name: "relative nested nul", path: `sub\NUL.txt`, want: true},
		{name: "relative nested forward slash prn", path: `sub/PRN.log`, want: true},
		{name: "unc benign cache", path: `\\server\share\cache`, want: false},
		{name: "relative console benign", path: `console`, want: false},
		{name: "relative nested nul suffix benign", path: `sub\nulled.txt`, want: false},
		{name: "relative com10 benign", path: `sub/COM10.txt`, want: false},
		{name: "namespace path ignored here", path: `\\.\NUL`, want: false},
		{name: "forward-slash rooted path reserved name", path: `/tmp/NUL`, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasReservedDOSNameComponent(tc.path); got != tc.want {
				t.Fatalf("HasReservedDOSNameComponent(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
