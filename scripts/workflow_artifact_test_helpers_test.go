package scripts

import "testing"

func assertWorkflowArtifactDownloadByID(t *testing.T, step workflowStepConfig, label string, wantID string, wantPath string, wantRepository string, wantRunID string, wantToken string) {
	t.Helper()

	assertCIArtifactAction(t, step, label, "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c", map[string]string{
		"artifact-ids": wantID,
		"github-token": wantToken,
		"path":         wantPath,
		"repository":   wantRepository,
		"run-id":       wantRunID,
	})
	if _, ok := step.With["name"]; ok {
		t.Fatalf("%s must bind publication to a trusted upload artifact ID instead of an artifact name", label)
	}
}
