package scripts

import "testing"

func TestCIWorkflowPinsPrivilegedSonarReviewCommentStep(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	verify := workflowJobByName(t, workflow.Jobs, "verify")
	assertWorkflowJobPermissions(t, verify, "ci verify", map[string]string{
		"contents":      "read",
		"issues":        "write",
		"pull-requests": "write",
	})

	assertWorkflowEnvKeyOnlyOnStep(t, workflow.Jobs, "SONAR_TOKEN", "verify", "Post SonarQube review comments (PR)")

	sonar := workflowStepByName(t, workflow.Jobs, "verify", "Post SonarQube review comments (PR)")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "Sonar review comment action", got: sonar.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
		{label: "Sonar review comment condition", got: sonar.If, want: "${{ github.event_name == 'pull_request' && !env.ACT && env.SONAR_TOKEN != '' }}"},
	})
	assertWorkflowStepEnv(t, sonar, "Sonar review comment step", map[string]string{
		"SONAR_HOST_URL":    "https://sonarcloud.io",
		"SONAR_PROJECT_KEY": "ben-ranford_lopper",
		"SONAR_TOKEN":       "${{ secrets.SONAR_TOKEN }}",
	})
}
