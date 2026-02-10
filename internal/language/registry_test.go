package language

import (
	"context"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

type testAdapter struct {
	id        string
	aliases   []string
	detection Detection
}

func (a testAdapter) ID() string {
	return a.id
}

func (a testAdapter) Aliases() []string {
	return a.aliases
}

func (a testAdapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return a.detection.Matched, nil
}

func (a testAdapter) DetectWithConfidence(ctx context.Context, repoPath string) (Detection, error) {
	return a.detection, nil
}

func (a testAdapter) Analyse(ctx context.Context, req Request) (report.Report, error) {
	return report.Report{}, nil
}

func TestResolveAutoSelectsHighestConfidence(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testAdapter{id: "js-ts", detection: Detection{Matched: true, Confidence: 70}}); err != nil {
		t.Fatalf("register js-ts: %v", err)
	}
	if err := registry.Register(testAdapter{id: "python", detection: Detection{Matched: true, Confidence: 85}}); err != nil {
		t.Fatalf("register python: %v", err)
	}

	candidates, err := registry.Resolve(context.Background(), ".", Auto)
	if err != nil {
		t.Fatalf("resolve auto: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	if candidates[0].Adapter.ID() != "python" {
		t.Fatalf("expected python adapter, got %q", candidates[0].Adapter.ID())
	}
}

func TestResolveAllReturnsMatches(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testAdapter{id: "js-ts", detection: Detection{Matched: true, Confidence: 70}}); err != nil {
		t.Fatalf("register js-ts: %v", err)
	}
	if err := registry.Register(testAdapter{id: "python", detection: Detection{Matched: false, Confidence: 0}}); err != nil {
		t.Fatalf("register python: %v", err)
	}

	candidates, err := registry.Resolve(context.Background(), ".", All)
	if err != nil {
		t.Fatalf("resolve all: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one matched candidate, got %d", len(candidates))
	}
	if candidates[0].Adapter.ID() != "js-ts" {
		t.Fatalf("expected js-ts adapter, got %q", candidates[0].Adapter.ID())
	}
}

func TestResolveAutoTieReturnsError(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testAdapter{id: "js-ts", detection: Detection{Matched: true, Confidence: 80}}); err != nil {
		t.Fatalf("register js-ts: %v", err)
	}
	if err := registry.Register(testAdapter{id: "python", detection: Detection{Matched: true, Confidence: 80}}); err != nil {
		t.Fatalf("register python: %v", err)
	}

	_, err := registry.Resolve(context.Background(), ".", Auto)
	if err == nil {
		t.Fatalf("expected tie error, got nil")
	}
	if err != ErrMultipleLanguages {
		t.Fatalf("expected ErrMultipleLanguages, got %v", err)
	}
}
