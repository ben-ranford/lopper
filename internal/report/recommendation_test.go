package report

import "testing"

func TestRecommendationPriorityRank(t *testing.T) {
	for _, tt := range []struct {
		priority string
		want     int
	}{
		{"high", 0}, {" HIGH\t", 0}, {"medium", 1}, {"\nMedium ", 1},
		{"low", 2}, {" Low ", 2}, {"", 3}, {" \t", 3}, {"critical", 3}, {"unknown", 3},
	} {
		t.Run(tt.priority, func(t *testing.T) {
			if got := RecommendationPriorityRank(tt.priority); got != tt.want {
				t.Fatalf("rank(%q) = %d, want %d", tt.priority, got, tt.want)
			}
		})
	}
}
