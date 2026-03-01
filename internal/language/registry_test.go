package language

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

const (
	registerJSErrFmt      = "register js-ts: %v"
	registerPythonErrFmt  = "register python: %v"
	registerAdapterErrFmt = "register adapter: %v"
)

type testAdapter struct {
	id        string
	aliases   []string
	detection Detection
	detectErr error
}

type simpleAdapter struct {
	id      string
	matched bool
	err     error
}

func (a *simpleAdapter) ID() string        { return a.id }
func (a *simpleAdapter) Aliases() []string { return nil }
func (a *simpleAdapter) Detect(context.Context, string) (bool, error) {
	return a.matched, a.err
}
func (a *simpleAdapter) Analyse(context.Context, Request) (report.Report, error) {
	return report.Report{}, nil
}

func (a *testAdapter) ID() string {
	return a.id
}

func (a *testAdapter) Aliases() []string {
	return a.aliases
}

func (a *testAdapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	if a.detectErr != nil {
		return false, a.detectErr
	}
	return a.detection.Matched, nil
}

func (a *testAdapter) DetectWithConfidence(ctx context.Context, repoPath string) (Detection, error) {
	if a.detectErr != nil {
		return Detection{}, a.detectErr
	}
	return a.detection, nil
}

func (a *testAdapter) Analyse(ctx context.Context, req Request) (report.Report, error) {
	return report.Report{}, nil
}

func TestResolveAutoSelectsHighestConfidence(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&testAdapter{id: "js-ts", detection: Detection{Matched: true, Confidence: 70}}); err != nil {
		t.Fatalf(registerJSErrFmt, err)
	}
	if err := registry.Register(&testAdapter{id: "python", detection: Detection{Matched: true, Confidence: 85}}); err != nil {
		t.Fatalf(registerPythonErrFmt, err)
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
	if err := registry.Register(&testAdapter{id: "js-ts", detection: Detection{Matched: true, Confidence: 70}}); err != nil {
		t.Fatalf(registerJSErrFmt, err)
	}
	if err := registry.Register(&testAdapter{id: "python", detection: Detection{Matched: false, Confidence: 0}}); err != nil {
		t.Fatalf(registerPythonErrFmt, err)
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
	if err := registry.Register(&testAdapter{id: "js-ts", detection: Detection{Matched: true, Confidence: 80}}); err != nil {
		t.Fatalf(registerJSErrFmt, err)
	}
	if err := registry.Register(&testAdapter{id: "python", detection: Detection{Matched: true, Confidence: 80}}); err != nil {
		t.Fatalf(registerPythonErrFmt, err)
	}

	_, err := registry.Resolve(context.Background(), ".", Auto)
	if err == nil {
		t.Fatalf("expected tie error, got nil")
	}
	if err != ErrMultipleLanguages {
		t.Fatalf("expected ErrMultipleLanguages, got %v", err)
	}
}

func TestRegisterValidationAndIDs(t *testing.T) {
	registry := NewRegistry()
	if registry.Register(nil) == nil {
		t.Fatalf("expected nil adapter error")
	}
	if registry.Register(&testAdapter{id: " ", detection: Detection{Matched: true}}) == nil {
		t.Fatalf("expected empty adapter id error")
	}
	if err := registry.Register(&testAdapter{id: "js-ts", aliases: []string{"js"}, detection: Detection{Matched: true}}); err != nil {
		t.Fatalf(registerJSErrFmt, err)
	}
	if registry.Register(&testAdapter{id: "other", aliases: []string{"js"}, detection: Detection{Matched: true}}) == nil {
		t.Fatalf("expected duplicate alias registration error")
	}

	ids := registry.IDs()
	if !slices.Equal(ids, []string{"js-ts"}) {
		t.Fatalf("unexpected registry IDs: %#v", ids)
	}
}

func TestSelectAndResolveErrors(t *testing.T) {
	registry := NewRegistry()
	if _, err := (*Registry)(nil).Resolve(context.Background(), ".", Auto); err == nil {
		t.Fatalf("expected nil registry resolve error")
	}
	if _, err := registry.Resolve(context.Background(), ".", Auto); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("expected ErrNoMatch, got %v", err)
	}
	if _, err := registry.Select(context.Background(), ".", Auto); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("expected select ErrNoMatch, got %v", err)
	}

	if err := registry.Register(&testAdapter{id: "js-ts", detection: Detection{Matched: true, Confidence: 10}}); err != nil {
		t.Fatalf(registerJSErrFmt, err)
	}
	if _, err := registry.Resolve(context.Background(), ".", "unknown"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("expected ErrUnknown, got %v", err)
	}
	adapter, err := registry.Select(context.Background(), ".", "js-ts")
	if err != nil {
		t.Fatalf("select explicit: %v", err)
	}
	if adapter.ID() != "js-ts" {
		t.Fatalf("unexpected selected adapter: %q", adapter.ID())
	}
}

func TestResolveAdapterErrorBubbles(t *testing.T) {
	registry := NewRegistry()
	expected := errors.New("detect failed")
	if err := registry.Register(&testAdapter{id: "broken", detection: Detection{Matched: true}, detectErr: expected}); err != nil {
		t.Fatalf("register broken: %v", err)
	}
	if _, err := registry.Resolve(context.Background(), ".", "broken"); !errors.Is(err, expected) {
		t.Fatalf("expected detect error, got %v", err)
	}
}

func TestNormalizeDetectionAndClamp(t *testing.T) {
	detection := normalizeDetection(".", Detection{Matched: true, Confidence: 0})
	if detection.Confidence != 1 {
		t.Fatalf("expected matched detection confidence normalization to 1, got %d", detection.Confidence)
	}
	if len(detection.Roots) != 1 {
		t.Fatalf("expected default root assignment, got %#v", detection.Roots)
	}
	if clampConfidence(-1) != 0 || clampConfidence(101) != 100 || clampConfidence(42) != 42 {
		t.Fatalf("unexpected clampConfidence behavior")
	}
}

func TestDetectAdapterFallbackPath(t *testing.T) {
	detection, err := detectAdapter(context.Background(), &simpleAdapter{id: "simple", matched: true}, ".")
	if err != nil {
		t.Fatalf("detect adapter fallback: %v", err)
	}
	if !detection.Matched || detection.Confidence != 60 {
		t.Fatalf("unexpected fallback detection: %#v", detection)
	}
	if _, err := detectAdapter(context.Background(), &simpleAdapter{id: "simple", err: errors.New("boom")}, "."); err == nil {
		t.Fatalf("expected detect error from fallback adapter")
	}
}

func TestResolveAllNoMatchAndIDsNilRegistry(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&testAdapter{id: "python", detection: Detection{Matched: false}}); err != nil {
		t.Fatalf(registerAdapterErrFmt, err)
	}
	if _, err := registry.Resolve(context.Background(), ".", All); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("expected ErrNoMatch for all-mode no match, got %v", err)
	}

	if ids := (*Registry)(nil).IDs(); len(ids) != 0 {
		t.Fatalf("expected nil IDs for nil registry, got %#v", ids)
	}
}

func TestResolveExplicitUnmatchedDetectionFallsBackToForcedMatch(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&testAdapter{
		id:        "python",
		detection: Detection{Matched: false, Confidence: 0, Roots: nil},
	}); err != nil {
		t.Fatalf(registerAdapterErrFmt, err)
	}

	candidates, err := registry.Resolve(context.Background(), ".", "python")
	if err != nil {
		t.Fatalf("resolve explicit adapter: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	if !candidates[0].Detection.Matched {
		t.Fatalf("expected fallback matched detection")
	}
	if candidates[0].Detection.Confidence != 100 {
		t.Fatalf("expected forced confidence 100, got %d", candidates[0].Detection.Confidence)
	}
	if len(candidates[0].Detection.Roots) == 0 {
		t.Fatalf("expected fallback roots to be populated")
	}
}

func TestResolveAllAndAutoDetectErrorBranches(t *testing.T) {
	expected := errors.New("detect failed")

	registry := NewRegistry()
	if err := registry.Register(&testAdapter{id: "broken", detection: Detection{Matched: true}, detectErr: expected}); err != nil {
		t.Fatalf("register broken adapter: %v", err)
	}
	if _, err := registry.Resolve(context.Background(), ".", All); !errors.Is(err, expected) {
		t.Fatalf("expected detect error in all-mode resolve, got %v", err)
	}

	registry = NewRegistry()
	if err := registry.Register(&testAdapter{id: "ok", detection: Detection{Matched: true, Confidence: 90}}); err != nil {
		t.Fatalf("register ok adapter: %v", err)
	}
	if err := registry.Register(&testAdapter{id: "broken-auto", detection: Detection{Matched: true, Confidence: 80}, detectErr: expected}); err != nil {
		t.Fatalf("register broken adapter: %v", err)
	}
	if _, err := registry.Resolve(context.Background(), ".", Auto); !errors.Is(err, expected) {
		t.Fatalf("expected detect error in auto-mode resolve, got %v", err)
	}
}

func TestDetectMatchesAndFallbackDetectorBranches(t *testing.T) {
	registry := NewRegistry()
	adapter := &testAdapter{
		id:      "js-ts",
		aliases: []string{"js"},
		detection: Detection{
			Matched:    true,
			Confidence: 50,
		},
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf(registerAdapterErrFmt, err)
	}

	// Same adapter appears under both id and alias; detectMatches should de-dupe by adapter ID.
	matches, err := registry.detectMatches(context.Background(), ".")
	if err != nil {
		t.Fatalf("detect matches: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected de-duped matches, got %#v", matches)
	}

	detection, err := detectAdapter(context.Background(), &simpleAdapter{id: "simple", matched: false}, ".")
	if err != nil {
		t.Fatalf("detect adapter fallback unmatched: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected unmatched fallback detection")
	}
}

func TestNormalizeDetectionFallbackToRepoPathOnAbsError(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})

	deadDir := t.TempDir()
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf("chdir deadDir: %v", err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove deadDir: %v", err)
	}

	detection := normalizeDetection(".", Detection{Matched: true})
	if len(detection.Roots) == 0 {
		t.Fatalf("expected roots to be set")
	}
}

func TestResolveAutoSingleMatchBranch(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&testAdapter{id: "js-ts", detection: Detection{Matched: true, Confidence: 60}}); err != nil {
		t.Fatalf(registerAdapterErrFmt, err)
	}
	candidates, err := registry.resolveAuto(context.Background(), ".")
	if err != nil {
		t.Fatalf("resolve auto with single match: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Adapter.ID() != "js-ts" {
		t.Fatalf("expected one js-ts candidate, got %#v", candidates)
	}
}
