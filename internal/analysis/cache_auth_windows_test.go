package analysis

import "testing"

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
