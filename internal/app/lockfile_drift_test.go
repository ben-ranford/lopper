package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectLockfileDriftGitManifestChangeWithoutLockfileChange(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	writeFile(t, filepath.Join(repo, "package-lock.json"), "{\n  \"name\": \"demo\"\n}\n")
	initGitRepo(t, repo)

	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\",\n  \"version\": \"1.0.1\"\n}\n")

	warnings, err := detectLockfileDrift(context.Background(), repo, false)
	if err != nil {
		t.Fatalf("detect lockfile drift: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %#v", warnings)
	}
	if !strings.Contains(warnings[0], "package.json changed while no matching lockfile changed") {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
}

func TestDetectLockfileDriftSkipsLopperCache(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".lopper-cache", "nested", "package.json"), "{\n  \"name\": \"cache-only\"\n}\n")

	warnings, err := detectLockfileDrift(context.Background(), repo, false)
	if err != nil {
		t.Fatalf("detect lockfile drift: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings from .lopper-cache contents, got %#v", warnings)
	}
}

func TestEvaluateLockfileDriftPolicyFailFormatsSinglePrefix(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	writeFile(t, filepath.Join(repo, "composer.lock"), "{}\n")

	warnings, err := evaluateLockfileDriftPolicy(context.Background(), repo, "fail")
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected ErrLockfileDrift, got %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected fail policy to stop after first warning, got %#v", warnings)
	}
	if strings.Count(err.Error(), "lockfile drift detected") != 1 {
		t.Fatalf("expected single lockfile drift prefix in error, got %q", err.Error())
	}
}

func TestEvaluateLockfileDriftPolicyOff(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")

	warnings, err := evaluateLockfileDriftPolicy(context.Background(), repo, "off")
	if err != nil {
		t.Fatalf("evaluate off policy: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for off policy, got %#v", warnings)
	}
}

func TestFormatLockfileDriftErrorNoWarnings(t *testing.T) {
	err := formatLockfileDriftError(nil)
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected ErrLockfileDrift for empty warnings, got %v", err)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func initGitRepo(t *testing.T, repo string) {
	t.Helper()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "init")
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", repo}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = sanitizedGitEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}
