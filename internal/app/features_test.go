package app

import (
	"context"
	"encoding/json"
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

func TestExecuteFeaturesBaselineStoreDiscoveryUsesCanonicalManifest(t *testing.T) {
	t.Parallel()

	for _, channel := range []string{"dev", "rolling", "release"} {
		channel := channel
		t.Run(channel, func(t *testing.T) {
			t.Parallel()

			req := DefaultRequest()
			req.Mode = ModeFeatures
			req.Features.Format = "json"
			req.Features.Channel = channel
			req.Features.Release = "v1.8.1"

			output, err := (&App{}).Execute(context.Background(), req)
			if err != nil {
				t.Fatalf("execute %s features: %v", channel, err)
			}
			assertCanonicalBaselineStoreManifest(t, output)
		})
	}
}

func assertCanonicalBaselineStoreManifest(t *testing.T, output string) {
	t.Helper()

	var manifest []featureflags.ManifestEntry
	if err := json.Unmarshal([]byte(output), &manifest); err != nil {
		t.Fatalf("decode feature manifest: %v", err)
	}
	for _, entry := range manifest {
		if entry.Name == "baseline-store-discovery-preview" {
			t.Fatalf("deprecated baseline discovery alias leaked into manifest: %#v", entry)
		}
		if entry.Code != "LOP-FEAT-0019" {
			continue
		}
		if entry.Name != "baseline-store-discovery" || entry.Lifecycle != featureflags.LifecycleStable || !entry.EnabledByDefault {
			t.Fatalf("unexpected canonical baseline discovery manifest entry: %#v", entry)
		}
		return
	}
	t.Fatalf("canonical baseline discovery entry missing from manifest: %s", output)
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
	if !strings.Contains(emptyOutput, "dart-source-attribution") ||
		!strings.Contains(emptyOutput, "lockfile-drift-ecosystem-expansion") ||
		!strings.Contains(emptyOutput, "swift-carthage") ||
		!strings.Contains(emptyOutput, "powershell-adapter") ||
		!strings.Contains(emptyOutput, "go-vendored-provenance") ||
		!strings.Contains(emptyOutput, "baseline-provenance-runtime-context") ||
		!strings.Contains(emptyOutput, "vscode-multi-root-workflows") ||
		!strings.Contains(emptyOutput, "mcp-server") ||
		!strings.Contains(emptyOutput, "true") {
		t.Fatalf("expected default feature table to include embedded graduated flags enabled by default, got %q", emptyOutput)
	}
	if strings.Contains(emptyOutput, "dart-source-attribution-preview") || strings.Contains(emptyOutput, "mcp-server-preview") {
		t.Fatalf("expected default feature table to use stable aliases, got %q", emptyOutput)
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

func TestExecuteFeaturesDefaultsBlankChannel(t *testing.T) {
	application := &App{Features: mustFeatureRegistry(t)}
	req := DefaultRequest()
	req.Mode = ModeFeatures
	req.Features.Format = "json"
	req.Features.Channel = "   "

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute features with blank channel: %v", err)
	}
	if !strings.Contains(output, `"code": "LOP-FEAT-0002"`) {
		t.Fatalf("expected feature manifest output, got %q", output)
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

func TestExecuteFeaturesTableReportsChannelDefaults(t *testing.T) {
	application := &App{Features: mustFeatureRegistry(t)}
	tests := []struct {
		channel     string
		previewWant string
	}{
		{channel: "dev", previewWant: "false"},
		{channel: "rolling", previewWant: "true"},
		{channel: "release", previewWant: "false"},
	}

	for _, test := range tests {
		t.Run(test.channel, func(t *testing.T) {
			assertFeatureTableChannelDefaults(t, application, test.channel, test.previewWant)
		})
	}
}

func assertFeatureTableChannelDefaults(t *testing.T, application *App, channel, previewWant string) {
	t.Helper()
	req := DefaultRequest()
	req.Mode = ModeFeatures
	req.Features.Format = "table"
	req.Features.Channel = channel
	req.Features.Release = "v999.0.0"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute %s feature table: %v", channel, err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 3 || !strings.HasSuffix(lines[0], "ENABLED_BY_DEFAULT") || strings.Contains(lines[0], "RELEASE_DEFAULT") {
		t.Fatalf("unexpected feature table heading for %s: %q", channel, output)
	}
	previewFields := strings.Fields(lines[1])
	stableFields := strings.Fields(lines[2])
	if len(previewFields) != 4 || previewFields[3] != previewWant {
		t.Fatalf("unexpected preview default for %s: %q", channel, lines[1])
	}
	if len(stableFields) != 4 || stableFields[3] != "true" {
		t.Fatalf("unexpected stable default for %s: %q", channel, lines[2])
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
