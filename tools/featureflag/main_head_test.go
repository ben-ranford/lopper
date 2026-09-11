package main

import (
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
)

func TestFeatureCodeContinuityPreservesHistoricalNames(t *testing.T) {
	previous := []featureflags.Flag{{
		Code:            "LOP-FEAT-0001",
		Name:            "renamed-feature",
		DeprecatedNames: []string{"original-feature"},
		Lifecycle:       featureflags.LifecyclePreview,
	}}

	for _, tc := range []struct {
		name    string
		current featureflags.Flag
		want    string
	}{
		{
			name: "rejects dropped alias after chained rename",
			current: featureflags.Flag{
				Code:            "LOP-FEAT-0001",
				Name:            "latest-feature",
				DeprecatedNames: []string{"renamed-feature"},
				Lifecycle:       featureflags.LifecyclePreview,
			},
			want: "original-feature",
		},
		{
			name: "rejects dropped alias without rename",
			current: featureflags.Flag{
				Code:      "LOP-FEAT-0001",
				Name:      "renamed-feature",
				Lifecycle: featureflags.LifecyclePreview,
			},
			want: "original-feature",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			violations := featureCodeContinuityViolations([]featureflags.Flag{tc.current}, previous)
			if len(violations) != 1 || !strings.Contains(violations[0], tc.want) {
				t.Fatalf("continuity violations = %#v, want missing historical name %q", violations, tc.want)
			}
		})
	}
}
