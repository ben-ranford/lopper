package analysis

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	cacheTestJSIndexFileName     = "index.js"
	cacheTestPackageJSONFileName = "package.json"
)

type countingAdapter struct {
	id    string
	calls int
}

func (a *countingAdapter) ID() string { return a.id }
func (a *countingAdapter) Aliases() []string {
	return nil
}
func (a *countingAdapter) Detect(context.Context, string) (bool, error) {
	return true, nil
}
func (a *countingAdapter) Analyse(context.Context, language.Request) (report.Report, error) {
	a.calls++
	return report.Report{
		Dependencies: []report.DependencyReport{{
			Name:              "dep",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			UsedPercent:       50,
		}},
	}, nil
}

func TestAnalysisCacheHitAndInvalidation(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	writeFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	adapter := &countingAdapter{id: "cachelang"}
	reg := language.NewRegistry()
	if err := reg.Register(adapter); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	svc := &Service{Registry: reg}
	cacheDir := filepath.Join(repo, "analysis-cache")

	req := Request{
		RepoPath: repo,
		Language: "cachelang",
		TopN:     1,
		Cache: &CacheOptions{
			Enabled: true,
			Path:    cacheDir,
		},
	}

	first, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected first run to call adapter once, got %d", adapter.calls)
	}
	if first.Cache == nil || first.Cache.Misses != 1 || first.Cache.Writes != 1 || first.Cache.Hits != 0 {
		t.Fatalf("unexpected first cache metadata: %#v", first.Cache)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected second run to be cache hit, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("unexpected second cache metadata: %#v", second.Cache)
	}

	writeFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import { filter } from \"lodash\"\nfilter([1], (x) => x)\n")
	third, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("third analyse: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected cache invalidation after source change, adapter calls=%d", adapter.calls)
	}
	if third.Cache == nil || third.Cache.Misses != 1 {
		t.Fatalf("expected miss after source change, got %#v", third.Cache)
	}
	if len(third.Cache.Invalidations) == 0 || !strings.Contains(third.Cache.Invalidations[0].Reason, "input-changed") {
		t.Fatalf("expected input-changed invalidation, got %#v", third.Cache.Invalidations)
	}
}

func TestAnalysisCacheReadOnlySkipsWrites(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	adapter := &countingAdapter{id: "cachelang"}
	reg := language.NewRegistry()
	if err := reg.Register(adapter); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	svc := &Service{Registry: reg}

	req := Request{
		RepoPath: repo,
		Language: "cachelang",
		TopN:     1,
		Cache: &CacheOptions{
			Enabled:  true,
			Path:     filepath.Join(repo, "analysis-cache"),
			ReadOnly: true,
		},
	}

	first, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first readonly analyse: %v", err)
	}
	if first.Cache == nil || !first.Cache.ReadOnly || first.Cache.Writes != 0 {
		t.Fatalf("expected readonly cache metadata with no writes, got %#v", first.Cache)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second readonly analyse: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected readonly mode to avoid persisting misses, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 0 || second.Cache.Misses == 0 {
		t.Fatalf("expected readonly run miss metadata, got %#v", second.Cache)
	}
}
