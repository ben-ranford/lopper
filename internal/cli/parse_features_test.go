package cli

import (
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
)

func TestParseArgsFeatures(t *testing.T) {
	req := mustParseArgs(t, []string{"features", "--format", "json", "--output", "features.json", "-o", "features.json", "--channel", "rolling", "--release", "v1.4.2"})
	if req.Mode != app.ModeFeatures {
		t.Fatalf(modeMismatchFmt, app.ModeFeatures, req.Mode)
	}
	if req.Features.Format != "json" {
		t.Fatalf("expected json format, got %q", req.Features.Format)
	}
	if req.Features.Channel != "rolling" {
		t.Fatalf("expected rolling channel, got %q", req.Features.Channel)
	}
	if req.Features.Release != "v1.4.2" {
		t.Fatalf("expected release version, got %q", req.Features.Release)
	}
	if req.Features.OutputPath != "features.json" {
		t.Fatalf("expected features output path, got %q", req.Features.OutputPath)
	}
}

func TestParseArgsFeaturesRejectsPositionals(t *testing.T) {
	err := expectParseArgsError(t, []string{"features", "extra"}, "expected features positional error")
	if !strings.Contains(err.Error(), "too many arguments") {
		t.Fatalf("expected too many arguments error, got %v", err)
	}
}

func TestParseArgsFeaturesOutputConflict(t *testing.T) {
	err := expectParseArgsError(t, []string{"features", "--output", "one.json", "-o", "two.json"}, "expected features output conflict")
	if !strings.Contains(err.Error(), "--output and -o must match") {
		t.Fatalf("expected output conflict, got %v", err)
	}
}

func TestParseArgsFeaturesNormalizesFlagsAfterUnexpectedPositional(t *testing.T) {
	err := expectParseArgsError(t, []string{"features", "extra", "--output", "one.json", "-o", "two.json"}, "expected normalized features flag parsing")
	if !strings.Contains(err.Error(), "--output and -o must match") {
		t.Fatalf("expected output conflict after normalization, got %v", err)
	}
}
