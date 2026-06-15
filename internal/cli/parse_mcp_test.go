package cli

import (
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/featureflags"
)

func TestParseArgsMCP(t *testing.T) {
	req := mustParseArgs(t, []string{"mcp"})
	if req.Mode != app.ModeMCP {
		t.Fatalf(modeMismatchFmt, app.ModeMCP, req.Mode)
	}
}

func TestParseArgsMCPFeatureFlags(t *testing.T) {
	withFeatureRegistry(t, featureflags.ChannelRelease, nil)

	req := mustParseArgs(t, []string{"mcp", "--enable-feature", "preview-flag", "--disable-feature", "stable-flag"})
	if !req.MCP.Features.Enabled("preview-flag") {
		t.Fatalf("expected mcp preview flag to be enabled")
	}
	if req.MCP.Features.Enabled("stable-flag") {
		t.Fatalf("expected mcp stable flag to be disabled")
	}
}

func TestParseArgsMCPFeatureFlagsRejectUnknownAndConflict(t *testing.T) {
	withFeatureRegistry(t, featureflags.ChannelRelease, nil)
	if err := expectParseArgsError(t, []string{"mcp", "--enable-feature", "missing"}, "expected unknown feature error"); !strings.Contains(err.Error(), "unknown feature") {
		t.Fatalf("expected unknown feature error, got %v", err)
	}
	if err := expectParseArgsError(t, []string{"mcp", "--enable-feature", "preview-flag", "--disable-feature", "preview-flag"}, "expected feature conflict error"); !strings.Contains(err.Error(), "both enabled and disabled") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestParseArgsMCPRejectsPositionals(t *testing.T) {
	err := expectParseArgsError(t, []string{"mcp", "extra"}, "expected mcp positional error")
	if !strings.Contains(err.Error(), "too many arguments") {
		t.Fatalf("expected too many arguments error, got %v", err)
	}
}

func TestParseArgsMCPNormalizesFlagsAfterUnexpectedPositional(t *testing.T) {
	withFeatureRegistry(t, featureflags.ChannelRelease, nil)

	err := expectParseArgsError(t, []string{"mcp", "extra", "--enable-feature", "missing"}, "expected normalized mcp flag parsing")
	if !strings.Contains(err.Error(), "unknown feature") {
		t.Fatalf("expected unknown feature error after normalization, got %v", err)
	}
}

func TestParseArgsMCPMissingFeatureValue(t *testing.T) {
	err := expectParseArgsError(t, []string{"mcp", "--enable-feature"}, "expected missing mcp feature value")
	if !strings.Contains(err.Error(), "flag needs an argument") {
		t.Fatalf("expected missing flag value error, got %v", err)
	}
}
