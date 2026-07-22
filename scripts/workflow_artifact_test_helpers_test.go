package scripts

import "testing"

type workflowArtifactDownloadExpectation struct {
	label     string
	wantID    string
	wantPath  string
	wantRepo  string
	wantRunID string
	wantToken string
}

func assertWorkflowArtifactDownloadByID(t *testing.T, step workflowStepConfig, want workflowArtifactDownloadExpectation) {
	t.Helper()

	assertCIArtifactAction(t, step, want.label, "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c", map[string]string{
		"artifact-ids": want.wantID,
		"github-token": want.wantToken,
		"path":         want.wantPath,
		"repository":   want.wantRepo,
		"run-id":       want.wantRunID,
	})
	if _, ok := step.With["name"]; ok {
		t.Fatalf("%s must bind publication to a trusted upload artifact ID instead of an artifact name", want.label)
	}
}
