package report

import "testing"

func TestSARIFArtifactURINormalizesWindowsDevicePaths(t *testing.T) {
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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := toSARIFArtifactURI(test.path); got != test.want {
				t.Fatalf("toSARIFArtifactURI(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}
