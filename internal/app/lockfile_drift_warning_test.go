package app

import (
	"errors"
	"strings"
	"testing"
)

func TestLockfileDriftWarningHelpers(t *testing.T) {
	findings := []lockfileDriftFinding{
		{
			kind:      lockfileDriftMissingLockfile,
			manifest:  "package.json",
			relDir:    "services/api",
			rule:      lockfileRule{manager: "npm", lockfiles: []string{"package-lock.json"}, remedy: "run npm install"},
			lockfiles: nil,
		},
		{
			kind:      lockfileDriftStaleLockfile,
			manifest:  "package.json",
			relDir:    ".",
			rule:      lockfileRule{manager: "npm", manifest: "package.json"},
			lockfiles: []presentLockfile{{name: "package-lock.json"}},
		},
	}

	warnings := buildLockfileDriftWarnings(findings)
	if len(warnings) != len(findings) {
		t.Fatalf("expected %d warnings, got %#v", len(findings), warnings)
	}
	if !strings.Contains(warnings[0], "package.json exists but no matching lockfile (package-lock.json) was found") {
		t.Fatalf("unexpected missing lockfile warning: %q", warnings[0])
	}
	if !strings.Contains(warnings[1], "package-lock.json exists without package.json") {
		t.Fatalf("unexpected stale lockfile warning: %q", warnings[1])
	}

	err := formatLockfileDriftError(warnings)
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected lockfile drift sentinel error, got %v", err)
	}
	if strings.Count(err.Error(), lockfileDriftWarningPrefix) != 1 {
		t.Fatalf("expected formatted error to keep only the sentinel prefix, got %q", err)
	}
	if !strings.Contains(err.Error(), "npm in services/api") || !strings.Contains(err.Error(), "npm in .") {
		t.Fatalf("expected both warnings in formatted error, got %q", err)
	}
}
