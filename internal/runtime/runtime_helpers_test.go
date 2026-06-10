package runtime

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestRuntimeAdditionalHelperBranches(t *testing.T) {
	tracePath := "/tmp/runtime.ndjson"
	env, err := withRuntimeTraceEnv([]string{"PATH=/usr/bin"}, tracePath)
	if err != nil {
		t.Fatalf("with runtime trace env: %v", err)
	}
	if readEnvValue(env, "NODE_OPTIONS") == "" {
		t.Fatalf("expected NODE_OPTIONS to be injected when absent")
	}
	if got := readEnvValue([]string{"BROKEN", "KEY=value"}, "KEY"); got != "value" {
		t.Fatalf("expected malformed env entries to be skipped, got %q", got)
	}

	deps := []report.DependencyReport{
		{Language: "z", Name: "b"},
		{Language: "a", Name: "c"},
		{Language: "a", Name: "b"},
	}
	sortDependencies(deps)
	if deps[0].Language != "a" || deps[0].Name != "b" || deps[1].Name != "c" || deps[2].Language != "z" {
		t.Fatalf("unexpected runtime dependency ordering: %#v", deps)
	}
}
