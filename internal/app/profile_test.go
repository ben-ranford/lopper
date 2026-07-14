package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func TestExecuteProfileRequiresFeature(t *testing.T) {
	application := &App{}
	req := DefaultRequest()
	req.Mode = ModeProfile
	req.Profile.Name = "strict"

	_, err := application.Execute(context.Background(), req)
	if !errors.Is(err, ErrProfileFeatureDisabled) {
		t.Fatalf("expected ErrProfileFeatureDisabled, got %v", err)
	}
}

func TestExecuteProfilePrintsConfig(t *testing.T) {
	application := &App{}
	req := DefaultRequest()
	req.Mode = ModeProfile
	req.Profile.Name = "balanced"
	req.Profile.Features = mustProfileFeatureSet(t)

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute profile: %v", err)
	}
	assertContainsAll(t, output, []string{
		"thresholds:",
		"fail_on_increase_percent: 2",
		"removal_candidate_weight_confidence: 0.20",
	})
}

func TestExecuteProfileWritesConfigWithOverwriteSafeguard(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), ".lopper.yml")
	application := &App{}
	req := DefaultRequest()
	req.Mode = ModeProfile
	req.Profile.Name = "noise-reduction"
	req.Profile.OutputPath = outputPath
	req.Profile.Features = mustProfileFeatureSet(t)

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute profile write: %v", err)
	}
	if !strings.Contains(output, outputPath) {
		t.Fatalf("expected output confirmation to include path, got %q", output)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read profile output: %v", err)
	}
	if !strings.Contains(string(data), "fail_on_increase_percent: 5") {
		t.Fatalf("expected noise-reduction config, got %q", string(data))
	}

	req.Profile.Name = "strict"
	if _, err := application.Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected overwrite safeguard error, got %v", err)
	}

	req.Profile.Force = true
	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute profile force overwrite: %v", err)
	}
	data, err = os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read forced profile output: %v", err)
	}
	if !strings.Contains(string(data), "fail_on_increase_percent: 1") {
		t.Fatalf("expected strict config after force overwrite, got %q", string(data))
	}
}

func TestExecuteProfileStdoutOutputPath(t *testing.T) {
	application := &App{}
	req := DefaultRequest()
	req.Mode = ModeProfile
	req.Profile.Name = "strict"
	req.Profile.OutputPath = "-"
	req.Profile.Features = mustProfileFeatureSet(t)

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute profile stdout path: %v", err)
	}
	if !strings.Contains(output, "fail_on_increase_percent: 1") || strings.Contains(output, "written to") {
		t.Fatalf("expected config on stdout, got %q", output)
	}
}

func TestExecuteProfileErrorPaths(t *testing.T) {
	application := &App{}
	req := DefaultRequest()
	req.Mode = ModeProfile
	req.Profile.Name = "missing"
	req.Profile.Features = mustProfileFeatureSet(t)
	if _, err := application.Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "unknown threshold profile") {
		t.Fatalf("expected unknown profile error, got %v", err)
	}

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	req.Profile.Name = "strict"
	req.Profile.OutputPath = filepath.Join(blocker, ".lopper.yml")
	if _, err := application.Execute(context.Background(), req); err == nil {
		t.Fatalf("expected mkdir error for output path under regular file")
	}

	outputDir := t.TempDir()
	req.Profile.OutputPath = outputDir
	req.Profile.Force = true
	if _, err := application.Execute(context.Background(), req); err == nil {
		t.Fatalf("expected write error when output path is a directory")
	}
}

func TestPersistProfileConfigPropagatesStatError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	_, err := persistProfileConfig("thresholds: {}", filepath.Join(blocker, "profile.yaml"), false)
	if err == nil {
		t.Fatal("expected stat error under regular file")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "stat" || pathErr.Path != filepath.Join(blocker, "profile.yaml") {
		t.Fatalf("expected propagated stat path error, got %v", err)
	}
}

func TestPersistProfileConfigPropagatesMkdirAllError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	if _, err := persistProfileConfig("thresholds: {}", filepath.Join(blocker, "profile.yaml"), true); err == nil {
		t.Fatal("expected mkdir error under regular file")
	}
}

func mustProfileFeatureSet(t *testing.T) featureflags.Set {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0001",
		Name:      thresholds.ProfilesPreviewFeature,
		Lifecycle: featureflags.LifecycleStable,
	}})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	features, err := registry.Resolve(featureflags.ResolveOptions{Channel: featureflags.ChannelDev})
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return features
}
