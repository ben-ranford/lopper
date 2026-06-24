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

func TestExecuteMCPRequiresFeature(t *testing.T) {
	application := &App{In: strings.NewReader(""), Out: io.Discard}
	req := DefaultRequest()
	req.Mode = ModeMCP

	_, err := application.Execute(context.Background(), req)
	if !errors.Is(err, ErrMCPFeatureDisabled) {
		t.Fatalf("expected ErrMCPFeatureDisabled, got %v", err)
	}
}

func TestExecuteMCPStartsWhenFeatureEnabled(t *testing.T) {
	application := &App{In: strings.NewReader(""), Out: io.Discard}
	req := DefaultRequest()
	req.Mode = ModeMCP
	req.MCP.Features = mustMCPFeatureSet(t, false)

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute mcp: %v", err)
	}
}

func TestExecuteMCPRejectsExplicitlyDisabledFeature(t *testing.T) {
	application := &App{In: strings.NewReader(""), Out: io.Discard}
	req := DefaultRequest()
	req.Mode = ModeMCP
	req.MCP.Features = mustMCPFeatureSet(t, true)

	_, err := application.Execute(context.Background(), req)
	if !errors.Is(err, ErrMCPFeatureDisabled) {
		t.Fatalf("expected ErrMCPFeatureDisabled, got %v", err)
	}
}

func mustMCPFeatureSet(t *testing.T, disable bool) featureflags.Set {
	t.Helper()
	return mustMCPFeatureSetWithMutations(t, disable, false)
}

func mustMCPMutationFeatureSet(t *testing.T) featureflags.Set {
	t.Helper()
	return mustMCPFeatureSetWithMutations(t, false, true)
}

func mustMCPFeatureSetWithMutations(t *testing.T, disableServer bool, enableMutations bool) featureflags.Set {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{
		{
			Code:      "LOP-FEAT-0001",
			Name:      mcp.ServerPreviewFeature,
			Lifecycle: featureflags.LifecycleStable,
		},
		{
			Code:      "LOP-FEAT-0002",
			Name:      mcp.MutationToolsFeature,
			Lifecycle: featureflags.LifecyclePreview,
		},
	})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	opts := featureflags.ResolveOptions{Channel: featureflags.ChannelDev}
	if disableServer {
		opts.Disable = []string{mcp.ServerPreviewFeature}
	}
	if enableMutations {
		opts.Enable = append(opts.Enable, mcp.MutationToolsFeature)
	}
	features, err := registry.Resolve(opts)
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return features
}
