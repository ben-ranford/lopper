package shared

import (
	"strings"
	"testing"
)

func TestFallbackDependency(t *testing.T) {
	normalize := strings.ToUpper

	tests := []struct {
		name   string
		module string
		want   string
	}{
		{name: "single segment", module: "ecto", want: "ECTO"},
		{name: "multiple segments", module: "phoenix.html.safe", want: "PHOENIX.HTML"},
		{name: "empty module", module: "", want: ""},
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
		{name: "trims final segment", module: "Foo.Bar ", want: "Bar"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LastModuleSegment(tc.module); got != tc.want {
				t.Fatalf("LastModuleSegment(%q) = %q, want %q", tc.module, got, tc.want)
			}
		})
	}
}

func TestNormalizeEscapedModuleSegments(t *testing.T) {
	tests := []struct {
		name   string
		module string
		want   string
	}{
		{name: "escaped keyword segment", module: "com.example.`when`.Widget", want: "com.example.when.Widget"},
		{name: "trims module and segment spacing", module: " custom.`when`.Widget ", want: "custom.when.Widget"},
		{name: "plain module", module: "plain.module", want: "plain.module"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeEscapedModuleSegments(tc.module); got != tc.want {
				t.Fatalf("NormalizeEscapedModuleSegments(%q) = %q, want %q", tc.module, got, tc.want)
			}
		})
	}
}
