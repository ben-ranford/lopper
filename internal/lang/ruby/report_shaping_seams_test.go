package ruby

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestRubyShapeDependencyReportSeam(t *testing.T) {
	stats := shared.DependencyStats{
		HasImports:      true,
		UsedCount:       1,
		TotalCount:      1,
		UsedPercent:     100,
		UsedImports:     []report.ImportUse{{Name: "private_gem", Module: "private_gem"}},
		WildcardImports: 1,
	}
	info := rubyDependencySource{Git: true, DeclaredGemfile: true}

	dep, warnings := shapeRubyDependencyReport("private-gem", stats, info)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", warnings)
	}
	if dep.Provenance == nil || dep.Provenance.Source != rubyDependencySourceGit {
		t.Fatalf("expected git provenance, got %#v", dep.Provenance)
	}
	if len(dep.RiskCues) != 1 || dep.RiskCues[0].Code != "dynamic-require" {
		t.Fatalf("expected dynamic-require risk cue, got %#v", dep.RiskCues)
	}
	if len(dep.Recommendations) != 1 || dep.Recommendations[0].Code != "review-runtime-requires" {
		t.Fatalf("expected runtime review recommendation, got %#v", dep.Recommendations)
	}
}

func TestRubyShapeDependencyWarningsSeam(t *testing.T) {
	warnings := shapeRubyDependencyWarnings("rack", shared.DependencyStats{})
	if len(warnings) != 1 || warnings[0] != `no requires found for dependency "rack"` {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}

	warnings = shapeRubyDependencyWarnings("rack", shared.DependencyStats{HasImports: true})
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings when imports exist, got %#v", warnings)
	}
}
