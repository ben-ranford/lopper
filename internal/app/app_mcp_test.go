package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/mcp"
)

func TestExecuteMCPRequiresPreviewFeature(t *testing.T) {
	application := &App{In: strings.NewReader(""), Out: io.Discard}
	req := DefaultRequest()
	req.Mode = ModeMCP

	_, err := application.Execute(context.Background(), req)
	if !errors.Is(err, ErrMCPPreviewDisabled) {
		t.Fatalf("expected ErrMCPPreviewDisabled, got %v", err)
	}
}

func TestExecuteMCPStartsWhenPreviewFeatureEnabled(t *testing.T) {
	application := &App{In: strings.NewReader(""), Out: io.Discard}
	req := DefaultRequest()
	req.Mode = ModeMCP
	req.MCP.Features = mustMCPPreviewFeatureSet(t)

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute mcp: %v", err)
	}
}

func mustMCPPreviewFeatureSet(t *testing.T) featureflags.Set {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0001",
		Name:      mcp.ServerPreviewFeature,
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	features, err := registry.Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{mcp.ServerPreviewFeature},
	})
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return features
}
