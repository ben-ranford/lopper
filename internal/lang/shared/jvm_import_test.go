package shared

import "testing"

func TestIsKotlinEscapedKeyword(t *testing.T) {
	tests := map[string]bool{
		"`when`":        true,
		"`typealias`":   true,
		"`WidgetAlias`": false,
		"WidgetAlias":   false,
		"`field`":       false,
	}
	for local, want := range tests {
		if got := IsKotlinEscapedKeyword(local); got != want {
			t.Errorf("IsKotlinEscapedKeyword(%q) = %t, want %t", local, got, want)
		}
	}
}
