package pep440

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, test := range []struct {
		name        string
		left        string
		right       string
		want        int
		wantOrdered bool
	}{
		{name: "epoch", left: "1!2.0", right: "2.0", want: 1, wantOrdered: true},
		{name: "right invalid", left: "2.0", right: "release-a", wantOrdered: false},
		{name: "release trailing zero", left: "2.0.0", right: "2.0", want: 0, wantOrdered: true},
		{name: "release numeric length", left: "2.0.10", right: "2.0.2", want: 1, wantOrdered: true},
		{name: "alpha alias", left: "2.0-alpha1", right: "2.0-beta1", want: -1, wantOrdered: true},
		{name: "compact beta", left: "2.0b1", right: "2.0rc1", want: -1, wantOrdered: true},
		{name: "pre alias", left: "2.0pre", right: "2.0rc0", want: 0, wantOrdered: true},
		{name: "post alias", left: "2.0-r1", right: "2.0.post0", want: 1, wantOrdered: true},
		{name: "development release", left: "2.0.dev", right: "2.0a0", want: -1, wantOrdered: true},
		{name: "numeric local sorts after lexical", left: "2.0+1", right: "2.0+local", want: 1, wantOrdered: true},
		{name: "lexical local sorts before numeric", left: "2.0+local", right: "2.0+1", want: -1, wantOrdered: true},
		{name: "lexical local parts", left: "2.0+alpha", right: "2.0+beta", want: -1, wantOrdered: true},
		{name: "local part count", left: "2.0+alpha.1", right: "2.0+alpha", want: 1, wantOrdered: true},
		{name: "local separators equivalent", left: "2.0+local-1", right: "2.0+local.1", want: 0, wantOrdered: true},
		{name: "invalid", left: "release-a", right: "2.0", wantOrdered: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ordered := CompareVersions(test.left, test.right)
			if ordered != test.wantOrdered || got != test.want {
				t.Fatalf("CompareVersions(%q, %q) = (%d, %t), want (%d, %t)", test.left, test.right, got, ordered, test.want, test.wantOrdered)
			}
		})
	}
}

func TestComparePEP440OptionalNumber(t *testing.T) {
	if got := comparePEP440OptionalNumber(false, "", true, "1", 1); got != -1 {
		t.Fatalf("missing value ordering = %d, want -1", got)
	}
	if got := comparePEP440OptionalNumber(true, "1", false, "", -1); got != -1 {
		t.Fatalf("present value ordering = %d, want -1", got)
	}
}
