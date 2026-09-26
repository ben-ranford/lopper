package shared

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestUniqueCleanPaths(t *testing.T) {
	got := UniqueCleanPaths([]string{" ./b ", "./a", "", "./a"})
	if !slices.Equal(got, []string{".", "a", "b"}) {
		t.Fatalf("unexpected unique paths: %#v", got)
	}
}

func TestUniqueTrimmedStrings(t *testing.T) {
	got := UniqueTrimmedStrings([]string{" alpha ", "", "alpha", "beta"})
	if !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("unexpected unique strings: %#v", got)
	}
}

func TestSortRecommendations(t *testing.T) {
	recommendations := []report.Recommendation{
		{Code: "z-last", Priority: "medium"},
		{Code: "a-first", Priority: "high"},
		{Code: "b-second", Priority: "high"},
	}

	SortRecommendations(recommendations, report.RecommendationPriorityRank)

	got := []string{
		recommendations[0].Code,
		recommendations[1].Code,
		recommendations[2].Code,
	}
	if !slices.Equal(got, []string{"a-first", "b-second", "z-last"}) {
		t.Fatalf("unexpected recommendation order: %#v", got)
	}
}

func TestTopCountKeys(t *testing.T) {
	if got := TopCountKeys(nil, 3); len(got) != 0 {
		t.Fatalf("expected nil result for empty counts, got %#v", got)
	}

	got := TopCountKeys(map[string]int{"c": 1, "b": 2, "a": 2}, 2)
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("unexpected top keys: %#v", got)
	}
}

func TestReadYAMLUnderRepo(t *testing.T) {
	repo := t.TempDir()
	type manifest struct {
		Name string `yaml:"name"`
	}

	pubspecPath := filepath.Join(repo, "pubspec.yaml")
	testutil.MustWriteFile(t, pubspecPath, "name: app\n")
	parsed, err := ReadYAMLUnderRepo[manifest](repo, pubspecPath)
	if err != nil {
		t.Fatalf("read yaml: %v", err)
	}
	if parsed.Name != "app" {
		t.Fatalf("unexpected parsed yaml: %#v", parsed)
	}

	missingPath := filepath.Join(repo, "missing.yaml")
	if _, err := ReadYAMLUnderRepo[manifest](repo, missingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing file error, got %v", err)
	}

	invalidPath := filepath.Join(repo, "invalid.yaml")
	testutil.MustWriteFile(t, invalidPath, "name: [\n")
	if _, err := ReadYAMLUnderRepo[manifest](repo, invalidPath); err == nil || !strings.Contains(err.Error(), "parse invalid.yaml") || strings.Contains(err.Error(), invalidPath) {
		t.Fatalf("expected parse error for invalid yaml, got %v", err)
	}
}

func TestSortRecommendationsCanonicalPriorities(t *testing.T) {
	recommendations := []report.Recommendation{
		{Code: "f", Priority: "other"}, {Code: "b", Priority: " HIGH "},
		{Code: "d", Priority: "Low"}, {Code: "e", Priority: ""},
		{Code: "c", Priority: " medium "}, {Code: "a", Priority: "high"},
	}
	SortRecommendations(recommendations, report.RecommendationPriorityRank)
	for i, code := range []string{"a", "b", "c", "d", "e", "f"} {
		if recommendations[i].Code != code {
			t.Fatalf("recommendation %d = %q, want %q", i, recommendations[i].Code, code)
		}
	}
	if recommendations[1].Priority != " HIGH " {
		t.Fatalf("sorting must not rewrite priority values: %#v", recommendations)
	}
}
