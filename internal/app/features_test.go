package app

import (
	"context"
	"os"
	"path/filepath"
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
	req.Features.Channel = "dev"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute features: %v", err)
	}
	if !strings.Contains(output, `"code": "LOP-FEAT-0001"`) || !strings.Contains(output, `"enabledByDefault": true`) {
		t.Fatalf("unexpected feature manifest output: %s", output)
	}
}

func TestExecuteFeaturesOutputFile(t *testing.T) {
	registry := mustFeatureRegistry(t)
	application := &App{Features: registry}
	outputPath := filepath.Join(t.TempDir(), "features.json")
	req := DefaultRequest()
	req.Mode = ModeFeatures
	req.Features.Format = "json"
	req.Features.OutputPath = outputPath
	req.Features.Channel = "dev"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute features with output path: %v", err)
	}
	if !strings.Contains(output, outputPath) {
		t.Fatalf("expected output file confirmation, got %q", output)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read feature output file: %v", err)
	}
	if !strings.Contains(string(data), `"code": "LOP-FEAT-0001"`) {
		t.Fatalf("expected feature JSON content, got %q", string(data))
	}
}

func TestExecuteFeaturesRollingChannel(t *testing.T) {
	application := &App{Features: mustFeatureRegistry(t)}
	req := DefaultRequest()
	req.Mode = ModeFeatures
	req.Features.Format = "json"
	req.Features.Channel = "rolling"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute rolling features: %v", err)
	}
	if !strings.Contains(output, `"code": "LOP-FEAT-0001"`) || !strings.Contains(output, `"enabledByDefault": true`) {
		t.Fatalf("expected rolling manifest to enable preview flag: %s", output)
	}
}

func TestExecuteFeaturesReleaseChannelAndEmptyRegistry(t *testing.T) {
	application := &App{Features: mustFeatureRegistry(t)}
	req := DefaultRequest()
	req.Mode = ModeFeatures
	req.Features.Format = "json"
	req.Features.Channel = "release"
	req.Features.Release = "v1.4.2"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute release features: %v", err)
	}
	if !strings.Contains(output, `"code": "LOP-FEAT-0002"`) || !strings.Contains(output, `"enabledByDefault": true`) {
		t.Fatalf("expected release manifest to enable stable flag: %s", output)
	}

	emptyReq := DefaultRequest()
	emptyReq.Mode = ModeFeatures
	// Keep this assertion stable across CI lanes that set BUILD_CHANNEL=rolling.
	emptyReq.Features.Channel = "dev"
	emptyOutput, err := (&App{}).Execute(context.Background(), emptyReq)
	if err != nil {
		t.Fatalf("execute empty features: %v", err)
	}
	if !strings.Contains(emptyOutput, "dart-source-attribution-preview") ||
		!strings.Contains(emptyOutput, "lockfile-drift-ecosystem-expansion-preview") ||
		!strings.Contains(emptyOutput, "swift-carthage-preview") ||
		!strings.Contains(emptyOutput, "powershell-adapter-preview") ||
		!strings.Contains(emptyOutput, "go-vendored-provenance-preview") ||
		!strings.Contains(emptyOutput, "baseline-provenance-runtime-context-preview") ||
		!strings.Contains(emptyOutput, "vscode-multi-root-workflows-preview") ||
		!strings.Contains(emptyOutput, "mcp-server-preview") ||
		!strings.Contains(emptyOutput, "true") {
		t.Fatalf("expected default feature table to include embedded graduated flags enabled by default, got %q", emptyOutput)
	}
}

func TestExecuteFeaturesDefaultReleaseAndStdoutOutputPath(t *testing.T) {
	application := &App{Features: mustFeatureRegistry(t)}
	req := DefaultRequest()
	req.Mode = ModeFeatures
	req.Features.Format = "json"
	req.Features.Channel = "release"
	req.Features.OutputPath = "-"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute release features with stdout output path: %v", err)
	}
	if !strings.Contains(output, `"code": "LOP-FEAT-0001"`) || strings.Contains(output, "written to") {
		t.Fatalf("expected feature JSON on stdout, got %q", output)
	}
}

func TestExecuteFeaturesTableAndInvalidFormat(t *testing.T) {
	application := &App{Features: mustFeatureRegistry(t)}
	req := DefaultRequest()
	req.Mode = ModeFeatures
	req.Features.Channel = "dev"

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

	req.Features.Format = "table"
	req.Features.Channel = "bad"
	if _, err := application.Execute(context.Background(), req); err == nil {
		t.Fatalf("expected invalid features channel error")
	}
}

func TestExecuteFeaturesExplicitEmptyRegistry(t *testing.T) {
	emptyRegistry, err := featureflags.NewRegistry(nil)
	if err != nil {
		t.Fatalf("new empty feature registry: %v", err)
	}
	application := &App{Features: emptyRegistry}
	req := DefaultRequest()
	req.Mode = ModeFeatures
	req.Features.Channel = "dev"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute empty features: %v", err)
	}
	if output != "No feature flags registered.\n" {
		t.Fatalf("unexpected empty feature output: %q", output)
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
