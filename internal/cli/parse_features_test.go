package cli

import (
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
)

func TestParseArgsFeatures(t *testing.T) {
	req := mustParseArgs(t, []string{"features", "--format", "json"})
	if req.Mode != app.ModeFeatures {
		t.Fatalf(modeMismatchFmt, app.ModeFeatures, req.Mode)
	}
	if req.Features.Format != "json" {
		t.Fatalf("expected json format, got %q", req.Features.Format)
	}
}

func TestParseArgsFeaturesRejectsPositionals(t *testing.T) {
	err := expectParseArgsError(t, []string{"features", "extra"}, "expected features positional error")
	if !strings.Contains(err.Error(), "too many arguments") {
		t.Fatalf("expected too many arguments error, got %v", err)
	}
}
