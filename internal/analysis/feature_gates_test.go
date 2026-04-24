package analysis

import (
	"context"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestPowerShellPreviewFeatureUsesShippedCode(t *testing.T) {
	flag := mustLookupPowerShellPreviewFlag(t)
	if flag.Code != "LOP-FEAT-0004" {
		t.Fatalf("expected powershell preview feature code LOP-FEAT-0004, got %s", flag.Code)
	}
}

func TestPreviewAdapterFeaturesUsesRegistryPattern(t *testing.T) {
	registry, err := featureflags.NewRegistry([]featureflags.Flag{
		{Code: "LOP-FEAT-0001", Name: "powershell-adapter-preview", Lifecycle: featureflags.LifecyclePreview},
		{Code: "LOP-FEAT-0002", Name: "swift-carthage-preview", Lifecycle: featureflags.LifecyclePreview},
		{Code: "LOP-FEAT-0003", Name: "ruby-adapter-preview", Lifecycle: featureflags.LifecycleStable},
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	got := previewAdapterFeatures(registry)
	if len(got) != 1 {
		t.Fatalf("expected only preview adapter features, got %#v", got)
	}
	if got["powershell"] != "powershell-adapter-preview" {
		t.Fatalf("expected powershell preview gate mapping, got %#v", got)
	}
	if _, ok := got["swift-carthage"]; ok {
		t.Fatalf("did not expect non-adapter preview feature in mapping, got %#v", got)
	}
	if _, ok := got["ruby"]; ok {
		t.Fatalf("did not expect stable adapter feature in mapping, got %#v", got)
	}
}

func TestAdapterFeatureFilterKeepsUnknownAdaptersEnabled(t *testing.T) {
	filter := adapterFeatureFilter(featureflags.Set{})
	if !filter(&gatedAdapterStub{id: "custom-adapter"}) {
		t.Fatalf("expected unknown adapter to stay enabled by default")
	}
}

func TestAdapterFeatureFilterUsesShippedPowerShellPreviewGate(t *testing.T) {
	flag := mustLookupPowerShellPreviewFlag(t)
	filter := adapterFeatureFilter(featureflags.Set{})
	if filter(&gatedAdapterStub{id: "powershell"}) {
		t.Fatalf("expected powershell adapter to stay gated until %s is enabled", flag.Name)
	}

	features, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{flag.Code},
	})
	if err != nil {
		t.Fatalf("resolve powershell preview feature: %v", err)
	}
	filter = adapterFeatureFilter(features)
	if !filter(&gatedAdapterStub{id: "powershell"}) {
		t.Fatalf("expected powershell adapter to be enabled when %s is enabled", flag.Code)
	}
}

type gatedAdapterStub struct {
	id string
}

func (a *gatedAdapterStub) ID() string {
	return a.id
}

func (a *gatedAdapterStub) Aliases() []string {
	return nil
}

func (a *gatedAdapterStub) Detect(context.Context, string) (bool, error) {
	return true, nil
}

func (a *gatedAdapterStub) Analyse(context.Context, language.Request) (report.Report, error) {
	return report.Report{}, nil
}
