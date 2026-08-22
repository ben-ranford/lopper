package report

import "testing"

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
	if gaps[0].Path != "fixtures/ trailing-space .go " {
		t.Fatalf("first path = %q, want normalized separator without trimming filename whitespace", gaps[0].Path)
	}
	if gaps[1].Path != "fixtures/trailing-space .go" {
		t.Fatalf("second path = %q, want surrounding filename whitespace preserved", gaps[1].Path)
	}
}
