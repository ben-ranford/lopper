package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestExecuteAnalyseCycloneDXRequiresPreviewFeature(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			Dependencies: []report.DependencyReport{{Name: "lodash", Language: "js-ts"}},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatCycloneDX

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), report.SBOMAttestationExportsPreviewFeature) {
		t.Fatalf("expected CycloneDX preview feature error, got %v", err)
	}
	if analyzer.called {
		t.Fatalf("expected preview format gating to happen before analysis")
	}
}

func TestExecuteAnalyseCycloneDXAllowsPreviewFeature(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			SchemaVersion: report.SchemaVersion,
			Dependencies:  []report.DependencyReport{{Name: "lodash", Language: "js-ts"}},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatCycloneDX
	req.Analyse.Features = mustSBOMPreviewFeatureSet(t)

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute CycloneDX analyse: %v", err)
	}
	if !strings.Contains(output, `"bomFormat": "CycloneDX"`) || !strings.Contains(output, `"name": "lodash"`) {
		t.Fatalf("expected CycloneDX output, got %s", output)
	}
	if !analyzer.called {
		t.Fatalf("expected analyzer to run when preview flag is enabled")
	}
}

func TestExecuteAnalyseVulnerabilityPrioritizationRequiresPreviewFeature(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.AdvisorySourcePath = "security/advisories.yml"

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), report.ReachabilityVulnerabilityPrioritizationPreviewFeature) {
		t.Fatalf("expected vulnerability preview feature error, got %v", err)
	}
	if analyzer.called {
		t.Fatalf("expected vulnerability feature gating to happen before analysis")
	}
}

func TestExecuteAnalyseVulnerabilityPrioritizationAllowsPreviewFeature(t *testing.T) {
	application := &App{
		Analyzer: &fakeAnalyzer{report: report.Report{
			SchemaVersion: report.SchemaVersion,
			Dependencies:  []report.DependencyReport{{Name: "lodash", Language: "js-ts"}},
		}},
		Formatter: report.NewFormatter(),
	}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Thresholds.ReachableVulnerabilityPriority = report.VulnerabilityPriorityHigh
	req.Analyse.Features = mustVulnerabilityPreviewFeatureSet(t)

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute vulnerability preview analyse: %v", err)
	}
}

func mustSBOMPreviewFeatureSet(t *testing.T) featureflags.Set {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0013",
		Name:      report.SBOMAttestationExportsPreviewFeature,
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	features, err := registry.Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{report.SBOMAttestationExportsPreviewFeature},
	})
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return features
}

func mustVulnerabilityPreviewFeatureSet(t *testing.T) featureflags.Set {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0015",
		Name:      report.ReachabilityVulnerabilityPrioritizationPreviewFeature,
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	features, err := registry.Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{report.ReachabilityVulnerabilityPrioritizationPreviewFeature},
	})
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return features
}
