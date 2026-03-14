package shared

import (
	"strings"
	"testing"
)

func TestFallbackDependencyUsesRootOrFirstTwoSegments(t *testing.T) {
	normalize := strings.ToUpper

	if got := FallbackDependency("ecto", normalize); got != "ECTO" {
		t.Fatalf("expected normalized single segment dependency, got %q", got)
	}
	if got := FallbackDependency("phoenix.html.safe", normalize); got != "PHOENIX.HTML" {
		t.Fatalf("expected normalized first two segments, got %q", got)
	}
}

func TestLastModuleSegmentHandlesEmptyAndWhitespace(t *testing.T) {
	if got := LastModuleSegment(""); got != "" {
		t.Fatalf("expected empty segment for empty module, got %q", got)
	}
	if got := LastModuleSegment("Foo.Bar "); got != "Bar" {
		t.Fatalf("expected trimmed last segment, got %q", got)
	}
}
