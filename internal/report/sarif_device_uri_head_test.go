package report

import "testing"

func TestSARIFArtifactURIPreservesWindowsAndLocalPathForms(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "extended drive", path: `\\?\C:\work\source.go`, want: "file:///C:/work/source.go"},
		{name: "device drive", path: `\\.\C:\work\source.go`, want: "file:///C:/work/source.go"},
		{name: "extended unc", path: `\\?\UNC\server\share\source.go`, want: "file://server/share/source.go"},
		{name: "device unc", path: `\\.\UNC\server\share\source.go`, want: "file://server/share/source.go"},
		{name: "device namespace", path: `\\.\PhysicalDrive0`, want: ""},
		{name: "bare UNC share", path: `\\server\share`, want: "file://server/share"},
		{name: "UNC share with separator", path: `\\server\share\`, want: "file://server/share"},
		{name: "POSIX double slash", path: "//workspace/src/source.go", want: "file:///workspace/src/source.go"},
		{name: "POSIX dot path", path: "//./workspace/source.go", want: "file:///workspace/source.go"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := toSARIFArtifactURI(test.path); got != test.want {
				t.Fatalf("toSARIFArtifactURI(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}
