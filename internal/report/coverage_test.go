package report

import (
	"runtime"
	"testing"
)

func TestStableCoverageGapsPreservesFilenameWhitespace(t *testing.T) {
	gaps := StableCoverageGaps([]CoverageGap{
		{
			Code:     " whitespace-path ",
			Language: " go ",
			Path:     "fixtures\\ trailing-space .go ",
			Evidence: []string{"head"},
		},
		{
			Code:     "whitespace-path",
			Language: "go",
			Path:     "fixtures/trailing-space .go",
			Evidence: []string{"baseline"},
		},
	})

	if len(gaps) != 2 {
		t.Fatalf("len(StableCoverageGaps) = %d, want 2 distinct paths: %#v", len(gaps), gaps)
	}
	wantBackslashPath := "fixtures\\ trailing-space .go "
	if runtime.GOOS == "windows" {
		wantBackslashPath = "fixtures/ trailing-space .go "
	}
	pathsByEvidence := map[string]string{}
	for _, gap := range gaps {
		if len(gap.Evidence) != 1 {
			t.Fatalf("gap evidence = %#v, want a single marker", gap.Evidence)
		}
		pathsByEvidence[gap.Evidence[0]] = gap.Path
	}
	if pathsByEvidence["head"] != wantBackslashPath {
		t.Fatalf("backslash path = %q, want normalized separator without trimming filename whitespace", pathsByEvidence["head"])
	}
	if pathsByEvidence["baseline"] != "fixtures/trailing-space .go" {
		t.Fatalf("slash path = %q, want surrounding filename whitespace preserved", pathsByEvidence["baseline"])
	}
}

func TestNewCoverageGapsPreservesUnixLiteralBackslashes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a path separator on Windows")
	}

	baseline := []CoverageGap{{
		Code:     CoverageGapRubyOversizedGemspec,
		Language: "ruby",
		Path:     "a\\b.gemspec",
		Evidence: []string{"baseline"},
	}}
	current := []CoverageGap{{
		Code:     CoverageGapRubyOversizedGemspec,
		Language: "ruby",
		Path:     "a/b.gemspec",
		Evidence: []string{"head"},
	}}

	gaps := newCoverageGaps(current, baseline)
	if len(gaps) != 1 {
		t.Fatalf("len(newCoverageGaps) = %d, want 1 distinct Unix path: %#v", len(gaps), gaps)
	}
	if gaps[0].Path != "a/b.gemspec" {
		t.Fatalf("new coverage gap path = %q, want a/b.gemspec", gaps[0].Path)
	}
}
