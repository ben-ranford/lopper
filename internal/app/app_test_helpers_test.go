package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/ui"
)

const (
	testSnapshotPath        = "snapshot.txt"
	testBaselinePath        = "baseline.json"
	missingBaselineFileName = "missing.json"
	saveBaselineStoreErr    = "--save-baseline requires --baseline-store"
	baselineStorePath       = ".artifacts/baselines"
	executeAnalyseErrFmt    = "execute analyse: %v"
	deniedLicenseSPDX       = "GPL-3.0-ONLY"
	testRuntimeTracePath    = "trace.ndjson"
)

type fakeAnalyzer struct {
	report  report.Report
	err     error
	lastReq analysis.Request
	called  bool
}

func (f *fakeAnalyzer) Analyse(_ context.Context, req analysis.Request) (report.Report, error) {
	f.called = true
	f.lastReq = req
	return f.report, f.err
}

type fakeTUI struct {
	startErr       error
	snapshotErr    error
	startCalled    bool
	snapshotCalled bool
	lastOptions    ui.Options
	lastSnapshot   string
}

type fakeNotifier struct {
	lastDelivery notify.Delivery
	called       bool
	err          error
}

func (f *fakeNotifier) Notify(_ context.Context, delivery notify.Delivery) error {
	f.called = true
	f.lastDelivery = delivery
	return f.err
}

func (f *fakeTUI) Start(_ context.Context, opts ui.Options) error {
	f.startCalled = true
	f.lastOptions = opts
	return f.startErr
}

func (f *fakeTUI) Snapshot(_ context.Context, opts ui.Options, outputPath string) error {
	f.snapshotCalled = true
	f.lastOptions = opts
	f.lastSnapshot = outputPath
	return f.snapshotErr
}

func assertContainsAll(t *testing.T, output string, expected []string) {
	t.Helper()
	for _, value := range expected {
		if !strings.Contains(output, value) {
			t.Fatalf("expected output to include %q", value)
		}
	}
}

func assertForwardedAnalyseRequest(t *testing.T, got analysis.Request) {
	t.Helper()
	checks := []struct {
		name string
		ok   bool
	}{
		{"low confidence threshold", got.LowConfidenceWarningPercent != nil && *got.LowConfidenceWarningPercent == 33},
		{"min usage threshold", got.MinUsagePercentForRecommendations != nil && *got.MinUsagePercentForRecommendations == 44},
		{"runtime profile", got.RuntimeProfile == "browser-import"},
		{"scope mode", got.ScopeMode == ScopeModeChangedPackages},
		{"cache options", got.Cache != nil && !got.Cache.Enabled && got.Cache.Path == "/tmp/lopper-cache" && got.Cache.ReadOnly},
		{"features", got.Features.Enabled("dart-source-attribution-preview")},
		{"suggest only", got.SuggestOnly},
		{"removal candidate weights", got.RemovalCandidateWeights != nil && got.RemovalCandidateWeights.Usage == 0.6 && got.RemovalCandidateWeights.Impact == 0.2 && got.RemovalCandidateWeights.Confidence == 0.2},
	}
	for _, check := range checks {
		if !check.ok {
			t.Fatalf("expected forwarded analyse request field: %s (got=%#v)", check.name, got)
		}
	}
}

func testTime() time.Time {
	return time.Date(2026, time.February, 22, 15, 0, 0, 0, time.UTC)
}

func mustEnabledPreviewFeatureSet(t *testing.T) featureflags.Set {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0001",
		Name:      "dart-source-attribution-preview",
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	features, err := registry.Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{"dart-source-attribution-preview"},
	})
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return features
}
