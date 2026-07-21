package scripts

import (
	"regexp"
	"testing"
)

func TestCIWorkflowPinsPrivilegedVerifyActions(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	verify := workflowJobByName(t, workflow.Jobs, "verify")
	assertWorkflowJobPermissions(t, verify, "ci verify job", map[string]string{
		"contents":      "read",
		"issues":        "write",
		"pull-requests": "write",
	})

	assertWorkflowEnvKeyOnlyOnStep(t, workflow.Jobs, "SONAR_TOKEN", "verify", "Post SonarQube review comments (PR)")

	for _, check := range []struct {
		stepName string
		wantUses string
	}{
		{
			stepName: "Checkout",
			wantUses: "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
		},
		{
			stepName: "Setup Go",
			wantUses: "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e",
		},
		{
			stepName: "Comment memory benchmark report on PR",
			wantUses: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3",
		},
		{
			stepName: "Comment lopper report on PR",
			wantUses: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3",
		},
		{
			stepName: "Post SonarQube review comments (PR)",
			wantUses: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3",
		},
		{
			stepName: "Comment on coverage failure",
			wantUses: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3",
		},
		{
			stepName: "Upload binary artifact",
			wantUses: "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a",
		},
	} {
		step := workflowStepByName(t, workflow.Jobs, "verify", check.stepName)
		if step.Uses != check.wantUses {
			t.Fatalf("verify step %q uses %q, want %q", check.stepName, step.Uses, check.wantUses)
		}
	}

	immutableAction := regexp.MustCompile(`^[^@[:space:]]+@[0-9a-f]{40}$`)
	for _, step := range verify.Steps {
		if step.Uses != "" && !immutableAction.MatchString(step.Uses) {
			t.Fatalf("verify step %q must use an immutable action SHA: %q", step.Name, step.Uses)
		}
	}

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
