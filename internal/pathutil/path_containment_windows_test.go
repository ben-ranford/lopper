//go:build windows

package pathutil

import "testing"

func TestWindowsPathContainment(t *testing.T) {
	tests := []struct {
		name      string
		root      string
		candidate string
		want      bool
	}{
		{
			name:      "mixed case",
			root:      `C:\Repo\Node_Modules`,
			candidate: `c:\repo\node_modules\Pkg`,
			want:      true,
		},
		{
			name:      "mixed separators",
			root:      `C:\Repo/Node_Modules`,
			candidate: `c:/repo\node_modules/Pkg`,
			want:      true,
		},
		{
			name:      "alternate volume",
			root:      `C:\Repo`,
			candidate: `D:\Repo\Pkg`,
			want:      false,
		},
		{
			name:      "clean dot and child dotdot",
			root:      `C:\Repo\.\Packages`,
			candidate: `c:\repo\packages\App\..\App\package.json`,
			want:      true,
		},
		{
			name:      "clean dotdot escapes root",
			root:      `C:\Repo\Packages`,
			candidate: `C:\Repo\Packages\..\Outside\package.json`,
			want:      false,
		},
		{
			name:      "sibling prefix",
			root:      `C:\Repo`,
			candidate: `C:\RepoSibling\node_modules\Pkg`,
			want:      false,
		},
		{
			name:      "unc same server and share",
			root:      `\\Server\Share\Repo`,
			candidate: `\\server\share\repo\Pkg`,
			want:      true,
		},
		{
			name:      "unc same share mixed separators",
			root:      `\\Server\Share/Repo`,
			candidate: `//server/share/repo/Pkg`,
			want:      true,
		},
		{
			name:      "unc different server",
			root:      `\\Server\Share\Repo`,
			candidate: `\\Other\Share\Repo\Pkg`,
			want:      false,
		},
		{
			name:      "unc different share",
			root:      `\\Server\Share\Repo`,
			candidate: `\\Server\Other\Repo\Pkg`,
			want:      false,
		},
		{
			name:      "unc sibling prefix",
			root:      `\\Server\Share\Repo`,
			candidate: `\\Server\Share\RepoSibling\Pkg`,
			want:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := WithinRoot(test.root, test.candidate); got != test.want {
				t.Fatalf("WithinRoot(%q, %q) = %v, want %v", test.root, test.candidate, got, test.want)
			}
		})
	}

	if !Equal(`C:\Repo\.\Node_Modules`, `c:/repo/node_modules`) {
		t.Fatal("expected Windows path equality to clean separators and ignore case")
	}
}
