package main

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
)

func TestFeatureCodeContinuityAllowsRetiringOlderAliases(t *testing.T) {
	previous := []featureflags.Flag{{
		Code:            "LOP-FEAT-0001",
		Name:            "renamed-feature",
		DeprecatedNames: []string{"original-feature"},
		Lifecycle:       featureflags.LifecyclePreview,
	}}

	current := featureflags.Flag{
		Code:            "LOP-FEAT-0001",
		Name:            "latest-feature",
		DeprecatedNames: []string{"renamed-feature"},
		Lifecycle:       featureflags.LifecyclePreview,
	}
	violations := featureCodeContinuityViolations([]featureflags.Flag{current}, previous)
	if len(violations) != 0 {
		t.Fatalf("continuity violations = %#v, want none", violations)
	}
}
