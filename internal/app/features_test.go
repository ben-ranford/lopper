package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
)

func TestExecuteFeaturesJSON(t *testing.T) {
	registry := mustFeatureRegistry(t)
	application := &App{Features: registry}
	req := DefaultRequest()
	req.Mode = ModeFeatures
	req.Features.Format = "json"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute features: %v", err)
	}
	if !strings.Contains(output, `"code": "LOP-FEAT-0001"`) || !strings.Contains(output, `"enabledByDefault": false`) {
		t.Fatalf("unexpected feature manifest output: %s", output)
	}
}

func TestExecuteFeaturesTableAndInvalidFormat(t *testing.T) {
	application := &App{Features: mustFeatureRegistry(t)}
	req := DefaultRequest()
	req.Mode = ModeFeatures

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute features table: %v", err)
	}
	if !strings.Contains(output, "LOP-FEAT-0002") || !strings.Contains(output, "true") {
		t.Fatalf("unexpected feature table: %s", output)
	}

	req.Features.Format = "xml"
	if _, err := application.Execute(context.Background(), req); err == nil {
		t.Fatalf("expected invalid features format error")
	}
}

func mustFeatureRegistry(t *testing.T) *featureflags.Registry {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{
		{Code: "LOP-FEAT-0001", Name: "preview-flag", Description: "Preview behavior", Lifecycle: featureflags.LifecyclePreview},
		{Code: "LOP-FEAT-0002", Name: "stable-flag", Description: "Stable behavior", Lifecycle: featureflags.LifecycleStable},
	})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	return registry
}
