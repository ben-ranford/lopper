package shared

import "testing"

func TestFallbackDependency(t *testing.T) {
	normalize := func(value string) string {
		return "[" + value + "]"
	}

	tests := []struct {
		name   string
		module string
		want   string
	}{
		{name: "single segment", module: "androidx", want: "[androidx]"},
		{name: "multiple segments", module: "androidx.appcompat.widget", want: "[androidx.appcompat]"},
		{name: "empty module", module: "", want: "[]"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FallbackDependency(tc.module, normalize); got != tc.want {
				t.Fatalf("FallbackDependency(%q) = %q, want %q", tc.module, got, tc.want)
			}
		})
	}
}

func TestLastModuleSegment(t *testing.T) {
	tests := []struct {
		name   string
		module string
		want   string
	}{
		{name: "empty module", module: "", want: ""},
		{name: "single segment", module: "androidx", want: "androidx"},
		{name: "trims final segment", module: "androidx.appcompat. widget ", want: "widget"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LastModuleSegment(tc.module); got != tc.want {
				t.Fatalf("LastModuleSegment(%q) = %q, want %q", tc.module, got, tc.want)
			}
		})
	}
}
