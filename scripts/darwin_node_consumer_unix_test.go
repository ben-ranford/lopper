//go:build !windows

package scripts

import "testing"

func TestDarwinNodeConsumerBoundary(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "regression-proof-darwin", "Prove Darwin regression tests for fix PRs")
	for _, scenario := range []string{"positive", "alternate path", "shell startup"} {
		t.Run(scenario, func(t *testing.T) {
			for _, compiler := range []bool{false, true} {
				t.Run(map[bool]string{false: "npm install", true: "existing compiler"}[compiler], func(t *testing.T) { assertNodeConsumerBoundary(t, step, scenario, compiler) })
			}
		})
	}
}
